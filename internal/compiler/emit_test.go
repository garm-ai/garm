package compiler_test

import (
	"go/parser"
	"go/token"
	"path"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// buildEmitPlugin mirrors cmd/protoc-gen-garm-tools's own test helper: a
// hand-built FileDescriptorProto wrapped in a CodeGeneratorRequest, resolved
// against the descriptor.proto/policy.proto dependency chain those custom
// options need. Kept local to this package (rather than shared with
// cmd/protoc-gen-garm-tools's tests) since the two packages cannot import
// each other's test-only helpers.
func buildEmitPlugin(t *testing.T, fdProto *descriptorpb.FileDescriptorProto) *protogen.Plugin {
	t.Helper()
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{fdProto.GetName()},
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

// emitFixture builds a file with two messages (Req, M) and one service S
// with a single annotated tool method Get(Req) returns (M).
func emitFixture(goPackage string) *descriptorpb.FileDescriptorProto {
	fieldPolicy := func() *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, toolv1.E_FieldPolicy, &toolv1.FieldPolicy{
			Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
			OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
		})
		return o
	}

	reqMsg := &descriptorpb.DescriptorProto{
		Name: proto.String("Req"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("id"), Number: proto.Int32(1),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String("id"), Options: fieldPolicy(),
		}},
	}
	respMsg := &descriptorpb.DescriptorProto{
		Name: proto.String("M"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("id"), Number: proto.Int32(1),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String("id"), Options: fieldPolicy(),
		}},
	}

	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "get", Description: "gets things",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	})

	pkg := "compiler.testdata.emit"
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("compiler/testdata/emit/fixture.proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"garm/tool/v1/tool.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String(goPackage),
		},
		MessageType: []*descriptorpb.DescriptorProto{reqMsg, respMsg},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("S"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Get"),
				InputType:  proto.String("." + pkg + ".Req"),
				OutputType: proto.String("." + pkg + ".M"),
				Options:    methodOpts,
			}},
		}},
	}
}

// emitForTest runs Emit against the fixture file with the given compartment
// and set declarations (and no tools beyond the fixture's own), returning
// the emitted source as a string.
func emitForTest(t *testing.T, compartments, sets []*toolv1.Decl) string {
	t.Helper()
	return emitFileForTest(t, emitFixture("example.com/emit;emit"), compartments, sets)
}

// emitFileForTest is emitForTest over an arbitrary hand-built file, for the
// fixtures (two services, say) the single-service emitFixture cannot express.
func emitFileForTest(
	t *testing.T, fd *descriptorpb.FileDescriptorProto, compartments, sets []*toolv1.Decl,
) string {
	t.Helper()
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, compartments, sets); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return gen.Response().GetFile()[0].GetContent()
}

// TestEmitsDeclarationSetsAsData pins that the generated package exposes the
// declaration SET, not just names. Config.Compartments takes
// []*toolv1.Decl, and a hand-typed copy of it drifting by one name silently
// re-governs: bits are positional over the sorted set, so dropping a name
// shifts every bit above it.
func TestEmitsDeclarationSetsAsData(t *testing.T) {
	src := emitForTest(t, []*toolv1.Decl{
		{Name: "financial", Description: "Money movement"},
		{Name: "pii-contact"},
	}, []*toolv1.Decl{{Name: "support"}})

	// The generated file's alias for garmv1 is "v1" (as g.QualifiedGoIdent
	// assigns it — the same alias already visible in every existing literal
	// below and in TestEmitCarriesDeclaredGovernance's "v1.Approval_..."
	// assertions), not "garmv1".
	for _, want := range []string{
		"var Compartments = []*v1.Decl{",
		`{Name: "financial", Description: "Money movement"},`,
		`{Name: "pii-contact"},`,
		"var ToolSets = []*v1.Decl{",
		`{Name: "support"},`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q:\n%s", want, src)
		}
	}
	// The constants stay — they are useful and something may reference them.
	// gofmt aligns the "=" column to the longest name in the const block, so
	// compare with interior whitespace collapsed (as TestEmitCarriesDeclared
	// Governance does below).
	flat := strings.Join(strings.Fields(src), " ")
	if !strings.Contains(flat, `CompartmentFinancial = "financial"`) {
		t.Error("string constants were dropped; they are additive, not replaced")
	}
}

