package policy_test

import (
	"strings"
	"testing"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
	"github.com/garm-ai/garm/policy/testdata"
	"github.com/garm-ai/garm/policy/testdata/testdatagarm"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestCompileInheritsMessageDefault(t *testing.T) {
	plan := compileProfile(t)

	locale := findAction(t, plan, "locale")
	if locale.Read != toolv1.Clearance_CLEARANCE_INTERNAL {
		t.Fatalf("locale did not inherit the message default: %v", locale.Read)
	}
	id := findAction(t, plan, "id")
	if id.Read != toolv1.Clearance_CLEARANCE_PUBLIC {
		t.Fatalf("id override lost: %v", id.Read)
	}
}

func TestCompileMarksSubtreeGate(t *testing.T) {
	plan := compileProfile(t)
	billing := findAction(t, plan, "billing")
	if !billing.IsSubtree {
		t.Fatal("message-typed field not marked as a subtree gate")
	}
	// Fields inside the subtree are still compiled: a caller who passes the
	// gate is then filtered by the inner policies.
	if findAction(t, plan, "billing.iban").Read != toolv1.Clearance_CLEARANCE_RESTRICTED {
		t.Fatal("nested field policy not compiled")
	}
	if findAction(t, plan, "billing.card_last4").Read != toolv1.Clearance_CLEARANCE_CONFIDENTIAL {
		t.Fatal("nested inheritance not applied")
	}
	// The positive case above is not enough: an implementation that marked
	// every field a subtree gate would also pass it. Pin the negative case
	// for a map and for an ordinary scalar.
	if findAction(t, plan, "tags").IsSubtree {
		t.Fatal("map field wrongly marked as a subtree gate")
	}
	if findAction(t, plan, "locale").IsSubtree {
		t.Fatal("scalar field wrongly marked as a subtree gate")
	}
}

func TestCompileFallsBackWriteToRead(t *testing.T) {
	// No field in the fixture sets `write` explicitly; every Action.Write
	// must fall back to its Read. An implementation that left Write
	// UNSPECIFIED would silently deny every write and no other test would
	// notice.
	id := findAction(t, compileProfile(t), "id")
	if id.Write != id.Read {
		t.Fatalf("write did not fall back to read: write=%v read=%v", id.Write, id.Read)
	}
	if id.Write != toolv1.Clearance_CLEARANCE_PUBLIC {
		t.Fatalf("write = %v, want CLEARANCE_PUBLIC", id.Write)
	}
}

func TestCompilePathsAreIndependentCopies(t *testing.T) {
	plan := compileProfile(t)

	id := findAction(t, plan, "id")
	if len(id.Path) != 1 || id.Path[0] != 1 {
		t.Fatalf("id path = %v, want [1]", id.Path)
	}

	iban := findAction(t, plan, "billing.iban")
	if len(iban.Path) != 2 || iban.Path[0] != 4 || iban.Path[1] != 2 {
		t.Fatalf("billing.iban path = %v, want [4 2]", iban.Path)
	}

	cardLast4 := findAction(t, plan, "billing.card_last4")
	if len(cardLast4.Path) != 2 || cardLast4.Path[0] != 4 || cardLast4.Path[1] != 1 {
		t.Fatalf("billing.card_last4 path = %v, want [4 1]", cardLast4.Path)
	}

	// Two sibling Actions must not share backing storage: mutating one
	// Path must never be visible through the other.
	before := cardLast4.Path[1]
	iban.Path[1] = 99
	if cardLast4.Path[1] != before {
		t.Fatalf("billing.iban and billing.card_last4 Path slices alias: "+
			"mutating one changed the other from %v to %v", before, cardLast4.Path[1])
	}
}

func TestCompileRejectsUnlabeledField(t *testing.T) {
	reg, err := policy.NewRegistry(testdatagarm.Compartments)
	if err != nil {
		t.Fatal(err)
	}
	_, err = policy.Compile((&testdata.Unlabeled{}).ProtoReflect().Descriptor(), reg)
	if err == nil {
		t.Fatal("compiled a message with no field policy and no message default")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Fatalf("error does not name the offending field: %v", err)
	}
}

func TestCompileWalksRepeatedMessages(t *testing.T) {
	plan := compilePlan(t, (&testdata.SearchResponse{}).ProtoReflect().Descriptor())
	if findAction(t, plan, "profiles.email").Read != toolv1.Clearance_CLEARANCE_CONFIDENTIAL {
		t.Fatal("policy inside a repeated message was not compiled")
	}
}

func TestCompileRecordsAuditFlag(t *testing.T) {
	if !findAction(t, compileProfile(t), "national_id").AuditOnRead {
		t.Fatal("audit_on_read lost")
	}
}

func TestCompileRejectsUndeclaredCompartment(t *testing.T) {
	reg, err := policy.NewRegistry(nil) // nothing declared
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Compile(
		(&testdata.Profile{}).ProtoReflect().Descriptor(), reg); err == nil {
		t.Fatal("compiled against an empty registry; undeclared use must fail")
	}
}

func compileProfile(t *testing.T) *policy.Plan {
	t.Helper()
	return compilePlan(t, (&testdata.Profile{}).ProtoReflect().Descriptor())
}

func compilePlan(t *testing.T, md protoreflect.MessageDescriptor) *policy.Plan {
	t.Helper()
	reg, err := policy.NewRegistry(testdatagarm.Compartments)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := policy.Compile(md, reg)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func findAction(t *testing.T, p *policy.Plan, name string) policy.Action {
	t.Helper()
	for _, a := range p.Actions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no action for %q; have %v", name, actionNames(p))
	return policy.Action{}
}

func actionNames(p *policy.Plan) []string {
	out := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, a.Name)
	}
	return out
}

