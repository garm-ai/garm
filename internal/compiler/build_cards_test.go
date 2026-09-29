package compiler_test

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/garm-ai/garm/contracts/cards"
	cardv1 "github.com/garm-ai/garm/contracts/garm/card/v1"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

const cardFixture = `
syntax = "proto3";
package fx.cards;
import "buf/validate/validate.proto";
import "garm/card/v1/card.proto";
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/fx/cards;c";
option (garm.tool.v1.compartments) = { declared: [{ name: "financial" description: "Money." }] };

enum Speed { SPEED_UNSPECIFIED = 0; SPEED_STANDARD = 1; SPEED_FAST = 2; }

message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_INTERNAL on_deny: { omit: {} } };
  // How much to send, in minor units.
  optional int64 amount_minor_units = 1 [(buf.validate.field).required = true,
                                         (buf.validate.field).int64 = { gt: 0, lte: 1000000 }];
  // Who is being paid.
  optional string beneficiary_iban = 2 [(buf.validate.field).string.max_len = 34];
  optional Speed speed = 3;
  optional bool notify = 4;
  // The runner's, not yours.
  optional string idempotency_key = 5 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_PUBLIC on_deny: { omit: {} } source: SOURCE_RUNNER }];
  repeated string tags = 6;
}
message PayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_INTERNAL on_deny: { omit: {} } };
  // The payment's id.
  optional string payment_id = 1;
  optional Speed speed = 2;
  // Only compliance may read this.
  optional string screening = 3 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_RESTRICTED compartments: ["financial"] on_deny: { omit: {} } }];
}
service Payments {
  option (garm.meta.v1.owner) = { team: "payments-platform" contact: "#payments-oncall" };
  rpc InitiatePayment(PayRequest) returns (PayResponse) {
    option (garm.tool.v1.tool) = {
      name: "initiate_payment" title: "Initiate a payment" description: "Move money."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_RESTRICTED
                  approver_compartments: ["financial"]
                  max_grant_age_seconds: 900
                  material_fields: ["amount_minor_units", "beneficiary_iban"] }
    };
    option (garm.card.v1.task_card) = {
      title: "Pay {amount_minor_units}?"
      body: [{ text: "To {beneficiary_iban}." }]
    };
  }
}
`

func payMethod(t *testing.T) protoreflect.MethodDescriptor {
	t.Helper()
	svc := serviceNamed(t, oneFile(t, cardFixture), "Payments")
	return svc.Methods().ByName("InitiatePayment")
}

// The generated form: one input per field the CALLER may set, with the
// control chosen by type and the constraints read from protovalidate.
func TestTheGeneratedInputCard(t *testing.T) {
	card, err := cards.BuildInputCard(payMethod(t))
	if err != nil {
		t.Fatal(err)
	}
	if card.GetKind() != cardv1.Kind_START || card.GetState() != cardv1.State_OPEN {
		t.Errorf("a form is a START card that is OPEN; got %v/%v", card.GetKind(), card.GetState())
	}

	byID := map[string]*cardv1.Input{}
	// The label lives on the ELEMENT, not on the input: an element is the
	// unit the daemon removes, and removing the control while leaving the
	// label would leave a caption with nothing under it.
	access := map[string]*cardv1.Label{}
	for _, el := range card.GetBody() {
		if in := el.GetInput(); in != nil {
			byID[in.GetId()] = in
			access[in.GetId()] = el.GetAccess()
		}
	}

	// A field the RUNNER supplies is absent: it is not the caller's to fill,
	// and showing a person an idempotency key is showing them a decision the
	// platform already made.
	if _, present := byID["idempotency_key"]; present {
		t.Error("a SOURCE_RUNNER field is offered as an input")
	}
	// A repeated field has no honest control, so it is left out rather than
	// rendered as something a person will fill in wrongly.
	if _, present := byID["tags"]; present {
		t.Error("a repeated field is offered as an input")
	}

	amount := byID["amount_minor_units"]
	if amount == nil {
		t.Fatal("no input for amount_minor_units")
	}
	// The caption is the author's own comment, not the field name: they
	// already wrote the sentence a reader needs.
	if amount.GetLabel() != "How much to send, in minor units" {
		t.Errorf("caption = %q, want the field's leading comment", amount.GetLabel())
	}
	if !amount.GetRequired() {
		t.Error("protovalidate says required and the form does not")
	}
	// An exclusive bound is narrowed to the inclusive one a form can express:
	// a control that admitted the boundary the server refuses produces one
	// rejected submission per person who tries it.
	if n := amount.GetNumber(); n == nil || n.GetMin() != 1 || n.GetMax() != 1000000 {
		t.Errorf("number bounds = %v, want min 1 (gt: 0 narrowed) and max 1000000", n)
	}
	if txt := byID["beneficiary_iban"].GetText(); txt == nil || txt.GetMaxLen() != 34 {
		t.Errorf("max_len = %v, want 34 from protovalidate", txt)
	}
	if byID["notify"].GetToggle() == nil {
		t.Error("a bool is not a toggle")
	}

	// An enum becomes a closed list, minus the zero value: offering
	// "unspecified" is offering a person the option of saying nothing while
	// appearing to answer.
	choices := byID["speed"].GetChoice().GetChoices()
	if len(choices) != 2 {
		t.Fatalf("speed has %d choices, want 2 (the zero value is not one)", len(choices))
	}
	if choices[0].GetValue() != "SPEED_STANDARD" || choices[0].GetTitle() != "Speed standard" {
		t.Errorf("first choice = %v", choices[0])
	}

	// Each input is labelled at the field's WRITE policy, joined with the
	// endpoint's own — so an input the viewer may not fill is DROPPED rather
	// than shown disabled, and no input is ever below the endpoint's floor.
	if got := access["amount_minor_units"].GetClearance(); got != toolv1.Clearance_CLEARANCE_INTERNAL {
		t.Errorf("input access = %v, want INTERNAL", got)
	}
	if card.GetAccess().GetClearance() != toolv1.Clearance_CLEARANCE_INTERNAL {
		t.Errorf("card access = %v, want the endpoint's INTERNAL", card.GetAccess().GetClearance())
	}
	if len(card.GetActions()) != 1 || card.GetActions()[0].GetId() != "start" {
		t.Errorf("a form has one action, `start`; got %v", card.GetActions())
	}
}