// TestEmitProducesValidCompilableSource builds a real *protogen.File and
// tool set from a hand-built descriptor and runs Emit end to end, checking
// the emitted text both parses as Go and contains the declarations the rest
// of the codebase (and generated call sites) depend on by name.
func TestEmitProducesValidCompilableSource(t *testing.T) {
	fd := emitFixture("example.com/emit;emit")
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	compartments := []*toolv1.Decl{{Name: "financial"}}
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, compartments, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	resp := gen.Response()
	if len(resp.GetFile()) != 1 {
		t.Fatalf("got %d generated files, want 1", len(resp.GetFile()))
	}
	src := resp.GetFile()[0].GetContent()

	for _, want := range []string{
		"package emit",
		"CompartmentFinancial = \"financial\"",
		"var Tools = []toolplane.ToolDef{",
		"func RegisterS(",
		"func toolsForS() []toolplane.ToolDef {",
		"srv.MountService(func(opts ...connect.HandlerOption) (string, http.Handler) {",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated source missing %q:\n%s", want, src)
		}
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "generated.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated source does not parse as Go: %v\n%s", err, src)
	}
}

// TestEmitFailsClosedOnCrossPackageType exercises goTypeFor's failure path:
// a tool whose request type is declared in a DIFFERENT Go package
// (garm.tool.v1.ToolPolicy, from the policy.proto dependency) rather than the
// package being generated. This codebase never does this today (every
// tool's Input and Output lives in the same Go package as its service), but
// Emit must fail loudly rather than silently emit a reference to nothing if
// that ever changes.
func TestEmitFailsClosedOnCrossPackageType(t *testing.T) {
	fd := emitFixture("example.com/emit;emit")
	fd.Service[0].Method[0].InputType = proto.String(".garm.tool.v1.ToolPolicy")

	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	// Tools() itself doesn't resolve the type, so this still finds the tool;
	// the failure belongs to Emit/goTypeFor.
	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %v, %v; want exactly one tool", tools, err)
	}

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	err = compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, nil, nil)
	if err == nil {
		t.Fatal("Emit succeeded despite an input type absent from the file")
	}
	if !strings.Contains(err.Error(), "cross-package") {
		t.Fatalf("error does not explain the cross-package limitation: %v", err)
	}
}

// TestEmitCarriesDeclaredGovernance pins the three fields codegen used to
// drop on the floor.
//
// ToolDef carried no approval, audit or authorization, so L14 and L21
// FORCED a destructive tool's author to declare MODE_GRANT and LEVEL_AUDIT
// with fail_closed, the build passed, and the tool then ran ungated with no
// grant check and no audit record. Emitting them is what lets
// toolplane.mount refuse; without this test, an emission that quietly
// stopped writing them would restore the silent gap and nothing would fail.
func TestEmitCarriesDeclaredGovernance(t *testing.T) {
	fd := emitFixture("example.com/emit;emit")
	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "wipe", Description: "wipes things",
		Verb:         toolv1.Verb_VERB_DESTRUCTIVE,
		MinClearance: toolv1.Clearance_CLEARANCE_RESTRICTED,
		Approval:     &toolv1.Approval{Mode: toolv1.Approval_MODE_GRANT},
		Audit: &toolv1.Audit{
			Level: toolv1.Audit_LEVEL_AUDIT, FailClosed: true, RetainDays: 365,
		},
		Authorization: &toolv1.Authorization{
			Backend: &toolv1.Authorization_Fga{Fga: &toolv1.Fga{
				Relation: "owner",
				Object:   &toolv1.Fga_FromRequestField{FromRequestField: "id"},
			}},
		},
	})
	fd.Service[0].Method[0].Options = methodOpts

	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]
	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %v, %v; want exactly one tool", tools, err)
	}

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, nil, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	// Alignment-insensitive: the emitted literal is gofmt'd, so the spacing
	// after a field name depends on the longest name in the struct.
	src := gen.Response().GetFile()[0].GetContent()
	flat := strings.Join(strings.Fields(src), " ")

	for _, want := range []string{
		"ApprovalMode: v1.Approval_MODE_GRANT",
		"AuditLevel: v1.Audit_LEVEL_AUDIT",
		"HasAuthorization: true",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("generated source missing %q; declared governance was dropped:\n%s", want, src)
		}
	}
}

