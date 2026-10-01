package compiler

import (
	"strings"
	"testing"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
)

func TestAGraphNeedsExactlyOneEntry(t *testing.T) {
	// Two steps, neither with an inbound edge.
	_, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}, {Id: "b"}},
		[]*agentv1.Edge{},
	)
	if len(probs) == 0 {
		t.Fatal("two entries must be refused")
	}
}

func TestAnEdgeToAnUnknownStepIsNamed(t *testing.T) {
	_, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}},
		[]*agentv1.Edge{{From: "a", To: "nope"}},
	)
	if len(probs) == 0 {
		t.Fatal("an edge to an undeclared step must be refused")
	}
	joined := strings.Join(probs, "; ")
	if !strings.Contains(joined, "nope") {
		t.Errorf("the diagnostic must name the missing step; got %q", joined)
	}
}

// The fixture must have an unambiguous entry, or the two-entry check fires
// first and the test passes for the wrong reason (mutual a<->b has no node
// with zero predecessors, so newGraph never reaches findCycle). a is the
// entry here; the cycle is b<->c.
func TestACycleIsRefused(t *testing.T) {
	_, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}, {Id: "b"}, {Id: "c"}},
		[]*agentv1.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "b"}},
	)
	if len(probs) == 0 {
		t.Fatal("a cycle must be refused; iteration belongs in react mode")
	}
	joined := strings.Join(probs, "; ")
	if !strings.Contains(joined, "cycle") {
		t.Errorf("the diagnostic must name the cycle; got %q", joined)
	}
}

// The fixture must be a graph that is otherwise entirely clean — a single
// entry, valid edges, no cycle — so that with the duplicate id merged away
// (as if the dedup guard did not exist) newGraph would report nothing at
// all. Steps a and the two b's, with an edge a->b: drop dedup and this
// becomes a clean single-entry DAG (two step records sharing one id, one
// inbound edge), so only the duplicate-id guard itself can object.
func TestDuplicateStepIdsAreRefused(t *testing.T) {
	_, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}, {Id: "b"}, {Id: "b"}},
		[]*agentv1.Edge{{From: "a", To: "b"}},
	)
	if len(probs) == 0 {
		t.Fatal("duplicate ids must be refused")
	}
	joined := strings.Join(probs, "; ")
	if !strings.Contains(joined, `"b"`) {
		t.Errorf("the diagnostic must name the duplicated id; got %q", joined)
	}
}

// A6's totality rule: a step with outgoing edges must have an unconditional one.
func TestAConditionalOnlyFanOutIsRefused(t *testing.T) {
	a := Agent{FQN: "x.Y", Policy: &agentv1.AgentPolicy{
		Mode:  agentv1.Mode_MODE_WORKFLOW,
		Steps: []*agentv1.Step{{Id: "a"}, {Id: "b"}, {Id: "c"}},
		Edges: []*agentv1.Edge{
			{From: "a", To: "b", When: "state.x"},
			{From: "a", To: "c", When: "!state.x"},
		},
	}}
	if !hasRule(lintWorkflowGraph(a), "A6") {
		t.Fatal("without an unconditional edge a typo in a `when` ends the run silently")
	}
}

func TestAValidGraphIsAccepted(t *testing.T) {
	g, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}, {Id: "b"}, {Id: "c"}},
		[]*agentv1.Edge{
			{From: "a", To: "b", When: "state.x"},
			{From: "a", To: "c"},
		},
	)
	if len(probs) != 0 {
		t.Fatalf("valid graph rejected: %v", probs)
	}
	if g.entry != "a" {
		t.Errorf("entry = %q, want a", g.entry)
	}
}

// Dominators, which Task 4 depends on. The diamond is the case that matters:
// `d` is reachable via `b` and via `c`, so neither dominates it.
func TestDominatorsOfADiamond(t *testing.T) {
	g, probs := newGraph(
		[]*agentv1.Step{{Id: "a"}, {Id: "b"}, {Id: "c"}, {Id: "d"}},
		[]*agentv1.Edge{
			{From: "a", To: "b", When: "state.x"},
			{From: "a", To: "c"},
			{From: "b", To: "d"},
			{From: "c", To: "d"},
		},
	)
	if len(probs) != 0 {
		t.Fatalf("setup: %v", probs)
	}
	dom := g.dominators()
	if !dom["d"]["a"] {
		t.Error("the entry dominates every step")
	}
	if dom["d"]["b"] {
		t.Error("b does not dominate d: the c path skips it — this is the whole rule")
	}
	if !dom["b"]["a"] || !dom["b"]["b"] {
		t.Error("a step dominates itself, and the entry dominates it")
	}
}
