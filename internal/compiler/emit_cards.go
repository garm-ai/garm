package compiler

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/garm-ai/contracts/cards"
)

// The generated card defaults.
//
// Every tool serves an input card, a result card and — when it is
// MODE_GRANT — an approval card, and an author writes no proto for any of
// them (contracts/cards). This file emits the Go: one default implementation
// per card, an optional interface that documents what an override looks like,
// and the registration in ServeX that prefers an override where the handler
// has one.
//
// What is NOT emitted is the card construction. That lives in
// contracts/cards as runtime functions over the method descriptor, and the
// generated default is a three-line wrapper around one. Generating the
// construction instead would put several hundred lines of literal-building
// into every tool module and make a fix to the layout a regeneration of every
// repository that has one — for a card whose content is entirely determined
// by a descriptor those modules already carry.
//
// The descriptor is resolved from the global registry by name rather than off
// the .pb.go file variable. The variable's Go name is derived from the proto
// PATH by protoc-gen-go, which makes it a spelling this generator would have
// to reproduce exactly; the full name of a service is in the contract.

const (
	cardsPkg         = protogen.GoImportPath("github.com/garm-ai/contracts/cards")
	cardv1Pkg        = protogen.GoImportPath("github.com/garm-ai/contracts/garm/card/v1")
	emptypbPkg       = protogen.GoImportPath("google.golang.org/protobuf/types/known/emptypb")
	protoreflectPkg  = protogen.GoImportPath("google.golang.org/protobuf/reflect/protoreflect")
	protoregistryPkg = protogen.GoImportPath("google.golang.org/protobuf/reflect/protoregistry")
	syncPkg          = protogen.GoImportPath("sync")
)

// serviceCards resolves the cards of one service, paired with the protogen
// method of their parent so the emitters can reach Go identifiers.
type serviceCard struct {
	cards.Synth
	parent microTool
}

func resolveServiceCards(tools []microTool) []serviceCard {
	var out []serviceCard
	for _, t := range tools {
		for _, s := range cards.Synthesise(t.Method) {
			out = append(out, serviceCard{Synth: s, parent: t})
		}
	}
	return out
}

// emitCardsInterface writes <Service>Cards: the optional interface an author
// implements to override a card.
//
// Optional, and per METHOD rather than all-or-nothing: ServeX asserts each
// card's own signature, so a handler may override the approval card and take
// the generated input card. The aggregate interface exists because an author
// needs somewhere to read the three signatures from — `h.(interface{ ... })`
// in generated code is precise and undiscoverable.
//
// There is no Unimplemented embed and no requirement to implement any of it:
// unlike <Service>Handler, where a missing tool must be a compile error
// because the proto promised it, a missing card has a correct answer already.
func emitCardsInterface(g *protogen.GeneratedFile, svc string, cs []serviceCard) {
	if len(cs) == 0 {
		return
	}
	g.P("// ", svc, "Cards is the optional interface for overriding a generated card.")
	g.P("//")
	g.P("// Implement a method of it on your handler and Serve", svc, " registers")
	g.P("// yours; leave it out and the generated default is registered. It is")
	g.P("// per method, not all or nothing: overriding the approval card of one")
	g.P("// tool leaves every other card generated.")
	g.P("//")
	g.P("// An override gets the ref, its own data from its own store, and the")
	g.P("// invocation from ctx. It MUST label what it adds — an unlabelled")
	g.P("// element takes the endpoint's own policy, and an element labelled")
	g.P("// BELOW the endpoint fails the whole card rather than being served.")
	g.P("// cards.Join is how to label something at the endpoint's floor or")
	g.P("// higher.")
	g.P("type ", svc, "Cards interface {")
	for _, c := range cs {
		g.P(cardSignature(g, c))
	}
	g.P("}")
	g.P()
}

// cardSignature is the one place a card's Go signature is spelled, so the
// interface, the default and the type assertion in ServeX cannot disagree.
func cardSignature(g *protogen.GeneratedFile, c serviceCard) string {
	return fmt.Sprintf("%s(%s, *%s) (*%s, error)",
		c.Name,
		g.QualifiedGoIdent(contextPkg.Ident("Context")),
		g.QualifiedGoIdent(cardRequestIdent(c)),
		g.QualifiedGoIdent(cardv1Pkg.Ident("Card")))
}

func cardRequestIdent(c serviceCard) protogen.GoIdent {
	switch c.InputType {
	case cards.CallRefType:
		return cardv1Pkg.Ident("CallRef")
	case cards.TaskRefType:
		return cardv1Pkg.Ident("TaskRef")
	default:
		return emptypbPkg.Ident("Empty")
	}
}

