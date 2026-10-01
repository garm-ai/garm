package compiler

import (
	"fmt"
	"testing"
)

// A7's agent-step fixtures need TWO agents, one invoking the other, not one
// agent invoking itself.
//
// A self-invoking agent is the fixture that looks right and proves nothing:
// its own Invoke and its own GetRun happen to be the same two methods the
// rule is being asked to tell apart, so a substitution that silently did
// nothing could still pass a test built that way. Here `Orchestrator` is one
// service and `Child` is a different one, in a different package, with its
// own Invoke and its own GetRun — the shape a real graph has, and the shape
// examples/bank/proto's new_beneficiary_payment agent actually is (it calls
// bank.agents.v1.compliance_screen, a sibling, not itself).

// childSrc is the invoked agent: Invoke returns garm.agent.v1.RunRef, GetRun
// returns garm.agent.v1.RunStatus, exactly as A1 requires of any agent. It
// declares no steps of its own — MODE_UNSPECIFIED is fine here, because
// nothing in this file runs A8 against it; only Agents() needs to resolve its
// Invoke and GetRun, which does not depend on mode.
const childSrc = `syntax = "proto3";
package child.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";

message ChildRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string topic = 1;
}

service Child {
  option (garm.agent.v1.agent) = {};
  rpc Invoke(ChildRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "child" title: "Child" description: "Run the child agent."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "child_run" title: "Child run" description: "Read the child's run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`

// plainSrc is a tool that answers garm.agent.v1.RunRef and belongs to no
// agent at all — nothing forbids a plain tool from reusing the type as its
// own reply. It exists to prove the OTHER direction of the fix: such a tool
// must still be checked against RunRef itself, never substituted, because
// agentStepReplies only has an entry for a tool that IS some agent's Invoke.
const plainSrc = `syntax = "proto3";
package plain.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";

message WeirdRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
}

service Plain {
  rpc Weird(WeirdRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "weird" title: "Weird" description: "A plain tool that answers a RunRef."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`

