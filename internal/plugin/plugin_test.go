package plugin

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// buildPlugin assembles a *protogen.Plugin from one hand-built
// FileDescriptorProto, following the same in-Go-descriptors pattern
// toolgen's own lint fixtures use (see toolgen/lint_fixture_test.go) rather
// than shelling out to buf: it keeps these fixtures self-contained and out
// of the buf workspace. The dependency chain a CodeGeneratorRequest needs —
// google/protobuf/descriptor.proto (for the custom option extensions) and
// garm/tool/v1/tool.proto (for garm.v1.tool / field_policy) — comes from the
// already-linked generated packages, converted back to FileDescriptorProto.
func buildPlugin(t *testing.T, fdProto *descriptorpb.FileDescriptorProto, parameter string) *protogen.Plugin {
	t.Helper()
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{fdProto.GetName()},
		Parameter:      proto.String(parameter),
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
			protodesc.ToFileDescriptorProto(toolv1.File_garm_tool_v1_tool_proto),
			fdProto,
		},
	}
	gen, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.Options{}.New: %v", err)
	}
	return gen
}

// fixtureFile builds a single-message, single-service file: message M has
// one field ("secret", policy supplied by the caller) and a canary field
// ("visible", always public and always safe) so a fixture aimed at one rule
// doesn't incidentally trip L11 (every field of the output invisible). The
// service has one method, Get(M) returns (M), with the tool policy supplied
// by the caller (nil means "not a tool at all").
func fixtureFile(name string, fieldPolicy *toolv1.FieldPolicy, tool *toolv1.ToolPolicy) *descriptorpb.FileDescriptorProto {
	var secretOpts *descriptorpb.FieldOptions
	if fieldPolicy != nil {
		secretOpts = &descriptorpb.FieldOptions{}
		proto.SetExtension(secretOpts, toolv1.E_FieldPolicy, fieldPolicy)
	}
	visibleOpts := &descriptorpb.FieldOptions{}
	proto.SetExtension(visibleOpts, toolv1.E_FieldPolicy, &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
	})

	msg := &descriptorpb.DescriptorProto{
		Name: proto.String("M"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name: proto.String("secret"), Number: proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("secret"), Options: secretOpts,
			},
			{
				Name: proto.String("visible"), Number: proto.Int32(2),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("visible"), Options: visibleOpts,
			},
		},
	}

	var methodOpts *descriptorpb.MethodOptions
	if tool != nil {
		methodOpts = &descriptorpb.MethodOptions{}
		proto.SetExtension(methodOpts, toolv1.E_Tool, tool)
	}

	pkg := "protocgentoolplane.testdata." + name
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("protocgentoolplane/testdata/" + name + ".proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"garm/tool/v1/tool.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.com/" + name + ";" + name),
		},
		MessageType: []*descriptorpb.DescriptorProto{msg},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("S"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Get"),
				InputType:  proto.String("." + pkg + ".M"),
				OutputType: proto.String("." + pkg + ".M"),
				Options:    methodOpts,
			}},
		}},
	}
}

// buildMultiPlugin is buildPlugin, but for a CodeGeneratorRequest spanning
// several proto files, all marked to-generate — for exercising cross-file
// grouping (Finding 1: files sharing a go_package must collapse into one
// generated file, not redeclare `var Tools` once per proto file).
func buildMultiPlugin(t *testing.T, parameter string, fdProtos ...*descriptorpb.FileDescriptorProto) *protogen.Plugin {
	t.Helper()
	protoFiles := []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
		protodesc.ToFileDescriptorProto(toolv1.File_garm_tool_v1_tool_proto),
	}
	var toGenerate []string
	for _, fd := range fdProtos {
		toGenerate = append(toGenerate, fd.GetName())
		protoFiles = append(protoFiles, fd)
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: toGenerate,
		Parameter:      proto.String(parameter),
		ProtoFile:      protoFiles,
	}
	gen, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.Options{}.New: %v", err)
	}
	return gen
}

