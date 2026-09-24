package policy

import (
	"fmt"
	"strings"
)

// maxDeclNameLen bounds a declared name. It is the DNS-label length, chosen
// because these names travel into JWT claims and ledger columns, and a
// stated limit is better than whichever downstream system truncates first.
const maxDeclNameLen = 63

// ValidateDeclName enforces the format of a declared compartment or tool set
// name: `[a-z][a-z0-9]*(-[a-z0-9]+)*`.
//
// The format is narrow because one of these names becomes four things at
// once — a Go constant, a JWT claim value, a ledger field, and a BIT
// POSITION assigned by sorting the whole name set — and each of the
// exclusions below is a failure mode rather than a style preference:
//
//   - UPPERCASE is rejected because two names differing only in case
//     generate two valid, distinct Go constants and receive two distinct
//     bits, and nothing downstream notices. Worse, Registry.SetLenient
//     silently DROPS a name it does not know, so an IdP minting
//     `PII-contact` against a declared `pii-contact` loses that compartment
//     with no error at the call site — only a line in the ledger.
//
//   - `_` is rejected because the code generator treats `-` and `_`
//     identically as word separators and drops both, so `pii-contact` and
//     `pii_contact` are two different compartments, with two different
//     bits, that generate the SAME Go constant. Permitting both means a
//     taxonomy that cannot compile.
//
//   - `.` and everything else is rejected because it is not a separator to
//     the generator, so `pii.contact` yields `Pii.contact`: not a valid Go
//     identifier. The failure then surfaces as generated code that will not
//     parse, a long way from the declaration that caused it.
//
//   - A leading digit is rejected for the same reason: `2fa` is not a valid
//     Go identifier.
//
// Hierarchy, where it is wanted, is spelled with the hyphen — `pii-gov-id`.
func ValidateDeclName(name string) error {
	if name == "" {
		return fmt.Errorf("declared name is empty")
	}
	if len(name) > maxDeclNameLen {
		return fmt.Errorf("declared name %q is %d characters; the maximum is %d",
			name, len(name), maxDeclNameLen)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("declared name %q must be lower-case: two names "+
			"differing only in case become two distinct compartments, and a "+
			"caller presenting the wrong case is dropped silently", name)
	}

	for _, seg := range strings.Split(name, "-") {
		if seg == "" {
			return fmt.Errorf("declared name %q has an empty segment: no "+
				"leading, trailing or repeated %q", name, "-")
		}
		for i, r := range seg {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= '0' && r <= '9':
				// A digit may not start the whole name, because the
				// generated Go identifier would not be valid.
				if i == 0 && strings.HasPrefix(name, seg) {
					return fmt.Errorf("declared name %q must start with a "+
						"letter (it becomes a Go identifier)", name)
				}
			default:
				return fmt.Errorf("declared name %q contains %q; only a-z, "+
					"0-9 and %q are allowed (%q and %q collide with the "+
					"generated Go identifier)", name, string(r), "-", "_", ".")
			}
		}
	}
	return nil
}
