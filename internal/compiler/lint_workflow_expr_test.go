package compiler

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
)

// A7's fixtures need a real descriptor tree: the rule resolves `with` keys
// against a tool's request, `set` keys against the state message, and every
// expression against a typed CEL environment built from those descriptors.
// Nothing about that can be faked with a hand-built Agent{}.
//
// Two trees, compiled together, because that is the shape A7 lives in: the
// agent is in one package and the tools it calls are in another, which is why
// the rule is whole-catalogue.

// workflowAgentSrc is an agent service whose manifest body is spliced in, so
// each test writes only the part it is about. The allowlist is fixed —
// screen, assess and pay — so a step naming anything else is a step outside
// it.
func workflowAgentSrc(body string) string {
	return `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
import "google/protobuf/timestamp.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "pay" description: "Payments." }] };

message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string memo = 1;
  optional int64 amount = 2;
}

message PayState {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional bool flagged = 1;
  optional string note = 2;
  optional string memo = 3;
  optional int64 amount = 4;
  optional double rate = 5;
  optional uint64 seq = 6;
  google.protobuf.Timestamp at = 7;
}

service Payer {
  option (garm.agent.v1.agent) = {
    mode: MODE_WORKFLOW
    tools: [
      { fqn: "s.v1.screen" }, { fqn: "s.v1.assess" }, { fqn: "s.v1.pay" },
      { fqn: "s.v1.pay_unkeyed" }
    ]
    ` + body + `
  };
  rpc Invoke(PayRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "payer" title: "Pay" description: "Start a payment run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "payer_run" title: "Payer run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
  rpc GetState(garm.agent.v1.RunRef) returns (PayState) {
    option (garm.tool.v1.tool) = {
      name: "payer_state" title: "Payer state" description: "Read a run's state."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["pay"]
    };
  }
}
`
}

// toolSrc is the package the steps call into: a separate proto package, as
// every real one is. `idempotency_key` carries source: SOURCE_RUNNER, which
// is the only definition of "the runner's" — the same one agentd reads to
// strip the field from the schema it shows a model.
const toolSrc = `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/s/v1;sv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "ledger" description: "The ledger." }] };

message ScreenRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message ScreenResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional bool flag = 1;
}
message AssessRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string subject = 1;
}
message AssessResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string text = 1;
}
message LedgerPayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string memo = 1;
  optional int64 amount = 2;
  optional string idempotency_key = 3 [(garm.tool.v1.field_policy) = {
    source: SOURCE_RUNNER read: CLEARANCE_PUBLIC on_deny: { omit: {} }
  }];
}
// LedgerPayUnkeyedRequest has a field NAMED idempotency_key and no source
// policy on it. L34 says so explicitly: SOURCE_UNSPECIFIED is the caller's,
// whatever the field is called. So this is the fixture that tells a
// name-matching isRunnerOwned apart from one that reads the annotation.
message LedgerPayUnkeyedRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string idempotency_key = 1;
}
message LedgerPayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string receipt = 1;
}
service Ledger {
  rpc Screen(ScreenRequest) returns (ScreenResponse) {
    option (garm.tool.v1.tool) = {
      name: "screen" title: "Screen" description: "Screen a payment."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
  rpc Assess(AssessRequest) returns (AssessResponse) {
    option (garm.tool.v1.tool) = {
      name: "assess" title: "Assess" description: "Assess a flagged payment."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
  rpc Pay(LedgerPayRequest) returns (LedgerPayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay" title: "Pay" description: "Move money."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
  rpc PayUnkeyed(LedgerPayUnkeyedRequest) returns (LedgerPayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay_unkeyed" title: "Pay unkeyed" description: "Move money, unkeyed."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["ledger"]
    };
  }
}
`

// workflowExprFixture compiles both packages as one set — the way `garm lint`
// sees a tree — and returns the agent and the tool index built from it.
func workflowExprFixture(t *testing.T, body string) (Agent, map[string]Tool) {
	t.Helper()
	fds := compileWorkflowFixture(t, map[string]string{
		"bank/v1/agent.proto": workflowAgentSrc(body),
		"s/v1/ledger.proto":   toolSrc,
	})
	agents := Agents(fds)
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if agents[0].StateMessage == nil {
		t.Fatal("GetState returns PayState; StateMessage should have resolved")
	}
	return agents[0], toolIndex(fds)
}

