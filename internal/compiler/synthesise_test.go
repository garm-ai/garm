package compiler_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/contracts/cards"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// oneFile is compileSource (agents_test.go) for a fixture that is one
// readable .proto, which is what every case here is.
func oneFile(t *testing.T, src string) []protoreflect.FileDescriptor {
	t.Helper()
	return compileSource(t, map[string]string{"case.proto": src})
}

func serviceNamed(t *testing.T, fds []protoreflect.FileDescriptor, name string) protoreflect.ServiceDescriptor {
	t.Helper()
	for _, fd := range fds {
		if svc := fd.Services().ByName(protoreflect.Name(name)); svc != nil {
			return svc
		}
	}
	t.Fatalf("no service %q in the fixture", name)
	return nil
}

// synthNames renders what a service's cards are called, as
// "<Method>=<tool_name>", so a golden reads as the two names that have to
// agree between the catalogue and the generated binding.
func synthNames(svc protoreflect.ServiceDescriptor) []string {
	var out []string
	for _, s := range cards.ForService(svc) {
		out = append(out, string(s.Name)+"="+s.ToolName())
	}
	return out
}

func wantNames(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("synthesised\n  got  %v\n  want %v", got, want)
	}
}

const oneToolService = `
syntax = "proto3";
package fx.one;
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/fx/one;c";
message Req { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional string q = 1; }
message Res { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional string a = 1; }
service Balances {
  option (garm.meta.v1.owner) = { team: "fx" };
  rpc GetBalance(Req) returns (Res) {
    option (garm.tool.v1.tool) = {
      name: "get_balance" title: "Get a balance" description: "Read a balance."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL compartments: ["financial"]
      audience: [AUDIENCE_AGENT]
      audit: { level: LEVEL_AUDIT record_request: true record_response: true retain_days: 400 }
    };
  }
}
`

// A service with one tool gets the bare names: there is nothing to
// distinguish the card from.
func TestSynthesiseOnASingleToolService(t *testing.T) {
	svc := serviceNamed(t, oneFile(t, oneToolService), "Balances")
	wantNames(t, synthNames(svc), []string{
		"InputCard=get_balance_input_card",
		"ResultCard=get_balance_result_card",
	})

	// A MODE_NONE tool gets no approval card. A tool nobody approves never
	// opens a task, so an approval card would answer about nothing.
	for _, s := range cards.ForService(svc) {
		if s.Kind == cards.Approval {
			t.Error("a MODE_NONE tool was given an approval card")
		}
	}
}

// And the policy a card carries, which is the whole reason a card is as
// governed as its tool.
func TestACardsPolicyIsItsParents(t *testing.T) {
	svc := serviceNamed(t, oneFile(t, oneToolService), "Balances")
	s := cards.ForService(svc)[0]
	p := s.Policy

	// Copied: the parent's reach. This is what makes "a card is governed the
	// same way as the call" true rather than aspirational.
	if p.GetMinClearance() != toolv1.Clearance_CLEARANCE_INTERNAL {
		t.Errorf("min_clearance = %v, want the parent's INTERNAL", p.GetMinClearance())
	}
	if len(p.GetCompartments()) != 1 || p.GetCompartments()[0] != "financial" {
		t.Errorf("compartments = %v, want the parent's [financial]", p.GetCompartments())
	}

	// Fixed: a fetch changes nothing, and needing an approval to look at an
	// approval does not terminate.
	if p.GetVerb() != toolv1.Verb_VERB_READ {
		t.Errorf("verb = %v, want VERB_READ", p.GetVerb())
	}
	if p.GetApproval().GetMode() != toolv1.Approval_MODE_NONE {
		t.Errorf("approval mode = %v, want MODE_NONE", p.GetApproval().GetMode())
	}

	// A card is never recorded by value: it is built for ONE viewer at one
	// moment, and writing it down would put another viewer's withheld facts
	// in the ledger. The audit LEVEL is still the parent's — a card about an
	// audited tool is worth the same row.
	if p.GetAudit().GetRecordRequest() || p.GetAudit().GetRecordResponse() {
		t.Error("a card records its request or response by value")
	}
	if p.GetAudit().GetLevel() != toolv1.Audit_LEVEL_AUDIT {
		t.Errorf("audit level = %v, want the parent's LEVEL_AUDIT", p.GetAudit().GetLevel())
	}

	// Added: PERSON, whatever the parent is. The parent here is AGENT-only.
	if len(p.GetAudience()) != 1 || p.GetAudience()[0] != toolv1.Audience_AUDIENCE_PERSON {
		t.Errorf("audience = %v, want [AUDIENCE_PERSON]: a card exists to be read by "+
			"a person, and that is why a model is never offered one", p.GetAudience())
	}

	// And the response type is the one the daemon knows how to project.
	if cards.CardType != "garm.card.v1.Card" {
		t.Errorf("the card type moved to %q", cards.CardType)
	}
}

