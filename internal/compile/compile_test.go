package compile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compile"
)

// calc is one governed tool, annotated the way `garm init` teaches.
const calc = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message AddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // Left operand, in minor units.
  // Never a float.
  optional double a = 1;
  optional double b = 2;
}
message AddResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double sum = 1 [(garm.tool.v1.field_policy) = { read: CLEARANCE_INTERNAL on_deny: { mask: { with: "[hidden]" } } }];
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

// tree writes a proto tree under a temporary root and returns it.
//
// Fixtures are written here rather than committed under proto/: that tree is
// the contract this repository publishes and `garm init` vendors verbatim, so
// a file added there to satisfy a test becomes something consumers carry.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func method(t *testing.T, fds []protoreflect.FileDescriptor, name string) protoreflect.MethodDescriptor {
	t.Helper()
	for _, fd := range fds {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			ms := svcs.Get(i).Methods()
			for j := 0; j < ms.Len(); j++ {
				if string(ms.Get(j).FullName()) == name {
					return ms.Get(j)
				}
			}
		}
	}
	t.Fatalf("no method %s in the compiled tree", name)
	return nil
}

func message(t *testing.T, fds []protoreflect.FileDescriptor, name string) protoreflect.MessageDescriptor {
	t.Helper()
	for _, fd := range fds {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			if string(msgs.Get(i).FullName()) == name {
				return msgs.Get(i)
			}
		}
	}
	t.Fatalf("no message %s in the compiled tree", name)
	return nil
}

// TestAnnotationsResolveToTheirGeneratedGoTypes is the property everything
// downstream of Tree depends on, and it was a panic before the round trip
// through GlobalTypes existed.
//
// protocompile keeps options as dynamic messages whatever the import resolver
// does, so proto.GetExtension(opts, toolv1.E_Tool) is handed a
// *dynamicpb.Message where it wants a *toolv1.ToolPolicy — two Go types
// describing one field — and panics rather than returning anything a caller
// could check. The linter, the catalogue builder and the code generator all
// read annotations exactly this way, so this is one failure that takes every
// consumer of the package with it.
//
// The failure here is the panic, not the type assertions below: those only
// catch a future GetExtension that returns a wrong type instead of panicking.
func TestAnnotationsResolveToTheirGeneratedGoTypes(t *testing.T) {
	_, fds, err := compile.Tree(t.Context(), tree(t, map[string]string{"acme/v1/calc.proto": calc}))
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	tool, ok := proto.GetExtension(
		method(t, fds, "acme.v1.Calculator.Add").Options(), toolv1.E_Tool).(*toolv1.ToolPolicy)
	if !ok {
		t.Fatalf("garm.tool.v1.tool did not read back as *toolv1.ToolPolicy; " +
			"the options were not re-parsed against the linked extension types")
	}
	if tool.GetName() != "add" || tool.GetVerb() != toolv1.Verb_VERB_READ ||
		tool.GetMinClearance() != toolv1.Clearance_CLEARANCE_PUBLIC {
		t.Errorf("tool policy = %v, want add/VERB_READ/CLEARANCE_PUBLIC", tool)
	}

	// The same round trip has to reach message and field options, which are
	// separate extendees: an implementation that special-cased methods would
	// pass the assertion above and still panic on the first annotated field.
	res := message(t, fds, "acme.v1.AddResponse")
	if _, ok := proto.GetExtension(
		res.Options(), toolv1.E_DefaultFieldPolicy).(*toolv1.FieldPolicy); !ok {
		t.Errorf("garm.tool.v1.default_field_policy did not read back as *toolv1.FieldPolicy")
	}
	sum, ok := proto.GetExtension(
		res.Fields().ByName("sum").Options(), toolv1.E_FieldPolicy).(*toolv1.FieldPolicy)
	if !ok {
		t.Fatalf("garm.tool.v1.field_policy did not read back as *toolv1.FieldPolicy")
	}
	if sum.GetOnDeny().GetMask().GetWith() != "[hidden]" {
		t.Errorf("field policy = %v, want the declared mask", sum)
	}
}

// TestTheLinkedAnnotationsBeatAVendoredCopy pins what the resolver is for.
//
// `garm init` vendors garm/tool/v1/tool.proto into the author's tree, so the
// file is both an import and a file on disk. Compiled from source it yields a
// second, unlinked copy of every extension — which is the dynamicpb panic
// above — and it also makes whatever happens to be vendored authoritative
// about what a declaration means. The vendored file below is a plausible
// stale one: it predates verb, min_clearance and default_field_policy, so if
// it were the copy in play nothing here would compile.
func TestTheLinkedAnnotationsBeatAVendoredCopy(t *testing.T) {
	const stale = `syntax = "proto3";
package garm.tool.v1;
import "google/protobuf/descriptor.proto";
option go_package = "github.com/garm-ai/garm/contracts/garm/tool/v1;toolv1";
message ToolPolicy { string name = 1; }
extend google.protobuf.MethodOptions { ToolPolicy tool = 50002; }
`
	_, fds, err := compile.Tree(t.Context(), tree(t, map[string]string{
		"garm/tool/v1/tool.proto": stale,
		"acme/v1/calc.proto":      calc,
	}))
	if err != nil {
		t.Fatalf("compiling against a vendored copy of the annotations: %v\n"+
			"the vendored file was parsed instead of the linked descriptors, so a stale "+
			"copy in someone's tree now decides what their declarations mean", err)
	}
	tool, ok := proto.GetExtension(
		method(t, fds, "acme.v1.Calculator.Add").Options(), toolv1.E_Tool).(*toolv1.ToolPolicy)
	if !ok {
		t.Fatalf("garm.tool.v1.tool did not read back as *toolv1.ToolPolicy")
	}
	if tool.GetVerb() != toolv1.Verb_VERB_READ {
		t.Errorf("verb = %v, want VERB_READ", tool.GetVerb())
	}
}