// TestCompileRejectsARecursiveMessage pins the fix for the leak the final
// branch review found: Compile used to return nil on a repeated message
// name, on the theory that "the outer occurrence already carries the
// policy". It does not — an Action's Path is rooted at the plan's own
// message, so the outer occurrence's actions apply to the outer INSTANCE
// only. A plan for Node would have held actions for `child` and `secret`
// and nothing for `child.secret`, so a PUBLIC caller got the top-level
// secret omitted and every nested one in the clear.
//
// The assertion is deliberately two-sided: the call must ERROR (silently
// truncating is the leak), and the error must name the recursive type and
// the path where it repeats, because an operator who hits this at boot has
// to find the offending field in a schema they may not have written.
func TestCompileRejectsARecursiveMessage(t *testing.T) {
	reg := testRegistry(t)

	_, err := policy.Compile((&testdata.Node{}).ProtoReflect().Descriptor(), reg)
	if err == nil {
		t.Fatal("compiled a self-recursive message: every classified field below depth 1 " +
			"would carry no policy and be returned unredacted")
	}
	if !strings.Contains(err.Error(), "policy.testdata.Node") {
		t.Fatalf("error does not name the recursive message: %v", err)
	}
	if !strings.Contains(err.Error(), "child") {
		t.Fatalf("error does not name the path at which the cycle closes: %v", err)
	}
}

// TestCompileRejectsAMutuallyRecursiveMessage is the same property for a
// cycle that spans two types. It discriminates a cycle check that only
// compares a field's message type against its immediate parent: neither
// LoopA nor LoopB references itself.
func TestCompileRejectsAMutuallyRecursiveMessage(t *testing.T) {
	reg := testRegistry(t)

	if _, err := policy.Compile(
		(&testdata.LoopA{}).ProtoReflect().Descriptor(), reg); err == nil {
		t.Fatal("compiled a mutually recursive message graph")
	}
	// Entering the same cycle from its other end must fail identically —
	// a plan's validity cannot depend on which message a tool happens to
	// name as its request or response.
	if _, err := policy.Compile(
		(&testdata.LoopB{}).ProtoReflect().Descriptor(), reg); err == nil {
		t.Fatal("compiled a mutually recursive message graph entered from LoopB")
	}
}

// TestCompileAcceptsARepeatedTypeThatIsNotACycle guards the other direction:
// the same message type appearing twice in one plan through DISTINCT paths
// (a diamond, not a cycle) must still compile, with policy flattened under
// each path independently. Without this, the obvious over-broad fix — a
// globally-scoped `seen` that is never unwound — would look correct.
func TestCompileAcceptsARepeatedTypeThatIsNotACycle(t *testing.T) {
	// Profile reaches Billing twice: once via `billing` and once via the
	// message-valued map `billing_by_id`.
	plan := compileProfile(t)
	for _, want := range []string{"billing.iban", "billing_by_id.iban"} {
		if findAction(t, plan, want).Read != toolv1.Clearance_CLEARANCE_RESTRICTED {
			t.Fatalf("%s missing or mis-classified; a non-cycle repeat must still compile", want)
		}
	}
}
