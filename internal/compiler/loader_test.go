package compiler_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

func TestDefaultToolName(t *testing.T) {
	for in, want := range map[string]string{
		"GetProfile":            "get_profile",
		"GetUserProfile":        "get_user_profile",
		"ListTools":             "list_tools",
		"SendVerificationEmail": "send_verification_email",
		"GetHTTPStatus":         "get_http_status", // consecutive capitals
		"Search":                "search",
	} {
		if got := compiler.SnakeCase(in); got != want {
			t.Fatalf("SnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSnakeCaseEdgeCases exercises naming conventions not covered by the
// brief's table. These are judgment calls, not spec: see the task report for
// the reasoning behind each expected value.
func TestSnakeCaseEdgeCases(t *testing.T) {
	for in, want := range map[string]string{
		// Already lowercase with underscores: nothing to do, and the
		// underscore itself must pass through unchanged.
		"already_snake": "already_snake",
		// A single capital letter is just that letter, lowercased. There is
		// no preceding character, so no leading underscore.
		"A": "a",
		// A name ending in a run of consecutive capitals: the run is one
		// word, but it starts a new word from the lowercase run before it.
		// "url" is a suffix word, not "u_r_l".
		"ParseURL": "parse_url",
		// Digits attach to the letter run they trail ("v2" stays joined),
		// but a new capital immediately after digits starts a new word,
		// since the digit itself carries no case to signal "same word".
		"GetV2Profile": "get_v2_profile",
	} {
		if got := compiler.SnakeCase(in); got != want {
			t.Fatalf("SnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// buildFile constructs a protoreflect.FileDescriptor for a synthetic service
// with an annotated method, an excluded method, and a bare (unannotated)
// method, without touching any existing fixture or proto file.
func buildFile(t *testing.T, path string, fileOpts *descriptorpb.FileOptions) protoreflect.FileDescriptor {
	t.Helper()

	annotatedOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(annotatedOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "do_the_thing",
	})

	excludedOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(excludedOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Exclude: true,
	})

	// Annotated but with no explicit Name: exercises the fallback path
	// through Tools(), not just SnakeCase in isolation. The method name is
	// chosen so the derivation is non-trivial (word split, not a no-op).
	fallbackOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(fallbackOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Description: "no explicit name",
	})

	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String(path),
		Package: proto.String("compiler.testdata"),
		Syntax:  proto.String("proto3"),
		Options: fileOpts,
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Empty")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("TestService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Annotated"),
						InputType:  proto.String(".compiler.testdata.Empty"),
						OutputType: proto.String(".compiler.testdata.Empty"),
						Options:    annotatedOpts,
					},
					{
						Name:       proto.String("ExcludedMethod"),
						InputType:  proto.String(".compiler.testdata.Empty"),
						OutputType: proto.String(".compiler.testdata.Empty"),
						Options:    excludedOpts,
					},
					{
						Name:       proto.String("BareMethod"),
						InputType:  proto.String(".compiler.testdata.Empty"),
						OutputType: proto.String(".compiler.testdata.Empty"),
					},
					{
						Name:       proto.String("GetUserProfile"),
						InputType:  proto.String(".compiler.testdata.Empty"),
						OutputType: proto.String(".compiler.testdata.Empty"),
						Options:    fallbackOpts,
					},
				},
			},
		},
	}

	fd, err := protodesc.NewFile(fdProto, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile(%q): %v", path, err)
	}
	return fd
}

func TestToolsSkipsExcludedAndUnannotated(t *testing.T) {
	fd := buildFile(t, "compiler/testdata/methods.proto", nil)

	got, err := compiler.Tools([]protoreflect.FileDescriptor{fd})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Tools returned %d tools, want 2: %+v", len(got), got)
	}

	byMethod := map[protoreflect.Name]compiler.Tool{}
	for _, tool := range got {
		byMethod[tool.Method.Name()] = tool
	}

	explicit, ok := byMethod["Annotated"]
	if !ok {
		t.Fatalf("Tools() missing method Annotated: %+v", got)
	}
	if explicit.Name != "do_the_thing" {
		t.Fatalf("Annotated tool Name = %q, want %q (explicit ToolPolicy.Name)", explicit.Name, "do_the_thing")
	}

	// Fallback path: GetUserProfile carries a ToolPolicy with no explicit
	// Name, so Tools() must derive one via DefaultToolName/SnakeCase.
	fallback, ok := byMethod["GetUserProfile"]
	if !ok {
		t.Fatalf("Tools() missing method GetUserProfile (fallback-name case): %+v", got)
	}
	if fallback.Name != "get_user_profile" {
		t.Fatalf("GetUserProfile tool Name = %q, want %q (SnakeCase-derived fallback)", fallback.Name, "get_user_profile")
	}

	if _, leaked := byMethod["ExcludedMethod"]; leaked {
		t.Fatalf("excluded method leaked into Tools(): %+v", got)
	}
	if _, leaked := byMethod["BareMethod"]; leaked {
		t.Fatalf("unannotated method leaked into Tools(): %+v", got)
	}
}

// declFile builds a minimal file with no message and no service, declaring
// one extension's decls at the file level only — the shape a package's
// taxonomy commonly lives in (see proto/garm/demo/v1beta1/taxonomy.proto,
// "This file has no messages and no service"). Shared by loader_test.go and
// lint_test.go, both in this package.
func declFile(t *testing.T, path string, xt protoreflect.ExtensionType, decls ...*toolv1.Decl) protoreflect.FileDescriptor {
	t.Helper()
	opts := &descriptorpb.FileOptions{}
	proto.SetExtension(opts, xt, &toolv1.DeclSet{Declared: decls})
	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String(path),
		Package: proto.String("compiler.testdata"),
		Syntax:  proto.String("proto3"),
		Options: opts,
	}
	fd, err := protodesc.NewFile(fdProto, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile(%q): %v", path, err)
	}
	return fd
}

func declSetOpts(t *testing.T, names ...string) *descriptorpb.FileOptions {
	t.Helper()
	var decls []*toolv1.Decl
	for _, n := range names {
		decls = append(decls, &toolv1.Decl{Name: n})
	}
	opts := &descriptorpb.FileOptions{}
	proto.SetExtension(opts, toolv1.E_Compartments, &toolv1.DeclSet{Declared: decls})
	return opts
}

func TestDeclaredCompartmentsUnionsAndDeduplicates(t *testing.T) {
	fd1 := buildFile(t, "compiler/testdata/compartments_a.proto", declSetOpts(t, "financial", "pii-contact"))
	fd2 := buildFile(t, "compiler/testdata/compartments_b.proto", declSetOpts(t, "pii-contact", "support"))

	got := compiler.DeclaredCompartments([]protoreflect.FileDescriptor{fd1, fd2})

	seen := map[string]int{}
	for _, d := range got {
		seen[d.GetName()]++
	}

	want := map[string]int{"financial": 1, "pii-contact": 1, "support": 1}
	if len(seen) != len(want) {
		t.Fatalf("DeclaredCompartments = %+v, want names %v", got, want)
	}
	for name, count := range want {
		if seen[name] != count {
			t.Fatalf("DeclaredCompartments count for %q = %d, want %d (full: %+v)", name, seen[name], count, got)
		}
	}
}