// The generated answer: a fact per scalar, labelled at its own read policy.
func TestTheGeneratedResultCard(t *testing.T) {
	md := payMethod(t)

	// With no stored response there is no card, and the refusal says so
	// rather than returning an empty one — an empty card is one a person
	// reads as "nothing happened".
	if _, err := cards.BuildResultCard(md, &cardv1.CallRef{}, nil); !errors.Is(err, cards.ErrResultUnavailable) {
		t.Errorf("a result card with no response returned %v, want ErrResultUnavailable", err)
	}

	resp := dynamicResponse(t, md, map[string]any{
		"payment_id": "pay_01J",
		"speed":      "SPEED_FAST",
		"screening":  "clear",
	})
	card, err := cards.BuildResultCard(md, &cardv1.CallRef{CallId: strptr("call_9")}, resp)
	if err != nil {
		t.Fatal(err)
	}
	if card.GetKind() != cardv1.Kind_RUN || card.GetSubjectId() != "call_9" {
		t.Errorf("a result card is a RUN card about its call; got %v/%q", card.GetKind(), card.GetSubjectId())
	}

	facts := map[string]*cardv1.Fact{}
	for _, el := range card.GetBody() {
		for _, f := range el.GetFacts().GetFacts() {
			facts[f.GetField()] = f
		}
	}
	// Fact.field is the dotted path, which is what lets a client rebuild a
	// material map and what the daemon names when it withholds one.
	if facts["payment_id"].GetValue() != "pay_01J" {
		t.Errorf("payment_id = %q", facts["payment_id"].GetValue())
	}
	// An enum reads as a name a person recognises, never as a number.
	if got := facts["speed"].GetValue(); got != "Speed fast" {
		t.Errorf("speed = %q, want a readable enum name", got)
	}

	// The labelling is the point of the whole exercise: a fact derived from a
	// RESTRICTED+financial field carries that, so the daemon drops it for a
	// viewer without the compartment. A default that labelled from the
	// endpoint would hand a compliance-only value to every approver.
	sc := facts["screening"].GetAccess()
	if sc.GetClearance() != toolv1.Clearance_CLEARANCE_RESTRICTED {
		t.Errorf("screening access = %v, want RESTRICTED from its own field policy", sc.GetClearance())
	}
	if len(sc.GetCompartments()) != 1 || sc.GetCompartments()[0] != "financial" {
		t.Errorf("screening compartments = %v, want [financial]", sc.GetCompartments())
	}
}

