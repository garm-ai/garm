package grant_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/contracts/grant"
)

// The digest is a contract between two repositories. Every property here is
// something one side could change alone and break the other silently.

func TestTheSameValuesGiveTheSameDigest(t *testing.T) {
	a := grant.Digest(map[string]string{"amount": "250", "iban": "GB29"})
	b := grant.Digest(map[string]string{"amount": "250", "iban": "GB29"})
	if a != b {
		t.Fatalf("not deterministic: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("digest = %q, want a sha256: prefix", a)
	}
}

// Map iteration is randomised in Go, so an implementation that walked the map
// would produce a different digest per call and refuse every grant it minted.
func TestPathOrderDoesNotChangeTheDigest(t *testing.T) {
	for range 50 {
		got := grant.Digest(map[string]string{
			"z": "1", "a": "2", "m": "3", "b": "4", "y": "5",
		})
		want := grant.Digest(map[string]string{
			"y": "5", "b": "4", "m": "3", "a": "2", "z": "1",
		})
		if got != want {
			t.Fatalf("order changed the digest: %s vs %s", got, want)
		}
	}
}

// Absence is a value. Approving with an amount and sending without must not
// match, and a field whose absence is itself material must still work.
func TestAbsenceIsDistinctFromEveryValue(t *testing.T) {
	unset := grant.Digest(map[string]string{"amount": grant.Unset})
	for _, v := range []string{"", "0", "unset", "<unset> "} {
		if grant.Digest(map[string]string{"amount": v}) == unset {
			t.Errorf("the value %q digests the same as absent", v)
		}
	}
	// And absent-approved matches absent-sent, which is the legitimate case.
	if grant.Digest(map[string]string{"schedule": grant.Unset}) !=
		grant.Digest(map[string]string{"schedule": grant.Unset}) {
		t.Error("two absent fields did not match")
	}
}

// The attack on any line-oriented encoding: a string value that forges the
// appearance of another field. Two different requests must never digest the
// same.
func TestAValueCannotForgeAnotherField(t *testing.T) {
	forged := grant.Digest(map[string]string{
		"reference": "harmless\namount=10",
		"amount":    "10000",
	})
	honest := grant.Digest(map[string]string{
		"reference": "harmless",
		"amount":    "10",
	})
	if forged == honest {
		t.Fatal("a newline in a value forged another field; a request for ten " +
			"thousand digests as one for ten")
	}
	// A backslash must not let the escaping itself be escaped.
	a := grant.Digest(map[string]string{"x": `a\`, "y": "b"})
	b := grant.Digest(map[string]string{"x": `a`, "y": `\b`})
	if a == b {
		t.Error("backslashes collide; the escaping is reversible only one way")
	}
}

// A missing field changes the digest, which is the whole point: it is what
// makes approve-with then send-without a refusal.
func TestDroppingAFieldChangesTheDigest(t *testing.T) {
	with := grant.Digest(map[string]string{"a": "1", "b": "2"})
	without := grant.Digest(map[string]string{"a": "1"})
	if with == without {
		t.Error("dropping a material field did not change the digest")
	}
}

func TestPathShapeIsChecked(t *testing.T) {
	for _, ok := range []string{"amount", "payment.amount", "a.b.c_d"} {
		if err := grant.ValidPath(ok); err != nil {
			t.Errorf("ValidPath(%q) = %v, want nil", ok, err)
		}
	}
	// Each of these would let a path forge a separator or name nothing.
	for _, bad := range []string{"", "a..b", ".a", "a.", "a=b", "a\nb"} {
		if err := grant.ValidPath(bad); err == nil {
			t.Errorf("ValidPath(%q) = nil, want an error", bad)
		}
	}
}
