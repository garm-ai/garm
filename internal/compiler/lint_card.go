package compiler

import (
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	cardv1 "github.com/garm-ai/contracts/garm/card/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
)

// Rule C1 — a card template references only what will be there to render
// (studio cards design §3.4, with C3 folded in).
//
// A template is read by agentd from the catalogue generation a task or run
// pinned, and rendered for a viewer who is deciding something. A `{field}`
// that resolves to nothing is not a blank in the card: it is a fact the
// author believed the approver would see and the approver will not. That is
// the same argument L32 makes for material_fields, one step later.
//
// Two placements, two sources of truth:
//
//   - task_card, on a MODE_GRANT tool. The task service stores the material
//     fields of the request and nothing else of it, so a reference names one
//     of approval.material_fields, by the same dotted path. (This is the
//     spec's C3; folded in here because "resolves against the request" and
//     "is material" are one check until the task keeps a projected request.)
//   - result_card, on an agent service. It renders RunStatus.result, which
//     is a google.protobuf.StringValue until agents declare an output type
//     (program plan §7 item 17). There is no field to resolve against, so a
//     result card in this release carries literal text only, and any
//     reference is an error that says why.
//
// `context.<field>` references are C7's business — the context RPC and the
// shape of its response — and C7 is not in this release. They are skipped
// here rather than refused, so that an author who writes one is told nothing
// false; KNOWN-GAPS says they are unchecked.
//
// Reported against the METHOD (task_card) or the SERVICE (result_card), like
// every rule here: a catalogue is built from many trees and "which team wrote
// this" has to be answerable from the diagnostic alone.
func lintCards(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			if tpl := serviceResultCard(svc); tpl != nil {
				out = append(out, lintResultCard(svc, tpl)...)
			}
			for j := 0; j < svc.Methods().Len(); j++ {
				md := svc.Methods().Get(j)
				if tpl := methodTaskCard(md); tpl != nil {
					out = append(out, lintTaskCard(md, tpl)...)
				}
			}
		}
	}
	return out
}

// refRE finds `{field}` references in a template string. Anything between a
// pair of braces is a reference; there is no escape, because a card is plain
// text and a literal brace has no meaning in one.
var refRE = regexp.MustCompile(`\{([^{}]*)\}`)

// templateRef is one reference in a template, with where it was found so a
// diagnostic can point at it: "title", "body[1].facts[0]", or
// "body[2].section.elements[0].text".
type templateRef struct {
	where string
	field string
}

// templateRefs walks a template in declaration order and lists every
// reference it makes: braces in the title and in text, and every
// FactRef.field, which is a reference by construction.
func templateRefs(tpl *cardv1.Template) []templateRef {
	var refs []templateRef
	refs = append(refs, stringRefs("title", tpl.GetTitle())...)
	refs = append(refs, elementRefs("body", tpl.GetBody())...)
	return refs
}

func stringRefs(where, s string) []templateRef {
	var refs []templateRef
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		refs = append(refs, templateRef{where: where, field: m[1]})
	}
	return refs
}

func elementRefs(where string, els []*cardv1.TemplateElement) []templateRef {
	var refs []templateRef
	for i, el := range els {
		at := fmt.Sprintf("%s[%d]", where, i)
		switch of := el.GetOf().(type) {
		case *cardv1.TemplateElement_Text:
			refs = append(refs, stringRefs(at+".text", of.Text)...)
		case *cardv1.TemplateElement_Facts:
			for j, f := range of.Facts.GetFacts() {
				refs = append(refs, templateRef{where: fmt.Sprintf("%s.facts[%d]", at, j), field: f.GetField()})
			}
		case *cardv1.TemplateElement_Section:
			refs = append(refs, stringRefs(at+".section.title", of.Section.GetTitle())...)
			refs = append(refs, elementRefs(at+".section.elements", of.Section.GetElements())...)
		}
	}
	return refs
}

// isContextRef says whether a reference names the context RPC's response
// rather than the request or output message. Not checked in this release;
// see the package comment.
func isContextRef(field string) bool {
	return strings.HasPrefix(field, "context.")
}