// TestEmitReportsNoAuthorizationWhenNoneIsDeclared is the negative case:
// HasAuthorization must reflect the absence of the block, not default to
// true (which would make every tool unmountable) and not be omitted (which
// in Go's zero-value world is the same as declaring nothing, but must be
// true by the emitter's intent rather than by accident).
func TestEmitReportsNoAuthorizationWhenNoneIsDeclared(t *testing.T) {
	fd := emitFixture("example.com/emit;emit")
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]
	tools, _ := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, nil, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	flat := strings.Join(strings.Fields(gen.Response().GetFile()[0].GetContent()), " ")
	if !strings.Contains(flat, "HasAuthorization: false") {
		t.Fatalf("expected HasAuthorization: false for a tool with no authorization block:\n%s", flat)
	}
}

// TestEmitCarriesFQN pins the globally-unique tool identity.
//
// ToolDef.Name is unique only within one proto package: L3's duplicate
// check lives in a single Lint call, and buf invokes this plugin once per
// proto package, so nothing ever compares names across packages. FQN
// prefixes the name with the proto package — globally unique by
// construction — so two teams can both declare get_order without one
// silently shadowing the other in any downstream catalogue.
func TestEmitCarriesFQN(t *testing.T) {
	fd := emitFixture("example.com/emit;emit")
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %v, %v; want exactly one tool", tools, err)
	}

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+"_garm.pb.go", f.GoImportPath)
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, nil, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := gen.Response().GetFile()[0].GetContent()

	// The fixture's proto package is compiler.testdata.emit and its tool
	// name is "get".
	// gofmt aligns the struct literal's field values, so compare against
	// the source with all horizontal whitespace removed.
	flat := strings.NewReplacer("\t", "", " ", "").Replace(src)
	const wantFQN = `FQN:"compiler.testdata.emit.get"`
	if !strings.Contains(flat, wantFQN) {
		t.Fatalf("generated source missing %s:\n%s", wantFQN, src)
	}
	// Name must survive unchanged: it is the short model-facing name, and
	// FullMethod must too — it is the runtime dispatch identity. FQN is an
	// addition, not a replacement for either.
	for _, want := range []string{`Name:"get"`, `FullMethod:"/compiler.testdata.emit.S/Get"`} {
		if !strings.Contains(flat, want) {
			t.Fatalf("generated source missing %s:\n%s", want, src)
		}
	}
}

// TestFQNSplitsAtLastDot pins the documented parse rule: everything before
// the last dot is the proto package, everything after is the tool name.
// L3's charset (toolNameRE) forbids dots in a tool name, so the last dot is
// always the separator however deeply nested the proto package is.
func TestFQNSplitsAtLastDot(t *testing.T) {
	for _, fqn := range []string{
		"garm.demo.v1beta1.get_account_summary",
		"a.b",
	} {
		i := strings.LastIndex(fqn, ".")
		if i < 0 {
			t.Fatalf("FQN %q has no dot", fqn)
		}
		pkg, name := fqn[:i], fqn[i+1:]
		if strings.Contains(name, ".") {
			t.Fatalf("FQN %q: name %q contains a dot", fqn, name)
		}
		if pkg == "" || name == "" {
			t.Fatalf("FQN %q split into %q, %q", fqn, pkg, name)
		}
	}
}

// twoServiceFile builds a file carrying TWO services, each with one
// annotated tool method over the same Req/M pair.
//
// Two services in one package is the case that matters for Mount: one
// service would not exercise the parameter ORDER, and the order is what
// `make gen` idempotency (and the aggregate index's positional call into
// this Mount) depends on.
func twoServiceFile(t *testing.T, services ...string) *descriptorpb.FileDescriptorProto {
	t.Helper()
	fd := emitFixture("example.com/emit;emit")
	pkg := fd.GetPackage()
	fd.Service = nil
	for i, svc := range services {
		methodOpts := &descriptorpb.MethodOptions{}
		proto.SetExtension(methodOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
			Name:         compiler.SnakeCase(svc) + "_get",
			Description:  "gets things",
			Verb:         toolv1.Verb_VERB_READ,
			MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
		})
		fd.Service = append(fd.Service, &descriptorpb.ServiceDescriptorProto{
			Name: proto.String(svc),
			Method: []*descriptorpb.MethodDescriptorProto{{
				// Distinct method names so the emitted resolver closures are
				// distinguishable in the output even when two services would
				// otherwise both declare Get.
				Name:       proto.String("Get" + string(rune('A'+i))),
				InputType:  proto.String("." + pkg + ".Req"),
				OutputType: proto.String("." + pkg + ".M"),
				Options:    methodOpts,
			}},
		})
	}
	return fd
}