func validTool() *toolv1.ToolPolicy {
	return &toolv1.ToolPolicy{
		Name: "get", Description: "d",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	}
}

// TestGenerateFailsOnErrorDiagnostic: an unlabeled field (no field_policy,
// no message default) is an L1 error. The plugin must fail generation, not
// just warn about it — a build that passed anyway would silently disable
// the whole lint gate.
func TestGenerateFailsOnErrorDiagnostic(t *testing.T) {
	fd := fixtureFile("errcase", nil, validTool())
	gen := buildPlugin(t, fd, "")

	var stderr bytes.Buffer
	err := generate(gen, &stderr, genOptions{emit: "server"})
	if err == nil {
		t.Fatal("generate succeeded despite an L1 error-level diagnostic")
	}
	if !strings.Contains(stderr.String(), "L1") {
		t.Fatalf("stderr does not mention L1:\n%s", stderr.String())
	}
	if resp := gen.Response(); len(resp.GetFile()) != 0 {
		t.Fatalf("generate produced output despite failing: %v", resp.GetFile())
	}
}

// TestGenerateSucceedsOnWarningsOnly: write clearance below read clearance
// (L10) is a warning, not an error. The plugin must still produce output —
// otherwise the whole lint gate is stricter than its own rule table says.
func TestGenerateSucceedsOnWarningsOnly(t *testing.T) {
	fd := fixtureFile("warncase", &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_INTERNAL,
		Write:  toolv1.Clearance_CLEARANCE_PUBLIC, // below read: L10, warn-only
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
	}, validTool())
	gen := buildPlugin(t, fd, "")

	var stderr bytes.Buffer
	if err := generate(gen, &stderr, genOptions{emit: "server"}); err != nil {
		t.Fatalf("generate failed on a warning-only diagnostic: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "L10") {
		t.Fatalf("stderr does not mention L10:\n%s", stderr.String())
	}
	if resp := gen.Response(); len(resp.GetFile()) != 1 {
		t.Fatalf("generate produced %d files, want 1", len(resp.GetFile()))
	}
}

// TestGenerateProducesNoFileForFileWithNoTools: a file whose methods carry
// no (garm.tool.v1.tool) annotation contributes zero tools. Emitting an empty
// _garm.pb.go there would be dead weight sitting in every consumer's tree;
// the plugin must emit nothing at all instead.
func TestGenerateProducesNoFileForFileWithNoTools(t *testing.T) {
	fd := fixtureFile("notool", &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
	}, nil) // no tool policy at all
	gen := buildPlugin(t, fd, "")

	var stderr bytes.Buffer
	if err := generate(gen, &stderr, genOptions{emit: "server"}); err != nil {
		t.Fatalf("generate failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if resp := gen.Response(); len(resp.GetFile()) != 0 {
		t.Fatalf("generate produced output for a file with no tools: %v", resp.GetFile())
	}
}

