package compiler

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

const (
	toolplanePkg  = protogen.GoImportPath("github.com/garm-ai/garm/toolplane")
	garmv1Pkg     = protogen.GoImportPath("github.com/garm-ai/garm/contracts/garm/tool/v1")
	connectRPCPkg = protogen.GoImportPath("connectrpc.com/connect")
	httpPkg       = protogen.GoImportPath("net/http")
	contextPkg    = protogen.GoImportPath("context")
	fmtPkg        = protogen.GoImportPath("fmt")
	protoPkg      = protogen.GoImportPath("google.golang.org/protobuf/proto")
	reflectPkg    = protogen.GoImportPath("reflect")
	sortPkg       = protogen.GoImportPath("sort")
	stringsPkg    = protogen.GoImportPath("strings")
)

// Emit writes the registry, the declared constants, and registration glue
// for every tool declared across files — every proto file that shares one
// Go package, not just one proto file. protoc-gen-go and protoc-gen-connect-go
// both emit one file's worth of output per proto file because each proto
// file gets its own base .pb.go; this plugin emits one file's worth of
// output per Go PACKAGE, because `var Tools` and the compartment/set
// constants are package-scoped declarations — emitting them once per proto
// file would redeclare the same symbols twice the moment two proto files in
// one package both carry tool annotations.
//
// It deliberately emits no sanitizers and no JSON Schemas: both depend on the
// caller's clearance and compartments, which are runtime values (spec §4.1).
//
// Emit produces exactly two ways to wire a service up, one per surface, and
// they sit BESIDE each other rather than one replacing the other:
//
//   - RegisterXService(srv, impl) puts the service on a *toolplane.Server's
//     connect mux. It builds the connect handler itself, using
//     toolplane.Options(srv) (via toolplane.MountService) to attach the
//     enforcing interceptor, and hands the caller nothing but the finished
//     registration call. There is deliberately no exported function here
//     that returns a bare http.Handler for a caller to mount on their own —
//     that is precisely the gap (Task 13's finding) that let a handler get
//     wired up with zero enforcement.
//
//   - Mount(c, <one handler per service>) registers typed resolvers on a
//     *toolplane.Core, which has no mux and no transport in it at all. That
//     registry is what MCP, the catalogue and any other future surface
//     reach a tool implementation through, via Core.Invoke.
//
// Emit also writes a THIRD thing for every service, alongside those two:
// New<Service>OverNats(nc, opts...), a NATS-backed implementation of the
// SAME connect handler interface Mount's own parameter takes (see
// emit_natsclient.go). It is not a fourth way to wire a service up — it is
// a second VALUE for the same Mount parameter, so a build chooses in
// process or over NATS per service by passing a different argument to the
// identical Mount call, and there is still exactly one mount path.
//
// Mount does not REPLACE RegisterXService and does not wrap it. It cannot:
// RegisterXService needs a *Server (there has to be a mux to mount a path
// on), Mount takes a *Core (Task 4 builds one with no Server anywhere), and
// the two do different things — one publishes an HTTP route, the other
// populates the resolver registry. Folding them together would force every
// Core-only deployment to construct a Server it never listens on, and would
// take away the ability to mount services one at a time, which the tests
// that mount a package containing a deliberately-ungovernable tool depend
// on.
//
// What Mount adds that RegisterXService cannot is the COMPILE-time
// guarantee: one typed parameter per service in the package, so a service
// added to the .proto breaks the build of every call site that has not
// supplied its handler. Per-service registration — the shape
// RegisterXService has — can always be forgotten one service at a time,
// which is exactly why Mount's own per-service halves are unexported.
//
// files is every generated file sharing one Go package, in a deterministic
// order (gen.Files order, filtered). files[0] is used as the representative
// for the package's own GoPackageName/GoImportPath (needed for ConnectNames
// and the connect package's import path) — files sharing one Go package
// necessarily share both, or protoc-gen-go itself would already be unable
// to compile them together.
//
// outPkg names the package the generated file itself declares. It is
// usually files[0].GoPackageName (the default: colocated with the base
// .pb.go, exactly like protoc-gen-go's own output) — but it can differ, the
// same way protoc-gen-connect-go's package_suffix option puts its output in
// a sibling package instead of the base one. Every OTHER package reference
// (the message types, the connect handler) is still resolved against
// files[0]'s own GoPackageName/GoImportPath regardless of outPkg, via
// g.QualifiedGoIdent, which adds whatever import and alias that requires.
func Emit(
	g *protogen.GeneratedFile,
	files []*protogen.File,
	outPkg protogen.GoPackageName,
	tools []Tool,
	compartments, sets []*toolv1.Decl,
) error {
	g.P("// Code generated by protoc-gen-garm-tools. DO NOT EDIT.")
	g.P()
	g.P("package ", outPkg)
	g.P()
	emitNatsImport(g)

	emitDecls(g, "Compartment", compartments)
	emitDecls(g, "ToolSet", sets)
	if err := emitRegistry(g, files, tools); err != nil {
		return err
	}
	emitRegistrations(g, files[0], tools)
	if err := emitMount(g, files, tools); err != nil {
		return err
	}
	return EmitNatsClient(g, files, tools)
}

