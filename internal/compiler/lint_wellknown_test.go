package compiler_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
	"github.com/garm-ai/garm/policy"
)

// findM returns the fixture's message M from a built fixture's file set.
func findM(t *testing.T, fds []protoreflect.FileDescriptor) protoreflect.MessageDescriptor {
	t.Helper()
	md := fds[0].Messages().ByName("M")
	if md == nil {
		t.Fatal("fixture has no message M")
	}
	return md
}

// TestWellKnownTypeIsAnOpaqueLeafForBothWalks is the agreement test.
//
// policy.Compile (which builds the plan the server enforces) and
// compiler.Lint (the build gate over the same graph) must treat exactly the
// same set of fields as reachable. When they disagreed about message-valued
// maps, the build passed and the server then refused to start; the
// well-known types were the same defect pointed the other way — both walks
// descended into google.protobuf.Timestamp, whose seconds and nanos carry
// no garm annotation and never can, so EVERY schema with a Timestamp
// anywhere under a tool failed the build with no recourse.
//
// Both halves are asserted against one fixture, in one test, deliberately:
// a pair of separate tests could each keep passing while the two walks
// drifted apart from each other.
func TestWellKnownTypeIsAnOpaqueLeafForBothWalks(t *testing.T) {
	fds := lintFixtureWellKnown(t, "wkt_leaf", validField(),
		methodSpec{name: "Get", tool: validTool("get")})

	// --- the lint half: not one diagnostic, of any rule, from inside the
	// Timestamp. L1 is the one that used to fire (twice, for seconds and
	// nanos) and the one that fails the build.
	for _, d := range compiler.Lint(fds) {
		if strings.Contains(d.Path, ".at.") {
			t.Fatalf("lint descended into the well-known type: %s", d)
		}
		if d.Rule == "L1" {
			t.Fatalf("unexpected L1 on a fully labeled fixture: %s", d)
		}
	}

	// --- the Compile half: exactly ONE Action for the field itself, and
	// nothing beneath it.
	reg, err := policy.NewRegistry(nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	plan, err := policy.Compile(findM(t, fds), reg)
	if err != nil {
		t.Fatalf("Compile rejected a message holding a well-known type: %v", err)
	}

	var own int
	for _, a := range plan.Actions {
		if a.Name == "at" {
			own++
			if a.IsSubtree {
				t.Fatal("the Timestamp field is marked IsSubtree, but the plan holds " +
					"no Actions below it; IsSubtree means \"there is something to prune\"")
			}
			if a.Read != toolv1.Clearance_CLEARANCE_CONFIDENTIAL {
				t.Fatalf("the field's own policy did not survive: read=%v", a.Read)
			}
		}
		if strings.HasPrefix(a.Name, "at.") {
			t.Fatalf("Compile emitted an Action inside the well-known type: %q", a.Name)
		}
	}
	if own != 1 {
		t.Fatalf("got %d Actions named \"at\", want exactly 1", own)
	}
}

// TestWellKnownLeafDoesNotSuppressL1OnOrdinaryFields is the regression test
// that matters most: the fix must NARROW the walk at one boundary, not turn
// it off. Same fixture as above, same Timestamp field, but `secret` — an
// ordinary string sitting right beside it in M — carries no policy at all.
// L1 must still name it.
func TestWellKnownLeafDoesNotSuppressL1OnOrdinaryFields(t *testing.T) {
	fds := lintFixtureWellKnown(t, "wkt_leaf_unlabeled", nil,
		methodSpec{name: "Get", tool: validTool("get")})

	if !hasRuleWithMsg(compiler.Lint(fds), "L1", false, "no field policy and no message default") {
		t.Fatal("L1 stopped firing for an unlabeled ordinary field once a " +
			"well-known type was present in the same message")
	}

	// And Compile must still reject it, for the same reason: the two walks
	// agree on the failure as well as on the success.
	reg, err := policy.NewRegistry(nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := policy.Compile(findM(t, fds), reg); err == nil {
		t.Fatal("Compile accepted a message with an unlabeled ordinary field")
	}
}

// TestOpaqueLeafPredicateIsScopedToTheWellKnownPackage pins the predicate
// itself. It is a package-prefix heuristic, and the risk of a heuristic is
// that it quietly grows: a message merely NAMED like a well-known type, or
// any ordinary application message, must still be walked.
func TestOpaqueLeafPredicateIsScopedToTheWellKnownPackage(t *testing.T) {
	fds := lintFixtureWellKnown(t, "wkt_predicate", validField(),
		methodSpec{name: "Get", tool: validTool("get")})
	m := findM(t, fds)

	at := m.Fields().ByName("at")
	if at == nil {
		t.Fatal("fixture lost its Timestamp field")
	}
	if !policy.IsOpaqueLeafMessage(at.Message()) {
		t.Fatalf("%s is not treated as an opaque leaf", at.Message().FullName())
	}
	if _, ok := policy.SubtreeOf(at); ok {
		t.Fatal("SubtreeOf descends into a well-known type")
	}

	// N is an ordinary application message; nothing about it is opaque.
	nested := m.Fields().ByName("nested")
	if policy.IsOpaqueLeafMessage(nested.Message()) {
		t.Fatalf("%s wrongly treated as an opaque leaf", nested.Message().FullName())
	}
	if _, ok := policy.SubtreeOf(nested); !ok {
		t.Fatal("SubtreeOf refuses to descend into an ordinary message field")
	}

	// A message-VALUED map is still a subtree; a scalar-valued one is not.
	// Asserted here because SubtreeOf now owns that rule for both walks, so
	// this file is where a regression in it would show up.
	if _, ok := policy.SubtreeOf(m.Fields().ByName("msg_map")); !ok {
		t.Fatal("SubtreeOf stopped descending into a message-valued map")
	}
	if _, ok := policy.SubtreeOf(m.Fields().ByName("str_map")); ok {
		t.Fatal("SubtreeOf descends into a scalar-valued map")
	}
}
