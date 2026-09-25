// Package wire is how a route becomes a NATS address.
//
// It exists because both sides of the hop were deriving these independently.
// garmd had a Subject function and garmtool had an identical one, in
// different repositories, with nothing keeping them identical — and if they
// ever diverged the symptom would be a call that goes nowhere, on a subject
// one side publishes to and the other never subscribed to.
//
// That is the same class of drift the descriptor hash exists to catch,
// except the hash does not cover it: two sides can agree perfectly about a
// message's shape while disagreeing about where to send it. A shared
// implementation is cheaper than another detector.
//
// These are pure functions over strings. Nothing here imports protobuf,
// NATS, or anything else — it is a naming convention, written once.
package wire

import "strings"

// Subject is where a route is reachable: the full method with its separators
// swapped for dots.
//
// The same string in two syntaxes, so neither side keeps a mapping table that
// could disagree with the other's.
//
//	/calc.v1.Calculator/Add  ->  calc.v1.Calculator.Add
func Subject(fullMethod string) string {
	return strings.ReplaceAll(strings.TrimPrefix(fullMethod, "/"), "/", ".")
}

// QueueGroup is what replicas of one service share, so NATS balances across
// instances of that service and nothing else.
//
// The proto service name:
//
//	/calc.v1.Calculator/Add  ->  calc.v1.Calculator
func QueueGroup(fullMethod string) string {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// MicroServiceName is a proto service name in the charset NATS micro allows,
// which excludes the dot.
//
//	calc.v1.Calculator  ->  calc_v1_Calculator
func MicroServiceName(service string) string {
	return strings.ReplaceAll(service, ".", "_")
}

// EndpointName is what $SRV.INFO shows for one route.
//
// Derived from the whole subject rather than the method, because two proto
// services in one process would otherwise both offer an endpoint called
// "Get" — and it is the SECOND registration that fails, long after the first
// looked fine.
func EndpointName(fullMethod string) string {
	return MicroServiceName(Subject(fullMethod))
}

// Where a record goes.
//
// Two streams, not one. The ledger degrades and the audit stream may refuse a
// call, so they differ in retention, in replication, and above all in what
// happens when they fill: an audit stream must refuse new writes rather than
// discard old ones, and a ledger must do the opposite. Sharing a stream would
// mean choosing one of those policies for both, and would let metering volume
// evict the records someone is legally obliged to keep.
//
// The prefixes are fixed rather than configurable. A configurable prefix is a
// way for a publisher and its forwarder to disagree silently — the publisher
// succeeds, the stream never sees the subject, and nothing reports an error
// because publishing to a subject nobody consumes is not an error. Isolating
// environments is what NATS accounts are for.
const (
	LedgerStream  = "GARM_LEDGER"
	LedgerSubject = "garm.v1.ledger"

	AuditStream  = "GARM_AUDIT"
	AuditSubject = "garm.v1.audit"
)

// LedgerSubjectFor and AuditSubjectFor place a record under its tenant and
// app, so a consumer can filter to one without reading the rest and a stream
// can be sharded by tenant later without moving anything.
func LedgerSubjectFor(tenant, app string) string {
	return LedgerSubject + "." + token(tenant) + "." + token(app)
}

func AuditSubjectFor(tenant, app string) string {
	return AuditSubject + "." + token(tenant) + "." + token(app)
}

// token makes an arbitrary string safe as ONE subject element.
//
// A tenant called "acme.corp" would otherwise silently become two elements,
// so `garm.v1.audit.acme.corp.*` would match where the consumer expected
// `garm.v1.audit.<tenant>.<app>` — records landing in a place no filter looks.
// The wildcards are worse: a tenant named ">" subscribes to everything.
//
// Empty becomes "_" because a subject may not contain an empty element, and a
// record with no tenant still has to land somewhere.
func token(s string) string {
	if s == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '*', '>', ' ', '\t', '\n', '\r':
			return '_'
		}
		return r
	}, s)
}
