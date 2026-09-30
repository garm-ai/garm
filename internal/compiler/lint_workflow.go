package compiler

import (
	"fmt"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
)

// lintAgentMode covers A8: the mode and the fields agree.
//
// A mode enum earns its keep by making the invalid combinations named rather
// than reachable states nobody tested. Every refusal here names the field, so
// an author is told what to remove and not merely that something is wrong.
//
// A1 decides what methods a service may HAVE; this rule decides which are
// valid IN THIS MODE. So a GetState on a react agent is refused here, not by
// A1 — one rule per question, so an author gets one diagnostic per mistake
// rather than two for the same one.
func lintAgentMode(a Agent) []Diag {
	var out []Diag
	p := a.Policy
	bad := func(format string, args ...any) {
		out = append(out, Diag{Rule: "A8", Path: string(a.FQN),
			Msg: fmt.Sprintf(format, args...)})
	}

	switch p.GetMode() {
	case agentv1.Mode_MODE_WORKFLOW:
		if len(p.GetSteps()) == 0 {
			bad("mode is MODE_WORKFLOW and declares no steps; a workflow is a graph")
		}
		if len(p.GetInitial()) == 0 {
			bad("mode is MODE_WORKFLOW and declares no initial; `initial` is the " +
				"only place a caller's request enters the state")
		}
		// A graph of tool calls makes no generation call of its own, so both of
		// these would be dead weight in the annotation — and dead weight is what
		// teaches a reviewer to skim.
		if len(p.GetPrompts()) > 0 {
			bad("mode is MODE_WORKFLOW and declares prompts; a graph of tool " +
				"calls makes no generation call, so remove them (the model " +
				"belongs to whichever agent a step invokes)")
		}
		if p.GetModel() != nil {
			bad("mode is MODE_WORKFLOW and declares a model; a graph of tool " +
				"calls makes no generation call, so remove it")
		}
		if a.StateMessage == nil {
			bad("mode is MODE_WORKFLOW and the service declares no GetState; " +
				"GetState's output type IS the state type, so there is nothing " +
				"for the graph's expressions to be checked against")
		}
	case agentv1.Mode_MODE_REACT:
		if p.GetPrompts()["system"] == nil {
			bad("mode is MODE_REACT and has no prompts[\"system\"]")
		}
		for field, n := range map[string]int{
			"steps": len(p.GetSteps()), "edges": len(p.GetEdges()),
			"initial": len(p.GetInitial()),
		} {
			if n > 0 {
				bad("mode is MODE_REACT and declares %s; react mode has no graph", field)
			}
		}
		if a.StateMessage != nil {
			bad("mode is MODE_REACT and the service declares GetState; react " +
				"mode has no state to read")
		}
	default:
		bad("mode is unset; declare MODE_REACT or MODE_WORKFLOW")
	}
	return out
}

// lintWorkflowGraph covers A6: the graph is a graph.
func lintWorkflowGraph(a Agent) []Diag {
	p := a.Policy
	if p.GetMode() != agentv1.Mode_MODE_WORKFLOW {
		return nil
	}
	var out []Diag
	g, probs := newGraph(p.GetSteps(), p.GetEdges())
	for _, s := range probs {
		out = append(out, Diag{Rule: "A6", Path: string(a.FQN), Msg: s})
	}
	if g == nil {
		return out
	}
	// Control flow must be TOTAL. A step with outgoing edges needs at least one
	// unconditional one, so "no predicate matched" cannot arise: without this a
	// typo in a `when` ends a run early and silently, which is the failure mode
	// that looks like success.
	for _, id := range g.ids {
		es := g.succ[id]
		if len(es) == 0 {
			continue // terminal, by having no outgoing edge
		}
		total := false
		for _, e := range es {
			if e.When == "" {
				total = true
				break
			}
		}
		if !total {
			out = append(out, Diag{Rule: "A6", Path: string(a.FQN), Msg: fmt.Sprintf(
				"every outgoing edge of step %q carries a `when`, so a run reaches "+
					"it and stops if none matches. Add one unconditional edge as the "+
					"fallback, or make %q terminal by removing its edges", id, id)})
		}
	}
	return out
}
