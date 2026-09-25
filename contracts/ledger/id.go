package ledger

import (
	"crypto/rand"
	"encoding/hex"
)

// NewEventID returns an idempotency key for one event.
//
// 128 bits from crypto/rand, hex encoded. Not a UUID, because a UUID library
// is a dependency for a string that only has to be unique — and this package
// is imported by tool services, where every dependency is one they inherit
// without asking.
//
// It does not fail. crypto/rand.Read is documented never to return an error
// on any supported platform, and the alternative — a recorder that refuses to
// record because the entropy source hiccuped — is worse than the problem.
func NewEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
