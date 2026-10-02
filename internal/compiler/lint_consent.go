package compiler

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoregistry"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"

	"github.com/garm-ai/celenv"
)

// Rules A12-A15 — the consent template an agent may publish (standing-grants
// design §1.1).
//
// A template is the thing a principal is asked to agree to: a scope of
// tools, a sentence they can read, and the limits a grant minted from it
// will carry. It is never a grant itself — the analogue is an OAuth app
// registration declaring the scopes it may request — but an unbounded
// template cannot be meaningfully agreed to, and an unreadable one cannot be
// agreed to at all, and BOTH of those have to fail here, at publish, rather
// than at 3am when a run tries to exercise a grant nobody could really have
// consented to.
//
// A consent block is optional (agentv1.AgentPolicy.Consent may be nil), and
// every rule below is silent for an agent that declares none — there is
// nothing to publish and nothing to refuse.
//
// lintConsentScope (A12), lintConsentDescription (A14) and lintConsentTriggers
// (A15) judge only the agent's OWN declaration — the allowlist consent.scope
// is checked against is a.Policy.GetTools(), not something resolved against
// the catalogue — so they run everywhere, PartialSet included, the same way
// A1, A6 and A8 do. lintConsentLimits (A13) needs the SCOPED tools' verb and
// request descriptors, which usually live in another directory, so it runs
// beside A3's allowlist resolution and A4's guard compilation instead — see
// lintAgents.

// lintConsentScope covers A12: every tool a template's scope names must be
// one the agent could actually call.
//
// A principal cannot meaningfully consent to a tool the agent has no
// allowlist entry for: the grant would mint, and every run against it would
// refuse at the door, which reads to the principal as the platform having
// quietly taken back what they agreed to.
func lintConsentScope(a Agent) []Diag {
	c := a.Policy.GetConsent()
	if c == nil {
		return nil
	}
	svc := string(a.Service.FullName())
	allowed := map[string]bool{}
	for _, ref := range a.Policy.GetTools() {
		allowed[ref.GetFqn()] = true
	}
	var out []Diag
	for i, fqn := range c.GetScope() {
		if !allowed[fqn] {
			out = append(out, Diag{Rule: "A12", Path: svc, Msg: fmt.Sprintf(
				"consent scope[%d] %q is not in this agent's allowlist; a principal "+
					"cannot meaningfully consent to a tool the agent could never call. "+
					"Add it to tools, or remove it from consent.scope", i, fqn)})
		}
	}
	return out
}

// lintConsentDescription covers A14: the sentence a principal reads before
// agreeing must exist.
func lintConsentDescription(a Agent) []Diag {
	c := a.Policy.GetConsent()
	if c == nil || strings.TrimSpace(c.GetDescription()) != "" {
		return nil
	}
	return []Diag{{Rule: "A14", Path: string(a.Service.FullName()), Msg: "" +
		"consent.description is empty. This is the sentence a principal reads " +
		"before agreeing to a standing grant, and a consent nobody can read is " +
		"not consent"}}
}

// lintConsentTriggers covers A15: EXTERNAL_EVENT is refused until the
// deployment declares A3's rate-budget machinery.
//
// An external event is attacker-controlled and a schedule is not, so a
// template that lets a grant fire from one is refused outright for now,
// rather than left to a principal to weigh — so that "e.g. mail received"
// cannot arrive as a trigger kind by accident, before anything bounds how
// often it may fire.
func lintConsentTriggers(a Agent) []Diag {
	c := a.Policy.GetConsent()
	if c == nil {
		return nil
	}
	for _, tr := range c.GetTriggers() {
		if tr == agentv1.TriggerKind_TRIGGER_KIND_EXTERNAL_EVENT {
			return []Diag{{Rule: "A15", Path: string(a.Service.FullName()), Msg: "" +
				"consent.triggers names TRIGGER_KIND_EXTERNAL_EVENT. An external " +
				"event is attacker-controlled and a schedule is not, so a grant " +
				"minted from this template may not be exercised by one until the " +
				"deployment declares A3's rate-budget machinery"}}
		}
	}
	return nil
}

