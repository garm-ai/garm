// The composition tests: a manifest resolved against a real module graph, its
// inputs compiled as one set, and the rules of §2 held one at a time.
//
// They use github.com/garm-ai/contracts, which this repository already requires,
// so the module inputs are resolved from a real module cache at a real version
// rather than from a fake. There is no fixture that could stand in for the thing
// under test here: the invariant IS "what does the go command say this tree
// resolves", and a stub would be this package agreeing with itself.
package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/garm/internal/catalogue"
	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/manifest"
)

// contractsModule is the one module every test here composes from. This
// repository requires it, which is what makes it a legitimate input to a tree
// whose go.mod is this repository's.
const contractsModule = "github.com/garm-ai/contracts"

// crossInput imports a file from the MODULE input, not from its own tree, and
// uses a message out of it.
//
// garm.ledger.v1 is chosen deliberately: unlike garm.tasks.v1 it is not linked
// into this binary, so `import "garm/ledger/v1/event.proto"` can only resolve
// through the module's import root. If the union did not put every input's root
// on the import path, this file would not compile — which is the property under
// test and not an incidental detail of the fixture.
const crossInput = `syntax = "proto3";
package acme.v1;
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
import "garm/ledger/v1/event.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
// A message typed by the module input's package, and deliberately not reachable
// from a tool: what is under test is that the IMPORT resolves, and dragging
// somebody else's message into a tool's schema would additionally require every
// field of it to carry a policy, which is a different rule.
message LedgerRef {
  garm.ledger.v1.Event event = 1;
}
message AuditRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // The call to describe.
  optional string event_id = 1;
}
message AuditResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // What the ledger says about it.
  optional string summary = 1;
}
service AuditService {
  option (garm.meta.v1.owner) = { team: "platform" };
  rpc Audit(AuditRequest) returns (AuditResponse) {
    option (garm.tool.v1.tool) = {
      name: "audit" title: "Audit" description: "Describe one ledger event."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`

// selfContained declares a tool and imports nothing but the annotations, so it
// compiles as one tree as well as one input.
const selfContained = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message AddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double a = 1;
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

// tree writes a working tree: a go.mod and go.sum copied from this repository,
// so `go list -m` has a real module graph to answer from, plus whatever protos
// and manifest a test asks for.
//
// The go.mod is copied rather than synthesised because the version the module
// graph resolves is exactly what is under test, and a hand-written require line
// with no go.sum entry cannot be resolved offline.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	root := moduleRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module github.com/garm-ai/garm\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod for github.com/garm-ai/garm above the working directory")
		}
		dir = parent
	}
}

// compose is the whole front of `catalogue build` with no cobra in it: resolve,
// compile the union, attribute the packages.
func compose(t *testing.T, dir string) ([]*manifest.Input, *descriptorpb.FileDescriptorSet, error) {
	t.Helper()
	m, err := manifest.Load(filepath.Join(dir, manifest.Filename))
	if err != nil {
		return nil, nil, err
	}
	inputs, err := manifest.Resolve(t.Context(), dir, m, m.Source)
	if err != nil {
		return nil, nil, err
	}
	set, _, err := compile.Union(t.Context(), manifest.Roots(inputs))
	if err != nil {
		return nil, nil, err
	}
	return inputs, set, manifest.Packages(inputs, set)
}

