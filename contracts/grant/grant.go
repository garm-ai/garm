// Package grant computes what a human approved.
//
// A grant binds an approval to the values a person actually saw. That only
// works if the service that MINTS the grant and the daemon that VERIFIES it
// agree byte for byte on how those values become a digest — and if they ever
// disagree, every approval is refused, or worse, one matches a request the
// approver never saw. Neither side can detect that alone.
//
// So this is one implementation, in the contracts module both sides already
// import, for the same reason wire.Subject is: two independent derivations of
// the same string is a divergence waiting to happen, and a shared function is
// cheaper than a detector.
//
// It is deliberately NOT a protobuf encoding. Material fields resolve to
// scalar leaves, so the digest is over text — no field ordering, no
// absent-versus-default, no float representation, nothing that needs a
// canonical form agreed across four languages. Anything that would need one
// is refused by the linter instead.
package grant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Unset is what an absent field contributes.
//
// A value rather than an omission, so approving a request with an amount and
// sending one without produces different digests. It is angle-bracketed
// because a scalar's text form never is, which keeps it from colliding with a
// field whose value happens to be the string "unset".
const Unset = "<unset>"

// Digest is the value a grant carries and a verifier recomputes.
//
// Paths are sorted, so the order they appear in the annotation cannot change
// the digest — an author reordering material_fields for readability must not
// invalidate every outstanding approval.
//
// The encoding is deliberately boring: `path=value`, one per line. Values are
// the scalar's canonical text form, which for every type this admits is
// unambiguous and identical in every language. A separator collision is
// impossible because a path cannot contain `=` or a newline (the linter
// enforces the shape) and a value's newlines are escaped below.
func Digest(values map[string]string) string {
	paths := make([]string, 0, len(values))
	for p := range values {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte('=')
		b.WriteString(escape(values[p]))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// escape keeps a value from spanning lines.
//
// Without it a string field containing "\nbeneficiary_iban=" could forge the
// appearance of another field, and two different requests would digest the
// same. That is the whole attack on a line-oriented encoding, and it costs two
// replacements to remove.
func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return strings.ReplaceAll(v, "\r", `\r`)
}

// ValidPath reports whether a material field path is well-formed.
//
// Shape only — whether it resolves to a scalar leaf of a real message is a
// question for the compiler, which has the descriptors. This is the half both
// the linter and a runtime can check without them.
func ValidPath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.ContainsAny(p, "=\n\r") {
		// Would let a path forge a separator in the digest.
		return fmt.Errorf("path %q contains a separator character", p)
	}
	for _, seg := range strings.Split(p, ".") {
		if seg == "" {
			return fmt.Errorf("path %q has an empty segment", p)
		}
	}
	return nil
}