// lintConsentLimits covers A13: default_period_days must be positive, a
// scope naming a WRITE or DESTRUCTIVE tool must declare default_limits
// bounding it, and every caveat in those limits must type-check as a
// predicate against the request message of EVERY tool the scope names.
//
// default_period_days applies to every consent block, writable or not — the
// contract's own field comment says it is "Lint: required, > 0" — so that
// check runs before the writable gate below, which only applies to the
// bounds half.
//
// The caveat half is not optional either. A caveat is evaluated against
// whichever tool in scope a run actually calls — it is "stateless, enforced
// by garmd at grant verification" (Limits.caveats), not tied to one tool —
// so a caveat valid for payments.v1.initiate_payment and nonsense for
// accounts.v1.get_balance cannot be declared on a template spanning both,
// and the refusal names which tool rejected it.
//
// Limits.cumulative is read here ONLY to say it is not yet an enforceable
// bound on its own — never accepted as satisfying the "at least one bound"
// requirement. Nothing enforces it today: garmd holds no database to
// evaluate a cumulative ceiling against, only the OWNING TOOL could from
// its own records once it declares that it does (none does yet), and the
// STS itself refuses to mint a grant that sets one. Accepting a template
// whose only declared bound is one nothing enforces would make this rule
// pass exactly the unbounded-authority case it exists to refuse.
func lintConsentLimits(a Agent, tools map[string]Tool, files *protoregistry.Files) []Diag {
	c := a.Policy.GetConsent()
	if c == nil {
		return nil
	}
	svc := string(a.Service.FullName())
	var out []Diag

	if c.GetDefaultPeriodDays() == 0 {
		out = append(out, Diag{Rule: "A13", Path: svc, Msg: "" +
			"consent.default_period_days is 0. This is the grant's default " +
			"validity period — the STS caps it but does not supply one — and a " +
			"zero-length default is a standing authority nobody could ever use. " +
			"Declare a positive number of days"})
	}

	// scoped is only the tools this run can actually resolve. A scope entry
	// outside the allowlist, or naming no tool at all, is already A12's (or
	// A3's) to report; there is no request descriptor here to check a
	// caveat against, so it is silently excluded from this rule rather than
	// reported a second time under a different number.
	var scoped []Tool
	writable := false
	for _, fqn := range c.GetScope() {
		t, ok := tools[fqn]
		if !ok {
			continue
		}
		scoped = append(scoped, t)
		if v := t.Policy.GetVerb(); v == toolv1.Verb_VERB_WRITE || v == toolv1.Verb_VERB_DESTRUCTIVE {
			writable = true
		}
	}
	if !writable {
		return out // nothing in scope needs bounding
	}

	limits := c.GetDefaultLimits()
	bounded := limits != nil && (limits.GetMaxRuns() > 0 || len(limits.GetCaveats()) > 0)
	switch {
	case bounded:
		// at least one enforceable bound; nothing to refuse here.
	case limits != nil && len(limits.GetCumulative()) > 0:
		out = append(out, Diag{Rule: "A13", Path: svc, Msg: "" +
			"consent.scope names a WRITE or DESTRUCTIVE tool, and " +
			"default_limits bounds it with a cumulative ceiling alone. A " +
			"cumulative ceiling is not yet an enforceable bound: garmd holds " +
			"no database to evaluate it against, no tool yet declares that it " +
			"enforces one from its own records, and the STS refuses to mint a " +
			"grant that sets one. Declare max_runs or a caveat, in addition to " +
			"or instead of the cumulative ceiling"})
	default:
		out = append(out, Diag{Rule: "A13", Path: svc, Msg: "" +
			"consent.scope names a WRITE or DESTRUCTIVE tool and declares no " +
			"default_limits bounding it. An unbounded standing authority cannot " +
			"be meaningfully consented to. Declare at least one of max_runs or a " +
			"caveat"})
	}

	for _, caveat := range limits.GetCaveats() {
		for _, t := range scoped {
			env, err := celenv.Env(files, t.Method.Input())
			if err != nil {
				out = append(out, Diag{Rule: "A13", Path: svc, Msg: fmt.Sprintf(
					"consent.default_limits.caveats %q cannot be checked against %s: "+
						"building its CEL environment failed: %v",
					caveat, t.Method.Input().FullName(), err)})
				continue
			}
			if _, _, err := celenv.Check(env, caveat); err != nil {
				out = append(out, Diag{Rule: "A13", Path: svc, Msg: fmt.Sprintf(
					"consent.default_limits.caveats %q is not a predicate against "+
						"%s's request (tool %q rejected it): %v",
					caveat, t.Method.FullName(), t.Name, err)})
			}
		}
	}
	return out
}