// allMsgs joins every diagnostic's message, so a test can pin what a
// refusal names without caring which diagnostic said it.
func allMsgs(ds []Diag) string {
	msgs := make([]string, 0, len(ds))
	for _, d := range ds {
		msgs = append(msgs, d.Msg)
	}
	return strings.Join(msgs, " | ")
}

// THE RULE. A field written only on one branch may not be read after the join.
func TestA7RefusesAReadOfAFieldNotWrittenOnEveryPath(t *testing.T) {
	// screen --[flagged]--> assess (writes note) --> pay
	//        --------------------------------------> pay
	// `pay` reads state.note, which only `assess` writes.
	a, tools := workflowExprFixture(t, `
		steps: [
		  { id: "screen" tool: "s.v1.screen" set: [{key:"flagged" value:"response.flag"}] },
		  { id: "assess" tool: "s.v1.assess" set: [{key:"note" value:"response.text"}] },
		  { id: "pay"    tool: "s.v1.pay"    with:[{key:"memo"  value:"state.note"}] }
		]
		edges: [
		  { from: "screen" to: "assess" when: "state.flagged" },
		  { from: "screen" to: "pay" },
		  { from: "assess" to: "pay" }
		]`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") {
		t.Fatalf("a read of a conditionally-written field must be refused; got %v", diags)
	}
	joined := allMsgs(diags)
	if !strings.Contains(joined, "note") {
		t.Errorf("must name the field; got %q", joined)
	}
	if !strings.Contains(joined, "assess") {
		t.Errorf("must name the step that writes it, so the author can see the "+
			"path that skips it; got %q", joined)
	}
}

// The same graph, with the read moved onto the dominated branch, must pass.
// Without this the test above passes on a rule that refuses every read.
func TestA7AcceptsAReadOnADominatedPath(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		steps: [
		  { id: "screen"       tool: "s.v1.screen" set: [{key:"flagged" value:"response.flag"}] },
		  { id: "assess"       tool: "s.v1.assess" set: [{key:"note" value:"response.text"}] },
		  { id: "pay_reviewed" tool: "s.v1.pay"    with:[{key:"memo" value:"state.note"}] },
		  { id: "pay_clean"    tool: "s.v1.pay"    with:[{key:"memo" value:"''"}] }
		]
		edges: [
		  { from: "screen" to: "assess" when: "state.flagged" },
		  { from: "screen" to: "pay_clean" },
		  { from: "assess" to: "pay_reviewed" }
		]`)
	if d := lintWorkflowExpressions(a, tools); len(d) != 0 {
		t.Fatalf("the split graph is the correct shape and must pass: %v", d)
	}
}

// A field in `initial` is written before any step, so it is readable everywhere.
func TestA7AcceptsAReadOfAnInitialField(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"memo" value:"state.memo"}] }]
		edges: []`)
	if d := lintWorkflowExpressions(a, tools); len(d) != 0 {
		t.Fatalf("an initial field is written on every path: %v", d)
	}
}

func TestA7RefusesAWrongResultType(t *testing.T) {
	// s.v1.pay's request has `amount` as int64; feed it a string.
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"amount" value:"state.memo"}] }]
		edges: []`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") {
		t.Fatalf("a string into an int64 field must be a lint error, not a "+
			"marshalling failure at run time; got %v", diags)
	}
}

func TestA7RefusesAnUnknownFieldPath(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"nope" value:"state.memo"}] }]
		edges: []`)
	if !hasRule(lintWorkflowExpressions(a, tools), "A7") {
		t.Fatal("a `with` key must be a real field path on the request")
	}
}

