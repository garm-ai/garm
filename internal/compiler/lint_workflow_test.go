package compiler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
	"github.com/garm-ai/garm/internal/compile"
)

// hasRule reports whether ds contains a diagnostic for rule, of either
// severity. Tasks 3, 4 and 5 append more tests to this file and reuse this
// helper, so it lives here rather than beside any one rule's tests.
func hasRule(ds []Diag, rule string) bool {
	for _, d := range ds {
		if d.Rule == rule {
			return true
		}
	}
	return false
}

// mentions reports whether some diagnostic's message contains substr. A rule
// that refuses without naming the field it refuses teaches nothing, so most
// tests here pin a substring in addition to the rule id.
func mentions(ds []Diag, substr string) bool {
	for _, d := range ds {
		if strings.Contains(d.Msg, substr) {
			return true
		}
	}
	return false
}

// someMessage returns an arbitrary, real message descriptor — any one will
// do, since the tests that use it only care whether Agent.StateMessage is nil
// or not.
func someMessage(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return (&agentv1.RunRef{}).ProtoReflect().Descriptor()
}

func TestA8RefusesAPromptOnAWorkflowAgent(t *testing.T) {
	a := Agent{FQN: "bank.agents.v1.Pay", Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_WORKFLOW,
		Initial: map[string]string{"x": "input.x"},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
		Prompts: map[string]*agentv1.Prompt{"system": {Path: "p.md", Sha256: "ab"}},
	}}
	diags := lintAgentMode(a)
	if !hasRule(diags, "A8") {
		t.Fatalf("want an A8 diagnostic, got %v", diags)
	}
	if !mentions(diags, "prompts") {
		t.Errorf("A8 must name the field it refuses; got %v", diags)
	}
}

func TestA8RefusesAModelOnAWorkflowAgent(t *testing.T) {
	a := Agent{FQN: "bank.agents.v1.Pay", Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_WORKFLOW,
		Initial: map[string]string{"x": "input.x"},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
		Model:   &agentv1.Model{Alias: "fast"},
	}}
	if !hasRule(lintAgentMode(a), "A8") {
		t.Fatal("a workflow agent makes no generation call; a model is dead weight")
	}
}

func TestA8RefusesStepsOnAReactAgent(t *testing.T) {
	a := Agent{FQN: "bank.agents.v1.Ask", Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_REACT,
		Prompts: map[string]*agentv1.Prompt{"system": {Path: "p.md", Sha256: "ab"}},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
	}}
	if !hasRule(lintAgentMode(a), "A8") {
		t.Fatal("react mode has no graph")
	}
}

func TestA8RequiresAGraphOnAWorkflowAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *agentv1.AgentPolicy
	}{
		{"no steps", &agentv1.AgentPolicy{Mode: agentv1.Mode_MODE_WORKFLOW,
			Initial: map[string]string{"x": "input.x"}}},
		{"no initial", &agentv1.AgentPolicy{Mode: agentv1.Mode_MODE_WORKFLOW,
			Steps: []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !hasRule(lintAgentMode(Agent{FQN: "x.Y", Policy: tc.p}), "A8") {
				t.Fatal("want A8")
			}
		})
	}
}

// The negative half. Without it every assertion above passes on a rule that
// refuses everything.
func TestA8AcceptsBothValidShapes(t *testing.T) {
	react := Agent{FQN: "x.Y", Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_REACT,
		Prompts: map[string]*agentv1.Prompt{"system": {Path: "p.md", Sha256: "ab"}},
	}}
	if d := lintAgentMode(react); len(d) != 0 {
		t.Errorf("valid react agent: %v", d)
	}
	wf := Agent{FQN: "x.Z", StateMessage: someMessage(t), Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_WORKFLOW,
		Initial: map[string]string{"x": "input.x"},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
	}}
	if d := lintAgentMode(wf); len(d) != 0 {
		t.Errorf("valid workflow agent: %v", d)
	}
}

func TestA8RequiresGetStateOnAWorkflowAgent(t *testing.T) {
	a := Agent{FQN: "x.Y", StateMessage: nil, Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_WORKFLOW,
		Initial: map[string]string{"x": "input.x"},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
	}}
	if !hasRule(lintAgentMode(a), "A8") {
		t.Fatal("GetState's output is the state type; without it nothing can be checked")
	}
}

