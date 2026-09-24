// Package contracts is the root of garm's generated tool contracts.
//
// It is a separate Go module on purpose (spec §1.2). A tool service depends
// on this module and on garmtool, and on nothing else of garm's: the
// boundary is what stops a tool importing toolplane and attempting the
// governance chain locally — a second, unreviewed implementation of a policy
// that already ran before the request crossed NATS.
//
// It is not published to a registry. Consume it at a git tag:
//
//	go get github.com/garm-ai/garm/contracts@contracts/v1.5.0
package contracts