// Two inputs, one of them a module resolved out of the cache, a cross-input
// import that resolves, and provenance that records both.
//
// This is the design in one test. The local tree declares a tool whose response
// carries a message from the module input's proto package; nothing is copied into
// the tree, nothing was fetched that `go mod download` had not already fetched,
// and the artifact says which version of the module the descriptors came from —
// which is the fact that is invisible in a catalogue built from one directory.
func TestComposesALocalTreeAndAModuleFromTheCache(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
source: garm-ai/garm/internal/manifest
include:
  - path: proto
  - module: ` + contractsModule + `
    packages: [garm.tasks.v1, garm.ledger.v1]
`,
	})

	inputs, set, err := compose(t, dir)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("want 2 inputs, got %d", len(inputs))
	}

	local, module := inputs[0], inputs[1]
	if local.Kind != manifest.KindLocal || local.Identity != "proto" {
		t.Errorf("first input: %+v", local)
	}
	if got := strings.Join(local.Packages, ","); got != "acme.v1" {
		t.Errorf("the local tree contributed %q, want acme.v1 — an imported file is a "+
			"dependency of the set, not a declaration of the input that imported it", got)
	}
	if module.Kind != manifest.KindModule || module.Identity != contractsModule {
		t.Errorf("second input: %+v", module)
	}
	if module.Version == "" || !strings.HasPrefix(module.Version, "v") {
		t.Errorf("a module input records the version the tree RESOLVED: %q", module.Version)
	}
	if got := strings.Join(module.Packages, ","); got != "garm.ledger.v1,garm.tasks.v1" {
		t.Errorf("the module contributed %q", got)
	}

	// The cross-input import resolved: the response field's type came out of a
	// package no file in the local tree declares, and which is not linked into
	// this binary either.
	if !declares(set, "garm.ledger.v1") {
		t.Fatal("garm.ledger.v1 is not in the set, so the module's import root was not " +
			"on the import path")
	}

	// And the composed set builds a catalogue carrying tools from both inputs.
	res, diags, err := catalogue.Build(request(t, dir, inputs, set))
	if err != nil {
		t.Fatalf("building the composed catalogue: %v (%d lint error(s))", err, diags.Errors())
	}
	if !has(res.ToolNames, "acme.v1.audit") {
		t.Errorf("the local tree's tool is missing: %v", res.ToolNames)
	}
	if !has(res.ToolNames, "garm.tasks.v1.create_task") {
		t.Errorf("the module's tools are missing: %v", res.ToolNames)
	}

	// Provenance records both, in the contract's canonical order: local before
	// module, so a reader can tell the reproducible half from the working tree.
	recorded := res.Catalogue.GetProvenance().GetInputs()
	if len(recorded) != 2 {
		t.Fatalf("provenance records %d input(s)", len(recorded))
	}
	if l := recorded[0].GetLocal(); l == nil || l.GetPath() != "proto" ||
		l.GetSource() != "garm-ai/garm/internal/manifest" {
		t.Errorf("first recorded input: %v", recorded[0])
	}
	if m := recorded[1].GetModule(); m == nil || m.GetPath() != contractsModule ||
		m.GetVersion() != module.Version {
		t.Errorf("second recorded input: %v", recorded[1])
	}
	if got := strings.Join(recorded[1].GetProtoPackages(), ","); got != "garm.ledger.v1,garm.tasks.v1" {
		t.Errorf("recorded packages are not sorted: %q", got)
	}
}

// The order of `include` is presentation, and the artifact proves it.
//
// A catalogue is byte-reproducible by requirement, and the file its inputs are
// read from is a human's to reorder. So the two orderings must produce the same
// bytes — not merely the same tools — which is what pins the canonical sort in
// catalogue.Build rather than leaving it to whoever wrote the file.
func TestIncludeOrderDoesNotReachTheArtifact(t *testing.T) {
	const local = `  - path: proto
`
	const module = `  - module: ` + contractsModule + `
    packages: [garm.tasks.v1]
`
	digest := func(order string) string {
		dir := tree(t, map[string]string{
			"proto/acme/v1/audit.proto": crossInput,
			manifest.Filename:           "schema: v1\nname: composed\ninclude:\n" + order,
		})
		inputs, set, err := compose(t, dir)
		if err != nil {
			t.Fatalf("composing:\n%v", err)
		}
		res, _, err := catalogue.Build(request(t, dir, inputs, set))
		if err != nil {
			t.Fatalf("building: %v", err)
		}
		return res.Digest
	}
	if a, b := digest(local+module), digest(module+local); a != b {
		t.Fatalf("reordering include moved the digest:\n  %s\n  %s", a, b)
	}
}

// Two inputs declaring one proto package is an error naming both. Never a merge
// and never last-wins: a silent winner is the drift a manifest exists to remove.
//
// The fixture is the half-migrated tree, which is the shape this will actually
// meet: a copy of an adopted package still sitting in proto/ beside the module
// entry that was meant to replace it.
func TestRefusesTwoInputsDeclaringOnePackage(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		// The copy that the module entry below is meant to replace.
		"proto/garm/ledger/v1/event.proto": `syntax = "proto3";
package garm.ledger.v1;
option go_package = "github.com/garm-ai/contracts/garm/ledger/v1;ledgerv1";
message Event { string event_id = 1; }
`,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: ` + contractsModule + `
    packages: [garm.ledger.v1]
`,
	})
	_, _, err := compose(t, dir)
	if err == nil {
		t.Fatal("a package declared by two inputs was merged instead of refused")
	}
	for _, want := range []string{"garm/ledger/v1/event.proto", "path proto", "module " + contractsModule} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%v", want, err)
		}
	}
}

// The invariant: a module the tree does not require cannot be an input, and the
// message has to teach, because in one case the fix is not obvious.
//
// A deployment may declare a tool it does not serve — the bank declares the web
// fetcher while another process answers it — and `go mod tidy` removes a
// requirement nothing imports. So the refusal names the blank import, which is
// the ordinary Go idiom for a dependency pinned without being called. Without
// that sentence the invariant reads as a bug.
func TestRefusesAModuleTheTreeDoesNotRequire(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: github.com/garm-ai/tools/web
    packages: [web.v1]
`,
	})
	_, _, err := compose(t, dir)
	if err == nil {
		t.Fatal("a module the tree does not require was accepted as an input")
	}
	for _, want := range []string{
		"github.com/garm-ai/tools/web",
		"not a known dependency",
		"go get github.com/garm-ai/tools/web",
		"go mod tidy",
		"blank import",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// A version key is readability and must agree. A disagreement is an error, not a
// precedence rule: if the manifest could pin a version go.mod does not, a
// deployment would be one edit from compiling against one tag and declaring
// another, which is the descriptor mismatch that took the plane offline.
func TestRefusesAVersionThatDisagreesWithTheModuleGraph(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: ` + contractsModule + `
    version: v0.0.1
    packages: [garm.tasks.v1]
`,
	})
	_, _, err := compose(t, dir)
	if err == nil {
		t.Fatal("a version the module graph does not resolve was accepted")
	}
	if !strings.Contains(err.Error(), "v0.0.1") || !strings.Contains(err.Error(), "resolves") {
		t.Errorf("the refusal does not name both versions:\n%v", err)
	}
}

