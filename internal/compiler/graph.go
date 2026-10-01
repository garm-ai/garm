package compiler

import (
	"fmt"
	"sort"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
)

// edgeRef is one outgoing edge of a step, kept in declaration order so a
// runner's tie-break (Index) and a lint diagnostic's ordering agree.
type edgeRef struct {
	To    string
	When  string // empty = unconditional
	Index int    // declaration order; the runner's tie-break
}

// graph is a workflow agent's control-flow graph: pure data, no CEL and no
// protobuf descriptors, so it can be built and tested without a compiled
// proto tree. Task 4 adds the CEL-checking rules on top of this.
type graph struct {
	entry string
	ids   []string // declaration order
	succ  map[string][]edgeRef
	pred  map[string][]string
}

// newGraph validates structure and returns the graph, or nil and the problems.
// Problems are sentences, because they become Diag messages an author reads.
func newGraph(steps []*agentv1.Step, edges []*agentv1.Edge) (*graph, []string) {
	var probs []string
	g := &graph{succ: map[string][]edgeRef{}, pred: map[string][]string{}}
	seen := map[string]bool{}
	for _, s := range steps {
		id := s.GetId()
		if id == "" {
			probs = append(probs, "a step has an empty id")
			continue
		}
		if seen[id] {
			probs = append(probs, fmt.Sprintf("step id %q is declared twice", id))
			continue
		}
		seen[id] = true
		g.ids = append(g.ids, id)
	}
	for i, e := range edges {
		from, to := e.GetFrom(), e.GetTo()
		if !seen[from] {
			probs = append(probs, fmt.Sprintf(
				"edges[%d].from is %q, which is not a declared step", i, from))
		}
		if !seen[to] {
			probs = append(probs, fmt.Sprintf(
				"edges[%d].to is %q, which is not a declared step", i, to))
		}
		if !seen[from] || !seen[to] {
			continue
		}
		g.succ[from] = append(g.succ[from], edgeRef{To: to, When: e.GetWhen(), Index: i})
		g.pred[to] = append(g.pred[to], from)
	}
	if len(probs) > 0 {
		return nil, probs
	}

	var entries []string
	for _, id := range g.ids {
		if len(g.pred[id]) == 0 {
			entries = append(entries, id)
		}
	}
	switch len(entries) {
	case 1:
		g.entry = entries[0]
	case 0:
		probs = append(probs, "no step has no inbound edge, so nothing can start; "+
			"a graph must have exactly one entry")
	default:
		sort.Strings(entries)
		probs = append(probs, fmt.Sprintf(
			"steps %v all have no inbound edge; a graph must have exactly one "+
				"entry, or where the run begins depends on something outside it",
			entries))
	}
	if len(probs) > 0 {
		return nil, probs
	}
	if cyc := g.findCycle(); cyc != "" {
		return nil, []string{fmt.Sprintf(
			"the graph has a cycle through %s; iteration belongs in react mode or "+
				"inside a tool", cyc)}
	}
	return g, nil
}

// findCycle returns a step on a cycle, or "". Colour-marking DFS: grey means on
// the current path, black means finished.
func (g *graph) findCycle() string {
	const white, grey, black = 0, 1, 2
	colour := map[string]int{}
	var walk func(string) string
	walk = func(id string) string {
		colour[id] = grey
		for _, e := range g.succ[id] {
			switch colour[e.To] {
			case grey:
				return e.To
			case white:
				if hit := walk(e.To); hit != "" {
					return hit
				}
			}
		}
		colour[id] = black
		return ""
	}
	for _, id := range g.ids {
		if colour[id] == white {
			if hit := walk(id); hit != "" {
				return hit
			}
		}
	}
	return ""
}

// dominators returns, for each step, the set of steps every path from the entry
// must pass through — itself included.
//
// This is what makes A7 possible: a state field may be read only where every
// path has written it, and "every path" is exactly this set. The iterative
// formulation is used rather than Lengauer-Tarjan because these graphs have a
// handful of nodes and a reader of this file should not have to know an
// algorithm to check it.
func (g *graph) dominators() map[string]map[string]bool {
	all := map[string]bool{}
	for _, id := range g.ids {
		all[id] = true
	}
	dom := map[string]map[string]bool{}
	for _, id := range g.ids {
		if id == g.entry {
			dom[id] = map[string]bool{id: true}
			continue
		}
		dom[id] = copySet(all)
	}
	for changed := true; changed; {
		changed = false
		for _, id := range g.ids {
			if id == g.entry {
				continue
			}
			// The intersection of the predecessors' dominator sets, plus self.
			var acc map[string]bool
			for _, p := range g.pred[id] {
				if acc == nil {
					acc = copySet(dom[p])
					continue
				}
				for k := range acc {
					if !dom[p][k] {
						delete(acc, k)
					}
				}
			}
			if acc == nil {
				acc = map[string]bool{}
			}
			acc[id] = true
			if !sameBoolSet(acc, dom[id]) {
				dom[id] = acc
				changed = true
			}
		}
	}
	return dom
}

func copySet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k := range in {
		out[k] = true
	}
	return out
}

// sameBoolSet reports whether a and b, sets represented as bool maps, hold
// the same keys. Named distinctly from the []string sameSet in lint_agent.go
// (A5's label-set comparison), which returns a rendered difference rather
// than a bool and takes a different representation — same word, different
// job, so a shared name would collide.
func sameBoolSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
