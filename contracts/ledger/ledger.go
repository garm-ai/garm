// Package ledger is the shape of a governed call's record.
//
// One Event for every plane. A tool call fills the principal and clearance
// fields and leaves the model ones empty; a generation call does the reverse.
// That is deliberate and it is the reason this lives in the contract rather
// than in either plane: two event schemas would mean two answers to "what
// happened", and reconciling them afterwards is the work nobody budgets for.
//
// The RECORDERS are not here. Where an event goes — a log, a stream, a lake —
// is a deployment's business and differs per plane. What it looks like is
// not.
//
// Usage and ErrorKind came from the provider package, which is the wrong home
// for them: they are ledger vocabulary that happens to be produced by a model
// call, and putting them there meant the tool plane could not record an event
// without linking provider adapters it will never use.
package ledger

import (
	"context"
	"time"
)

// Usage is what a model call consumed. Zero for a tool call.
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
}

// ErrorKind classifies a failure. The values are the provider package's, kept
// as a plain string type so that recording one does not require linking it.
type ErrorKind string

type Outcome string

const (
	OutcomeOK          Outcome = "ok"
	OutcomeError       Outcome = "error"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeDenied      Outcome = "denied" // blocked by policy under mode: enforce; no provider call
)

type Event struct {
	Time time.Time

	Tenant        string
	App           string
	Feature       string
	RunID         string
	CorrelationID string
	CausationID   string
	BudgetID      string
	Tags          map[string]string

	PromptName string // "raw" for message-path calls
	PromptHash string

	Alias           string
	ResolvedModel   string
	ModelOverridden bool

	Usage      Usage
	CostUSD    float64
	CostSource string // "computed" | "" when no price known
	LatencyMS  int64

	ProviderRequestID string
	FallbackUsed      bool
	Outcome           Outcome
	ErrorKind         string // ErrorKind value when Outcome == "error"

	PolicyMode       string
	PolicyViolations []string

	// Tool attribution (tools spec §9). Paths and plan hashes only — a
	// redacted value must never reach the ledger.
	Tool             string
	PrincipalSubject string
	PrincipalActor   string

	// PrincipalKind is what the subject is — user, agent or service — as a
	// PrincipalKind name, or empty when the token did not say.
	//
	// It is here and not in the policy chain on purpose. "Which of these
	// rows were agents" is a question people ask of a ledger constantly and
	// could not previously answer; it is not a question the chain should be
	// able to ask, because a fifth thing that can deny a call means two
	// places to look when one is refused.
	PrincipalKind         string
	ChainDepth            int
	ClearanceEffective    string
	CompartmentsEffective []string
	RedactionPlan         string
	RedactionCount        int

	// DisclosedCount is how many audit_on_read fields this caller DID see
	// (tools spec §10.5). The sanitizer already knows which fields passed,
	// so recording the disclosed subset costs nothing extra — and without
	// it, audit_on_read is an annotation the build enforces and the runtime
	// computes for nobody.
	DisclosedCount int

	// ErrorDetail is the pre-scrub error text for a failed call, and the
	// only field here that may carry unsanitized free text.
	//
	// Spec §5.1: the error path is a leak channel. A resolver error
	// routinely interpolates the value it was protecting ("user with email
	// ada@corp.com not found") and the response sanitizer never sees it.
	// toolplane.ScrubError reduces what reaches the wire to a code and a
	// static message; the detail lands HERE so it is not simply destroyed.
	// Destroying it is not neutral — it is the pressure that makes someone
	// add an slog.Error(..., "err", err) inside a handler and leak exactly
	// what the scrubbing prevented.
	//
	// Recorders and lake consumers must hold this at the retention and
	// access grade of the most sensitive field in the registry.
	ErrorDetail string
}

type Recorder interface {
	// Record must not fail the call: implementations swallow their own
	// errors (logging them) and must be safe under a cancelled ctx.
	Record(ctx context.Context, ev Event)
}
