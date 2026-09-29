package policy_test

import (
	"testing"

	cardv1 "github.com/garm-ai/garm/contracts/garm/card/v1"
	"github.com/garm-ai/garm/policy"
)

// A card is an opaque VALUE to the field-policy walk.
//
// The reason is in card.proto and worth repeating where the code is: a field
// policy is per descriptor and says what every caller of this RPC may see of
// this field, while a card's rows have different policies from one another —
// a queue holds one task per approver. So the policy travels on the value, as
// a label on each element, and the daemon projects it there after the field
// plan has run. Compiling a plan over a card's insides would be a second
// mechanism answering the same question differently.
func TestACardIsAnOpaqueLeaf(t *testing.T) {
	card := (&cardv1.Card{}).ProtoReflect().Descriptor()
	if !policy.IsOpaqueLeafMessage(card) {
		t.Fatal("garm.card.v1.Card is not an opaque leaf; a card endpoint's response " +
			"would then need a field policy on every element of the vocabulary")
	}

	// As the ROOT of a plan — which is every card endpoint, whose whole
	// response is a card — it compiles to nothing, rather than to one action
	// per field inside it.
	plan, err := policy.Compile(card, mustRegistry(t))
	if err != nil {
		t.Fatalf("compiling a plan for garm.card.v1.Card: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("a plan for a card carries %d action(s), want none: the labels on the "+
			"value are what governs it", len(plan.Actions))
	}

	// And only Card. CallRef and TaskRef are request messages with ordinary
	// scalar fields, classified like anything else — the addition is one
	// named type, not the whole namespace.
	if policy.IsOpaqueLeafMessage((&cardv1.TaskRef{}).ProtoReflect().Descriptor()) {
		t.Error("garm.card.v1.TaskRef is an opaque leaf; it is an ordinary request message " +
			"and its fields must be classified")
	}
	if policy.IsOpaqueLeafMessage((&cardv1.CallRef{}).ProtoReflect().Descriptor()) {
		t.Error("garm.card.v1.CallRef is an opaque leaf; it is an ordinary request message " +
			"and its fields must be classified")
	}
}

func mustRegistry(t *testing.T) *policy.Registry {
	t.Helper()
	reg, err := policy.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