// TestCommentsSurviveCompilation is why SourceInfoMode is set.
//
// A field's prose is lifted out of SourceCodeInfo into the catalogue's
// field_docs table, and it is the only thing that tells a model what a typed
// field means. Compiling without source info costs nothing visible — every
// test of shape still passes — and silently ships schemas that are names and
// types with no documentation at all.
func TestCommentsSurviveCompilation(t *testing.T) {
	_, fds, err := compile.Tree(t.Context(), tree(t, map[string]string{"acme/v1/calc.proto": calc}))
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	req := message(t, fds, "acme.v1.AddRequest")
	got := req.ParentFile().SourceLocations().ByDescriptor(req.Fields().ByName("a")).LeadingComments
	if !strings.Contains(got, "Left operand, in minor units.") {
		t.Errorf("leading comments on acme.v1.AddRequest.a = %q, want the declared prose; "+
			"without SourceInfoMode a projected schema has no field documentation", got)
	}
}

// TestAnEmptyTreeIsAnError: silently compiling nothing produces an empty
// catalogue, and a daemon started on one serves no tools while reporting a
// clean build.
func TestAnEmptyTreeIsAnError(t *testing.T) {
	if _, _, err := compile.Tree(t.Context(), t.TempDir()); err == nil {
		t.Fatal("a directory with no .proto files compiled successfully")
	}
}

// TestAMissingTreeIsAnError covers the mistyped --proto path, which is
// otherwise indistinguishable from an empty one.
func TestAMissingTreeIsAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nope")
	_, _, err := compile.Tree(t.Context(), root)
	if err == nil {
		t.Fatal("a root that does not exist compiled successfully")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %v, want the path named so the typo is visible", err)
	}
}

// TestASyntaxErrorNamesTheFileAndLine. The author is the reader of this
// error, and "compile failed" sends them looking through a tree.
func TestASyntaxErrorNamesTheFileAndLine(t *testing.T) {
	_, _, err := compile.Tree(t.Context(), tree(t, map[string]string{
		"acme/v1/broken.proto": "syntax = \"proto3\";\npackage acme.v1;\nmessage Oops {\n",
	}))
	if err == nil {
		t.Fatal("a proto with a syntax error compiled successfully")
	}
	if !strings.Contains(err.Error(), "acme/v1/broken.proto") {
		t.Errorf("error = %v, want the offending file named", err)
	}
}

// TestTheDescriptorSetIsTopologicallyOrdered is what makes the set rebuildable
// on the far side.
//
// A FileDescriptorSet is a flat list, and protodesc.NewFiles resolves imports
// in order: a file listed before its dependency fails to resolve, so a
// catalogue that builds here refuses to load in the daemon. The ordering only
// breaks on trees with a real import graph, which is why this one has a chain
// rather than a single file.
func TestTheDescriptorSetIsTopologicallyOrdered(t *testing.T) {
	set, _, err := compile.Tree(t.Context(), tree(t, map[string]string{
		"acme/v1/calc.proto": calc,
		"acme/v1/agg.proto": `syntax = "proto3";
package acme.v1;
import "acme/v1/calc.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message SumRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  repeated AddRequest terms = 1;
}
service Aggregator {
  rpc Sum(SumRequest) returns (AddResponse) {
    option (garm.tool.v1.tool) = {
      name: "sum" title: "Sum" description: "Add many numbers."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`,
	}))
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	at := map[string]int{}
	for i, f := range set.GetFile() {
		at[f.GetName()] = i
	}
	for i, f := range set.GetFile() {
		for _, dep := range f.GetDependency() {
			j, ok := at[dep]
			if !ok {
				t.Errorf("%s imports %s, which is not in the set at all", f.GetName(), dep)
				continue
			}
			if j > i {
				t.Errorf("%s (%d) imports %s (%d): a dependency listed after its dependent "+
					"does not resolve when the set is rebuilt", f.GetName(), i, dep, j)
			}
		}
	}

	// The same claim, made the way a daemon makes it at boot.
	if _, err := protodesc.NewFiles(set); err != nil {
		t.Fatalf("rebuilding the returned set: %v", err)
	}
}

// TestVersionAlwaysNamesACompiler covers the fallback, which is the only half
// of Version a test can see.
//
// The value is stamped into a catalogue's provenance so that a digest
// mismatch is a diff rather than a mystery, and it comes from the build
// info's dependency list — which a `go test` binary does not carry, so what
// this observes is always the "unknown" branch. That branch is still worth
// pinning: provenance with an empty compiler field reads as "no compiler was
// recorded", which is a different and wronger claim than "this build cannot
// tell you".
func TestVersionAlwaysNamesACompiler(t *testing.T) {
	if got := compile.Version(); !strings.HasPrefix(got, "protocompile/") {
		t.Fatalf("Version() = %q, want a protocompile/ identifier for the catalogue's provenance", got)
	}
}