// TestEmitsTypedMount is the task's headline: a generated Mount with one
// TYPED parameter per service, so that forgetting a service is a compile
// error rather than a runtime Unimplemented.
//
// Today every service implementation may embed UnimplementedXServiceHandler,
// so a forgotten handler compiles, mounts, is governed, and answers
// Unimplemented on the first real call. Mount's parameter list is what moves
// that failure to build time.
func TestEmitsTypedMount(t *testing.T) {
	src := emitFileForTest(t, twoServiceFile(t, "AccountsService", "PaymentsService"), nil, nil)

	for _, want := range []string{
		"func Mount(",
		"c *toolplane.Core,",
		"accounts emitconnect.AccountsServiceHandler,",
		"payments emitconnect.PaymentsServiceHandler,",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q:\n%s", want, src)
		}
	}

	// The ORDER, not just the presence: the aggregate index calls this
	// Mount positionally, and both parameters have distinct types, so a
	// reordering here is a compile error over there rather than a silent
	// swap — but it is still a `make gen` diff on every run if the order is
	// not deterministic. Compare against the whole signature with interior
	// whitespace collapsed, which is insensitive to gofmt's line breaks.
	const wantSig = "func Mount( c *toolplane.Core, " +
		"accounts emitconnect.AccountsServiceHandler, " +
		"payments emitconnect.PaymentsServiceHandler, ) error {"

	// Re-emitted, not read once: with two services, an order that came from
	// map iteration would satisfy a single check about half the time and
	// dirty the tree on every other `make gen`. Eight runs makes that a
	// failing test rather than an intermittent diff.
	for i := 0; i < 8; i++ {
		again := emitFileForTest(t, twoServiceFile(t, "AccountsService", "PaymentsService"), nil, nil)
		if i == 0 && again != src {
			t.Fatalf("two emissions of the same input differ:\n--- a ---\n%s\n--- b ---\n%s",
				src, again)
		}
		flat := strings.Join(strings.Fields(again), " ")
		if !strings.Contains(flat, wantSig) {
			t.Fatalf("run %d: Mount's signature is not %q:\n%s", i, wantSig, again)
		}
	}
}

// TestParamName pins the service-name-to-parameter-name rule.
//
// ParamName is exported so `garmdev gen toolindex` names the aggregate
// Mount's parameters the same way protoc-gen-garm-tools names each
// package's — across a binary boundary, with no shared call to compare
// against. The cases below are the branches the demo and testkit fixtures
// never reach: every service in this tree is PascalCase with a "Service"
// suffix and no initialism, so nothing else exercises the leading-capital
// run at all.
func TestParamName(t *testing.T) {
	for _, c := range []struct{ service, want string }{
		{"AccountsService", "accounts"},
		{"PaymentsService", "payments"},
		// A trailing "Service" is stripped, but a service named exactly
		// "Service" must not become the empty string.
		{"Service", "service"},
		{"S", "s"},
		// A whole-name initialism lower-cases entirely...
		{"HTTPService", "http"},
		// ...but an initialism followed by another word keeps that word's
		// capital, because the run's LAST capital starts it.
		{"GRPCGateway", "grpcGateway"},
		{"HTTPServer", "httpServer"},
		// ParamName itself does not care about Go keywords; that is
		// MountParamNames' job, and TestMountParamNames covers it.
		{"FuncService", "func"},
	} {
		if got := compiler.ParamName(c.service); got != c.want {
			t.Errorf("ParamName(%q) = %q, want %q", c.service, got, c.want)
		}
	}
}