// exportIdent converts a declared kebab- or snake-case name into a Go
// exported identifier segment: "pii-contact" -> "PiiContact".
func exportIdent(s string) string {
	var b strings.Builder
	upperNext := true
	for _, r := range s {
		if r == '-' || r == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			b.WriteRune(unicode.ToUpper(r))
			upperNext = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// goStringSlice renders a []string literal, or the bare word "nil" for an
// empty slice so generated code does not carry pointless empty-slice
// allocations.
func goStringSlice(ss []string) string {
	if len(ss) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]string{")
	for i, s := range ss {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", s)
	}
	b.WriteString("}")
	return b.String()
}

// goTypeFor resolves a message full name to the protogen-computed Go
// identifier for it, searching every file's own (possibly nested) messages.
//
// This only resolves messages declared in one of files. Every tool's Input
// and Output in this codebase's protos is defined somewhere in the same Go
// package as its service (usually the same proto file; emitRegistry is
// called once per package, so a sibling proto file in the same package now
// resolves too — see Emit's doc comment). A schema that imported its
// request/response types from a genuinely different Go package would need
// this extended further, to search the plugin's full file set, and fails
// closed here (ok=false) rather than silently emitting a wrong reference.
func goTypeFor(files []*protogen.File, name string) (protogen.GoIdent, bool) {
	var find func([]*protogen.Message) (protogen.GoIdent, bool)
	find = func(msgs []*protogen.Message) (protogen.GoIdent, bool) {
		for _, m := range msgs {
			if string(m.Desc.FullName()) == name {
				return m.GoIdent, true
			}
			if id, ok := find(m.Messages); ok {
				return id, true
			}
		}
		return protogen.GoIdent{}, false
	}
	for _, f := range files {
		if id, ok := find(f.Messages); ok {
			return id, true
		}
	}
	return protogen.GoIdent{}, false
}

func emitDecls(g *protogen.GeneratedFile, kind string, decls []*toolv1.Decl) {
	if len(decls) == 0 {
		return
	}
	g.P("// Declared ", kind, " names, from the file-level declaration.")
	g.P("const (")
	for _, d := range decls {
		if c := d.GetDescription(); c != "" {
			g.P("// ", c)
		}
		g.P(kind, exportIdent(d.GetName()), " = ", fmt.Sprintf("%q", d.GetName()))
	}
	g.P(")")
	g.P()

	// The same declarations, as data. toolplane.Config.Compartments (and any
	// other consumer that needs the declared SET, not just its names as
	// string constants) takes []*toolv1.Decl; without this, every call site
	// retypes the list by hand, and a hand-copied list that drifts by one
	// name silently reinterprets one compartment as another, since bits are
	// assigned positionally over the sorted name set
	// (policy.NewRegistry). Emitted in declaration order, not sorted, so
	// a reader can see any difference from the registry's sorted bit order.
	plural := kind + "s"
	g.P("var ", plural, " = []*", g.QualifiedGoIdent(garmv1Pkg.Ident("Decl")), "{")
	for _, d := range decls {
		if c := d.GetDescription(); c != "" {
			g.P("{Name: ", fmt.Sprintf("%q", d.GetName()), ", Description: ", fmt.Sprintf("%q", c), "},")
		} else {
			g.P("{Name: ", fmt.Sprintf("%q", d.GetName()), "},")
		}
	}
	g.P("}")
	g.P()
}

func emitRegistry(g *protogen.GeneratedFile, files []*protogen.File, tools []Tool) error {
	g.P("// Tools is every tool declared in this Go package.")
	g.P("var Tools = []", g.QualifiedGoIdent(toolplanePkg.Ident("ToolDef")), "{")
	for _, t := range tools {
		in, ok := goTypeFor(files, string(t.Method.Input().FullName()))
		if !ok {
			return fmt.Errorf("garm-tools: %s: input type %s is not declared in %s "+
				"(cross-package request/response types are not supported)",
				t.Method.FullName(), t.Method.Input().FullName(), filePaths(files))
		}
		out, ok := goTypeFor(files, string(t.Method.Output().FullName()))
		if !ok {
			return fmt.Errorf("garm-tools: %s: output type %s is not declared in %s "+
				"(cross-package request/response types are not supported)",
				t.Method.FullName(), t.Method.Output().FullName(), filePaths(files))
		}
		g.P("{")
		g.P("FullMethod: ", fmt.Sprintf("%q", "/"+string(t.Method.Parent().FullName())+
			"/"+string(t.Method.Name())), ",")
		g.P("Name: ", fmt.Sprintf("%q", t.Name), ",")
		// FQN: proto package + tool name. Name is unique only within a
		// package (L3, one plugin invocation's view); the proto package is
		// globally unique, so the pair is too. See toolplane.ToolDef.FQN.
		g.P("FQN: ", fmt.Sprintf("%q", string(t.Method.ParentFile().Package())+"."+t.Name), ",")
		g.P("Title: ", fmt.Sprintf("%q", t.Policy.GetTitle()), ",")
		g.P("Description: ", fmt.Sprintf("%q", t.Policy.GetDescription()), ",")
		g.P("Verb: ", g.QualifiedGoIdent(garmv1Pkg.Ident("Verb_"+t.Policy.GetVerb().String())), ",")
		g.P("MinClearance: ", g.QualifiedGoIdent(
			garmv1Pkg.Ident("Clearance_"+t.Policy.GetMinClearance().String())), ",")
		g.P("Compartments: ", goStringSlice(t.Policy.GetCompartments()), ",")
		g.P("Sets: ", goStringSlice(t.Policy.GetSets()), ",")
		// Declared governance. Plan A implements none of it; emitting it is
		// what lets toolplane.mount REFUSE a tool whose schema promises
		// supervision this build cannot deliver. Dropping these three on the
		// floor — which is what this plugin used to do — made L14 and L21
		// force declarations that then had no effect at all, which reads as
		// protection in review and is not.
		g.P("ApprovalMode: ", g.QualifiedGoIdent(garmv1Pkg.Ident(
			"Approval_"+t.Policy.GetApproval().GetMode().String())), ",")
		g.P("AuditLevel: ", g.QualifiedGoIdent(garmv1Pkg.Ident(
			"Audit_"+t.Policy.GetAudit().GetLevel().String())), ",")
		g.P("HasAuthorization: ", t.Policy.GetAuthorization() != nil, ",")
		g.P("Idempotent: ", t.Policy.GetEffects().GetIdempotent(), ",")
		g.P("Reversibility: ", g.QualifiedGoIdent(garmv1Pkg.Ident(
			"Reversibility_"+t.Policy.GetEffects().GetReversibility().String())), ",")
		g.P("External: ", t.Policy.GetEffects().GetExternal(), ",")
		if v := t.Policy.GetGuidance().GetWhenNotToUse(); v != "" {
			g.P("WhenNotToUse: ", strconv.Quote(v), ",")
		}
		if v := t.Policy.GetGuidance().GetOnError(); v != "" {
			g.P("OnError: ", strconv.Quote(v), ",")
		}
		g.P("Input: (*", g.QualifiedGoIdent(in), ")(nil).ProtoReflect().Descriptor(),")
		g.P("Output: (*", g.QualifiedGoIdent(out), ")(nil).ProtoReflect().Descriptor(),")
		emitFieldDocs(g, files, t)
		g.P("},")
	}
	g.P("}")
	g.P()
	return nil
}

// emitFieldDocs writes the FieldDocs map for one tool: the leading proto
// comment of every field reachable from its request and response messages,
// keyed by the field's full proto name.
//
// It has to happen here, at generation time, because protoc-gen-go strips
// SourceCodeInfo from the runtime descriptor — by the time anything can read
// a descriptor at run time, the comments are gone. This is the only moment
// they exist and the Go type that needs them is being written.
func emitFieldDocs(g *protogen.GeneratedFile, files []*protogen.File, t Tool) {
	docs := map[string]string{}
	for _, name := range []string{
		string(t.Method.Input().FullName()),
		string(t.Method.Output().FullName()),
	} {
		if msg, ok := messageNamed(files, name); ok {
			collectFieldDocs(msg, docs, map[string]bool{})
		}
	}
	if len(docs) == 0 {
		return
	}
	// Sorted: the generated file must be byte-identical across runs, and Go
	// map iteration order is deliberately not.
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	g.P("FieldDocs: map[string]string{")
	for _, k := range keys {
		g.P(strconv.Quote(k), ": ", strconv.Quote(docs[k]), ",")
	}
	g.P("},")
}

// collectFieldDocs walks a message and every message reachable from it.
//
// seen guards against recursion: a message may reference itself directly or
// through a cycle, and protos permit that. Without the guard a self-
// referential request message would hang the plugin rather than fail it.
func collectFieldDocs(msg *protogen.Message, into map[string]string, seen map[string]bool) {
	full := string(msg.Desc.FullName())
	if seen[full] {
		return
	}
	seen[full] = true

	for _, f := range msg.Fields {
		if doc := commentText(f.Comments.Leading); doc != "" {
			into[string(f.Desc.FullName())] = doc
		}
		if f.Message != nil && !f.Desc.IsMap() {
			collectFieldDocs(f.Message, into, seen)
		}
	}
}

// commentText turns a protogen comment into prose.
//
// protogen hands back the comment with its "//" markers and newlines intact;
// what a model reads should be a sentence, not a Go comment. Lines are joined
// with a space rather than a newline because the consumer is a JSON schema
// description, where a newline is just an awkward character.
func commentText(c protogen.Comments) string {
	var parts []string
	for _, line := range strings.Split(string(c), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

// messageNamed finds a message by full proto name across the package's files.
func messageNamed(files []*protogen.File, name string) (*protogen.Message, bool) {
	var find func([]*protogen.Message) (*protogen.Message, bool)
	find = func(msgs []*protogen.Message) (*protogen.Message, bool) {
		for _, m := range msgs {
			if string(m.Desc.FullName()) == name {
				return m, true
			}
			if got, ok := find(m.Messages); ok {
				return got, true
			}
		}
		return nil, false
	}
	for _, f := range files {
		if got, ok := find(f.Messages); ok {
			return got, true
		}
	}
	return nil, false
}

// filePaths renders every file's proto path, for an error message that
// names the whole package's proto files rather than just one of them.
func filePaths(files []*protogen.File) string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Desc.Path()
	}
	return strings.Join(paths, ", ")
}

// ServicesInOrder groups tools by their proto service name and returns the
// service names in FIRST-APPEARANCE order.
//
// tools is already in a deterministic order (file declaration order, from
// Tools()), but a Go map over the grouping would not be — iterating it
// directly would make the emitted order of RegisterX/toolsForX blocks, and
// of Mount's parameter list, vary run to run for any package with more than
// one service, breaking `make gen` idempotency.
//
// Exported because `garmdev gen toolindex` has to reproduce exactly this
// order: the aggregate Mount it emits calls each package's Mount
// POSITIONALLY, so the two orders must agree. They agree because they are
// ONE FUNCTION over the same deterministic input, and that is the whole of
// the guarantee.
//
// There is deliberately no fallback claimed here. Go interface
// assignability is STRUCTURAL, not nominal: two services in one proto
// package declaring the same method names over the same request and
// response messages produce two distinct named handler interfaces with
// identical method sets, and swapping that pair compiles cleanly — mounting
// the wrong implementation under the right procedure name, which is worse
// than omitting it. Nothing in this build has that shape today, and nothing
// anywhere checks that it never will. So this ordering is load-bearing on
// its own.
func ServicesInOrder(tools []Tool) (order []string, byService map[string][]Tool) {
	byService = map[string][]Tool{}
	for _, t := range tools {
		svc := string(t.Method.Parent().Name())
		if _, seen := byService[svc]; !seen {
			order = append(order, svc)
		}
		byService[svc] = append(byService[svc], t)
	}
	return order, byService
}

func emitRegistrations(g *protogen.GeneratedFile, f *protogen.File, tools []Tool) {
	order, _ := ServicesInOrder(tools)

	for _, svc := range order {
		pkgName, iface := ConnectNames(string(f.GoPackageName), svc)
		svcConnectPkg := protogen.GoImportPath(path.Join(string(f.GoImportPath), pkgName))

		g.P("// Register", svc, " mounts the handler behind the garm policy chain")
		g.P("// and serves it on every configured surface. It is the only supported")
		g.P("// way to wire this service into a *toolplane.Server: MountService, not")
		g.P("// this function, supplies toolplane.Options(srv) to the connect handler")
		g.P("// constructor, so the enforcing interceptor cannot be forgotten at the")
		g.P("// call site — there is no seam here that hands out a bare http.Handler.")
		g.P("func Register", svc, "(")
		g.P("srv *", g.QualifiedGoIdent(toolplanePkg.Ident("Server")), ",")
		g.P("h ", g.QualifiedGoIdent(svcConnectPkg.Ident(iface)), ",")
		g.P(") error {")
		g.P("return srv.MountService(func(opts ...", g.QualifiedGoIdent(connectRPCPkg.Ident("HandlerOption")),
			") (string, ", g.QualifiedGoIdent(httpPkg.Ident("Handler")), ") {")
		g.P("return ", g.QualifiedGoIdent(svcConnectPkg.Ident("New"+svc+"Handler")), "(h, opts...)")
		g.P("}, toolsFor", svc, "())")
		g.P("}")
		g.P()

		g.P("func toolsFor", svc, "() []", g.QualifiedGoIdent(toolplanePkg.Ident("ToolDef")), " {")
		g.P("var out []", g.QualifiedGoIdent(toolplanePkg.Ident("ToolDef")))
		g.P("for _, t := range Tools {")
		g.P("if t.Service() == ", fmt.Sprintf("%q", svc), " {")
		g.P("out = append(out, t)")
		g.P("}")
		g.P("}")
		g.P("return out")
		g.P("}")
		g.P()
	}
}

// ParamName derives the Go parameter name Mount uses for a service: the
// service name with a trailing "Service" removed and its leading run of
// capitals lower-cased. AccountsService -> accounts, HTTPService -> http.
//
// Exported for the same reason as ServicesInOrder: `garmdev gen toolindex`
// names the aggregate Mount's parameters the same way, so that a reader
// moving between the two files sees one convention rather than two.
func ParamName(service string) string {
	base := strings.TrimSuffix(service, "Service")
	if base == "" {
		base = service
	}
	rs := []rune(base)
	n := 0
	for n < len(rs) && unicode.IsUpper(rs[n]) {
		n++
	}
	// A run of capitals followed by more letters is an initialism whose LAST
	// capital starts the next word: HTTPService -> "HTTP" + "Service" is
	// already split off, but GRPCGateway -> grpcGateway, not gRPCGateway.
	if n > 1 && n < len(rs) {
		n--
	}
	for i := 0; i < n; i++ {
		rs[i] = unicode.ToLower(rs[i])
	}
	return string(rs)
}

// reservedParamNames are identifiers a Mount parameter must not take.
//
// Go keywords would not compile; the rest are identifiers the emitted Mount
// body or signature resolves through — the local variables it declares and
// the import aliases protogen assigns for this file. A service named
// ToolplaneService or ErrService is not hypothetical enough to leave as a
// build break in someone else's repository, and the fix (a numeric suffix,
// below) costs nothing.
var reservedParamNames = map[string]bool{
	// locals and receivers in the emitted bodies
	"c": true, "h": true, "err": true, "in": true, "ok": true, "res": true,
	"ctx": true, "req": true, "rv": true, "skip": true, "keep": true,
	"known": true, "have": true, "except": true,
	// import aliases this file's emitted code uses
	"toolplane": true, "connect": true, "proto": true, "context": true,
	"fmt": true, "http": true, "v1": true, "reflect": true, "sort": true,
	"strings": true,
	// Go keywords
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// MountParamNames returns one parameter name per service, in the given
// order, disambiguated against each other and against reservedParamNames.
//
// Exported so `garmdev gen toolindex` produces the same names for the same
// services.
func MountParamNames(order []string) []string {
	used := map[string]bool{}
	out := make([]string, len(order))
	for i, svc := range order {
		name := ParamName(svc)
		if reservedParamNames[name] || used[name] {
			for n := 2; ; n++ {
				cand := fmt.Sprintf("%s%d", name, n)
				if !reservedParamNames[cand] && !used[cand] {
					name = cand
					break
				}
			}
		}
		used[name] = true
		out[i] = name
	}
	return out
}

// emitMount writes the typed Mount for this package.
//
// # What Mount is for
//
// RegisterXService(srv, impl) is one hand-written call per service. Forget
// one and nothing says so: the service is simply absent, and if the
// forgotten implementation was a resolver embedding
// UnimplementedXServiceHandler it compiles, mounts, is governed, and returns
// Unimplemented on the first real call — at runtime, to a caller, with the
// schema still promising the tool exists.
//
// Mount takes one TYPED parameter per service in the package, so adding a
// service to a .proto and regenerating turns every call site that has not
// supplied its handler into a compile error ("not enough arguments in call
// to Mount"). That is the whole value: the failure moves from the first
// production call to the build.
//
// # What it registers
//
// For each tool, a resolver closing over the handler's own method —
// typed, no reflection, no dynamic dispatch. The resolver goes INTO
// toolplane.Core via Register and cannot come back out (see
// toolplane/resolver.go): Mount hands closures in, so the only reachable
// path to an implementation stays Core.Invoke, which is the chain.
//
// Mount also calls AddTools per service, exactly as RegisterXService does
// via MountService, so a tool declaring governance this build does not
// implement is refused here too — with the same error, naming the same
// tool. Because Mount is all-or-nothing, ONE such tool refuses the whole
// package: that is deliberate, and a Core whose Mount returned an error
// must be discarded rather than used, since the services before the failing
// one have already registered.
func emitMount(g *protogen.GeneratedFile, files []*protogen.File, tools []Tool) error {
	f := files[0]
	order, byService := ServicesInOrder(tools)
	params := MountParamNames(order)
	methods := methodsByFullName(files)

	connectIdent := func(svc, name string) protogen.GoIdent {
		pkgName, _ := ConnectNames(string(f.GoPackageName), svc)
		return protogen.GoImportPath(path.Join(string(f.GoImportPath), pkgName)).Ident(name)
	}

	g.P("// Mount registers a typed resolver for every tool in this package on c,")
	g.P("// and adds their declarations (and compiled redaction plans) to it.")
	g.P("//")
	g.P("// There is one parameter per service, so a service added to the .proto")
	g.P("// and regenerated here makes every call site that has not supplied its")
	g.P("// handler fail to COMPILE. That is the point: an implementation that")
	g.P("// embeds Unimplemented", order[0], "Handler would otherwise mount, be")
	g.P("// governed, and answer Unimplemented to a real caller at runtime.")
	g.P("//")
	g.P("// A handler passed as nil is refused here, at startup, naming the")
	g.P("// service — the compiler cannot catch that one, and the first call")
	g.P("// would otherwise panic inside the generated resolver.")
	g.P("//")
	g.P("// The resolvers go in and never come back out — toolplane.Core exposes")
	g.P("// no way to retrieve or enumerate one, so the only reachable path to an")
	g.P("// implementation is Core.Invoke, which is the governance chain.")
	g.P("//")
	g.P("// Mount is all-or-nothing and NOT atomic: a tool declaring governance")
	g.P("// this build does not implement refuses the whole package, and the")
	g.P("// services registered before the failure stay registered. Discard the")
	g.P("// Core on error rather than retrying into it.")
	g.P("func Mount(")
	g.P("c *", g.QualifiedGoIdent(toolplanePkg.Ident("Core")), ",")
	for i, svc := range order {
		_, iface := ConnectNames(string(f.GoPackageName), svc)
		g.P(params[i], " ", g.QualifiedGoIdent(connectIdent(svc, iface)), ",")
	}
	g.P(") error {")
	g.P("return mountAll(c, nil", func() string {
		var b strings.Builder
		for i := range order {
			b.WriteString(", " + params[i])
		}
		return b.String()
	}(), ")")
	g.P("}")
	g.P()

	g.P("// MountExcept is Mount with named tools left out, for a build that")
	g.P("// declares a tool it cannot yet serve.")
	g.P("//")
	g.P("// Every handler is still required, so this is not a way to mount half a")
	g.P("// build by accident — only a way to say, out loud and in one place,")
	g.P("// which tools are deliberately not served. The names are FQNs")
	g.P("// (proto package + tool name), because a bare tool name is unique only")
	g.P("// within a package and this list will one day span several.")
	g.P("//")
	g.P("// An excluded tool is ABSENT, not merely unregistered: its declaration")
	g.P("// never reaches the Core, so it answers exactly as a procedure this")
	g.P("// build never declared does — Unimplemented, this server does not serve")
	g.P("// that. Skipping only the resolver would leave the declaration in the")
	g.P("// catalogue, and a caller who clears the tool would then pass step 2 and")
	g.P("// fail at step 6, making the difference observable.")
	g.P("//")
	g.P("// Note which absence this is. NotFound means the build DOES serve the")
	g.P("// tool and this caller may not see it; Unimplemented means the build")
	g.P("// does not serve it. An exclusion is the second, and must not be the")
	g.P("// first — answering NotFound would tell every caller that a tool exists")
	g.P("// here which is deliberately not being served.")
	g.P("//")
	g.P("// A name matching nothing in this package is an ERROR. A typo or a tool")
	g.P("// renamed in a later .proto would otherwise leave the operator believing")
	g.P("// they excluded something while it quietly mounted.")
	g.P("func MountExcept(")
	g.P("c *", g.QualifiedGoIdent(toolplanePkg.Ident("Core")), ",")
	g.P("except []string,")
	for i, svc := range order {
		_, iface := ConnectNames(string(f.GoPackageName), svc)
		g.P(params[i], " ", g.QualifiedGoIdent(connectIdent(svc, iface)), ",")
	}
	g.P(") error {")
	g.P("skip, err := skipSet(except)")
	g.P("if err != nil {")
	g.P("return err")
	g.P("}")
	g.P("return mountAll(c, skip", func() string {
		var b strings.Builder
		for i := range order {
			b.WriteString(", " + params[i])
		}
		return b.String()
	}(), ")")
	g.P("}")
	g.P()

	g.P("// skipSet validates an exclusion list against this package's tools.")
	g.P("func skipSet(except []string) (map[string]bool, error) {")
	g.P("if len(except) == 0 {")
	g.P("return nil, nil")
	g.P("}")
	g.P("known := make(map[string]bool, len(Tools))")
	g.P("for _, t := range Tools {")
	g.P("known[t.FQN] = true")
	g.P("}")
	g.P("skip := make(map[string]bool, len(except))")
	g.P("for _, name := range except {")
	g.P("if !known[name] {")
	g.P("have := make([]string, 0, len(Tools))")
	g.P("for _, t := range Tools {")
	g.P("have = append(have, t.FQN)")
	g.P("}")
	g.P(g.QualifiedGoIdent(sortPkg.Ident("Strings")), "(have)")
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
		fmt.Sprintf("%q", "garm: cannot exclude %q: this package declares no such tool; it declares %s"),
		", name, ", g.QualifiedGoIdent(stringsPkg.Ident("Join")), "(have, \", \"))")
	g.P("}")
	g.P("skip[name] = true")
	g.P("}")
	g.P("return skip, nil")
	g.P("}")
	g.P()

	g.P("func mountAll(")
	g.P("c *", g.QualifiedGoIdent(toolplanePkg.Ident("Core")), ",")
	g.P("skip map[string]bool,")
	for i, svc := range order {
		_, iface := ConnectNames(string(f.GoPackageName), svc)
		g.P(params[i], " ", g.QualifiedGoIdent(connectIdent(svc, iface)), ",")
	}
	g.P(") error {")
	for i, svc := range order {
		g.P("if err := mount", svc, "(c, ", params[i], ", skip); err != nil {")
		g.P("return err")
		g.P("}")
	}
	g.P("return nil")
	g.P("}")
	g.P()

	for _, svc := range order {
		_, iface := ConnectNames(string(f.GoPackageName), svc)
		g.P("// mount", svc, " is Mount's per-service half. Unexported on purpose:")
		g.P("// an exported per-service mount would reopen exactly the hole Mount")
		g.P("// closes, by letting a caller mount some services and silently omit")
		g.P("// others. Mount is the only way in, and it takes them all.")
		g.P("//")
		g.P("// The nil check is the other half of that. An OMITTED handler is a")
		g.P("// compile error — Mount's parameter list sees to that — but a nil one")
		g.P("// type-checks, and without this would mount the service, register its")
		g.P("// resolvers, advertise its tools, and panic on the first call, ledgered")
		g.P("// as \"resolver panicked: invalid memory address or nil pointer")
		g.P("// dereference\" with nothing said at startup. Refuse it where the error")
		g.P("// can still name the service.")
		g.P("func mount", svc, "(")
		g.P("c *", g.QualifiedGoIdent(toolplanePkg.Ident("Core")), ",")
		g.P("h ", g.QualifiedGoIdent(connectIdent(svc, iface)), ",")
		g.P("skip map[string]bool,")
		g.P(") error {")
		g.P("if h == nil {")
		g.P("return ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
			fmt.Sprintf("%q", "garm: "+svc+": a handler is required, got nil"), ")")
		g.P("}")
		// A TYPED nil is the likelier mistake and the one `h == nil` misses: a
		// constructor returning (*impl, error) whose error was ignored leaves a
		// non-nil interface holding a nil pointer, for which h == nil is FALSE.
		// It would mount, advertise and panic exactly as an untyped nil would,
		// so a guard that catches only the literal reads as complete and is not.
		g.P("if rv := ", g.QualifiedGoIdent(reflectPkg.Ident("ValueOf")), "(h); rv.Kind() == ",
			g.QualifiedGoIdent(reflectPkg.Ident("Ptr")), " && rv.IsNil() {")
		g.P("return ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
			fmt.Sprintf("%q", "garm: "+svc+": a handler is required, got a typed nil %s"),
			", rv.Type())")
		g.P("}")
		g.P("// Excluded tools are filtered out BEFORE AddTools, so their")
		g.P("// declarations never enter the Core and step 2 cannot see them.")
		g.P("keep := toolsFor", svc, "()[:0:0]")
		g.P("for _, t := range toolsFor", svc, "() {")
		g.P("if !skip[t.FQN] {")
		g.P("keep = append(keep, t)")
		g.P("}")
		g.P("}")
		g.P("if err := c.AddTools(keep); err != nil {")
		g.P("return err")
		g.P("}")
		for _, t := range byService[svc] {
			if err := emitResolver(g, methods, t); err != nil {
				return err
			}
		}
		g.P("return nil")
		g.P("}")
		g.P()
	}
	return nil
}

// emitResolver writes one Core.Register call: the procedure, a factory for
// its request message, and a closure that calls the handler's own method.
//
// newReq is what lets a surface that receives JSON rather than a typed
// message (MCP) or needs an empty one to project a schema from (the
// catalogue) build a request for this procedure at all; registering it
// alongside the resolver means such a surface cannot be wired up against a
// procedure it has no way to construct a request for.
func emitResolver(
	g *protogen.GeneratedFile, methods map[protoreflect.FullName]*protogen.Method, t Tool,
) error {
	m, ok := methods[t.Method.FullName()]
	if !ok {
		// Unreachable for any real generation run — tools are read off the
		// same files protogen built — but emitting a reference to nothing
		// would be a compile error in someone else's tree with no hint of
		// where it came from.
		return fmt.Errorf("garm-tools: %s: no generated method for this tool "+
			"(is its service in a different Go package?)", t.Method.FullName())
	}
	if m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer() {
		// L4 already refuses this at lint time, so reaching here means Emit
		// was called without Lint. Refuse rather than emit a resolver whose
		// signature does not match the streaming handler's.
		return fmt.Errorf("garm-tools: %s: a streaming RPC cannot be a tool "+
			"(lint rule L4); set exclude: true", t.Method.FullName())
	}

	procedure := "/" + string(t.Method.Parent().FullName()) + "/" + string(t.Method.Name())
	in := g.QualifiedGoIdent(m.Input.GoIdent)

	g.P("if err := c.Register(")
	g.P(fmt.Sprintf("%q", procedure), ",")
	g.P("func() ", g.QualifiedGoIdent(protoPkg.Ident("Message")), " { return new(", in, ") },")
	g.P("func(ctx ", g.QualifiedGoIdent(contextPkg.Ident("Context")), ", req ",
		g.QualifiedGoIdent(protoPkg.Ident("Message")), ") (",
		g.QualifiedGoIdent(protoPkg.Ident("Message")), ", error) {")
	g.P("in, ok := req.(*", in, ")")
	g.P("if !ok {")
	// Fail closed on a message of the wrong type rather than panicking in
	// the assertion: the chain would convert the panic into Internal, but
	// the ledger row would read "resolver panicked" for what is a wiring
	// bug with a precise name.
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
		fmt.Sprintf("%q", "garm: "+procedure+": request is %T, want %T"),
		", req, (*", in, ")(nil))")
	g.P("}")
	g.P("res, err := h.", m.GoName, "(ctx, ",
		g.QualifiedGoIdent(connectRPCPkg.Ident("NewRequest")), "(in))")
	g.P("if err != nil {")
	g.P("return nil, err")
	g.P("}")
	g.P("if res == nil {")
	// connect's own unary handler panics on (nil, nil) rather than
	// serialising it; here it would be a nil-pointer dereference one line
	// down. Name the procedure instead.
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
		fmt.Sprintf("%q", "garm: "+procedure+": handler returned no response and no error"), ")")
	g.P("}")
	g.P("return res.Msg, nil")
	g.P("},")
	g.P("); err != nil {")
	g.P("return err")
	g.P("}")
	return nil
}

// methodsByFullName indexes every generated method in the package by its
// proto full name, so a Tool (which carries only a descriptor) can be
// resolved to protogen's Go names for the method and its request type.
func methodsByFullName(files []*protogen.File) map[protoreflect.FullName]*protogen.Method {
	out := map[protoreflect.FullName]*protogen.Method{}
	for _, f := range files {
		for _, s := range f.Services {
			for _, m := range s.Methods {
				out[m.Desc.FullName()] = m
			}
		}
	}
	return out
}
