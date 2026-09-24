package policy_test

import (
	"testing"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
)

func TestAllowsFailsClosedOnUnspecified(t *testing.T) {
	const (
		unspec = toolv1.Clearance_CLEARANCE_UNSPECIFIED
		public = toolv1.Clearance_CLEARANCE_PUBLIC
		conf   = toolv1.Clearance_CLEARANCE_CONFIDENTIAL
		restr  = toolv1.Clearance_CLEARANCE_RESTRICTED
	)
	cases := []struct {
		name       string
		have, need toolv1.Clearance
		want       bool
	}{
		{"equal passes", conf, conf, true},
		{"higher passes", restr, conf, true},
		{"lower denied", public, conf, false},
		// An unlabeled requirement must deny everyone, not admit everyone.
		// Naive `have >= need` would make this true and fail open.
		{"unspecified requirement denies the highest clearance", restr, unspec, false},
		{"unspecified caller denied", unspec, public, false},
		{"unspecified both denied", unspec, unspec, false},
	}
	for _, c := range cases {
		if got := policy.Allows(c.have, c.need); got != c.want {
			t.Fatalf("%s: Allows(%v,%v) = %v, want %v", c.name, c.have, c.need, got, c.want)
		}
	}
}

func TestCompartmentSetCovers(t *testing.T) {
	reg, err := newTestRegistry(t, "financial", "pii-contact", "support")
	if err != nil {
		t.Fatal(err)
	}
	held, err := reg.Set([]string{"support", "pii-contact"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		need []string
		want bool
	}{
		{nil, true},                 // empty requirement passes
		{[]string{"support"}, true}, // subset
		{[]string{"support", "pii-contact"}, true},
		{[]string{"financial"}, false},            // not held
		{[]string{"support", "financial"}, false}, // AND, not OR
	} {
		needSet, err := reg.Set(c.need)
		if err != nil {
			t.Fatal(err)
		}
		if got := held.Covers(needSet); got != c.want {
			t.Fatalf("Covers(%v) = %v, want %v", c.need, got, c.want)
		}
	}
}

func TestRegistryRejectsUndeclaredAndOverflow(t *testing.T) {
	reg, err := newTestRegistry(t, "support")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Set([]string{"suport"}); err == nil {
		t.Fatal("typo accepted; undeclared compartments must be rejected")
	}

	many := make([]string, 65)
	for i := range many {
		many[i] = "c" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	if _, err := newTestRegistry(t, many...); err == nil {
		t.Fatal("65 compartments accepted; must be a hard error, not truncation")
	}
}

func newTestRegistry(t *testing.T, names ...string) (*policy.Registry, error) {
	t.Helper()
	decls := make([]*toolv1.Decl, 0, len(names))
	for _, n := range names {
		decls = append(decls, &toolv1.Decl{Name: n})
	}
	return policy.NewRegistry(decls)
}