// TestMountParamNames pins the two branches ParamName does not have: a name
// that would not compile, and a name already taken.
//
// Both are unreachable from this repo's own protos, and both produce code
// rather than an error — a parameter named `func`, or two parameters named
// `accounts`, is a generated file that does not build, in someone else's
// tree, with nothing pointing back at the generator.
func TestMountParamNames(t *testing.T) {
	for _, c := range []struct {
		name     string
		services []string
		want     []string
	}{
		{
			name:     "the ordinary case passes through untouched",
			services: []string{"AccountsService", "PaymentsService"},
			want:     []string{"accounts", "payments"},
		},
		{
			name: "a Go keyword is suffixed rather than emitted",
			// "FuncService" -> "func", which is not an identifier.
			services: []string{"FuncService"},
			want:     []string{"func2"},
		},
		{
			name: "an import alias the emitted body uses is reserved too",
			// "toolplane" is the alias Mount's own signature resolves
			// through; a parameter taking it would shadow the package.
			services: []string{"ToolplaneService", "ConnectService"},
			want:     []string{"toolplane2", "connect2"},
		},
		{
			name: "two services reducing to one name are disambiguated",
			// The aggregate index flattens every package's services into
			// one list, so two packages' AccountsService meet here.
			services: []string{"AccountsService", "AccountsService"},
			want:     []string{"accounts", "accounts2"},
		},
		{
			name: "and the disambiguator keeps going when its own answer collides",
			// "accounts" and "accounts2" are both taken by the time the
			// third arrives already calling itself "accounts2".
			services: []string{"AccountsService", "AccountsService", "Accounts2Service"},
			want:     []string{"accounts", "accounts2", "accounts22"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := compiler.MountParamNames(c.services)
			if len(got) != len(c.want) {
				t.Fatalf("MountParamNames(%v) = %v, want %v", c.services, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("MountParamNames(%v) = %v, want %v", c.services, got, c.want)
				}
			}
			// Whatever the names are, they must be usable as parameters:
			// distinct, and valid Go identifiers that are not keywords.
			seen := map[string]bool{}
			for _, n := range got {
				if seen[n] {
					t.Errorf("duplicate parameter name %q in %v", n, got)
				}
				seen[n] = true
				if !token.IsIdentifier(n) || token.Lookup(n).IsKeyword() {
					t.Errorf("%q is not usable as a Go parameter name", n)
				}
			}
		})
	}
}

// toolsForTest builds the tool set for a hand-made file, the same way the
// plugin does.
func toolsForTest(t *testing.T, fd *descriptorpb.FileDescriptorProto) []compiler.Tool {
	t.Helper()
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]
	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	return tools
}

// TestServicesInOrder pins the grouping both generators share.
//
// `garmdev gen toolindex` calls this to reproduce a package's Mount
// parameter order from the descriptor set, and the aggregate Mount it emits
// then calls that Mount positionally. There is no compile-time backstop for
// a disagreement — Go interface assignability is structural, so two services
// with the same method names over the same messages have identical handler
// interfaces and swap silently — so this ordering is the guarantee itself.
func TestServicesInOrder(t *testing.T) {
	fd := twoServiceFile(t, "AccountsService", "PaymentsService", "IdentityService")
	// Give the first service a second tool, so the test covers grouping and
	// not only ordering: one tool per service would pass even if the
	// function returned the tools rather than grouping them.
	extra := &descriptorpb.MethodOptions{}
	proto.SetExtension(extra, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "accounts_service_second", Description: "gets more things",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	})
	pkg := fd.GetPackage()
	fd.Service[0].Method = append(fd.Service[0].Method, &descriptorpb.MethodDescriptorProto{
		Name:       proto.String("GetSecond"),
		InputType:  proto.String("." + pkg + ".Req"),
		OutputType: proto.String("." + pkg + ".M"),
		Options:    extra,
	})

	tools := toolsForTest(t, fd)
	if len(tools) != 4 {
		t.Fatalf("fixture produced %d tools, want 4", len(tools))
	}

	// Repeated, because the failure this pins is a map iteration that
	// happens to come out right once.
	for i := 0; i < 8; i++ {
		order, byService := compiler.ServicesInOrder(tools)
		want := []string{"AccountsService", "PaymentsService", "IdentityService"}
		if len(order) != len(want) {
			t.Fatalf("run %d: order = %v, want %v", i, order, want)
		}
		for j := range order {
			if order[j] != want[j] {
				t.Fatalf("run %d: order = %v, want %v (declaration order, not sorted)",
					i, order, want)
			}
		}
		if n := len(byService["AccountsService"]); n != 2 {
			t.Fatalf("run %d: AccountsService has %d tools, want 2; the grouping "+
				"dropped one", i, n)
		}
		for _, svc := range []string{"PaymentsService", "IdentityService"} {
			if n := len(byService[svc]); n != 1 {
				t.Fatalf("run %d: %s has %d tools, want 1", i, svc, n)
			}
		}
	}
}