// orcSrc is the invoking agent. orcStep is spliced in so each test writes
// only the `set` expression it is about; everything else — the allowlist,
// the one step's `id` and `tool`, the terminal (edge-less) graph — stays
// fixed.
func orcSrc(orcStep string) string {
	return `syntax = "proto3";
package orc.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";

message OrchRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string topic = 1;
}
message OrchState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 run_state = 1;
}

service Orchestrator {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [ { fqn: "child.v1.child" }, { fqn: "plain.v1.weird" } ]
    steps: [` + orcStep + `]
    edges: []
  };
  rpc Invoke(OrchRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "orchestrator" title: "Orchestrator" description: "Run the orchestrator."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (OrchState) {
    option (garm.tool.v1.tool) = {
      name: "orchestrator_state" title: "Orchestrator state" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
}

// agentStepFixture compiles Child, Plain and Orchestrator as one set — the
// way `garm lint` sees a tree — and returns the Orchestrator agent, every
// agent in the set (for agentStepReplies) and the tool index.
func agentStepFixture(t *testing.T, orcStep string) (Agent, []Agent, map[string]Tool) {
	t.Helper()
	fds := compileWorkflowFixture(t, map[string]string{
		"child/v1/child.proto": childSrc,
		"plain/v1/plain.proto": plainSrc,
		"orc/v1/orc.proto":     orcSrc(orcStep),
	})
	agents := Agents(fds)
	if len(agents) != 2 {
		t.Fatalf("found %d agents, want 2 (Child, Orchestrator)", len(agents))
	}
	tools := toolIndex(fds)
	var orchestrator Agent
	found := false
	for _, a := range agents {
		if a.FQN == "orc.v1.Orchestrator" {
			orchestrator, found = a, true
		}
	}
	if !found {
		t.Fatalf("orc.v1.Orchestrator not found among %d agents", len(agents))
	}
	if orchestrator.StateMessage == nil {
		t.Fatal("GetState returns OrchState; StateMessage should have resolved")
	}
	return orchestrator, agents, tools
}

func stepReadingResponse(expr string) string {
	return fmt.Sprintf(
		`{ id: "ask_child" tool: "child.v1.child" set: [{key:"run_state" value:%q}] }`, expr)
}

// THE REGRESSION TEST. garm.agent.v1.RunState.state exists on RunStatus — the
// child's GetRun output, and what the runner actually hands this step's
// `set` at run time (design §6.1 frames 10e-10f) — and NOT on RunRef, which
// is all Invoke itself returns. Before the fix `response` was bound to the
// tool's own declared output (RunRef) for every step, agent or not, so this
// expression was refused by lint and accepted by the runner: exactly the
// disagreement the bug report describes, and the one examples/bank/proto's
// new_beneficiary_payment agent demonstrated live.
func TestA7AcceptsAnAgentStepFieldOnlyOnRunStatus(t *testing.T) {
	a, agents, tools := agentStepFixture(t, stepReadingResponse("response.state"))
	agentReplies := agentStepReplies(agents, tools)
	diags := lintWorkflowExpressionsWith(a, tools, agentReplies, Options{})
	if hasRule(diags, "A7") {
		t.Fatalf("response.state is RunStatus's field, and RunStatus is what this "+
			"step's `response` resolves to at run time; lint must accept it: %v", diags)
	}
}

// THE OTHER DIRECTION. A field on neither RunRef nor RunStatus must still be
// refused, naming the field. Without this, a substitution that bound
// `response` to something permissive enough to accept `response.state` — or
// to anything else that is not actually RunStatus — would pass the test above
// while checking nothing at all.
func TestA7RefusesAnAgentStepFieldOnNeither(t *testing.T) {
	a, agents, tools := agentStepFixture(t, stepReadingResponse("response.not_a_real_field"))
	agentReplies := agentStepReplies(agents, tools)
	diags := lintWorkflowExpressionsWith(a, tools, agentReplies, Options{})
	if !hasRule(diags, "A7") {
		t.Fatalf("a field on neither RunRef nor RunStatus must be refused: %v", diags)
	}
	if !mentions(diags, "not_a_real_field") {
		t.Errorf("the refusal must name the field: %v", diags)
	}
}

// THE OTHER HALF OF THE FIX. plain.v1.weird answers garm.agent.v1.RunRef —
// nothing forbids a plain tool from reusing the type — but it is nobody's
// Invoke, so agentStepReplies has no entry for it and `response` must stay
// bound to RunRef itself. A field that only RunStatus has must still be
// refused on a step calling THIS tool, exactly as it was before the fix,
// because substituting here would be wrong in the other direction: this tool
// never awaits a run and never answers RunStatus at any time.
func TestA7StillChecksAPlainRunRefToolAgainstRunRefItself(t *testing.T) {
	step := `{ id: "ask_child" tool: "plain.v1.weird" set: [{key:"run_state" value:"response.state"}] }`
	a, agents, tools := agentStepFixture(t, step)
	agentReplies := agentStepReplies(agents, tools)
	diags := lintWorkflowExpressionsWith(a, tools, agentReplies, Options{})
	if !hasRule(diags, "A7") {
		t.Fatalf("plain.v1.weird answers RunRef, which has no `state` field, and it "+
			"must stay refused: %v", diags)
	}
	if !mentions(diags, "state") {
		t.Errorf("the refusal must name the field: %v", diags)
	}

	// And the field RunRef DOES have is still fine, so this is a refusal of
	// the field and not of the step.
	okStep := `{ id: "ask_child" tool: "plain.v1.weird" with: [] set: [{key:"run_state" value:"0"}] }`
	a2, agents2, tools2 := agentStepFixture(t, okStep)
	diags2 := lintWorkflowExpressionsWith(a2, tools2, agentStepReplies(agents2, tools2), Options{})
	if hasRule(diags2, "A7") {
		t.Fatalf("a literal into run_state must not be refused: %v", diags2)
	}
}