func TestA7RefusesAnAuthorSettingTheIdempotencyKey(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay"
		          with: [{key:"idempotency_key" value:"state.memo"}] }]
		edges: []`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") {
		t.Fatal("the idempotency key is the runner's and an author may never set it")
	}
	if !mentions(diags, "idempotency_key") {
		t.Errorf("the refusal must name the field; got %v", diags)
	}
}

func TestA7RefusesANonBooleanEdgePredicate(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "a" tool: "s.v1.pay" }, { id: "b" tool: "s.v1.pay" }]
		edges: [{ from: "a" to: "b" when: "state.memo" }]`)
	if !hasRule(lintWorkflowExpressions(a, tools), "A7") {
		t.Fatal("an edge predicate must be bool")
	}
}

func TestA7RefusesAToolNotInTheAllowlist(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "a" tool: "s.v1.not_allowed" }]
		edges: []`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") {
		t.Fatal("the allowlist is the single authority; a graph may not widen it")
	}
	// Pinning the SENTENCE, not just the rule id: an unlisted tool is also a
	// tool no package declares, so without this the test passes on the
	// "no linted package declares" branch and the allowlist check could be
	// deleted outright. (It was, in a mutation run, and this is what caught it.)
	if !mentions(diags, "allowlist") {
		t.Errorf("the refusal must be the allowlist one, not merely an unknown "+
			"tool: %v", diags)
	}
}

func TestStateSelectionsFindsNestedReads(t *testing.T) {
	env, _ := cel.NewEnv()
	ast, iss := env.Parse(`'x' + state.a + string(size(state.b)) ? state.c : ''`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("parse: %v", iss.Err())
	}
	got := map[string]bool{}
	for _, f := range stateSelections(ast) {
		got[f] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !got[want] {
			t.Errorf("missed state.%s; a substring search would have found it but "+
				"a walk must too", want)
		}
	}
}

// ---------------------------------------------------------------------------
// A7 is a WHOLE-CATALOGUE rule, and the split is what keeps it from being
// either vacuous or a spurious error on every `buf generate`. The next three
// tests pin both directions.

// Under PartialSet a step's tool is simply not in the request, and that is
// nobody's mistake. It warns, and the warning names the commands that do run
// the check — the shape A3, A9 and P1 already have.
//
// THREE steps, all of whose tools are absent, and exactly ONE warning. A
// one-step fixture cannot tell one-per-agent from one-per-step, and one per step
// would be three same-location findings differing only in an interchangeable
// clause — which is not what A3, the model for this warning, does: it emits one
// per agent covering the whole allowlist.
func TestA7WarnsOncePerAgentWhenToolsAreOutsideThisDirectory(t *testing.T) {
	a, _ := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [
		  { id: "screen" tool: "s.v1.screen" },
		  { id: "assess" tool: "s.v1.assess" },
		  { id: "pay"    tool: "s.v1.pay" with: [{key:"memo" value:"state.memo"}] }
		]
		edges: [{ from: "screen" to: "assess" }, { from: "assess" to: "pay" }]`)
	// An empty index is exactly what buf hands the plugin for the agent's own
	// directory: every tool lives in s/v1, which is not in this request.
	diags := lintWorkflowExpressionsWith(a, map[string]Tool{}, Options{PartialSet: true})
	if len(diags) != 1 {
		t.Fatalf("want exactly one diagnostic for three absent tools, got %d: %v",
			len(diags), diags)
	}
	if !diags[0].Warn {
		t.Errorf("an author running `buf generate` on one directory has done "+
			"nothing wrong; this must be a warning: %v", diags[0])
	}
	msg := diags[0].Msg
	// Every step, so the author knows which of them went unchecked, and every
	// tool, so they know where to look.
	for _, want := range []string{"screen", "assess", "pay",
		"s.v1.screen", "s.v1.assess", "s.v1.pay"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the warning must name %q; got %q", want, msg)
		}
	}
	// Declaration order, so the sentence is byte-identical across runs.
	if i, j, k := strings.Index(msg, "screen"), strings.Index(msg, "assess"),
		strings.Index(msg, "pay ("); !(i < j && j < k) {
		t.Errorf("the steps must be listed in declaration order; got %q", msg)
	}
	// And the skipped checks and the commands are stated ONCE, not per step.
	for _, want := range []string{"garm lint", "garm catalogue build", "NOT checked here"} {
		if n := strings.Count(msg, want); n != 1 {
			t.Errorf("%q appears %d times, want 1: %q", want, n, msg)
		}
	}
}