// The generated approval card: the material, valued from the ref, labelled at
// the approver predicate joined with each field's own policy.
func TestTheGeneratedApprovalCard(t *testing.T) {
	card, err := cards.BuildApprovalCard(payMethod(t), &cardv1.TaskRef{
		TaskId: strptr("tsk_1"),
		Material: map[string]string{
			"amount_minor_units": "1250",
			"beneficiary_iban":   "GB29NWBK60161331926819",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if card.GetKind() != cardv1.Kind_TASK || card.GetSubjectId() != "tsk_1" {
		t.Errorf("an approval card is a TASK card about its task; got %v/%q", card.GetKind(), card.GetSubjectId())
	}

	var material []*cardv1.Fact
	for _, el := range card.GetBody() {
		if s := el.GetSection(); s != nil && s.GetTitle() == "Material" {
			for _, inner := range s.GetElements() {
				material = append(material, inner.GetFacts().GetFacts()...)
			}
		}
	}
	if len(material) != 2 {
		t.Fatalf("the Material section has %d facts, want 2", len(material))
	}
	// Declaration order of material_fields, values from the ref — because the
	// daemon refuses a grant-mode call before it resolves the tool, so this
	// service never saw the request that was parked.
	if material[0].GetField() != "amount_minor_units" || material[0].GetValue() != "1250" {
		t.Errorf("first material fact = %v", material[0])
	}
	// Labelled at the APPROVER predicate joined with the field's own policy:
	// the field reads at INTERNAL, and an approver must be RESTRICTED with
	// `financial`, so the fact is the tighter of the two.
	a := material[0].GetAccess()
	if a.GetClearance() != toolv1.Clearance_CLEARANCE_RESTRICTED {
		t.Errorf("material access = %v, want the approver predicate's RESTRICTED", a.GetClearance())
	}
	if len(a.GetCompartments()) != 1 || a.GetCompartments()[0] != "financial" {
		t.Errorf("material compartments = %v, want [financial]", a.GetCompartments())
	}

	// The owner, so an approver deciding something irreversible can see who
	// is answerable without leaving the page.
	var owner bool
	// And the declared task_card template, interpolated from the material.
	var heading string
	for _, el := range card.GetBody() {
		for _, f := range el.GetFacts().GetFacts() {
			if f.GetLabel() == "Owner" && f.GetValue() == "payments-platform" {
				owner = true
			}
		}
		if txt := el.GetText(); txt != nil && txt.GetEmphasis() == cardv1.Emphasis_HEADING {
			heading = txt.GetText()
		}
	}
	if !owner {
		t.Error("the approval card carries no owner")
	}
	if heading != "Pay 1250?" {
		t.Errorf("the task_card title rendered as %q, want the material interpolated in", heading)
	}
}

// Join is the TIGHTER of two labels, and the asymmetry is the safety
// property: an override cannot label something below the endpoint that let
// the viewer in.
func TestJoinTakesTheTighterOfTwoLabels(t *testing.T) {
	got := cards.Join(
		cards.Label(toolv1.Clearance_CLEARANCE_INTERNAL, []string{"financial"}),
		cards.Label(toolv1.Clearance_CLEARANCE_RESTRICTED, []string{"compliance"}),
	)
	if got.GetClearance() != toolv1.Clearance_CLEARANCE_RESTRICTED {
		t.Errorf("clearance = %v, want the higher of the two", got.GetClearance())
	}
	if len(got.GetCompartments()) != 2 {
		t.Errorf("compartments = %v, want the union: needing two is stricter than needing one",
			got.GetCompartments())
	}
	// A nil label is the identity, because absent means "no requirement of
	// its own" and never "public".
	if cards.Join(nil, nil) != nil {
		t.Error("joining two absent labels invented one")
	}
}

func strptr(s string) *string { return &s }

// dynamicResponse builds an instance of a method's response message from a
// map of field name to value.
//
// dynamicpb rather than a generated type, because the fixture is compiled in
// this test and has no Go. That is also how the daemon builds messages from a
// catalogue, so the card builders are exercised against the same shape they
// will meet in production.
func dynamicResponse(t *testing.T, md protoreflect.MethodDescriptor, values map[string]any) *dynamicpb.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(md.Output())
	for name, v := range values {
		fd := md.Output().Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			t.Fatalf("the fixture's response has no field %q", name)
		}
		switch val := v.(type) {
		case string:
			if fd.Kind() == protoreflect.EnumKind {
				ev := fd.Enum().Values().ByName(protoreflect.Name(val))
				if ev == nil {
					t.Fatalf("%s has no value %q", fd.Enum().FullName(), val)
				}
				msg.Set(fd, protoreflect.ValueOfEnum(ev.Number()))
				continue
			}
			msg.Set(fd, protoreflect.ValueOfString(val))
		default:
			t.Fatalf("unsupported fixture value %T", v)
		}
	}
	return msg
}

// A ref names a card AND whose endpoint serves it.
//
// A kind and an id say which card; they do not say whose ResultCard or
// ApprovalCard to call for it. A client that started a run itself knows the
// answer and a client that reached the same run from a task does not — which
// is the hole that left one page unable to show itself. The constructor
// refuses a ref without it rather than letting it render fine and fail on a
// route nobody pictured.
func TestARefCarriesTheToolThatServesIt(t *testing.T) {
	md := payMethod(t)

	if got := cards.ToolFQN(md); got != "fx.cards.initiate_payment" {
		t.Errorf("ToolFQN = %q, want the package-qualified tool name", got)
	}
	// And a card's own FQN, which is what a ref pointing AT it must carry.
	for _, s := range cards.Synthesise(md) {
		if s.Kind == cards.Approval && s.FQN() != "fx.cards.initiate_payment_approval_card" {
			t.Errorf("the approval card's FQN is %q", s.FQN())
		}
	}

	ref, err := cards.Ref(cardv1.Kind_RUN, "run_7", "The run", cardv1.State_RUNNING,
		"bank.agents.v1.assistant_result_card")
	if err != nil {
		t.Fatal(err)
	}
	if ref.GetToolFqn() != "bank.agents.v1.assistant_result_card" {
		t.Errorf("tool_fqn = %q", ref.GetToolFqn())
	}

	if _, err := cards.Ref(cardv1.Kind_RUN, "run_7", "The run", cardv1.State_RUNNING, ""); err == nil {
		t.Fatal("a ref with no tool was built; it would render fine and fail on the one " +
			"route its author did not picture")
	}
}