// TestGenerateFailsOnConflictingCompartmentDeclarations reproduces the
// review finding verbatim: two files in the SAME proto package (taxonomy.proto
// and a sibling), one declaring "shared" as "Alpha's meaning" and the other
// as "A COMPLETELY DIFFERENT meaning". Before L29
// (internal/toolgen/lint.go), generate() had no rule watching for this and
// silently kept whichever description DeclaredCompartments' internal dedupe
// saw first — exit 0, wrong description compiled into the package's own
// Compartments var. Lint already gates every non-Warn diagnostic
// (dedupeDiags(compiler.Lint(fds)) above), so L29 alone closes the gap; this
// pins that generate() actually refuses.
func TestGenerateFailsOnConflictingCompartmentDeclarations(t *testing.T) {
	const name = "sharedconflict"
	pkg := "protocgentoolplane.testdata." + name
	goPkg := "example.com/" + name + ";" + name

	// fileA: no message, no service — taxonomy.proto's own shape — carrying
	// only the (now conflicting) file-level compartment declaration.
	fileA := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("protocgentoolplane/testdata/" + name + "_taxonomy.proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"garm/tool/v1/tool.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String(goPkg)},
	}
	proto.SetExtension(fileA.Options, toolv1.E_Compartments, &toolv1.DeclSet{
		Declared: []*toolv1.Decl{{Name: "shared", Description: "Alpha's meaning"}},
	})

	// fileB: a real tool-bearing sibling in the SAME package/go_package,
	// declaring "shared" differently.
	fileB := fixtureFile(name, &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
	}, validTool())
	proto.SetExtension(fileB.Options, toolv1.E_Compartments, &toolv1.DeclSet{
		Declared: []*toolv1.Decl{{Name: "shared", Description: "A COMPLETELY DIFFERENT meaning"}},
	})

	gen := buildMultiPlugin(t, "", fileA, fileB)
	var stderr bytes.Buffer
	err := generate(gen, &stderr, genOptions{emit: "server"})
	if err == nil {
		t.Fatalf("generate succeeded despite two files in one package declaring "+
			"\"shared\" with different descriptions; stderr:\n%s", stderr.String())
	}
	for _, want := range []string{
		"L29", "shared", name + "_taxonomy.proto", name + ".proto",
		"Alpha's meaning", "A COMPLETELY DIFFERENT meaning",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr does not mention %q:\n%s", want, stderr.String())
		}
	}
	if resp := gen.Response(); len(resp.GetFile()) != 0 {
		t.Fatalf("generate produced output despite the conflict: %v", resp.GetFile())
	}
}

// TestGenerateToolsdkEmitsMicroFileWithContractVersion drives the
// emit=toolsdk branch, which every other test in this file bypasses (they
// all pass genOptions{emit: "server"}). Nothing else in this package
// exercises the <pkg>_micro.pb.go filename construction or the threading
// of contract_version from the plugin parameter through to EmitMicro's
// ContractVersion constant — `make gen` run twice would eventually catch a
// break here (a tracked diff, or the wrong file left untracked), but only
// late and with a confusing message. This turns that into an immediate,
// local failure.
func TestGenerateToolsdkEmitsMicroFileWithContractVersion(t *testing.T) {
	fd := fixtureFile("toolsdkcase", &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
	}, validTool())
	gen := buildPlugin(t, fd, "")

	var stderr bytes.Buffer
	if err := generate(gen, &stderr, genOptions{emit: "toolsdk", contractVersion: "vtest"}); err != nil {
		t.Fatalf("generate failed: %v\nstderr:\n%s", err, stderr.String())
	}

	resp := gen.Response()
	if len(resp.GetFile()) != 1 {
		t.Fatalf("got %d generated files, want 1", len(resp.GetFile()))
	}
	got := resp.GetFile()[0]
	if !strings.HasSuffix(got.GetName(), "_micro.pb.go") {
		t.Fatalf("emit=toolsdk generated filename %q does not end in _micro.pb.go", got.GetName())
	}
	if !strings.Contains(got.GetContent(), `const ContractVersion = "vtest"`) {
		t.Fatalf("generated source does not thread contract_version through to "+
			"ContractVersion:\n%s", got.GetContent())
	}
}

func TestDedupeDiags(t *testing.T) {
	d := compiler.Diag{Rule: "L1", Path: "a.b", Msg: "no field policy and no message default"}
	got := dedupeDiags([]compiler.Diag{d, d, d})
	if len(got) != 1 {
		t.Fatalf("dedupeDiags collapsed 3 identical diagnostics to %d entries, want 1", len(got))
	}
	other := compiler.Diag{Rule: "L2", Path: "a.b", Msg: "different rule, same path"}
	got = dedupeDiags([]compiler.Diag{d, other, d})
	if len(got) != 2 {
		t.Fatalf("dedupeDiags = %d entries, want 2 (one duplicate collapsed, one distinct kept)",
			len(got))
	}
}