// A version key that agrees is fine, which is the point of permitting it.
func TestAcceptsAVersionThatAgrees(t *testing.T) {
	probe := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: ` + contractsModule + `
    packages: [garm.tasks.v1]
`,
	})
	inputs, _, err := compose(t, probe)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	resolved := inputs[1].Version

	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: ` + contractsModule + `
    version: ` + resolved + `
    packages: [garm.tasks.v1]
`,
	})
	if _, _, err := compose(t, dir); err != nil {
		t.Fatalf("a version equal to what the tree resolves was refused:\n%v", err)
	}
}

// A named package the module does not have is an error naming both, rather than
// an input that quietly contributes nothing.
func TestRefusesAPackageAbsentFromTheModule(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/audit.proto": crossInput,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
  - module: ` + contractsModule + `
    packages: [garm.tasks.v1, garm.nonesuch.v1]
`,
	})
	_, _, err := compose(t, dir)
	if err == nil {
		t.Fatal("a package the module does not declare was accepted")
	}
	if !strings.Contains(err.Error(), "garm.nonesuch.v1") || !strings.Contains(err.Error(), contractsModule) {
		t.Errorf("the refusal does not name both the module and the package:\n%v", err)
	}
	if !strings.Contains(err.Error(), "garm/nonesuch/v1") {
		t.Errorf("the refusal does not say where it looked:\n%v", err)
	}
}

// A manifest with module entries and no go.mod is refused where the version
// would have come from, rather than by the go command three layers down.
func TestRefusesModuleEntriesWithNoModuleGraph(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "proto/acme/v1/audit.proto"), crossInput)
	write(t, filepath.Join(dir, manifest.Filename), `schema: v1
name: composed
include:
  - module: `+contractsModule+`
    packages: [garm.tasks.v1]
`)
	_, _, err := compose(t, dir)
	if err == nil {
		t.Fatal("module entries were resolved with no go.mod to resolve them against")
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("the refusal does not name go.mod:\n%v", err)
	}
}

// A one-entry manifest is the single-directory case, and composing it must
// produce what compiling that directory produces.
//
// `--proto` is spelled as a manifest with one `path:` entry precisely so that
// there is one code path through composition, and if a single input went through
// a different one, every catalogue in the estate could move its digest on this
// release for no reason anybody could point at.
func TestOneLocalEntryIsTheSameSetAsOneTree(t *testing.T) {
	dir := tree(t, map[string]string{
		"proto/acme/v1/calc.proto": selfContained,
		manifest.Filename: `schema: v1
name: composed
include:
  - path: proto
`,
	})
	viaTree, _, err := compile.Tree(t.Context(), filepath.Join(dir, "proto"))
	if err != nil {
		t.Fatalf("compile.Tree: %v", err)
	}
	inputs, viaUnion, err := compose(t, dir)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if len(inputs) != 1 || len(inputs[0].Files) != 1 {
		t.Fatalf("one entry resolved to %d input(s)", len(inputs))
	}
	a, err := proto.MarshalOptions{Deterministic: true}.Marshal(viaTree)
	if err != nil {
		t.Fatal(err)
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(viaUnion)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("composing one path entry produced different bytes from compiling that tree")
	}
}

func request(t *testing.T, dir string, inputs []*manifest.Input, set *descriptorpb.FileDescriptorSet) catalogue.Request {
	t.Helper()
	_, fds, err := compile.Union(t.Context(), manifest.Roots(inputs))
	if err != nil {
		t.Fatalf("recompiling for descriptors: %v", err)
	}
	return catalogue.Request{
		Set:         set,
		Descriptors: fds,
		Origin:      filepath.Join(dir, manifest.Filename),
		Inputs:      manifest.Provenance(inputs),
		Producer:    "garm/test",
		Compiler:    "protocompile/test",
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func declares(set *descriptorpb.FileDescriptorSet, pkg string) bool {
	for _, f := range set.GetFile() {
		if f.GetPackage() == pkg {
			return true
		}
	}
	return false
}

func hasFile(set *descriptorpb.FileDescriptorSet, name string) bool {
	for _, f := range set.GetFile() {
		if f.GetName() == name {
			return true
		}
	}
	return false
}

func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
