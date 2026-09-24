package policy_test

import (
	"strings"
	"testing"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
)

// A declared name becomes four things at once: a Go constant, a JWT claim
// value, a ledger field, and a BIT POSITION assigned by sorting the name
// set. That last one is why the format has to be narrow — the failure modes
// are not cosmetic.
func TestValidateDeclNameAccepts(t *testing.T) {
	for _, n := range []string{
		"financial", "pii-contact", "pii-gov-id", "kyc", "a", "v2", "a1-b2-c3",
	} {
		if err := policy.ValidateDeclName(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
}

func TestValidateDeclNameRejects(t *testing.T) {
	for name, tc := range map[string]struct{ in, because string }{
		// Two names differing only in case generate two VALID, distinct Go
		// constants and get two distinct bits. Nothing downstream catches
		// it, and SetLenient silently DROPS an unmatched name — so an IdP
		// minting the wrong case loses that compartment with no error at
		// the call site, only a ledger note. This is the dangerous one.
		"uppercase":  {"PII-contact", "case"},
		"mixed case": {"piiContact", "case"},
		// exportIdent treats - and _ identically and drops both, so these
		// two distinct compartments generate the SAME Go constant.
		"underscore": {"pii_contact", "separator"},
		// `.` is not a separator, so this yields `Pii.contact` — not a
		// valid Go identifier, and the failure lands as generated code that
		// will not parse, far from the cause.
		"dot":             {"pii.contact", "character"},
		"slash":           {"pii/contact", "character"},
		"space":           {"pii contact", "character"},
		"leading digit":   {"2fa", "start"},
		"leading hyphen":  {"-pii", "start"},
		"trailing hyphen": {"pii-", "end"},
		"double hyphen":   {"pii--contact", "empty segment"},
		"empty":           {"", "empty"},
		"unicode":         {"pií", "character"},
	} {
		t.Run(name, func(t *testing.T) {
			err := policy.ValidateDeclName(tc.in)
			if err == nil {
				t.Fatalf("%q accepted (%s)", tc.in, tc.because)
			}
			if !strings.Contains(err.Error(), tc.in) {
				t.Errorf("error does not quote the name: %v", err)
			}
		})
	}
}

// A name long enough to be unwieldy in a ledger column or a JWT is refused
// at a stated limit rather than at whatever the first downstream system
// happens to truncate at.
func TestValidateDeclNameRejectsOverlongNames(t *testing.T) {
	if err := policy.ValidateDeclName(strings.Repeat("a", 64)); err == nil {
		t.Fatal("a 64-character name was accepted")
	}
	if err := policy.ValidateDeclName(strings.Repeat("a", 63)); err != nil {
		t.Fatalf("a 63-character name was refused: %v", err)
	}
}

// NewRegistry is the runtime half. Every consumer today hand-types the
// declaration list into Config.Compartments (the plugin is not necessarily
// in the path), so the check cannot live only at generation time.
func TestNewRegistryRejectsMalformedNames(t *testing.T) {
	_, err := policy.NewRegistry([]*toolv1.Decl{
		{Name: "financial"}, {Name: "PII-contact"},
	})
	if err == nil {
		t.Fatal("NewRegistry accepted a name differing from another only by case")
	}
	if !strings.Contains(err.Error(), "PII-contact") {
		t.Errorf("error does not name the offender: %v", err)
	}
}