func TestA8RefusesGetStateOnAReactAgent(t *testing.T) {
	a := Agent{FQN: "x.Y", StateMessage: someMessage(t), Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_REACT,
		Prompts: map[string]*agentv1.Prompt{"system": {Path: "p.md", Sha256: "ab"}},
	}}
	if !hasRule(lintAgentMode(a), "A8") {
		t.Fatal("react mode has no state")
	}
}

// A2 must be silent on a workflow agent. Without this the rule and A8
// contradict, and the contradiction is only visible when a real agent lints.
func TestA2IsSilentOnAWorkflowAgent(t *testing.T) {
	a := Agent{FQN: "x.Y", Policy: &agentv1.AgentPolicy{
		Mode:    agentv1.Mode_MODE_WORKFLOW,
		Initial: map[string]string{"x": "input.x"},
		Steps:   []*agentv1.Step{{Id: "a", Tool: "t.v1.x"}},
	}}
	if d := lintAgentPrompts(a, Options{}); len(d) != 0 {
		t.Fatalf("a workflow agent has no prompts by design: %v", d)
	}
}

// And still fires on a react agent, or the guard above disabled the rule.
func TestA2StillRequiresASystemPromptInReactMode(t *testing.T) {
	a := Agent{FQN: "x.Y", Policy: &agentv1.AgentPolicy{Mode: agentv1.Mode_MODE_REACT}}
	if !hasRule(lintAgentPrompts(a, Options{}), "A2") {
		t.Fatal("react mode still needs prompts[system]")
	}
}

// a1StateSrc is a minimal well-formed agent service (Invoke + GetRun), with
// extra spliced in just before the service's closing brace — the same anchor
// technique lint_agent_test.go's TestA1RefusesAThirdMethod uses.
func a1StateSrc(extra string) map[string]string {
	return map[string]string{"bank/v1/agent.proto": `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
message Ask {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
service SupportAssistant {
  option (garm.agent.v1.agent) = {
    mode: MODE_REACT
    prompts: { key: "system" value: { path: "prompts/support.md" sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" } }
  };
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant" title: "Support" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant_run" title: "Support run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
` + extra + `}
`, "bank/v1/taxonomy.proto": `syntax = "proto3";
package bank.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "support" description: "Support desk." }] };
`}
}

const getStateRPC = `  rpc GetState(garm.agent.v1.RunRef) returns (Ask) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
`

const setStateRPC = `  rpc SetState(Ask) returns (Ask) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant_set_state" title: "Set state" description: "Write a run's state."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
`

// compileWorkflowFixture writes files into a temp tree and compiles them.
// Defined locally (rather than reusing agents_test.go's compileSource)
// because this file is package compiler — white-box, so it can call
// unexported functions like lintAgentShape directly — while compileSource
// lives in the black-box compiler_test package.
func compileWorkflowFixture(t *testing.T, files map[string]string) []protoreflect.FileDescriptor {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, fds, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return fds
}

// A8's mode governs whether GetState is meaningful; A1 only decides shape. A
// service with Invoke, GetRun and a correctly-typed GetState lints clean
// under A1 — GetState is recognised as a legitimate method, not refused as "a
// third method" — and the same service with SetState added gets exactly one
// A1 diagnostic naming it.
func TestA1AcceptsGetStateAndRefusesSetState(t *testing.T) {
	withState := compileWorkflowFixture(t, a1StateSrc(getStateRPC))
	agents := Agents(withState)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if agents[0].StateMessage == nil {
		t.Fatal("GetState's input is garm.agent.v1.RunRef; StateMessage should be resolved")
	}
	if diags := lintAgentShape(agents[0]); len(diags) != 0 {
		t.Errorf("a correctly-typed GetState produced an A1: %v", diags)
	}

	withSetState := compileWorkflowFixture(t, a1StateSrc(getStateRPC+setStateRPC))
	agents2 := Agents(withSetState)
	if len(agents2) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents2))
	}
	diags := lintAgentShape(agents2[0])
	if len(diags) != 1 {
		t.Fatalf("got %d A1 diagnostics with SetState present, want 1: %v", len(diags), diags)
	}
	if !mentions(diags, "may not declare SetState") {
		t.Errorf("SetState was not refused by name: %v", diags)
	}
}
