package compiler

import (
	"fmt"
	"path"

	"google.golang.org/protobuf/compiler/protogen"
)

const natsresolverPkg = protogen.GoImportPath("github.com/garm-ai/garm/toolplane/natsresolver")

// natsImportPath is nats.go's own module path — which, unusually, ends in
// ".go". protogen's default import aliasing derives a package's local name
// from the LAST PATH ELEMENT of its import path (path.Base), sanitized for
// Go identifier syntax; for this one path that produces "nats_go", not
// "nats" — the package's own declared name (nats.go's root file says
// `package nats`), and the name every hand-written file in this repo
// already imports it under (see garmtool/runtime.go). protogen has no
// public API to override the alias QualifiedGoIdent picks, so
// emitNatsImport writes this one import by hand instead of going through
// it — see that function's own doc comment.
const natsImportPath = "github.com/nats-io/nats.go"

// emitNatsImport writes `import nats "github.com/nats-io/nats.go"` once, at
// the top of the file — Go requires every import declaration to precede
// every other top-level declaration, so this must run before anything else
// Emit writes, which is why Emit calls it immediately after the package
// clause rather than EmitNatsClient calling it itself (EmitNatsClient runs
// last, after Mount's own declarations).
//
// Deliberately never routed through g.QualifiedGoIdent: doing so would
// register natsImportPath under ITS auto-derived alias ("nats_go"), and the
// generated file would then carry two import specs for the same path under
// two different names — one from this hand-written line, one synthesized
// by protogen's own Content() — which the Go compiler correctly rejects as
// a duplicate import.
func emitNatsImport(g *protogen.GeneratedFile) {
	g.P(`import nats "`, natsImportPath, `"`)
	g.P()
}

// EmitNatsClient writes, per service, a NATS-backed implementation of the
// SAME connect handler interface Mount's own per-service parameter already
// takes — New<Service>OverNats(nc, opts...) <pkg>connect.<Service>Handler —
// so a build wires a service over NATS by passing a different value to the
// exact same Mount call, and the aggregate Mount/MountExcept in
// gen/toolindex is untouched.
//
// This is called from Emit, alongside Mount, deliberately: the generated
// client needs toolplane/natsresolver (for the shared Option type and the
// micro-error-to-connect.Error mapping) exactly the way Mount needs
// toolplane, so it belongs in the emit=server output — the parent module's
// own wiring — and not in EmitMicro's toolsdk output, which contracts
// (a module with no dependency on garm's own runtime) must be able to
// build standalone. See cmd/protoc-gen-garm-tools/main.go's emit flag.
//
// Per this plan's decision 1 (see this package's callers): there is
// deliberately no MountOverNats. A parallel mount path would read more
// directly than "pass a NATS client where Mount wants a handler," but it
// would let a build mount some services in process and others over NATS
// with no single call site enforcing that EVERY service got a decision —
// reopening exactly the hole Mount's one-parameter-per-service shape
// exists to close (emitMount's own doc comment). Implementing the existing
// interface instead of adding a new entry point is what keeps Mount's
// compile-time guarantee intact.
func EmitNatsClient(g *protogen.GeneratedFile, files []*protogen.File, tools []Tool) error {
	f := files[0]
	methods := methodsByFullName(files)
	order, byService := ServicesInOrder(tools)

	for _, svc := range order {
		resolved, err := resolveMicroTools(methods, byService[svc])
		if err != nil {
			return err
		}
		emitNatsClient(g, f, svc, resolved)
	}
	return nil
}

// emitNatsClient writes one service's New<Service>OverNats constructor, its
// backing struct, and one thin method per tool. Each method does nothing
// but build the two things that are genuinely per-tool — the subject
// literal and a fresh response value — and hand the rest to
// natsresolver.Call, which is where the actual NATS round trip, the
// InvocationContext, and the error mapping live (once, not once per
// service). See natsresolver.Call's own doc comment for what it does with
// them.
func emitNatsClient(g *protogen.GeneratedFile, f *protogen.File, svc string, tools []microTool) {
	pkgName, iface := ConnectNames(string(f.GoPackageName), svc)
	connectIdent := protogen.GoImportPath(path.Join(string(f.GoImportPath), pkgName)).Ident(iface)
	typeName := lowerFirst(svc) + "OverNats"

	g.P("// New", svc, "OverNats returns a ", pkgName, ".", iface, " that reaches ", svc, "'s")
	g.P("// tools over NATS micro request-reply (design spec's stack A, step 6's")
	g.P("// resolver hop) instead of in process. It implements the SAME interface")
	g.P("// Mount's own ", svc, " parameter takes, so a build chooses per service by")
	g.P("// passing this or an in-process implementation to the identical Mount")
	g.P("// call — there is no second mount path.")
	g.P("func New", svc, "OverNats(nc *nats.Conn, opts ...",
		g.QualifiedGoIdent(natsresolverPkg.Ident("Option")), ") ", g.QualifiedGoIdent(connectIdent), " {")
	g.P("return &", typeName, "{nc: nc, opts: opts}")
	g.P("}")
	g.P()
	g.P("type ", typeName, " struct {")
	g.P("nc   *nats.Conn")
	g.P("opts []", g.QualifiedGoIdent(natsresolverPkg.Ident("Option")))
	g.P("}")
	g.P()

	for _, t := range tools {
		in := g.QualifiedGoIdent(t.m.Input.GoIdent)
		out := g.QualifiedGoIdent(t.m.Output.GoIdent)
		_, subject, _, _ := toolRefLiteral(t.Tool)

		g.P("func (c *", typeName, ") ", t.m.GoName, "(ctx ", g.QualifiedGoIdent(contextPkg.Ident("Context")),
			", req *", g.QualifiedGoIdent(connectRPCPkg.Ident("Request")), "[", in, "]) (*",
			g.QualifiedGoIdent(connectRPCPkg.Ident("Response")), "[", out, "], error) {")
		g.P("resp := new(", out, ")")
		g.P("if err := ", g.QualifiedGoIdent(natsresolverPkg.Ident("Call")), "(ctx, c.nc, c.opts, ",
			fmt.Sprintf("%q", subject), ", req.Msg, resp); err != nil {")
		g.P("return nil, err")
		g.P("}")
		g.P("return ", g.QualifiedGoIdent(connectRPCPkg.Ident("NewResponse")), "(resp), nil")
		g.P("}")
		g.P()
	}
}