// emitCardDefaults writes one default per card.
//
// The input and approval cards are built from the descriptor and are
// complete. The result card is the one that cannot be: a card about an
// answer needs the answer, and the generated wrapper has no way to reach a
// response the handler returned to somebody else. Two functions are emitted
// for it — the registered default, which answers result_unavailable, and a
// typed builder a tool with its OWN record calls from an override.
func emitCardDefaults(g *protogen.GeneratedFile, svc string, cs []serviceCard) {
	for _, c := range cs {
		def := "Default" + string(c.Name)
		req := g.QualifiedGoIdent(cardRequestIdent(c))
		card := g.QualifiedGoIdent(cardv1Pkg.Ident("Card"))
		ctx := g.QualifiedGoIdent(contextPkg.Ident("Context"))
		parentFQN := string(c.Parent.Parent().FullName())
		parentName := string(c.Parent.Name())

		switch c.Kind {
		case cards.Input:
			g.P("// ", def, " is the generated form for ", parentName, ".")
			g.P("//")
			g.P("// One input per field the caller may set, in declaration order, with")
			g.P("// the control chosen by type and the constraints read from")
			g.P("// protovalidate. Each input is labelled at its field's WRITE policy,")
			g.P("// so an input the viewer may not fill is dropped rather than shown")
			g.P("// disabled — a box a person cannot use is a box they will try to use.")
			g.P("// Fields the runner supplies are absent.")
			g.P("func ", def, "(_ ", ctx, ", _ *", req, ") (*", card, ", error) {")
			g.P("md, err := garmCardMethod(", fmt.Sprintf("%q", parentFQN), ", ", fmt.Sprintf("%q", parentName), ")")
			g.P("if err != nil { return nil, err }")
			g.P("return ", g.QualifiedGoIdent(cardsPkg.Ident("BuildInputCard")), "(md)")
			g.P("}")
			g.P()
		case cards.Approval:
			g.P("// ", def, " is the generated approval card for ", parentName, ".")
			g.P("//")
			g.P("// The values come from the ref, because the daemon refuses a")
			g.P("// grant-mode call before it resolves the tool: this service never saw")
			g.P("// the request that was parked. Every material fact carries the dotted")
			g.P("// path it came from and is labelled at its own read policy joined with")
			g.P("// this tool's approver predicate — who may approve is a stricter")
			g.P("// question than who may read.")
			g.P("func ", def, "(_ ", ctx, ", ref *", req, ") (*", card, ", error) {")
			g.P("md, err := garmCardMethod(", fmt.Sprintf("%q", parentFQN), ", ", fmt.Sprintf("%q", parentName), ")")
			g.P("if err != nil { return nil, err }")
			g.P("return ", g.QualifiedGoIdent(cardsPkg.Ident("BuildApprovalCard")), "(md, ref)")
			g.P("}")
			g.P()
		case cards.Result:
			out := g.QualifiedGoIdent(c.parent.m.Output.GoIdent)
			g.P("// ", def, " answers result_unavailable.")
			g.P("//")
			g.P("// A card about an answer needs the answer, and this wrapper has no way")
			g.P("// to reach a response ", parentName, " returned to somebody else. The")
			g.P("// runtime is meant to seal each call's response under its call id and")
			g.P("// hand it back here; until that store exists, only a tool that keeps")
			g.P("// its OWN record has a result card — override ", c.Name, ", read your")
			g.P("// own row, and call ", string(c.Name), "From with it.")
			g.P("func ", def, "(_ ", ctx, ", _ *", req, ") (*", card, ", error) {")
			g.P("return nil, ", g.QualifiedGoIdent(cardsPkg.Ident("ErrResultUnavailable")))
			g.P("}")
			g.P()
			g.P("// ", c.Name, "From builds ", parentName, "'s result card from a response")
			g.P("// you already hold.")
			g.P("//")
			g.P("// One fact per scalar of the response, in declaration order, each")
			g.P("// labelled at its own field's READ policy and carrying its dotted path.")
			g.P("// This is what an override calls after reading its own store.")
			g.P("func ", c.Name, "From(ref *", req, ", resp *", out, ") (*", card, ", error) {")
			g.P("md, err := garmCardMethod(", fmt.Sprintf("%q", parentFQN), ", ", fmt.Sprintf("%q", parentName), ")")
			g.P("if err != nil { return nil, err }")
			g.P("return ", g.QualifiedGoIdent(cardsPkg.Ident("BuildResultCard")), "(md, ref, resp)")
			g.P("}")
			g.P()
		}
	}
}