// TestGenerateGroupsFilesSharingGoPackage is Finding 1's regression test:
// two proto files, each with their own service and tool, sharing one
// go_package, must produce exactly ONE generated file — with `var Tools`
// declared once, covering both services' tools — not two files that both
// declare `var Tools` and the same compartment constants, which `go build`
// would reject as a redeclaration.
func TestGenerateGroupsFilesSharingGoPackage(t *testing.T) {
	build := func(protoName, svcName string) *descriptorpb.FileDescriptorProto {
		fieldOpts := &descriptorpb.FieldOptions{}
		proto.SetExtension(fieldOpts, toolv1.E_FieldPolicy, &toolv1.FieldPolicy{
			Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
			OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
		})
		msg := &descriptorpb.DescriptorProto{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("id"), Number: proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("id"), Options: fieldOpts,
			}},
		}
		methodOpts := &descriptorpb.MethodOptions{}
		proto.SetExtension(methodOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
			Name: strings.ToLower(svcName), Description: "d",
			Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
		})
		pkg := "protocgentoolplane.testdata.shared." + strings.ToLower(svcName)
		return &descriptorpb.FileDescriptorProto{
			Name:       proto.String(protoName),
			Package:    proto.String(pkg),
			Syntax:     proto.String("proto3"),
			Dependency: []string{"garm/tool/v1/tool.proto"},
			Options: &descriptorpb.FileOptions{
				// Both files declare the SAME go_package: this is the shape
				// proto/garm/v1's control.proto and generation.proto already
				// have today (one go_package, two proto files) — latent only
				// because neither carries a tool annotation yet.
				GoPackage: proto.String("example.com/shared;shared"),
			},
			MessageType: []*descriptorpb.DescriptorProto{msg},
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: proto.String(svcName),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Get"),
					InputType:  proto.String("." + pkg + ".M"),
					OutputType: proto.String("." + pkg + ".M"),
					Options:    methodOpts,
				}},
			}},
		}
	}

	fd1 := build("protocgentoolplane/testdata/shared1.proto", "S1")
	fd2 := build("protocgentoolplane/testdata/shared2.proto", "S2")

	gen := buildMultiPlugin(t, "", fd1, fd2)
	var stderr bytes.Buffer
	if err := generate(gen, &stderr, genOptions{emit: "server"}); err != nil {
		t.Fatalf("generate failed: %v\nstderr:\n%s", err, stderr.String())
	}

	resp := gen.Response()
	if len(resp.GetFile()) != 1 {
		var names []string
		for _, f := range resp.GetFile() {
			names = append(names, f.GetName())
		}
		t.Fatalf("got %d generated files %v, want exactly 1 for two proto files "+
			"sharing one go_package", len(resp.GetFile()), names)
	}

	src := resp.GetFile()[0].GetContent()
	if n := strings.Count(src, "var Tools = []toolplane.ToolDef{"); n != 1 {
		t.Fatalf("generated source declares `var Tools` %d times, want exactly 1:\n%s", n, src)
	}
	// Matched with the gofmt alignment normalised away: the emitted struct
	// literal is gofmt'd, so the run of spaces after "Name:" is a function
	// of the LONGEST field name in the literal. Asserting on the literal
	// spacing made this test fail the moment ToolDef gained a longer field,
	// which is drift in the fixture's formatting, not in what it is testing.
	flat := strings.Join(strings.Fields(src), " ")
	for _, want := range []string{
		"func RegisterS1(", "func RegisterS2(",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated source missing %q:\n%s", want, src)
		}
	}
	for _, want := range []string{`Name: "s1"`, `Name: "s2"`} {
		if !strings.Contains(flat, want) {
			t.Fatalf("generated source missing %q:\n%s", want, src)
		}
	}
}