// The same absent tool with the WHOLE set is an error: there, nothing else
// will ever resolve it.
func TestA7ErrorsOnAnUndeclaredToolWithTheWholeSet(t *testing.T) {
	a, _ := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"memo" value:"state.memo"}] }]
		edges: []`)
	diags := lintWorkflowExpressions(a, map[string]Tool{})
	if !hasRule(diags, "A7") {
		t.Fatalf("want A7, got %v", diags)
	}
	for _, d := range diags {
		if d.Warn {
			t.Errorf("with the whole set assembled there is nothing left to defer "+
				"to; this must be an error: %v", d)
		}
	}
}

// And the load-bearing half still ERRORS under PartialSet. The reads are found
// by parsing, which needs no tool descriptor, so the one rule that prevents a
// silent zero in a payment reference runs on every `buf generate`.
func TestA7StillRefusesAnUnwrittenReadUnderPartialSet(t *testing.T) {
	a, _ := workflowExprFixture(t, `
		steps: [
		  { id: "screen" tool: "s.v1.screen" set: [{key:"flagged" value:"response.flag"}] },
		  { id: "assess" tool: "s.v1.assess" set: [{key:"note" value:"response.text"}] },
		  { id: "pay"    tool: "s.v1.pay"    with:[{key:"memo"  value:"state.note"}] }
		]
		edges: [
		  { from: "screen" to: "assess" when: "state.flagged" },
		  { from: "screen" to: "pay" },
		  { from: "assess" to: "pay" }
		]`)
	diags := lintWorkflowExpressionsWith(a, map[string]Tool{}, Options{PartialSet: true})
	var refused bool
	for _, d := range diags {
		if d.Rule == "A7" && !d.Warn && strings.Contains(d.Msg, "state.note") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the write-dominator rule needs only the agent's own file and "+
			"must error here; got %v", diags)
	}
}

// A `set` reads state as well as response, and an unwritten read there is the
// same hole. (The brief's tests only cover `with`.)
func TestA7RefusesAnUnwrittenReadInASetExpression(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		steps: [
		  { id: "screen" tool: "s.v1.screen" set: [{key:"flagged" value:"response.flag"}] },
		  { id: "assess" tool: "s.v1.assess" set: [{key:"note" value:"response.text"}] },
		  { id: "pay"    tool: "s.v1.pay"    set: [{key:"memo" value:"state.note + 'x'"}] }
		]
		edges: [
		  { from: "screen" to: "assess" when: "state.flagged" },
		  { from: "screen" to: "pay" },
		  { from: "assess" to: "pay" }
		]`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") || !mentions(diags, "state.note") {
		t.Fatalf("a `set` expression reading a conditionally-written field is the "+
			"same zero-value hole as a `with`; got %v", diags)
	}
}

// `initial` is the only place a caller's request enters the state, so a typo
// in one is a field that is silently zero for the whole run.
func TestA7RefusesABadInitialExpression(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown state key", `
			initial: [{ key: "nope" value: "input.memo" }]
			steps: [{ id: "pay" tool: "s.v1.pay" }]
			edges: []`},
		{"unknown input field", `
			initial: [{ key: "memo" value: "input.nope" }]
			steps: [{ id: "pay" tool: "s.v1.pay" }]
			edges: []`},
		{"wrong type", `
			initial: [{ key: "amount" value: "input.memo" }]
			steps: [{ id: "pay" tool: "s.v1.pay" }]
			edges: []`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, tools := workflowExprFixture(t, tc.body)
			if !hasRule(lintWorkflowExpressions(a, tools), "A7") {
				t.Fatal("want A7")
			}
		})
	}
}

// The negative half of the three above: a correct `initial` is silent.
func TestA7AcceptsAWellTypedInitial(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }, { key: "amount" value: "input.amount" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"amount" value:"state.amount"}] }]
		edges: []`)
	if d := lintWorkflowExpressions(a, tools); len(d) != 0 {
		t.Fatalf("a well-typed initial must pass: %v", d)
	}
}

