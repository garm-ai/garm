package wire_test

import (
	"strings"
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

// A tenant name must never become two subject elements.
//
// "acme.corp" would otherwise land on garm.v1.audit.acme.corp.<app>, which is
// one element deeper than any consumer filters for — the publish succeeds,
// nothing consumes it, and no error is reported anywhere, because publishing
// to a subject with no subscriber is not an error in NATS. For the audit
// stream that is a record silently not kept.
func TestASubjectElementCannotEscapeItsPosition(t *testing.T) {
	for _, tenant := range []string{"acme.corp", "a b", "x\ty", "a\nb"} {
		got := wire.AuditSubjectFor(tenant, "app")
		if n := strings.Count(got, "."); n != 4 {
			t.Errorf("AuditSubjectFor(%q) = %q, which has %d separators and not 4; "+
				"the tenant escaped its element", tenant, got, n)
		}
	}
}

// The wildcards are the dangerous case: a tenant named ">" would subscribe a
// naive consumer to everything, and a tenant named "*" matches any sibling.
func TestATenantCannotSmuggleAWildcard(t *testing.T) {
	for _, tenant := range []string{">", "*", "a>b", "a*b"} {
		got := wire.LedgerSubjectFor(tenant, "app")
		if strings.ContainsAny(got[len(wire.LedgerSubject):], "*>") {
			t.Errorf("LedgerSubjectFor(%q) = %q; a wildcard reached the subject", tenant, got)
		}
	}
}

// Empty is a real case — not every event has a tenant — and a subject may not
// contain an empty element.
func TestAnEmptyElementBecomesAPlaceholder(t *testing.T) {
	if got, want := wire.AuditSubjectFor("", ""), "garm.v1.audit._._"; got != want {
		t.Errorf("AuditSubjectFor(\"\", \"\") = %q, want %q", got, want)
	}
}

// The two streams must not share a subject space. If they did, one stream's
// retention and discard policy would silently apply to both.
func TestTheLedgerAndAuditSubjectsDoNotOverlap(t *testing.T) {
	if strings.HasPrefix(wire.AuditSubject, wire.LedgerSubject) ||
		strings.HasPrefix(wire.LedgerSubject, wire.AuditSubject) {
		t.Errorf("%q and %q overlap, so a stream capturing one would capture the other",
			wire.LedgerSubject, wire.AuditSubject)
	}
}
