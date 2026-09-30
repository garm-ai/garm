package compiler

import (
	"testing"
)

// A11's fixtures each get their own proto source, full state message and
// field policies spelled out, per the brief: sharing one fixture across the
// five cases is how a test ends up asserting the fixture instead of the rule.
// Each follows lint_workflow_expr_test.go's pattern — an agent package and a
// tool package compiled together — since A11, like A7, resolves a `set`
// source against the tool's RESPONSE descriptor.

// TestA11RefusesAWeakerStateField: a direct copy from a field that reads at
// CONFIDENTIAL under "kyc" into a state field that claims only INTERNAL, with
// no `derives` to say the downgrade is deliberate.
func TestA11RefusesAWeakerStateField(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message WeakRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message WeakState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // Claims INTERNAL, though "check" (below) writes it from a field that
  // reads at CONFIDENTIAL under "kyc" — the weaker grade this test refuses.
  optional bool flag = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_INTERNAL
    on_deny: { omit: {} }
  }];
}

service WeakAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.check" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "check" tool: "s.v1.check"
      set: [{ key: "flag" value: "response.flagged" }] }]
  };
  rpc Invoke(WeakRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "weak_agent" title: "Weak" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "weak_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (WeakState) {
    option (garm.tool.v1.tool) = {
      name: "weak_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message CheckRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message CheckResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional bool flagged = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_CONFIDENTIAL
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}
service Checker {
  rpc Check(CheckRequest) returns (CheckResponse) {
    option (garm.tool.v1.tool) = {
      name: "check" title: "Check" description: "Check a subject."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/check.proto":    toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if agents[0].StateMessage == nil {
		t.Fatal("GetState returns WeakState; StateMessage should have resolved")
	}
	diags := lintStatePropagation(agents[0], toolIndex(fds))
	if !hasRule(diags, "A11") {
		t.Fatalf("publishing a governed value at a weaker grade is a disclosure; got %v", diags)
	}
	if !mentions(diags, "flag") || !mentions(diags, "flagged") {
		t.Errorf("A11 must name both the state field and its source: %v", diags)
	}
}

// TestA11AcceptsAnInheritedPolicy: the state field is graded EXACTLY like the
// field it is copied from — same clearance, same compartment.
func TestA11AcceptsAnInheritedPolicy(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message MatchingRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message MatchingState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // Inherits the source's grade exactly: CONFIDENTIAL, "kyc".
  optional bool flag = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_CONFIDENTIAL
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}

service MatchingAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.check" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "check" tool: "s.v1.check"
      set: [{ key: "flag" value: "response.flagged" }] }]
  };
  rpc Invoke(MatchingRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "matching_agent" title: "Matching" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "matching_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (MatchingState) {
    option (garm.tool.v1.tool) = {
      name: "matching_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message CheckRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message CheckResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional bool flagged = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_CONFIDENTIAL
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}
service Checker {
  rpc Check(CheckRequest) returns (CheckResponse) {
    option (garm.tool.v1.tool) = {
      name: "check" title: "Check" description: "Check a subject."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/check.proto":    toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if d := lintStatePropagation(agents[0], toolIndex(fds)); len(d) != 0 {
		t.Fatalf("an exactly-inherited policy must pass: %v", d)
	}
}

// TestA11IsSilentWhenTheSourceIsUngoverned: `set` derives from a response
// field that carries neither its own field_policy nor a message default, so
// there is no classification to propagate — regardless of what the state
// field claims, and without demanding a `derives` annotation either.
//
// The read is behind `size(...)` — a non-direct selection — rather than a
// bare copy, deliberately: a bare copy's zero-value fallback (an ungoverned
// source reads as CLEARANCE_UNSPECIFIED, which is always <= any real state
// grade) would let this test pass whether or not the silence guard runs at
// all. Only the non-direct branch, which otherwise unconditionally demands
// `derives`, can actually tell the guard's presence from its absence.
func TestA11IsSilentWhenTheSourceIsUngoverned(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message UngovernedRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message UngovernedState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 item_count = 1;
}

service UngovernedAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.lookup" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "lookup" tool: "s.v1.lookup"
      set: [{ key: "item_count" value: "size(response.items)" }] }]
  };
  rpc Invoke(UngovernedRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "ungoverned_agent" title: "Ungoverned" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "ungoverned_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (UngovernedState) {
    option (garm.tool.v1.tool) = {
      name: "ungoverned_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	// LookupResponse carries NO default_field_policy and `items` carries no
	// field_policy of its own — the one case in this file where a field's
	// effective policy resolves to nil.
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message LookupRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message LookupResponse {
  repeated string items = 1;
}
service Lookuper {
  rpc Lookup(LookupRequest) returns (LookupResponse) {
    option (garm.tool.v1.tool) = {
      name: "lookup" title: "Lookup" description: "Look a subject up."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/lookup.proto":   toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if d := lintStatePropagation(agents[0], toolIndex(fds)); len(d) != 0 {
		t.Fatalf("propagation must be silent when there is nothing to propagate: %v", d)
	}
}

// TestA11AcceptsADeclaredDowngrade: `match_count` is `size(response.matches)`
// — a count of a RESTRICTED, "kyc" list — declared with a `derives` carrying
// a real reason, per the design's own worked example.
func TestA11AcceptsADeclaredDowngrade(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message ScreenAgentRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message ScreenAgentState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // A count of a RESTRICTED list is routing information, not the names, so
  // it downgrades with a reason.
  optional int64 match_count = 1 [
    (garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } },
    (garm.agent.v1.derives) = {
      from: "response.matches"
      reason: "a count of a RESTRICTED list is routing information, not the "
              "names or why they matched, and a supervisor can act on it "
              "without seeing either"
    }
  ];
}

service ScreenAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.screen" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "screen" tool: "s.v1.screen"
      set: [{ key: "match_count" value: "size(response.matches)" }] }]
  };
  rpc Invoke(ScreenAgentRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "screen_agent" title: "Screen" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "screen_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (ScreenAgentState) {
    option (garm.tool.v1.tool) = {
      name: "screen_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message ScreenRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message Match {
  option (garm.tool.v1.default_field_policy) = {
    read: CLEARANCE_RESTRICTED
    compartments: ["kyc"]
    on_deny: { omit: {} }
  };
  optional string list_name = 1;
}
message ScreenResponse {
  repeated Match matches = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_RESTRICTED
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}
service Screener {
  rpc Screen(ScreenRequest) returns (ScreenResponse) {
    option (garm.tool.v1.tool) = {
      name: "screen" title: "Screen" description: "Screen a subject."
      verb: VERB_READ min_clearance: CLEARANCE_CONFIDENTIAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/screen.proto":   toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if d := lintStatePropagation(agents[0], toolIndex(fds)); len(d) != 0 {
		t.Fatalf("a declared downgrade with a reason must pass: %v", d)
	}
}

// TestA11RefusesADowngradeWithNoReason: the same shape as the declared
// downgrade above, but `derives.reason` is empty — the escape hatch with
// nothing in it, which must be refused rather than silently accepted.
func TestA11RefusesADowngradeWithNoReason(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message SilentAgentRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message SilentAgentState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 match_count = 1 [
    (garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } },
    (garm.agent.v1.derives) = { from: "response.matches" reason: "" }
  ];
}

service SilentAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.screen" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "screen" tool: "s.v1.screen"
      set: [{ key: "match_count" value: "size(response.matches)" }] }]
  };
  rpc Invoke(SilentAgentRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "silent_agent" title: "Silent" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "silent_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (SilentAgentState) {
    option (garm.tool.v1.tool) = {
      name: "silent_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message ScreenRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message Match {
  option (garm.tool.v1.default_field_policy) = {
    read: CLEARANCE_RESTRICTED
    compartments: ["kyc"]
    on_deny: { omit: {} }
  };
  optional string list_name = 1;
}
message ScreenResponse {
  repeated Match matches = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_RESTRICTED
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}
service Screener {
  rpc Screen(ScreenRequest) returns (ScreenResponse) {
    option (garm.tool.v1.tool) = {
      name: "screen" title: "Screen" description: "Screen a subject."
      verb: VERB_READ min_clearance: CLEARANCE_CONFIDENTIAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/screen.proto":   toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	diags := lintStatePropagation(agents[0], toolIndex(fds))
	if !hasRule(diags, "A11") {
		t.Fatal("a downgrade whose escape hatch is silence is no rule at all")
	}
	if !mentions(diags, "empty reason") && !mentions(diags, "empty") {
		t.Errorf("A11 must say the reason is empty, not just refuse: %v", diags)
	}
}

// TestA11IsWiredIntoLintAgents proves A11 is reached from the real entry
// point, `lintAgents` — the thing `garm lint` and `garm catalogue build`
// actually call — and not merely present and separately callable. A rule
// nothing calls is a rule that does not run, even with five green tests
// exercising `lintStatePropagation` directly.
//
// A dedicated fixture, not a reuse of TestA11RefusesAWeakerStateField's: this
// test's subject is the WIRING, not the rule, and mixing the two would make a
// failure here ambiguous between "not wired" and "fixture changed under it".
func TestA11IsWiredIntoLintAgents(t *testing.T) {
	agentSrc := `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message WiredRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}

message WiredState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  // Claims INTERNAL; "check" (below) writes it from a field that reads at
  // CONFIDENTIAL under "kyc" — the weaker grade lintAgents must still catch.
  optional bool flag = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_INTERNAL
    on_deny: { omit: {} }
  }];
}

service WiredAgent {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [{ fqn: "s.v1.check" }]
    initial: [{ key: "subject" value: "input.subject" }]
    steps: [{ id: "check" tool: "s.v1.check"
      set: [{ key: "flag" value: "response.flagged" }] }]
  };
  rpc Invoke(WiredRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "wired_agent" title: "Wired" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "wired_agent_run" title: "Run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (WiredState) {
    option (garm.tool.v1.tool) = {
      name: "wired_agent_state" title: "State" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
	toolSrc := `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message CheckRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message CheckResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional bool flagged = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_CONFIDENTIAL
    compartments: ["kyc"]
    on_deny: { omit: {} }
  }];
}
service Checker {
  rpc Check(CheckRequest) returns (CheckResponse) {
    option (garm.tool.v1.tool) = {
      name: "check" title: "Check" description: "Check a subject."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
}
`
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": agentSrc,
		"s/v1/check.proto":    toolSrc,
	})
	diags := lintAgents(fds, Options{})
	if !hasRule(diags, "A11") {
		t.Fatalf("lintAgents must reach A11 — a rule nothing calls does not run: %v", diags)
	}
}