// isRunnerOwned must read the ANNOTATION, not the name. A field called
// idempotency_key with no `source` is the caller's — L34 says so in as many
// words — and a rule that matched names would refuse a legitimate request
// field and would drift the day a second runner field is added.
func TestA7ReadsRunnerOwnershipFromTheAnnotationNotTheName(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay_unkeyed"
		          with: [{key:"idempotency_key" value:"state.memo"}] }]
		edges: []`)
	if d := lintWorkflowExpressions(a, tools); len(d) != 0 {
		t.Fatalf("an unannotated field is the caller's, whatever it is called: %v", d)
	}
	// And the annotated one on the same service is still refused, or the
	// assertion above passed on a check that never fires.
	keyed := tools["s.v1.pay"]
	if !isRunnerOwned(keyed, "idempotency_key") {
		t.Error("source: SOURCE_RUNNER is what makes a field the runner's")
	}
	if isRunnerOwned(keyed, "memo") {
		t.Error("memo carries no source policy and is the caller's")
	}
}

// celTypeFits is the other half of A7, and it is deliberately conservative:
// an exact match and the widenings protobuf marshalling genuinely performs.
// A permissive version puts the failure back at run time inside a governed
// call, so the refusals matter as much as the acceptances.
func TestCelTypeFitsAcceptsOnlyRealWidenings(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" }]
		edges: []`)
	state := a.StateMessage
	env, err := stateEnv(state, map[string]protoreflect.MessageDescriptor{
		"response": tools["s.v1.pay"].Method.Output()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		expr  string
		field string
		want  bool
		why   string
	}{
		{"state.memo", "memo", true, "exact string"},
		{"state.amount", "amount", true, "exact int64"},
		{"1", "amount", true, "CEL has no int32, so an int is all an author can write"},
		{"1", "rate", true, "an integer into a double is the widening protobuf JSON performs"},
		{"state.rate", "rate", true, "exact double"},
		{"state.at", "at", true, "a Timestamp is CEL's timestamp"},
		{"state.seq", "seq", true, "exact uint64"},
		{"state.memo", "amount", false, "a string is not an int64"},
		{"state.amount", "memo", false, "an int64 is not a string"},
		{"state.rate", "amount", false, "a double into an int64 loses the fraction"},
		{"state.seq", "amount", false, "half a uint64's range has no int64"},
		{"state.amount", "seq", false, "a negative int has no uint64"},
		{"state.at", "memo", false, "a timestamp is not a string"},
		{"state.memo", "at", false, "a string is not a Timestamp"},
		{"state.flagged", "memo", false, "a bool is not a string"},
		{"state.amount", "flagged", false, "an int64 is not a bool"},
	} {
		t.Run(tc.expr+"->"+tc.field, func(t *testing.T) {
			ast, iss := env.Compile(tc.expr)
			if iss != nil && iss.Err() != nil {
				t.Fatalf("compiling %q: %v", tc.expr, iss.Err())
			}
			fd, err := resolveFieldPath(state, tc.field)
			if err != nil {
				t.Fatal(err)
			}
			if got := celTypeFits(ast.OutputType(), fd); got != tc.want {
				t.Errorf("celTypeFits(%s, %s) = %v, want %v: %s",
					ast.OutputType(), fieldType(fd), got, tc.want, tc.why)
			}
		})
	}
}

// pathCovers decides what a nested `set` key writes. Writing amount.minor
// constructs amount, and writing amount fills everything a read below it
// sees; two sibling subtrees share nothing.
func TestPathCoversTreatsASubtreeWriteAsAWrite(t *testing.T) {
	for _, tc := range []struct {
		written, read string
		want          bool
	}{
		{"amount", "amount", true},
		{"amount", "amount.minor_units", true},
		{"amount.minor_units", "amount", true},
		{"amount.minor_units", "amount.minor_units", true},
		{"amount.minor_units", "amount.currency", false},
		{"amount", "amount_due", false},
		{"amount_due", "amount", false},
		{"note", "amount", false},
	} {
		if got := pathCovers(tc.written, tc.read); got != tc.want {
			t.Errorf("pathCovers(%q, %q) = %v, want %v", tc.written, tc.read, got, tc.want)
		}
	}
}

