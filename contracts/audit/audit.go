// Package audit is the durable, separately-retained half of the record.
//
// It exists because one interface could not be both halves. ledger.Recorder
// is documented as "must not fail the call" — every call leaves a row, and a
// recorder having a bad day must never be the reason a request fails. That is
// the right contract for the ledger and the wrong one for an audit trail: a
// tool declaring `audit: { fail_closed: true }` is saying the opposite, that
// the call must not proceed unless it was recorded.
//
// Those cannot be the same interface. A Recorder that could refuse would make
// every ordinary call fragile; an audit stream that could not refuse would
// make the strongest promise in the annotation undeliverable. So the ledger
// keeps its contract unchanged and this is a second seam, with the opposite
// guarantee.
//
// The split is not invented here — the spec already separates the ledger from
// the audit stream, and `audit.level` names LEVEL_LEDGER and LEVEL_AUDIT as
// different things. This is that distinction reaching the type system.
package audit

import (
	"context"
	"time"

	"github.com/garm-ai/garm/contracts/ledger"
)

// Sink is where an audited call is written.
//
// Implementations are expected to be durable and to survive the process. A
// Sink that buffers in memory and returns nil has answered the one question
// this interface exists to ask, incorrectly.
type Sink interface {
	// Write records ev, and its error FAILS THE CALL when the tool declared
	// fail_closed.
	//
	// That is the whole point of this seam, and it means Write is on the
	// request path: it is called BEFORE the tool runs, with the intent
	// (ev.Outcome == ledger.OutcomeIntent), and again after with what
	// happened.
	//
	// Write-ahead rather than write-after, because the alternative does not
	// work for anything irreversible. Recording after the fact and failing
	// the response when the record failed tells the caller the payment did
	// not happen, when it did. Recording the intent first and refusing means
	// the side effect never occurs — which is what "fail closed" has to mean
	// for a tool whose effects cannot be undone.
	//
	// The second write cannot fail closed, because by then the tool has run.
	// It must still be loud: an outcome that was never recorded leaves a
	// trail claiming a call was started and never finished.
	Write(ctx context.Context, ev ledger.Event) error

	// Retention is how long this sink keeps what it is given.
	//
	// Declared rather than assumed, so a tool asking for `retain_days: 2555`
	// can be refused at mount by a sink configured for thirty. Otherwise the
	// promise is discovered to be false seven years late, by whoever went
	// looking for the row.
	//
	// Zero means indefinite. A sink that does not know its own retention
	// should not report zero — it should report the shortest interval it can
	// guarantee, because this value is used to refuse.
	Retention() time.Duration
}
