// Package toolbind is the seam between generated tool bindings and whatever
// runs them.
//
// Generated code registers against Registrar rather than importing a
// concrete runtime. That inversion is what keeps the contracts module thin:
// a tool author who wants only the message types does not pull in a NATS
// client, and contracts cannot import the package that enforces the chain.
package toolbind

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"
)

// ToolRef is one tool's identity, as the generator saw it.
type ToolRef struct {
	// FQN is the globally-unique tool identity, e.g.
	// "garm.demo.v1beta1.get_account_summary".
	FQN string
	// Subject is the NATS subject, derived from the fully-qualified method.
	// Proto names and NATS subjects are both dot-separated, so there is
	// nothing to configure and nothing to drift.
	Subject string
	// Method is the bare method name, which is also the micro endpoint name.
	Method string
	// Service is the fully-qualified proto service name; it is also the
	// endpoint queue group verbatim. It is NOT the micro service name — see
	// MicroServiceName, below, for why that needs a narrower derivation.
	Service string
	// ContractVersion and DescriptorHash are the generated package's own
	// contract identity (its ContractVersion/DescriptorHash constants),
	// carried on every ToolRef in that package so a Registrar can learn
	// them from the calls it already receives via Endpoint, without a
	// second parameter threaded through ServeX. §7.1's drift detection
	// has nothing to compare without these.
	ContractVersion string
	DescriptorHash  string
}

// Invoke is one tool call at the untyped boundary.
type Invoke func(ctx context.Context, req proto.Message) (proto.Message, error)

// Registrar accepts one endpoint. A runtime implements it; generated code
// calls it once per tool.
type Registrar interface {
	Endpoint(ref ToolRef, newReq func() proto.Message, fn Invoke) error
}

// MicroServiceName derives the NATS micro service Name for a
// fully-qualified proto service (ToolRef.Service, e.g.
// "garm.demo.v1beta1.AccountsService") — the identity
// micro.Config.Name/EndpointConfig's own name require, which is a
// NARROWER charset than a NATS subject: github.com/nats-io/nats.go/micro
// validates a service Name against `^[A-Za-z0-9\-_]+$` (its unexported
// nameRegexp, service.go), which a dotted proto package name never
// satisfies. Registering a tool service without this returns, at the
// first Endpoint call, "validation: service name: name should not be
// empty and should consist of alphanumerical characters, dashes and
// underscores" — a real defect this function exists to close (found while
// building garm's $SRV.INFO reconciliation: no test in this build had ever
// dialed a real broker on this path).
//
// This does NOT change the wire subject (ToolRef.Subject stays the dotted,
// fully-qualified method — subjects are dot-separated by design, and that
// is what makes them derivable rather than configured) or the queue group
// (ToolRef.Service is passed to QueueGroup verbatim: micro validates a
// queue group against ITS OWN, more permissive charset — service.go's
// subjectRegexp, `^[^ >]*[>]?$` — which allows dots). Sanitizing those too
// would be solving a problem that regex does not have.
//
// Both sides of the NATS hop must derive the SAME name from the SAME
// service string — garmtool registers under it
// (garmtool/runtime.go's getOrCreateMicroService), and garm's $SRV.INFO
// reconciliation queries it (toolplane/svcwatch.go's
// queryServiceEndpoints) — so the transform lives here, in the one
// package both already import, rather than being reimplemented on each
// side where it could silently drift.
//
// The substitution is deliberately simple (every "." becomes "_") and
// deliberately not collision-proof: two distinct proto services whose
// fully-qualified names differ only in whether a boundary is "." or "_"
// (a shape lint rule L3's charset does not forbid within one path segment)
// would collide here. Proto package and service names in practice never
// do this, and closing that residual gap costs a hash or a second field
// nothing here currently carries the budget for.
func MicroServiceName(service string) string {
	return strings.ReplaceAll(service, ".", "_")
}