// emitCardMethodLookup writes the one descriptor lookup every default shares.
//
// By full name out of the global registry, which the generated .pb.go in this
// same package has already populated at init. Cached per (service, method)
// because a card fetch is a request and walking the registry on each one is
// work with a known answer.
func emitCardMethodLookup(g *protogen.GeneratedFile) {
	g.P("// garmCardMethod resolves a method descriptor for a generated card.")
	g.P("//")
	g.P("// The descriptors come from this package's own .pb.go, which registered")
	g.P("// them at init; the lookup is by the full name the contract gives,")
	g.P("// rather than off the file variable, whose Go name is derived from the")
	g.P("// proto path and is a spelling this generator would have to reproduce.")
	g.P("var garmCardMethods ", g.QualifiedGoIdent(syncPkg.Ident("Map")))
	g.P()
	g.P("func garmCardMethod(service, method string) (", g.QualifiedGoIdent(protoreflectPkg.Ident("MethodDescriptor")), ", error) {")
	g.P("key := service + \"/\" + method")
	g.P("if v, ok := garmCardMethods.Load(key); ok {")
	g.P("return v.(", g.QualifiedGoIdent(protoreflectPkg.Ident("MethodDescriptor")), "), nil")
	g.P("}")
	g.P("d, err := ", g.QualifiedGoIdent(protoregistryPkg.Ident("GlobalFiles")), ".FindDescriptorByName(",
		g.QualifiedGoIdent(protoreflectPkg.Ident("FullName")), "(service))")
	g.P("if err != nil {")
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(\"garm cards: %s is not in the descriptor registry: %w\", service, err)")
	g.P("}")
	g.P("sd, ok := d.(", g.QualifiedGoIdent(protoreflectPkg.Ident("ServiceDescriptor")), ")")
	g.P("if !ok {")
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(\"garm cards: %s is not a service\", service)")
	g.P("}")
	g.P("md := sd.Methods().ByName(", g.QualifiedGoIdent(protoreflectPkg.Ident("Name")), "(method))")
	g.P("if md == nil {")
	g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(\"garm cards: %s has no method %s\", service, method)")
	g.P("}")
	g.P("garmCardMethods.Store(key, md)")
	g.P("return md, nil")
	g.P("}")
	g.P()
}

// emitCardEndpoints writes the ServeX registrations for a service's cards.
//
// Each is an ordinary endpoint — the daemon routes to a card exactly as it
// routes to the tool, which is the whole design. The handler prefers an
// override where the handler value has one, by asserting that card's own
// signature, and falls back to the default.
func emitCardEndpoints(g *protogen.GeneratedFile, svc string, cs []serviceCard) {
	for _, c := range cs {
		fqn := string(c.Parent.ParentFile().Package()) + "." + c.ToolName()
		service := string(c.Parent.Parent().FullName())
		subject := service + "." + string(c.Name)
		req := g.QualifiedGoIdent(cardRequestIdent(c))

		g.P("if err := r.Endpoint(")
		g.P(g.QualifiedGoIdent(toolbindPkg.Ident("ToolRef")), "{")
		g.P("FQN: ", fmt.Sprintf("%q", fqn), ",")
		g.P("Subject: ", fmt.Sprintf("%q", subject), ",")
		g.P("Method: ", fmt.Sprintf("%q", string(c.Name)), ",")
		g.P("Service: ", fmt.Sprintf("%q", service), ",")
		g.P("ContractVersion: ContractVersion,")
		g.P("DescriptorHash: DescriptorHash,")
		g.P("},")
		g.P("func() ", g.QualifiedGoIdent(protoPkg.Ident("Message")), " { return new(", req, ") },")
		g.P("func(ctx ", g.QualifiedGoIdent(contextPkg.Ident("Context")), ", req ",
			g.QualifiedGoIdent(protoPkg.Ident("Message")), ") (",
			g.QualifiedGoIdent(protoPkg.Ident("Message")), ", error) {")
		g.P("in, ok := req.(*", req, ")")
		g.P("if !ok {")
		g.P("return nil, ", g.QualifiedGoIdent(fmtPkg.Ident("Errorf")), "(",
			fmt.Sprintf("%q", subject+": request is %T, want %T"), ", req, (*", req, ")(nil))")
		g.P("}")
		g.P("if o, ok := h.(interface{ ", cardSignature(g, c), " }); ok {")
		g.P("return o.", c.Name, "(ctx, in)")
		g.P("}")
		g.P("return Default", c.Name, "(ctx, in)")
		g.P("},")
		g.P("); err != nil {")
		g.P("return err")
		g.P("}")
	}
}