// lintTaskCard covers C1 for a task_card.
func lintTaskCard(md protoreflect.MethodDescriptor, tpl *cardv1.Template) []Diag {
	var out []Diag
	path := string(md.FullName())

	pol := methodPolicy(md)
	if pol == nil || pol.GetExclude() {
		return append(out, Diag{Rule: "C1", Path: path,
			Msg: "task_card on a method that carries no (garm.tool.v1.tool), or excludes " +
				"itself; a task card is rendered for the open approval of a governed tool " +
				"and nothing renders one here"})
	}
	if mode := pol.GetApproval().GetMode(); mode != toolv1.Approval_MODE_GRANT {
		return append(out, Diag{Rule: "C1", Path: path, Msg: fmt.Sprintf(
			"task_card on a tool whose approval mode is %v, not MODE_GRANT; a task card is "+
				"the open approval, and a tool nobody approves never has one", mode)})
	}

	material := pol.GetApproval().GetMaterialFields()
	allowed := make(map[string]bool, len(material))
	for _, f := range material {
		allowed[f] = true
	}

	for _, r := range templateRefs(tpl) {
		switch {
		case r.field == "":
			out = append(out, Diag{Rule: "C1", Path: path, Msg: fmt.Sprintf(
				"task_card %s references nothing: the field name is empty", r.where)})
		case isContextRef(r.field):
			continue
		case !allowed[r.field]:
			if len(material) == 0 {
				out = append(out, Diag{Rule: "C1", Path: path, Msg: fmt.Sprintf(
					"task_card %s references {%s}, and this tool lists no "+
						"approval.material_fields; the task stores the material fields and "+
						"nothing else of the request, so a task card here can carry literal "+
						"text only", r.where, r.field)})
				continue
			}
			out = append(out, Diag{Rule: "C1", Path: path, Msg: fmt.Sprintf(
				"task_card %s references {%s}, which is not in approval.material_fields %q; "+
					"the task stores the material fields and nothing else of the request, "+
					"so a card can only show those", r.where, r.field, material)})
		}
	}
	return out
}

// lintResultCard covers C1 for a result_card.
func lintResultCard(svc protoreflect.ServiceDescriptor, tpl *cardv1.Template) []Diag {
	var out []Diag
	path := string(svc.FullName())

	if ServiceAgentPolicy(svc) == nil {
		return append(out, Diag{Rule: "C1", Path: path,
			Msg: "result_card on a service that carries no (garm.agent.v1.agent); a result " +
				"card renders an agent's run, and only an agent has runs"})
	}

	for _, r := range templateRefs(tpl) {
		if r.field != "" && isContextRef(r.field) {
			continue
		}
		out = append(out, Diag{Rule: "C1", Path: path, Msg: fmt.Sprintf(
			"result_card %s references {%s}, but agents cannot declare an output type yet "+
				"(RunStatus.result is a google.protobuf.StringValue), so a result card in this "+
				"release carries literal text only; there is no field for the reference to "+
				"resolve against", r.where, r.field)})
	}
	return out
}

// serviceResultCard returns the result_card template on svc, or nil.
//
// The type assertion mirrors methodPolicy: protocompile hands back a dynamic
// message unless the option was re-parsed against the linked extension types,
// which internal/compile.Tree does for the whole set.
func serviceResultCard(svc protoreflect.ServiceDescriptor) *cardv1.Template {
	opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
	if !ok || !proto.HasExtension(opts, cardv1.E_ResultCard) {
		return nil
	}
	tpl, _ := proto.GetExtension(opts, cardv1.E_ResultCard).(*cardv1.Template)
	return tpl
}

// methodTaskCard returns the task_card template on md, or nil.
func methodTaskCard(md protoreflect.MethodDescriptor) *cardv1.Template {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(opts, cardv1.E_TaskCard) {
		return nil
	}
	tpl, _ := proto.GetExtension(opts, cardv1.E_TaskCard).(*cardv1.Template)
	return tpl
}