// microAccountsFixture builds a file shaped like the real
// proto/garm/demo/v1beta1/accounts.proto: one service (AccountsService) with
// two tools (get_account_summary, search_transactions), a nested message
// (AccountSummary) and a repeated one (Transaction), so
// TestMicroEmitsContractIdentityAndToolRefs' DescriptorHash has something to
// walk transitively.
//
// The go_package's import path deliberately ENDS in "demov1beta1" — not
// "v1beta1", which is what the real contracts package's path ends in — so
// that protogen.QualifiedGoIdent's default cross-package alias (derived from
// path.Base of the import path, not the declared local package name; see
// protogen.Options.New) comes out as "demov1beta1", matching the real base
// package's own declared Go package name and the identifiers
// emit_micro_test.go asserts on.
func microAccountsFixture(protoPackage string) *descriptorpb.FileDescriptorProto {
	strField := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
		}
	}
	msgField := func(name string, num int32, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
		label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		if repeated {
			label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		}
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(num),
			Label:    label.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String("." + protoPackage + "." + typeName),
			JsonName: proto.String(name),
		}
	}

	accountSummary := &descriptorpb.DescriptorProto{
		Name:  proto.String("AccountSummary"),
		Field: []*descriptorpb.FieldDescriptorProto{strField("account_id", 1)},
	}
	transaction := &descriptorpb.DescriptorProto{
		Name:  proto.String("Transaction"),
		Field: []*descriptorpb.FieldDescriptorProto{strField("transaction_id", 1)},
	}
	getReq := &descriptorpb.DescriptorProto{
		Name:  proto.String("GetAccountSummaryRequest"),
		Field: []*descriptorpb.FieldDescriptorProto{strField("account_id", 1)},
	}
	getRes := &descriptorpb.DescriptorProto{
		Name:  proto.String("GetAccountSummaryResponse"),
		Field: []*descriptorpb.FieldDescriptorProto{msgField("summary", 1, "AccountSummary", false)},
	}
	searchReq := &descriptorpb.DescriptorProto{
		Name:  proto.String("SearchTransactionsRequest"),
		Field: []*descriptorpb.FieldDescriptorProto{strField("account_id", 1)},
	}
	searchRes := &descriptorpb.DescriptorProto{
		Name:  proto.String("SearchTransactionsResponse"),
		Field: []*descriptorpb.FieldDescriptorProto{msgField("transactions", 1, "Transaction", true)},
	}

	getOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(getOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "get_account_summary", Description: "gets an account summary",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_INTERNAL,
	})
	searchOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(searchOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "search_transactions", Description: "searches transactions",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_INTERNAL,
	})

	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("compiler/testdata/micro/accounts.proto"),
		Package:    proto.String(protoPackage),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"garm/tool/v1/tool.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.com/demov1beta1;demov1beta1"),
		},
		MessageType: []*descriptorpb.DescriptorProto{
			accountSummary, transaction, getReq, getRes, searchReq, searchRes,
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("AccountsService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       proto.String("GetAccountSummary"),
					InputType:  proto.String("." + protoPackage + ".GetAccountSummaryRequest"),
					OutputType: proto.String("." + protoPackage + ".GetAccountSummaryResponse"),
					Options:    getOpts,
				},
				{
					Name:       proto.String("SearchTransactions"),
					InputType:  proto.String("." + protoPackage + ".SearchTransactionsRequest"),
					OutputType: proto.String("." + protoPackage + ".SearchTransactionsResponse"),
					Options:    searchOpts,
				},
			},
		}},
	}
}

