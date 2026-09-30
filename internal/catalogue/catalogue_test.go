// These tests are the reason the package exists.
//
// Every one of them used to be writable only by driving `garm catalogue build`
// through cobra and reading its stderr, which is why most of them were not
// written at all. They are an external test package on purpose: what they
// exercise is the API a command is allowed to see.
package catalogue_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/garm/internal/catalogue"
	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

// tree writes one .proto into a temporary directory and compiles it.
//
// No vendored annotations: internal/compile resolves garm/tool/v1 and its
// siblings out of this binary's linked registry, which is the same thing that
// lets a real tree import them without a copy.
func tree(t *testing.T, name, body string) (*descriptorpb.FileDescriptorSet, []protoreflect.FileDescriptor) {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, fds, err := compile.Tree(context.Background(), root)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return set, fds
}

func request(set *descriptorpb.FileDescriptorSet, fds []protoreflect.FileDescriptor) catalogue.Request {
	return catalogue.Request{
		Set:         set,
		Descriptors: fds,
		Origin:      "proto",
		Producer:    "garm/test",
		Compiler:    "protocompile/test",
	}
}

const calculator = `syntax = "proto3";
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

// A tree with no tools in it is refused, and the refusal names where it looked.
//
// A catalogue with nothing in it would start a daemon that serves nothing,
// which is a deployment nobody meant. The likely cause is a --proto pointing at
// the wrong directory, and an empty artifact that boots is far harder to
// diagnose than a build that stops.
func TestBuildRefusesATreeThatDeclaresNoTools(t *testing.T) {
	set, fds := tree(t, "empty/v1/empty.proto", `syntax = "proto3";
package empty.v1;
option go_package = "example.com/empty/gen/empty/v1;emptyv1";
message Nothing {
  string id = 1;
}
`)
	res, diags, err := catalogue.Build(request(set, fds))
	if err == nil {
		t.Fatal("a tree declaring no tools produced a catalogue")
	}
	if res != nil {
		t.Error("a refused build still returned a Result")
	}
	if got := diags.Errors(); got != 0 {
		t.Errorf("the refusal came with %d lint error(s); it is not a lint failure", got)
	}
	if !strings.Contains(err.Error(), "proto") {
		t.Errorf("the refusal does not name where it looked: %v", err)
	}
}

// A tree that does not lint is never built, and the Result is nil rather than
// partial.
//
// This is the gate the command's help text promises. The alternative is an
// artifact that fails at the daemon's mount check, where the author is not
// present and the failure is an outage rather than a build error. The
// diagnostics come back so a caller can print them; the decision has already
// been made by the time it sees them.
func TestBuildRefusesATreeThatDoesNotLint(t *testing.T) {
	// VERB_DESTRUCTIVE with no approval: irreversible, and supervised by
	// nobody.
	set, fds := tree(t, "danger/v1/danger.proto", `syntax = "proto3";
