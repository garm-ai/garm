package compiler

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Rules A9 and A10 — audience (cards-and-tasks design §2.5).
//
// They are A9′ and A10′ in the design, primed because they replace two rules
// an earlier revision wrote against tool SETS. Nothing ever shipped under
// those numbers, so the primes are dropped here and the numbers are simply
// A9 and A10.
//
// The mechanism being replaced is worth naming, because it is why these two
// rules are small. An earlier design kept a card away from a model, and a
// low-level tool out of a person's client, by putting them in `studio` and
// `runner` tool SETS. A set says who HOLDS a tool, which is a different
// question from what a tool is FOR, and answering the second with the first
// meant every tool author had to remember a set name to stay safe. Audience
// is declared once, per tool, and defaults to the safe answer.

// ResolvedAudience is a tool's audience with the default applied: an empty
// declaration reads as AGENT.
//
// One implementation, exported, because the same question is asked by two
// rules here and by the card synthesiser, and "empty means AGENT" written
// three times is a default that eventually disagrees with itself.
func ResolvedAudience(pol *toolv1.ToolPolicy) []toolv1.Audience {
	got := pol.GetAudience()
	if len(got) == 0 {
		return []toolv1.Audience{toolv1.Audience_AUDIENCE_AGENT}
	}
	return got
}

// Admits says whether a tool's audience includes one kind of viewer.
func Admits(pol *toolv1.ToolPolicy, who toolv1.Audience) bool {
	for _, a := range ResolvedAudience(pol) {
		if a == who {
			return true
		}
	}
	return false
}

// audienceNames renders a resolved audience for a diagnostic, as the author
// would have written it.
func audienceNames(pol *toolv1.ToolPolicy) string {
	names := make([]string, 0, 3)
	for _, a := range ResolvedAudience(pol) {
		names = append(names, a.String())
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// lintManifestToolAudience covers A9: a manifest's allowlist may name only
// tools a model may be offered.
//
// This is one rule where an earlier design had three. A card endpoint, a
// person's decision (`decide_task`) and a runner's call (`create_task`) are
// all things a model must never be handed, and all three are now the same
// fact: no AUDIENCE_AGENT. An entry naming one is not a harmless extra —
// the runner projects the model's tool list from the catalogue and filters
// it by this list, so the manifest either lies about what the agent can do
// or the model is offered a call that is refused every time.
//
// Catalogue-scoped, like A3: the tools an allowlist names usually live in
// another directory, so the protoc plugin cannot resolve them and says so
// rather than checking here.
func lintManifestToolAudience(a Agent, tools map[string]Tool) []Diag {
	var out []Diag
	svc := string(a.Service.FullName())
	for i, ref := range a.Policy.GetTools() {
		t, ok := tools[ref.GetFqn()]
		if !ok {
			// A3 owns "names no tool in this catalogue" and has already
			// said so. Reporting it twice under two rule numbers reads as
			// two problems.
			continue
		}
		if Admits(t.Policy, toolv1.Audience_AUDIENCE_AGENT) {
			continue
		}
		out = append(out, Diag{Rule: "A9", Path: svc, Msg: fmt.Sprintf(
			"tools[%d].fqn %q has audience %s, which does not admit an agent; a model is "+
				"never offered it, so an allowlist entry naming it can only lie about what "+
				"this agent can do. Card endpoints, a person's decision and a runner's call "+
				"are all this one fact",
			i, ref.GetFqn(), audienceNames(t.Policy))})
	}
	return out
}

// lintApprovalCardAudience covers A10: a MODE_GRANT tool's approval card
// must be reachable by a person.
//
// It is automatic — the synthesised card carries AUDIENCE_PERSON whatever
// its parent's audience is, because a card exists to be read by a person —
// and it is checked anyway, because the one way it can stop being true is a
// hand-written method taking the synthesised name and declaring something
// else. A grant-mode tool whose approval card no person can fetch is a tool
// whose approvals nobody can decide, and that failure surfaces as a stuck
// queue in production rather than as a build error here.
func lintApprovalCardAudience(t Tool, cardName func(Tool) protoreflect.Name) []Diag {
	if t.Policy.GetApproval().GetMode() != toolv1.Approval_MODE_GRANT {
		return nil
	}
	svc, ok := t.Method.Parent().(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	md := svc.Methods().ByName(cardName(t))
	if md == nil {
		// Nothing hand-written under that name: the synthesised card is
		// what serves, and it carries PERSON by construction.
		return nil
	}
	pol := methodPolicy(md)
	if pol == nil || Admits(pol, toolv1.Audience_AUDIENCE_PERSON) {
		return nil
	}
	return []Diag{{Rule: "A10", Path: string(md.FullName()), Msg: fmt.Sprintf(
		"%s is MODE_GRANT and this is its approval card, but the card's audience is %s and "+
			"does not admit a person; nobody could fetch it, so nobody could decide the "+
			"approvals this tool opens. A card is person-facing whatever its parent is",
		t.Method.FullName(), audienceNames(pol))}}
}

// approvalCardMethodName is the method name an approval card takes on the
// parent's own service.
//
// A service with ONE tool gets the bare name, because there is nothing to
// distinguish it from; a service with several prefixes each card with its
// parent method. Both spellings are the card synthesiser's (§2.1), and this
// is the same rule read backwards: given a tool, which method name would its
// approval card occupy.
func approvalCardMethodName(t Tool) protoreflect.Name {
	svc, ok := t.Method.Parent().(protoreflect.ServiceDescriptor)
	if !ok {
		return protoreflect.Name("ApprovalCard")
	}
	if serviceToolCount(svc) == 1 {
		return protoreflect.Name("ApprovalCard")
	}
	return protoreflect.Name(string(t.Method.Name()) + "ApprovalCard")
}

// serviceToolCount counts the methods on svc that are tools — annotated and
// not excluded. Not svc.Methods().Len(): an excluded RPC and an unannotated
// one are not tools, and a service with one tool beside a health check still
// gets the bare card names.
func serviceToolCount(svc protoreflect.ServiceDescriptor) int {
	n := 0
	for i := 0; i < svc.Methods().Len(); i++ {
		if pol := methodPolicy(svc.Methods().Get(i)); pol != nil && !pol.GetExclude() {
			n++
		}
	}
	return n
}
