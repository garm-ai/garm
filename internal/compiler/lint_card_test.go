package compiler_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// C1 — a template references only what will be there to render. For a
// task_card that is the tool's approval.material_fields (C3 folded in); for
// a result_card, in this release, nothing at all.
//
// Real proto source rather than hand-built descriptors: what is under test
// is how a template written by a schema author arrives — the text-format
// option, the nested oneof, the braces in a string.

// cardSrc is one MODE_GRANT tool with an owner, its material fields and its
// task_card both left to the test.
func cardSrc(materialFields, taskCard string) map[string]string {
	return map[string]string{"pay/v1/pay.proto": `syntax = "proto3";
package pay.v1;
import "buf/validate/validate.proto";
import "garm/card/v1/card.proto";
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/pay/v1;payv1";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional int64 amount = 1 [(buf.validate.field).required = true];
  optional string reference = 2 [(buf.validate.field).required = true];
  optional string beneficiary_name = 3;
}
message PayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service Payments {
  option (garm.meta.v1.owner) = { team: "payments-platform" };
  rpc Pay(PayRequest) returns (PayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay" title: "Pay" description: "Pay someone."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_RESTRICTED
                  max_grant_age_seconds: 900 ` + materialFields + ` }
    };
    ` + taskCard + `
  }
}
`}
}

const payPath = "pay.v1.Payments.Pay"

func c1Errors(ds []compiler.Diag) []compiler.Diag {
	var out []compiler.Diag
	for _, d := range ds {
		if d.Rule == "C1" {
			out = append(out, d)
		}
	}
	return out
}

func TestC1RefusesATaskCardFieldThatIsNotMaterial(t *testing.T) {
	for _, tc := range []struct{ name, card, where string }{
		{"in the title",
			`option (garm.card.v1.task_card) = { title: "Pay {beneficiary_name}" };`,
			"title"},
		{"in a text element",
			`option (garm.card.v1.task_card) = { title: "Pay" body: [{ text: "To {beneficiary_name}" }] };`,
			"body[0].text"},
		{"in a fact",
			`option (garm.card.v1.task_card) = { title: "Pay" body: [{ facts: { facts: [{ field: "beneficiary_name" label: "To" }] } }] };`,
			"body[0].facts[0]"},
		{"in a section's fact",
			`option (garm.card.v1.task_card) = { title: "Pay" body: [{ divider: {} }, { section: { title: "Who" elements: [{ facts: { facts: [{ field: "beneficiary_name" label: "To" }] } }] } }] };`,
			"body[1].section.elements[0].facts[0]"},
		{"in a section's title",
			`option (garm.card.v1.task_card) = { title: "Pay" body: [{ section: { title: "{beneficiary_name}" } }] };`,
			"body[0].section.title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := compiler.Lint(compileSource(t, cardSrc(`material_fields: ["amount", "reference"]`, tc.card)))
			want := "task_card " + tc.where + " references {beneficiary_name}, which is not in approval.material_fields"
			if !hasDiag(diags, "C1", payPath, want) {
				t.Errorf("want a C1 error at %s containing %q, got:\n%s", payPath, want, render(diags))
			}
		})
	}
}

// The material fields are named in the message. An author fixing a template
// should not have to open the tool to see what they may reference.
func TestC1NamesTheMaterialFieldsInTheMessage(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc(`material_fields: ["amount", "reference"]`,
		`option (garm.card.v1.task_card) = { title: "Pay {beneficiary_name}" };`)))
	if !hasDiag(diags, "C1", payPath, `["amount" "reference"]`) {
		t.Errorf("the C1 message does not list the material fields:\n%s", render(diags))
	}
}

func TestC1AcceptsATaskCardOverMaterialFieldsOnly(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc(`material_fields: ["amount", "reference"]`,
		`option (garm.card.v1.task_card) = {
		  title: "Payment {reference}"
		  body: [
		    { text: "{amount} minor units" },
		    { facts: { facts: [{ field: "amount" label: "Amount" }, { field: "reference" label: "Reference" }] } },
		    { divider: {} },
		    { section: { title: "Context" elements: [{ text: "Literal only." }] } }
		  ]
		};`)))
	if got := c1Errors(diags); len(got) != 0 {
		t.Errorf("a template over material fields only was refused:\n%s", render(got))
	}
}

// Literal text is always fine, braces or not. A template with no references
// on a tool with no material fields is a card that says something fixed.
func TestC1AcceptsLiteralTextWithNoMaterialFields(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc("",
		`option (garm.card.v1.task_card) = { title: "Approve this payment" body: [{ text: "The amount is on the request." }] };`)))
	if got := c1Errors(diags); len(got) != 0 {
		t.Errorf("a literal template was refused:\n%s", render(got))
	}
}

func TestC1SaysWhenTheToolListsNoMaterialFields(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc("",
		`option (garm.card.v1.task_card) = { title: "Pay {amount}" };`)))
	if !hasDiag(diags, "C1", payPath, "this tool lists no approval.material_fields") {
		t.Errorf("want C1 to say the tool lists no material fields, got:\n%s", render(diags))
	}
}