package danger.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/danger/gen/danger/v1;dangerv1";
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
`)
	res, diags, err := catalogue.Build(request(set, fds))
	if err == nil {
		t.Fatal("built a catalogue containing a destructive tool that declares no supervision")
	}
	if res != nil {
		t.Error("a refused build still returned a Result; a caller could write it out")
	}
	if diags.Errors() == 0 {
		t.Fatalf("no diagnostic was an error, so nothing explains the refusal:\n%v", diags)
	}
	if !strings.Contains(err.Error(), "refusing to build") {
		t.Errorf("the error does not say a build was refused: %v", err)
	}
}

// And a warning is information, not a refusal. O1 — a service with no owner —
// warns so that catalogues built before the rule existed still build, and the
// same separation is what lets `garm lint` print a warning and exit zero.
func TestAWarningDoesNotRefuseTheBuild(t *testing.T) {
	set, fds := tree(t, "acme/v1/calc.proto", calculator)
	res, diags, err := catalogue.Build(request(set, fds))
	if err != nil {
		t.Fatalf("a tree whose only diagnostic is a warning was refused: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("the fixture declares no owner and should have produced the O1 warning")
	}
	if got := diags.Errors(); got != 0 {
		t.Fatalf("%d of %d diagnostics counted as errors:\n%v", got, len(diags), diags)
	}
	if len(res.Body) == 0 || res.Digest == "" {
		t.Error("the build produced no bytes and no digest")
	}
}

// The property the whole estate's mount checks rest on: a package's descriptor
// hash covers the tools its AUTHOR declared, and not the card endpoints this
// package adds afterwards.
//
// A tool service computes the digest it advertises from the tools in its own
// generated binding, and the daemon compares that against the one in the
// catalogue. Hashing the synthesised cards here would move every existing
// service's digest without a single wire shape having changed — every mount in
// the plane would fail at once, and the catalogue would be the half that was
// wrong.
//
// So this asserts both halves: the stored hash IS the hash over the author's
// tools, and it is NOT the hash over the tools the finished artifact carries.
// The second half is what fails if someone reorders Build into a sequence that
// reads better.
func TestTheStoredHashCoversTheAuthorsToolsAndNotTheSynthesisedCards(t *testing.T) {
	set, fds := tree(t, "acme/v1/calc.proto", calculator)

	declared, err := compiler.Tools(fds)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) != 1 {
		t.Fatalf("the fixture declares %d tools, want 1", len(declared))
	}
	want := compiler.DescriptorHash(declared)

	res, _, err := catalogue.Build(request(set, fds))
	if err != nil {
		t.Fatal(err)
	}
	if res.SynthesisedCards == 0 {
		t.Fatal("no cards were synthesised, so this test proves nothing")
	}
	if got := res.Catalogue.GetDescriptorHashes()["acme.v1"]; got != want {
		t.Errorf("acme.v1 hashes to %s in the catalogue and %s over the author's tools; "+
			"a service advertising the second would be refused at mount", got, want)
	}

	// What the artifact actually carries, re-resolved the way a daemon resolves
	// it. It rebuilds — the property the daemon's boot depends on, and the one
	// that adding methods to somebody else's file most easily breaks.
	files, err := protodesc.NewFiles(res.Catalogue.GetFiles())
	if err != nil {
		t.Fatalf("the built catalogue's descriptor set does not rebuild: %v", err)
	}
	var carried []protoreflect.FileDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		carried = append(carried, fd)
		return true
	})
	withCards, err := compiler.Tools(carried)
	if err != nil {
		t.Fatal(err)
	}
	if len(withCards) <= len(declared) {
		t.Fatalf("the artifact carries %d tools and the author declared %d; the cards are missing",
			len(withCards), len(declared))
	}
	var pkg []compiler.Tool
	for _, tl := range withCards {
		if tl.Method.ParentFile().Package() == "acme.v1" {
			pkg = append(pkg, tl)
		}
	}
	if got := compiler.DescriptorHash(pkg); got == want {
		t.Error("the hash over the author's tools and the hash over the tools including the " +
			"synthesised cards are equal, so this test can no longer tell them apart")
	}

	// And the cards are in the artifact: a card that is not in the file set is
	// a card no viewer can fetch, whatever the tool service registered.
	if len(res.ToolNames) != len(declared) {
		t.Errorf("ToolNames has %d entries, want the %d the author declared: the names a "+
			"caller prints are the declarations, not the synthesised endpoints",
			len(res.ToolNames), len(declared))
	}
}

// The field documentation survives the strip, and SourceCodeInfo does not.
//
// Losing either half is a quiet failure: no docs means a model reads typed
// fields with no idea what they mean, and keeping SourceCodeInfo means paying
// for the same words twice — measured at 9.7 MB and 180 MB retained against
// 3.7 MB and 93 MB on a 10,000-tool catalogue.
func TestTheProseIsLiftedBeforeSourceInfoIsDropped(t *testing.T) {
	set, fds := tree(t, "doc/v1/d.proto", `syntax = "proto3";
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
`)
	res, _, err := catalogue.Build(request(set, fds))
	if err != nil {
		t.Fatal(err)
	}
	const want = "Balance in minor units, so 1234 is 12.34. Never a float."
	if got := res.Catalogue.GetFieldDocs()["doc.v1.In.minor_units"]; got != want {
		t.Errorf("field doc = %q, want %q", got, want)
	}
	for _, f := range res.Catalogue.GetFiles().GetFile() {
		if f.SourceCodeInfo != nil {
			t.Errorf("%s still carries SourceCodeInfo; the prose was lifted so this could go",
				f.GetName())
		}
	}
}

// Two builds of identical source produce identical bytes, which is what makes
// the digest identify the content rather than the build.
func TestTheArtifactIsReproducible(t *testing.T) {
	a, _, err := catalogue.Build(request(tree(t, "acme/v1/calc.proto", calculator)))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := catalogue.Build(request(tree(t, "acme/v1/calc.proto", calculator)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("two builds of identical source differ: %s vs %s", a.Digest, b.Digest)
	}
}
