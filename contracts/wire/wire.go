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
