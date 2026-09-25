package wire_test

import (
	"testing"

	"github.com/garm-ai/garm/contracts/wire"
)

// The rules are pinned by example rather than by restating the
// implementation, because what matters is that both sides of the hop agree on
// these exact strings — and a test that mirrors the code would pass through a
// change that broke that agreement.
func TestTheNamesAreExactlyThese(t *testing.T) {
	const route = "/calc.v1.Calculator/Add"
	for _, c := range []struct{ name, got, want string }{
		{"Subject", wire.Subject(route), "calc.v1.Calculator.Add"},
		{"QueueGroup", wire.QueueGroup(route), "calc.v1.Calculator"},
		{"EndpointName", wire.EndpointName(route), "calc_v1_Calculator_Add"},
		{"MicroServiceName", wire.MicroServiceName("calc.v1.Calculator"), "calc_v1_Calculator"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// A route without the leading slash must give the same answer: the two sides
// hold it in different shapes, and a subject that depended on which would be
// the drift this package exists to remove.
func TestTheLeadingSlashDoesNotMatter(t *testing.T) {
	with, without := "/calc.v1.Calculator/Add", "calc.v1.Calculator/Add"
	if wire.Subject(with) != wire.Subject(without) {
		t.Errorf("Subject disagrees on the leading slash: %q vs %q",
			wire.Subject(with), wire.Subject(without))
	}
	if wire.QueueGroup(with) != wire.QueueGroup(without) {
		t.Errorf("QueueGroup disagrees on the leading slash: %q vs %q",
			wire.QueueGroup(with), wire.QueueGroup(without))
	}
}

// Nothing NATS rejects may survive into a name it has to accept.
func TestMicroNamesCarryNoDots(t *testing.T) {
	for _, in := range []string{"a.b.c", "calc.v1.Calculator", "no_dots_here"} {
		if got := wire.MicroServiceName(in); contains(got, ".") {
			t.Errorf("MicroServiceName(%q) = %q, which micro rejects", in, got)
		}
	}
	if got := wire.EndpointName("/a.b.C/D"); contains(got, ".") {
		t.Errorf("EndpointName kept a dot: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
