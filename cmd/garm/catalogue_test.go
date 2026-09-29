package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	garm "github.com/garm-ai/garm"
	"github.com/garm-ai/garm/contracts/cards"
	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/compiler"
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

// writeAgentFixture writes an agent service beside its prompts, at the layout
// --prompts-root defaults to: proto/ and prompts/ as siblings under dir. The
// declared hash is the caller's to vary, since the two tests that use this
// differ only in whether it matches the prompt on disk.
func writeAgentFixture(t *testing.T, dir, sha string) {
	t.Helper()
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
    prompts: { key: "system" value: { path: "prompts/system.md" sha256: "` + sha + `" } }
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
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "system.md"),
		[]byte("You are a support assistant. Answer from the tools you are given.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A catalogue that pins a prompt by hash and a tree whose prompt says
// something else is the change that must never ship: the runner fetches
// prompts/<sha256>.md and would serve instructions nobody reviewed, or refuse
// the agent at load time in production. `catalogue build` lints first, so this
// is a build error with the author present.
func TestCatalogueBuildRefusesADriftedPrompt(t *testing.T) {
	dir := fixture(t)
	writeAgentFixture(t, dir, "0000000000000000000000000000000000000000000000000000000000000000")

	root := newRoot()
	root.SetArgs([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"),
		"-o", filepath.Join(t.TempDir(), "catalogue.binpb")})
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	if err := root.Execute(); err == nil {
		t.Fatal("a catalogue with a drifted prompt was built")
	}
	if !strings.Contains(errBuf.String(), "A2") {
		t.Errorf("the refusal does not name A2:\n%s", errBuf.String())
	}
}

// And the default prompts root is the parent of --proto: the same tree with
// the correct hash builds with no --prompts-root at all.
func TestCatalogueBuildResolvesPromptsBesideTheProtoTree(t *testing.T) {
	dir := fixture(t)
	// (same tree as above, with sha256 d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a)
	writeAgentFixture(t, dir, "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a")
	root := newRoot()
	root.SetArgs([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"),
		"-o", filepath.Join(t.TempDir(), "catalogue.binpb")})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("a correct tree failed to build: %v", err)
	}
}

// The catalogue's schema version is scoped to garm.tool.v1 and nothing else.
//
// A daemon reads that number and refuses a catalogue outside its window. The
// other three vocabularies — garm.agent.v1 (the manifest), garm.card.v1 (a
// template), garm.meta.v1 (an owner) — are read by the runner and never by the
// daemon, so a catalogue that carries all three still stamps the tool schema
// version and nothing more: garmd stays agent-, card- and owner-blind, and the
// daemon-side half of this test (garmd's agentblind tests) never sees a
// number it does not understand. If any of those namespaces ever moved this
// version, a daemon would refuse a catalogue at boot over an annotation it
// does not read.
func TestCatalogueSchemaVersionIgnoresTheRunnerNamespaces(t *testing.T) {
	dir := fixture(t)
	writeAgentFixture(t, dir, "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a")
	// An owner and a task card on the tool service; a result card on the
	// agent. Every non-tool namespace in one tree.
	appendProto(t, dir, `
import "garm/card/v1/card.proto";
import "garm/meta/v1/meta.proto";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string reference = 1;
}
message PayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service Payments {
  option (garm.meta.v1.owner) = { team: "payments-platform" contact: "#payments-oncall" };
  rpc Pay(PayRequest) returns (PayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay" title: "Pay" description: "Pay someone."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_INTERNAL
                  max_grant_age_seconds: 900 material_fields: ["reference"] }
    };
    option (garm.card.v1.task_card) = {
      title: "Payment {reference}"
      body: [{ facts: { facts: [{ field: "reference" label: "Reference" }] } }]
    };
  }
}
`)
	agentPath := filepath.Join(dir, "proto", "bank", "v1", "agent.proto")
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	agent = bytes.Replace(agent, []byte(`import "garm/tool/v1/tool.proto";`),
		[]byte("import \"garm/tool/v1/tool.proto\";\nimport \"garm/card/v1/card.proto\";\nimport \"garm/meta/v1/meta.proto\";"), 1)
	agent = bytes.Replace(agent, []byte("service Assistant {\n"),
		[]byte("service Assistant {\n  option (garm.meta.v1.owner) = { team: \"agent-platform\" };\n"+
			"  option (garm.card.v1.result_card) = { title: \"Answered\" body: [{ text: \"See the run.\" }] };\n"), 1)
	if err := os.WriteFile(agentPath, agent, 0o644); err != nil {
		t.Fatal(err)
	}

	var cat cataloguev1.Catalogue
	if err := proto.Unmarshal(build(t, dir, filepath.Join(t.TempDir(), "c.binpb")), &cat); err != nil {
		t.Fatal(err)
	}
	if got := cat.GetAnnotationSchemaVersion(); got != garm.AnnotationSchemaVersion {
		t.Errorf("annotation_schema_version = %d, want %d: the version is garm.tool.v1's and "+
			"an agent, a card template or an owner must not move it", got, garm.AnnotationSchemaVersion)
	}
	// And the artifact is self-contained: a runner resolving the templates
	// and owners it reads finds the files that define them in the catalogue,
	// not on a registry.
	have := map[string]bool{}
	for _, f := range cat.GetFiles().GetFile() {
		have[f.GetName()] = true
	}
	for _, want := range []string{"garm/agent/v1/agent.proto", "garm/card/v1/card.proto", "garm/meta/v1/meta.proto"} {
		if !have[want] {
			t.Errorf("the catalogue does not carry %s; a runner reading that namespace would have to reach a registry", want)
		}
	}
}

