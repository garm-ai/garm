package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	mu       sync.Mutex
	ops      []string          // "HEAD <bucket>/<key>" and "PUT <bucket>/<key>", in order
	objects  map[string][]byte // "<bucket>/<key>" -> body
	failPut  string            // a key whose PUT is refused with 403
	failHead string            // a key whose HEAD is refused with 500 (not 404)
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
		if f.failHead == name {
			// 403, not 404: real S3 answers a HEAD on a key the caller
			// cannot confirm is missing (no s3:ListBucket, most commonly)
			// this way, and it must not be read as "not there, go ahead and
			// upload". Chosen over 500 so the SDK's default retrier — which
			// treats 5xx as transient — does not retry this into multiple
			// HEADs and make the test's operation count nondeterministic.
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>no</Message></Error>`))
			return
		}
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

func publishEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
}

// Three prompts, three distinct sha256 keys. "system" is shared by both
// agents in multiPromptFixture — the same file, the same key — so that
// verifyPrompts' dedup (item 3 of the review) has something real to
// exercise: without it, this file would be HEAD'd and PUT twice.
const (
	systemPromptBody = "You are a support assistant. Answer from the tools you are given.\n"
	fixtureSHA       = "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a"

	guidancePromptBody = "Follow the playbook exactly and escalate anything unclear.\n"
	guidanceSHA        = "fe7fd46cf8792065d1404879c2e7e3ce8695bb265121d8d841d4a890d5e9877d"

	playbookPromptBody = "Step 1: verify identity. Step 2: read the account. Step 3: answer only from tools.\n"
	playbookSHA        = "d84d4c5eec4eaeb8546af9d343939af4be2f5b1c3e265af3e0e5bf6875ffa065"
)

// multiPromptFixture builds a tree with two agents — Assistant and
// Escalation, both in bank.v1 — declaring three prompt files between them:
// "system", shared by both agents under the same key and the same bytes, and
// one prompt unique to each. Four prompt declarations, three distinct
// sha256s: verifyPrompts must upload each real file exactly once, and in a
// stable, sha256-sorted order.
//
// That order, ascending by sha256, is system, playbook, guidance — every
// test below that "breaks" a specific prompt or a specific upload relies on
// knowing where in the sequence it falls.
//
// Kept independent of catalogue_test.go's writeAgentFixture, which several
// other tests in this package pin to declaring exactly one prompt; changing
// its shape here would ripple into tests this task does not own.
func multiPromptFixture(t *testing.T) (dir, catalogue string) {
	t.Helper()
	dir = fixture(t)
	agentDir := filepath.Join(dir, "proto", "bank", "v1")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const assistant = `syntax = "proto3";
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
    prompts: { key: "system" value: { path: "prompts/system.md" sha256: "` + fixtureSHA + `" } }
    prompts: { key: "guidance" value: { path: "prompts/guidance.md" sha256: "` + guidanceSHA + `" } }
  };
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "assistant" title: "Assistant" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(agentDir, "assistant.proto"), []byte(assistant), 0o644); err != nil {
		t.Fatal(err)
	}

	const escalation = `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
message EscalateRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
service Escalation {
  option (garm.agent.v1.agent) = {
    mode: MODE_REACT
    principal: { clearance: CLEARANCE_INTERNAL }
    model: { alias: "fast" }
    bounds: { max_steps: 8 }
    prompts: { key: "system" value: { path: "prompts/system.md" sha256: "` + fixtureSHA + `" } }
    prompts: { key: "playbook" value: { path: "prompts/playbook.md" sha256: "` + playbookSHA + `" } }
  };
  rpc Invoke(EscalateRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "escalate" title: "Escalate" description: "Start an escalation."
      verb: VERB_WRITE min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(agentDir, "escalation.proto"), []byte(escalation), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"system.md":   systemPromptBody,
		"guidance.md": guidancePromptBody,
		"playbook.md": playbookPromptBody,
	} {
		if err := os.WriteFile(filepath.Join(dir, "prompts", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

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

func runPublish(t *testing.T, catalogue, dir string) error {
	t.Helper()
	root := newRoot()
	root.SetArgs([]string{"catalogue", "publish", catalogue, "s3://garm/catalogue/",
		"--prompts-root", dir})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root.Execute()
}

// The whole point of the command: a catalogue must never be visible before the
// files it pins. A reader that fetches the catalogue and then fetches
// prompts/<sha>.md must never find the second missing.
//
// Three prompts across two agents, one shared, is Review Focus 5 with teeth
// (review items 2/3): a verify-one-upload-one implementation cannot pass
// this, because the shared "system" prompt must be uploaded exactly once
// despite two agents referencing it, and the order below is asserted for all
// three, not just one.
func TestPublishUploadsEveryPromptBeforeTheCatalogue(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)

	if err := runPublish(t, catalogue, dir); err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []string{
		"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/prompts/" + fixtureSHA + ".md",
		"HEAD garm/catalogue/prompts/" + playbookSHA + ".md",
		"PUT garm/catalogue/prompts/" + playbookSHA + ".md",
		"HEAD garm/catalogue/prompts/" + guidanceSHA + ".md",
		"PUT garm/catalogue/prompts/" + guidanceSHA + ".md",
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
	fake, endpoint := newFakeS3(t, "garm/catalogue/prompts/"+playbookSHA+".md")
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)

	if err := runPublish(t, catalogue, dir); err != nil {
		t.Fatalf("publish: %v", err)
	}
	want := []string{
		"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/prompts/" + fixtureSHA + ".md",
		"HEAD garm/catalogue/prompts/" + playbookSHA + ".md",
		"HEAD garm/catalogue/prompts/" + guidanceSHA + ".md",
		"PUT garm/catalogue/prompts/" + guidanceSHA + ".md",
		"PUT garm/catalogue/catalogue.binpb",
	}
	if got := fake.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Review Focus 5 — nothing at all is uploaded when a prompt is missing. Not
// "nothing after the failure": the ordering guarantee is worthless if the
// catalogue can land beside a prompt set that was only half checked.
//
// Breaks the LAST prompt in upload order (guidance, review item 2): with a
// verify-as-you-go implementation this would still HEAD and PUT the two
// prompts that sort before it; the assertion below requires zero.
func TestPublishUploadsNothingWhenAPromptIsMissing(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)
	if err := os.Remove(filepath.Join(dir, "prompts", "guidance.md")); err != nil {
		t.Fatal(err)
	}

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("publish succeeded with a missing prompt")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// Same refusal for a prompt that drifted: the catalogue pins the old hash, the
// tree holds new bytes. Publishing would put the new bytes under the old
// hash's key, which is the one thing content addressing must never allow.
//
// Breaks the LAST prompt in upload order, same reasoning as the missing-file
// test above.
func TestPublishRefusesAPromptThatHashesDifferently(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "prompts", "guidance.md"),
		[]byte("Something else entirely.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("publish succeeded with a drifted prompt")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// Review Focus 3, publish half. A catalogue built elsewhere can carry any
// path at all, so publish applies compiler.ValidatePromptPath itself rather
// than trusting that lint has already run on this catalogue — this is now
// the identical lexical check the linter (A2) applies, sharing one
// implementation.
func TestPublishRefusesAPromptPathThatEscapesTheRoot(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, _ := multiPromptFixture(t)
	// Rebuild the catalogue with an escaping path, bypassing lint by writing
	// the artifact the same way runCatalogueBuild does — see
	// writeCatalogueWithPromptPath below.
	catalogue := writeCatalogueWithPromptPath(t, "../../etc/passwd", fixtureSHA)

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("publish accepted a prompt path outside the tree")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// Critical (review item 1). ValidatePromptPath is a LEXICAL check: the
// declared path "prompts/system.md" never leaves the tree as text. A symlink
// at that name resolving outside the root escapes one level further down,
// where only compiler.ContainedPromptPath — re-resolving both the path and
// the root with filepath.EvalSymlinks before anything is read — can catch
// it. The target's bytes hash correctly, which is exactly the case a purely
// lexical check would sail through: the point is that this is refused before
// a single byte reaches the store, not because the content is wrong.
func TestPublishRefusesASymlinkThatEscapesTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs elevated privileges on windows")
	}
	fake, endpoint := newFakeS3(t)
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)

	target := filepath.Join(filepath.Dir(dir), "system-outside-the-tree.md")
	if err := os.WriteFile(target, []byte(systemPromptBody), 0o644); err != nil {
		t.Fatal(err)
	}
	systemPath := filepath.Join(dir, "prompts", "system.md")
	if err := os.Remove(systemPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, systemPath); err != nil {
		t.Fatal(err)
	}

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("publish accepted a prompt that is a symlink escaping the root")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a failed publish still touched the store: %v", got)
	}
}

// A refused PUT fails the command, and nothing that sorts after the refused
// key — including the catalogue itself — is ever attempted. "system" is
// first in upload order, so failing its PUT must leave "playbook",
// "guidance" and the catalogue entirely untouched: not merely "the catalogue
// was not uploaded" (review item 2), which a single-prompt fixture cannot
// tell apart from "nothing else was left to upload".
func TestPublishStopsAtTheFirstRefusedUpload(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	fake.failPut = "garm/catalogue/prompts/" + fixtureSHA + ".md"
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("a refused upload was reported as success")
	}
	want := []string{
		"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md",
		"PUT garm/catalogue/prompts/" + fixtureSHA + ".md",
	}
	if got := fake.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant exactly:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Review item 6 — a HEAD that fails for a reason OTHER than "not there" must
// fail the publish, not be read as "go ahead and upload". Real S3 answers a
// HEAD on a genuinely missing key with 404 only when the caller can list the
// bucket; without s3:ListBucket it answers 403, which looks nothing like
// "not found" and must not be treated as one.
func TestPublishFailsWhenHeadIsRefused(t *testing.T) {
	fake, endpoint := newFakeS3(t)
	fake.failHead = "garm/catalogue/prompts/" + fixtureSHA + ".md"
	publishEnv(t, endpoint)
	dir, catalogue := multiPromptFixture(t)

	if err := runPublish(t, catalogue, dir); err == nil {
		t.Fatal("publish succeeded when HEAD was refused for a reason other than 404")
	}
	want := []string{"HEAD garm/catalogue/prompts/" + fixtureSHA + ".md"}
	if got := fake.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant exactly:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