// renderMicroFor runs compiler.EmitMicro over a fixture shaped like the demo
// AccountsService and returns the generated source. protoPackage becomes
// both the fixture's proto package (so FQN/Subject strings read exactly as
// the real demo's would) and the seed for microAccountsFixture's synthetic
// go_package.
func renderMicroFor(t *testing.T, protoPackage string) string {
	t.Helper()
	return renderMicroFromFD(t, microAccountsFixture(protoPackage))
}

// renderMicroFromFD is renderMicroFor's shared plumbing, factored out so a
// test that needs a fixture shape renderMicroFor's own (fixed) one cannot
// express — see hashFixture below — can still drive compiler.EmitMicro
// end to end without repeating the *protogen.Plugin/GeneratedFile setup.
func renderMicroFromFD(t *testing.T, fd *descriptorpb.FileDescriptorProto) string {
	t.Helper()
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	outPkg := f.GoPackageName + protogen.GoPackageName("micro")
	outImportPath := protogen.GoImportPath(path.Join(string(f.GoImportPath), string(outPkg)))
	filename := path.Join(path.Dir(f.GeneratedFilenamePrefix), string(outPkg), string(outPkg)) + "_micro.pb.go"

	g := gen.NewGeneratedFile(filename, outImportPath)
	if err := compiler.EmitMicro(g, []*protogen.File{f}, outPkg, tools, "dev"); err != nil {
		t.Fatalf("EmitMicro: %v", err)
	}
	return gen.Response().GetFile()[0].GetContent()
}

// hashFixture builds a minimal one-tool, one-field fixture for pinning
// DescriptorHash's exclusion/inclusion boundary: message Req has exactly
// one field ("value"), whose field NUMBER, TYPE (kind), field_policy
// OPTION and leading COMMENT are each independently overridable here, so a
// hash test can hold every other dimension fixed and vary exactly one.
//
// fieldNumber and fieldType are wire-relevant — DescriptorHash must move
// when either changes. withFieldPolicy and comment are not — they are
// exactly the two things the brief says DescriptorHash must exclude (a
// garm.v1.field_policy annotation, and a comment), so DescriptorHash must
// NOT move when either changes alone.
func hashFixture(
	protoPackage string, fieldNumber int32, fieldType descriptorpb.FieldDescriptorProto_Type,
	withFieldPolicy bool, comment string,
) *descriptorpb.FileDescriptorProto {
	var fieldOpts *descriptorpb.FieldOptions
	if withFieldPolicy {
		fieldOpts = &descriptorpb.FieldOptions{}
		proto.SetExtension(fieldOpts, toolv1.E_FieldPolicy, &toolv1.FieldPolicy{
			Read:   toolv1.Clearance_CLEARANCE_RESTRICTED,
			OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Omit{Omit: &toolv1.Omit{}}},
		})
	}

	req := &descriptorpb.DescriptorProto{
		Name: proto.String("Req"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("value"), Number: proto.Int32(fieldNumber),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     fieldType.Enum(),
			JsonName: proto.String("value"), Options: fieldOpts,
		}},
	}
	res := &descriptorpb.DescriptorProto{
		Name: proto.String("Res"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("ok"), Number: proto.Int32(1),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
			JsonName: proto.String("ok"),
		}},
	}

	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, toolv1.E_Tool, &toolv1.ToolPolicy{
		Name: "get", Description: "d",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	})

	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("compiler/testdata/hash/fixture.proto"),
		Package:    proto.String(protoPackage),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"garm/tool/v1/tool.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.com/hashfixture;hashfixture"),
		},
		MessageType: []*descriptorpb.DescriptorProto{req, res},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("S"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Get"),
				InputType:  proto.String("." + protoPackage + ".Req"),
				OutputType: proto.String("." + protoPackage + ".Res"),
				Options:    methodOpts,
			}},
		}},
	}
	if comment != "" {
		fd.SourceCodeInfo = &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{{
				// FileDescriptorProto.message_type[0] ("Req", field 4 of
				// FileDescriptorProto) .field[0] ("value", field 2 of
				// DescriptorProto) — the standard SourceCodeInfo path
				// encoding for "the first field of the first message".
				Path: []int32{4, 0, 2, 0},
				// Span is required by protoc's own wire format (a location
				// with no span is rejected); its value is irrelevant here —
				// nothing under test reads source positions.
				Span:            []int32{5, 2, 20},
				LeadingComments: proto.String(comment),
			}},
		}
	}
	return fd
}
