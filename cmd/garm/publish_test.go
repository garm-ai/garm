package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/compile"
)

// fakeS3 is enough of the S3 API for the two calls publish makes — HEAD an
// object and PUT an object — and it records the ORDER, which is the property
// the command exists to guarantee.
//
// An httptest server rather than a mocked client, so the code under test
// builds real requests against a real signer and real object keys. What it
// deliberately does not cover is in KNOWN-GAPS: multipart, retries, and the
// behaviour of any particular store.
type fakeS3 struct {
	mu      sync.Mutex
	ops     []string          // "HEAD <bucket>/<key>" and "PUT <bucket>/<key>", in order
	objects map[string][]byte // "<bucket>/<key>" -> body
	failPut string            // a key whose PUT is refused with 403
}

func newFakeS3(t *testing.T, existing ...string) (*fakeS3, string) {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	for _, k := range existing {
		f.objects[k] = []byte("already here")
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Path-style: /<bucket>/<key…>
	name := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodHead:
		f.ops = append(f.ops, "HEAD "+name)
		if _, ok := f.objects[name]; !ok {
			// Real S3 answers a HEAD on a missing key with a bodyless 404,
			// which the SDK models as *types.NotFound.
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		f.ops = append(f.ops, "PUT "+name)
		if f.failPut == name {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>no</Message></Error>`))
			return
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		f.objects[name] = body.Bytes()
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeS3) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// publishFixture builds a real catalogue from a tree holding one agent with
// one prompt, and returns the directory (the prompts root) and the path to the
// artifact.
func publishFixture(t *testing.T, sha string) (dir, catalogue string) {
	t.Helper()
	dir = fixture(t)
	writeAgentFixture(t, dir, sha)
	catalogue = filepath.Join(t.TempDir(), "catalogue.binpb")
	root := newRoot()
	root.SetArgs([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"), "-o", catalogue})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("building the fixture catalogue: %v", err)
	}
	return dir, catalogue
}

func publishEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
}

const fixtureSHA = "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a"

// The whole point of the command: a catalogue must never be visible before the
// files it pins. A reader that fetches the catalogue and then fetches
// prompts/<sha>.md must never find the second missing.
func TestPublishUploadsEveryPromptBeforeTheCatalogue(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []string{
		"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/catalogue.binpb",
	}
	if got := fake.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A prompt is content-addressed, so an object that is already there is already
// correct. Skipping it is not an optimisation — it is what makes republishing
// a catalogue that shares prompts with the last one cheap enough to do often.
func TestPublishSkipsAPromptThatIsAlreadyThere(t *testing.T) {
	fake, endpoint := newFakeS3(t, "garm/catalogue/prompts/"+fixtureSHA+".md")
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	want := []string{
		"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/catalogue.binpb",
	}
	if got := fake.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Review Focus 5 — nothing at all is uploaded when a prompt is missing. Not
// "nothing after the failure": the ordering guarantee is worthless if the
// catalogue can land beside a prompt set that was only half checked.
func TestPublishUploadsNothingWhenAPromptIsMissing(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)
	if err := os.Remove(filepath.Join(dir, "prompts", "system.md")); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	if err := root.Execute(); err == nil {
		t.Fatal("publish succeeded with a missing prompt")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// Same refusal for a prompt that drifted: the catalogue pins the old hash, the
// tree holds new bytes. Publishing would put the new bytes under the old
// hash's key, which is the one thing content addressing must never allow.
func TestPublishRefusesAPromptThatHashesDifferently(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)
	if err := os.WriteFile(filepath.Join(dir, "prompts", "system.md"),
		[]byte("Something else entirely.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	if err := root.Execute(); err == nil {
		t.Fatal("publish succeeded with a drifted prompt")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// Review Focus 3, publish half — the same path check the linter applies. A
// catalogue built elsewhere can carry any path at all, so publish may not
// trust that lint has run.
func TestPublishRefusesAPromptPathThatEscapesTheRoot(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)
	// Rebuild the catalogue with an escaping path, bypassing lint by writing
	// the artifact the same way runCatalogueBuild does — see
	// writeCatalogueWithPromptPath below.
	catalogue = writeCatalogueWithPromptPath(t, "../../etc/passwd", fixtureSHA)

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("publish accepted a prompt path outside the tree")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// A refused PUT fails the command, and the catalogue is not uploaded after it.
func TestPublishStopsAtTheFirstRefusedUpload(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	fake.failPut = "garm/catalogue/prompts/" + fixtureSHA + ".md"
	publishEnv(t, endpoint)
	dir, catalogue := publishFixture(t, fixtureSHA)

	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("a refused upload was reported as success")
	}
	for _, op := range fake.recorded() {
		if strings.HasSuffix(op, "catalogue.binpb") {
			t.Error("the catalogue was uploaded after a prompt upload failed")
		}
	}
}

func TestParseS3Dest(t *testing.T) {
	for raw, want := range map[string]s3Dest{
		"s3://garm/catalogue/": {Bucket: "garm", Prefix: "catalogue"},
		"s3://garm/catalogue":  {Bucket: "garm", Prefix: "catalogue"},
		"s3://garm/":           {Bucket: "garm", Prefix: ""},
		"s3://garm":            {Bucket: "garm", Prefix: ""},
		"s3://garm/a/b/":       {Bucket: "garm", Prefix: "a/b"},
	} {
		got, err := parseS3Dest(raw)
		if err != nil || got != want {
			t.Errorf("parseS3Dest(%q) = %+v, %v; want %+v, nil", raw, got, err, want)
		}
	}
	for _, bad := range []string{"", "garm/catalogue", "https://garm/catalogue", "s3://"} {
		if _, err := parseS3Dest(bad); err == nil {
			t.Errorf("parseS3Dest(%q) = nil error; the destination must be s3://bucket/prefix/", bad)
		}
	}
}

// writeCatalogueWithPromptPath writes a proto tree declaring promptPath as an
// agent's prompt path, compiles it, and marshals a bare cataloguev1.Catalogue
// directly — the same three lines runCatalogueBuild uses
// (proto.MarshalOptions{Deterministic: true}.Marshal, os.WriteFile) —
// deliberately skipping the lint gate. The point of
// TestPublishRefusesAPromptPathThatEscapesTheRoot is that a catalogue built
// elsewhere may carry any path at all, so publish may not trust that lint has
// run.
func writeCatalogueWithPromptPath(t *testing.T, promptPath, sha string) string {
	t.Helper()
	dir := fixture(t)
	agentDir := filepath.Join(dir, "proto", "bank", "v1")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
message Ask {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
service Assistant {
  option (garm.agent.v1.agent) = {
    mode: MODE_REACT
    principal: { clearance: CLEARANCE_INTERNAL }
    model: { alias: "fast" }
    bounds: { max_steps: 8 }
    prompts: { key: "system" value: { path: "` + promptPath + `" sha256: "` + sha + `" } }
  };
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "assistant" title: "Assistant" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(agentDir, "agent.proto"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}

	set, _, err := compile.Tree(context.Background(), filepath.Join(dir, "proto"))
	if err != nil {
		t.Fatalf("compiling the escaping-path fixture: %v", err)
	}
	cat := &cataloguev1.Catalogue{Files: set}
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(cat)
	if err != nil {
		t.Fatalf("marshalling the escaping-path fixture: %v", err)
	}
	out := filepath.Join(t.TempDir(), "catalogue.binpb")
	if err := os.WriteFile(out, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}