const manyToolService = `
syntax = "proto3";
package fx.many;
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/fx/many;c";
message Pay { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional int64 amount = 1; }
message Ok  { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional string id = 1; }
service Payments {
  option (garm.meta.v1.owner) = { team: "payments" };
  rpc InitiatePayment(Pay) returns (Ok) {
    option (garm.tool.v1.tool) = {
      name: "initiate_payment" title: "Initiate a payment" description: "Move money."
      verb: VERB_WRITE min_clearance: CLEARANCE_RESTRICTED compartments: ["financial"]
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_RESTRICTED
                  max_grant_age_seconds: 900 material_fields: ["amount"] }
    };
  }
  rpc ListPayments(Pay) returns (Ok) {
    option (garm.tool.v1.tool) = {
      name: "list_payments" title: "List payments" description: "Recent payments."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`

// Several tools on a service means every card is prefixed with its parent's
// method name: three tools would otherwise want the same three names. And a
// MODE_GRANT tool, and only it, gets an approval card.
func TestSynthesiseOnAMultiToolService(t *testing.T) {
	svc := serviceNamed(t, oneFile(t, manyToolService), "Payments")
	wantNames(t, synthNames(svc), []string{
		"InitiatePaymentInputCard=initiate_payment_input_card",
		"InitiatePaymentResultCard=initiate_payment_result_card",
		"InitiatePaymentApprovalCard=initiate_payment_approval_card",
		"ListPaymentsInputCard=list_payments_input_card",
		"ListPaymentsResultCard=list_payments_result_card",
	})
}

const agentService = `
syntax = "proto3";
package fx.agent;
import "garm/agent/v1/agent.proto";
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/fx/agent;c";
message Ask { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional string question = 1; }
service Assistant {
  option (garm.meta.v1.owner) = { team: "agents" };
  option (garm.agent.v1.agent) = {
    mode: MODE_REACT
    principal: { clearance: CLEARANCE_INTERNAL }
    model: { alias: "fast" }
    bounds: { max_steps: 8 max_tokens: 100000 max_tool_calls: 20 timeout: { seconds: 600 } }
  };
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "assistant" title: "Assistant" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
      audience: [AUDIENCE_PERSON, AUDIENCE_AGENT]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "assistant_run" title: "Assistant run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
      audience: [AUDIENCE_PERSON, AUDIENCE_AGENT]
    };
  }
}
`

// An agent gets a start card and a run card, from Invoke only.
//
// Its Invoke is MODE_NONE, so there is no approval card — an agent's
// approvals are its tasks, and those cards come from the tasks tool. And
// GetRun gets nothing: it is the same run read a second way, so giving it its
// own pair would put two InputCards on one service.
func TestSynthesiseOnAnAgentService(t *testing.T) {
	svc := serviceNamed(t, oneFile(t, agentService), "Assistant")
	wantNames(t, synthNames(svc), []string{
		"InputCard=assistant_input_card",
		"ResultCard=assistant_result_card",
	})
}

// A card endpoint gets no cards of its own. Serving a form for a form is an
// infinite regress with nothing at the bottom: what a person needs about a
// card is the card.
func TestACardEndpointGetsNoCards(t *testing.T) {
	const src = `
syntax = "proto3";
package fx.regress;
import "garm/card/v1/card.proto";
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/fx/regress;c";
message Req { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } }; optional string q = 1; }
service Q {
  option (garm.meta.v1.owner) = { team: "fx" };
  rpc Look(Req) returns (Req) {
    option (garm.tool.v1.tool) = { name: "look" title: "Look" description: "Look." verb: VERB_READ min_clearance: CLEARANCE_PUBLIC };
  }
  rpc HandWritten(garm.card.v1.TaskRef) returns (garm.card.v1.Card) {
    option (garm.tool.v1.tool) = { name: "hand_written" title: "A card" description: "A card."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC audience: [AUDIENCE_PERSON] };
  }
}
`
	svc := serviceNamed(t, oneFile(t, src), "Q")
	for _, s := range cards.ForService(svc) {
		if s.Parent.Name() == "HandWritten" {
			t.Errorf("a card endpoint was given its own %s", s.Kind)
		}
	}
	// And `Look` is still not bare-named, because the card endpoint beside it
	// does not count as a tool for the naming decision but the service does
	// have exactly one tool that takes cards.
	wantNames(t, synthNames(svc), []string{
		"InputCard=look_input_card",
		"ResultCard=look_result_card",
	})
}

// The one thing that must never drift: the tool name a card derives and the
// tool name the compiler derives for its parent come from the same rule.
func TestTheTwoSnakeCasesAgree(t *testing.T) {
	for _, name := range []string{"GetBalance", "InitiatePayment", "GetHTTPStatus", "Invoke", "A"} {
		if got, want := cards.SnakeCase(name), compiler.SnakeCase(name); got != want {
			t.Errorf("SnakeCase(%q): cards says %q, the compiler says %q; a card whose "+
				"name derived differently from its parent's would be a tool nobody "+
				"could find", name, got, want)
		}
	}
}