// A read inside a string literal is not a read, and a read inside a nested
// call is. Together these are why stateSelections parses instead of grepping.
func TestStateSelectionsIgnoresAFieldNameInsideALiteral(t *testing.T) {
	env, _ := cel.NewEnv()
	ast, iss := env.Parse(`'state.note is not a read' + string(state.a)`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("parse: %v", iss.Err())
	}
	got := stateSelections(ast)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v, want [a]: a substring search would have found `note` in "+
			"the literal and refused a correct expression", got)
	}
}

// A chain is ONE read, of the longest path: state.amount.minor_units is a read
// of amount.minor_units, not also of amount, or an author gets two
// diagnostics for one mistake.
func TestStateSelectionsReportsTheLongestChain(t *testing.T) {
	env, _ := cel.NewEnv()
	ast, iss := env.Parse(`state.amount.minor_units`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("parse: %v", iss.Err())
	}
	if got := stateSelections(ast); len(got) != 1 || got[0] != "amount.minor_units" {
		t.Fatalf("got %v, want [amount.minor_units]", got)
	}
}

// Nothing in A7 fires on a react agent: it has no graph, and A8 is the rule
// that says so.
func TestA7IsSilentOnAReactAgent(t *testing.T) {
	a := Agent{FQN: "x.Y", Policy: &agentv1.AgentPolicy{Mode: agentv1.Mode_MODE_REACT}}
	if d := lintWorkflowExpressions(a, nil); len(d) != 0 {
		t.Fatalf("react mode has no steps for A7 to judge: %v", d)
	}
}

// A step cannot read what its own `set` writes: both are evaluated against the
// state as it was before the step ran. The general sentence would tell an
// author their own step does not dominate itself, which is true and useless.
func TestA7RefusesAStepReadingItsOwnWrite(t *testing.T) {
	a, tools := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "assess" tool: "s.v1.assess"
		          set: [{key:"note" value:"response.text"}]
		          with:[{key:"subject" value:"state.note"}] }]
		edges: []`)
	diags := lintWorkflowExpressions(a, tools)
	if !hasRule(diags, "A7") {
		t.Fatalf("a `with` cannot read the same step's `set`; got %v", diags)
	}
	if !mentions(diags, "this same step") {
		t.Errorf("the refusal must say WHY, or the author reads that their own "+
			"step does not dominate itself: %v", diags)
	}
}

// A `with` expression that does not compile against `state` is an error under
// PartialSet too. Only the type FIT of the value against the request field it
// feeds needs the absent tool; whether `state.nope` is a field at all is
// answered by the state message, which is in this agent's own file.
func TestA7StillRefusesAnUncompilableWithExpressionUnderPartialSet(t *testing.T) {
	a, _ := workflowExprFixture(t, `
		initial: [{ key: "memo" value: "input.memo" }]
		steps: [{ id: "pay" tool: "s.v1.pay" with: [{key:"memo" value:"state.nope"}] }]
		edges: []`)
	diags := lintWorkflowExpressionsWith(a, map[string]Tool{}, Options{PartialSet: true})
	var refused bool
	for _, d := range diags {
		if d.Rule == "A7" && !d.Warn && strings.Contains(d.Msg, "does not compile") &&
			strings.Contains(d.Msg, "nope") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("a mistyped state field in a `with` must error where the state "+
			"message is visible, which is everywhere; got %v", diags)
	}
	// And the same expression is still refused with the whole set, or the check
	// moved rather than being added.
	full := lintWorkflowExpressions(a, nil)
	if !hasRule(full, "A7") {
		t.Errorf("want A7 with the whole set too; got %v", full)
	}
}