// The catalogue carries every tool's card endpoints, and still rebuilds.
//
// A card that is not in the file set is a card no viewer can fetch, whatever
// the tool service registered — the daemon mounts what the catalogue
// declares. And a set that gained methods referring to types the author's
// file never imported would fail at the daemon's BOOT rather than here,
// which is the wrong end of the pipe, so the imports are added too and the
// result is re-resolved before it is written.
func TestTheCatalogueCarriesEveryToolsCards(t *testing.T) {
	dir := fixture(t)
	appendProto(t, dir, `
import "garm/meta/v1/meta.proto";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string reference = 1;
}
message PayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service Payments {
  option (garm.meta.v1.owner) = { team: "payments-platform" contact: "#payments-oncall" };
  rpc Pay(PayRequest) returns (PayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay" title: "Pay" description: "Pay someone."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_INTERNAL
                  max_grant_age_seconds: 900 material_fields: ["reference"] }
    };
  }
}
`)
	var cat cataloguev1.Catalogue
	if err := proto.Unmarshal(build(t, dir, filepath.Join(t.TempDir(), "c.binpb")), &cat); err != nil {
		t.Fatal(err)
	}

	// It rebuilds. This is the property the daemon's boot depends on, and the
	// one that adding methods to somebody else's file most easily breaks.
	files, err := protodesc.NewFiles(cat.GetFiles())
	if err != nil {
		t.Fatalf("the catalogue's descriptor set does not rebuild: %v", err)
	}

	// And the three cards of the one MODE_GRANT tool are on its own service,
	// under the names contracts/cards derives — the same function the
	// generator calls, which is why the daemon and the service agree.
	var svc protoreflect.ServiceDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if s := fd.Services().ByName("Payments"); s != nil {
			svc = s
		}
		return svc == nil
	})
	if svc == nil {
		t.Fatal("the Payments service is not in the rebuilt catalogue")
	}
	for _, want := range []protoreflect.Name{"InputCard", "ResultCard", "ApprovalCard"} {
		md := svc.Methods().ByName(want)
		if md == nil {
			t.Errorf("%s is not on the service; nothing could fetch it", want)
			continue
		}
		if got := md.Output().FullName(); got != cards.CardType {
			t.Errorf("%s returns %s, want %s", want, got, cards.CardType)
		}
	}

	// The names the CATALOGUE carries are what the daemon routes by, so they
	// are asserted as tool names and not only as method names.
	tools, err := compiler.Tools(descriptorsOf(t, files))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools {
		have[tl.Name] = true
	}
	for _, want := range []string{"pay_input_card", "pay_result_card", "pay_approval_card"} {
		if !have[want] {
			t.Errorf("the catalogue has no tool %q", want)
		}
	}
}

func descriptorsOf(t *testing.T, files *protoregistry.Files) []protoreflect.FileDescriptor {
	t.Helper()
	var out []protoreflect.FileDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		out = append(out, fd)
		return true
	})
	return out
}
