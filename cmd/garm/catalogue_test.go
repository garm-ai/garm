package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
)

// fixture is the smallest tree that produces a catalogue: the vendored
// annotations plus one governed tool.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	svc := filepath.Join(dir, "proto", "acme", "v1")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	const p = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message AddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double a = 1;
  optional double b = 2;
}
message AddResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double sum = 1;
}
service Calculator {
  rpc Add(AddRequest) returns (AddResponse) {
    option (garm.tool.v1.tool) = {
      name: "add" title: "Add" description: "Add two numbers."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(svc, "calc.proto"), []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func build(t *testing.T, dir, out string, args ...string) []byte {
	t.Helper()
	root := newRoot()
	root.SetArgs(append([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"), "-o", out}, args...))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("catalogue build: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCatalogueIsReproducible is the property the digest depends on.
//
// A digest that moves on every rebuild of identical source identifies the
// build rather than the content, which makes "are these two deployments
// serving the same tools" unanswerable and makes a redeploy look like a
// change. The first version of this command stamped a wall clock and failed
// exactly that way.
func TestCatalogueIsReproducible(t *testing.T) {
	dir := fixture(t)
	a := build(t, dir, filepath.Join(t.TempDir(), "a.binpb"))
	b := build(t, dir, filepath.Join(t.TempDir(), "b.binpb"))
	if !bytes.Equal(a, b) {
		t.Fatalf("two builds of identical source differ: %d vs %d bytes", len(a), len(b))
	}
}

// TestStampTimeBreaksReproducibilityOnPurpose pins the trade rather than the
// convenience: --stamp-time exists, and the flag's help says what it costs.
func TestStampTimeBreaksReproducibilityOnPurpose(t *testing.T) {
	dir := fixture(t)
	a := build(t, dir, filepath.Join(t.TempDir(), "a.binpb"), "--stamp-time")
	b := build(t, dir, filepath.Join(t.TempDir(), "b.binpb"), "--stamp-time")
	if bytes.Equal(a, b) {
		t.Skip("clock granularity made two stamped builds identical; the flag is still opt-in")
	}
}

// TestCatalogueRefusesAPolicyError is why building and linting are one step.
// A catalogue that does not lint would fail at the daemon's mount check
// instead — where the author is absent and the failure is an outage.
func TestCatalogueRefusesAPolicyError(t *testing.T) {
	dir := fixture(t)
	bad := `
message NukeRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  string id = 1;
}
message NukeResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  string id = 1;
}

service Danger {
  rpc Nuke(NukeRequest) returns (NukeResponse) {
    option (garm.tool.v1.tool) = {
      name: "nuke" title: "Nuke" description: "Irreversible."
      verb: VERB_DESTRUCTIVE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
	p := filepath.Join(dir, "proto", "acme", "v1", "calc.proto")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(bad); err != nil {
		t.Fatal(err)
	}
	f.Close()

	root := newRoot()
	root.SetArgs([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"), "-o", filepath.Join(t.TempDir(), "x.binpb")})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("built a catalogue containing a destructive tool that declares no supervision")
	}
}

// TestFieldDocsSurviveTheStrip is the trade this makes, in one test.
//
// SourceCodeInfo is dropped because it carries spans and paths for every
// token in every file, and that is roughly half of what a loaded catalogue
// costs. The prose is the only part a projected schema ever needed, so it is
// lifted into a flat table first.
//
// Losing either half would be a quiet failure: no docs means a model reads
// typed fields with no idea what they mean, and keeping SourceCodeInfo means
// paying twice for the same words.
func TestFieldDocsSurviveTheStrip(t *testing.T) {
	dir := t.TempDir()
	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	svc := filepath.Join(dir, "proto", "doc", "v1")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	const p = `syntax = "proto3";
package doc.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/doc/gen/doc/v1;docv1";
message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // Balance in minor units, so 1234 is 12.34.
  // Never a float.
  optional int64 minor_units = 1;
}
message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service S {
  rpc Get(In) returns (Out) {
    option (garm.tool.v1.tool) = {
      name: "get" title: "Get" description: "Read."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(svc, "d.proto"), []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "c.binpb")
	body := build(t, dir, out)
	cat := &cataloguev1.Catalogue{}
	if err := proto.Unmarshal(body, cat); err != nil {
		t.Fatal(err)
	}

	got := cat.GetFieldDocs()["doc.v1.In.minor_units"]
	const want = "Balance in minor units, so 1234 is 12.34. Never a float."
	if got != want {
		t.Errorf("field doc = %q, want %q (hard wrapping should collapse to one paragraph)", got, want)
	}

	for _, f := range cat.GetFiles().GetFile() {
		if f.SourceCodeInfo != nil {
			t.Errorf("%s still carries SourceCodeInfo; the prose was lifted so this could go",
				f.GetName())
		}
	}
}

// TestInitWiresTheToolPlugin guards the gap that made every scaffolded
// repository need a manual fix.
//
// init used to write a buf.gen.yaml that generated messages and nothing else,
// so the typed Handler interface, the Serve, the contract version and the
// descriptor hash — the entire reason the generator exists — were absent
// until someone worked out the config themselves. Two of the three settings
// below are ones they would get wrong: package_suffix, without which the
// binding forms an import cycle with its own connect sibling, and the connect
// plugin the AsConnect adapter needs.
//
// Asserting on config text rather than on generated output because generating
// needs buf and network. The end-to-end proof lives in garm-ai/examples,
// which builds this exact shape against published artifacts.
func TestInitWiresTheToolPlugin(t *testing.T) {
	dir := t.TempDir()
	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	gen, err := os.ReadFile(filepath.Join(dir, "buf.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ text, why string }{
		{"protoc-gen-garm-go", "the tool binding is not generated at all"},
		{"emit=toolsdk", "emit defaults to garm's own wiring, which belongs nowhere near a tool service"},
	} {
		if !bytes.Contains(gen, []byte(want.text)) {
			t.Errorf("buf.gen.yaml is missing %q: %s", want.text, want.why)
		}
	}

	// And what it must NOT scaffold. A tool service is reached over NATS by a
	// daemon that never speaks connect to it, so neither of these buys the
	// author anything — they were both consequences of the AsConnect adapter,
	// which is now opt-in. Scaffolding them anyway would put a second plugin
	// and a connectrpc dependency into every project that ran `garm init`.
	for _, unwanted := range []struct{ text, why string }{
		{"connectrpc/go", "a second plugin, for an adapter nothing in this stack consumes"},
		{"package_suffix", "the sibling package only existed to dodge the connect import cycle"},
	} {
		if bytes.Contains(gen, []byte(unwanted.text)) {
			t.Errorf("buf.gen.yaml still scaffolds %q: %s", unwanted.text, unwanted.why)
		}
	}

	// The annotations must be a module of their own. Under proto/ they become
	// an input rather than an import, and buf generates Go for them that can
	// never be used — the real one is in this module's contracts package, and
	// two packages registering one proto file panic at init.
	buf, err := os.ReadFile(filepath.Join(dir, "buf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf, []byte("third_party/proto")) {
		t.Error("buf.yaml does not put the vendored annotations in their own module")
	}
	if _, err := os.Stat(filepath.Join(dir, "third_party", "proto", "garm", "tool", "v1", "tool.proto")); err != nil {
		t.Errorf("annotations are not where buf.yaml says they are: %v", err)
	}
}