func TestC1RefusesAnEmptyFactField(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc(`material_fields: ["amount"]`,
		`option (garm.card.v1.task_card) = { title: "Pay" body: [{ facts: { facts: [{ label: "Amount" }] } }] };`)))
	if !hasDiag(diags, "C1", payPath, "body[0].facts[0] references nothing") {
		t.Errorf("an empty FactRef.field was accepted:\n%s", render(diags))
	}
}

// A task card is the open approval. On a tool nobody approves, or on a method
// that is not a governed tool at all, nothing ever renders it.
func TestC1RefusesATaskCardWhereNothingRendersIt(t *testing.T) {
	t.Run("MODE_NOTIFY", func(t *testing.T) {
		src := cardSrc("", `option (garm.card.v1.task_card) = { title: "Pay" };`)
		src["pay/v1/pay.proto"] = strings.Replace(src["pay/v1/pay.proto"], "mode: MODE_GRANT", "mode: MODE_NOTIFY", 1)
		diags := compiler.Lint(compileSource(t, src))
		if !hasDiag(diags, "C1", payPath, "approval mode is MODE_NOTIFY, not MODE_GRANT") {
			t.Errorf("a task_card on a MODE_NOTIFY tool was accepted:\n%s", render(diags))
		}
	})
	t.Run("no tool option", func(t *testing.T) {
		src := cardSrc("", "")
		src["pay/v1/pay.proto"] = strings.Replace(src["pay/v1/pay.proto"], "  }\n}\n", `  }
  rpc Quote(PayRequest) returns (PayResponse) {
    option (garm.card.v1.task_card) = { title: "Quote" };
  }
}
`, 1)
		diags := compiler.Lint(compileSource(t, src))
		if !hasDiag(diags, "C1", "pay.v1.Payments.Quote", "carries no (garm.tool.v1.tool)") {
			t.Errorf("a task_card on an unannotated method was accepted:\n%s", render(diags))
		}
	})
}

// context.<field> is C7's business — the context RPC and its response shape —
// and C7 is not in this release. C1 leaves those references alone rather than
// refusing them against a message they were never meant to resolve in.
func TestC1LeavesContextReferencesToC7(t *testing.T) {
	diags := compiler.Lint(compileSource(t, cardSrc(`material_fields: ["amount"]`,
		`option (garm.card.v1.task_card) = { title: "Pay {amount}" body: [{ facts: { facts: [{ field: "context.balance" label: "Balance" }] } }] context: "ApprovalContext" };`)))
	if got := c1Errors(diags); len(got) != 0 {
		t.Errorf("a context.* reference was refused by C1:\n%s", render(got))
	}
}

// resultSrc is the agent fixture with a result_card on the service.
func resultSrc(resultCard string) map[string]string {
	files := agentSrc(fullPolicy)
	src := files["bank/v1/agent.proto"]
	src = strings.Replace(src, `import "garm/agent/v1/agent.proto";`,
		"import \"garm/agent/v1/agent.proto\";\nimport \"garm/card/v1/card.proto\";", 1)
	src = strings.Replace(src, "service SupportAssistant {\n",
		"service SupportAssistant {\n  "+resultCard+"\n", 1)
	files["bank/v1/agent.proto"] = src
	return files
}

const assistantPath = "bank.v1.SupportAssistant"

func TestC1AcceptsALiteralResultCard(t *testing.T) {
	diags := compiler.Lint(compileSource(t, resultSrc(
		`option (garm.card.v1.result_card) = { title: "Answered" body: [{ text: "The assistant has answered; open the run." }, { divider: {} }] };`)))
	if got := c1Errors(diags); len(got) != 0 {
		t.Errorf("a literal result_card was refused:\n%s", render(got))
	}
}

// Until agents declare an output type there is nothing for a reference to
// resolve against, and the message says so rather than "no field answer".
func TestC1RefusesAnyReferenceInAResultCard(t *testing.T) {
	for _, tc := range []struct{ name, card, where string }{
		{"in the title", `option (garm.card.v1.result_card) = { title: "{answer}" };`, "title"},
		{"in a fact", `option (garm.card.v1.result_card) = { title: "Done" body: [{ facts: { facts: [{ field: "answer" label: "Answer" }] } }] };`, "body[0].facts[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := compiler.Lint(compileSource(t, resultSrc(tc.card)))
			want := "result_card " + tc.where + " references {answer}, but agents cannot declare an output type yet"
			if !hasDiag(diags, "C1", assistantPath, want) {
				t.Errorf("want a C1 error containing %q, got:\n%s", want, render(diags))
			}
		})
	}
}

func TestC1RefusesAResultCardOnAServiceThatIsNotAnAgent(t *testing.T) {
	src := cardSrc("", "")
	src["pay/v1/pay.proto"] = strings.Replace(src["pay/v1/pay.proto"],
		`option (garm.meta.v1.owner) = { team: "payments-platform" };`,
		"option (garm.meta.v1.owner) = { team: \"payments-platform\" };\n  option (garm.card.v1.result_card) = { title: \"Paid\" };", 1)
	diags := compiler.Lint(compileSource(t, src))
	if !hasDiag(diags, "C1", "pay.v1.Payments", "only an agent has runs") {
		t.Errorf("a result_card on a tool service was accepted:\n%s", render(diags))
	}
}
