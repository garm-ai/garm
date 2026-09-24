// Package callctx encodes the InvocationContext for the NATS hop.
//
// Encoding lives behind this package so that Plan 2 can move the context
// from a NATS header into SealedEnvelope — and bind its digest into the
// AEAD's additional data — by changing one file rather than every call site.
package callctx

import (
	"context"
	"encoding/base64"
	"fmt"

	"google.golang.org/protobuf/proto"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Header is the NATS header key the encoded context travels in.
const Header = "Garm-Invocation"

func Encode(ic *toolv1.InvocationContext) (string, error) {
	if ic == nil {
		return "", fmt.Errorf("callctx: refusing to encode a nil InvocationContext")
	}
	b, err := proto.Marshal(ic)
	if err != nil {
		return "", fmt.Errorf("callctx: marshalling: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Decode parses a header value. It is deliberately strict: this input is
// attacker-reachable if NATS subject permissions are ever misconfigured, so
// a malformed value must be an error the caller handles, never a half-built
// context the chain goes on to trust.
func Decode(s string) (*toolv1.InvocationContext, error) {
	if s == "" {
		return nil, fmt.Errorf("callctx: no %s header on the request", Header)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("callctx: %s is not base64: %w", Header, err)
	}
	var ic toolv1.InvocationContext
	if err := proto.Unmarshal(b, &ic); err != nil {
		return nil, fmt.Errorf("callctx: %s is not an InvocationContext: %w", Header, err)
	}
	if ic.GetCallId() == "" {
		return nil, fmt.Errorf("callctx: %s carries no call_id", Header)
	}
	return &ic, nil
}

type ctxKey struct{}

func NewContext(ctx context.Context, ic *toolv1.InvocationContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, ic)
}

// FromContext returns the context for this call, or nil. Returning nil rather
// than a zero value is deliberate: code that needs the call id must notice
// its absence rather than ledger an empty string.
func FromContext(ctx context.Context) *toolv1.InvocationContext {
	ic, _ := ctx.Value(ctxKey{}).(*toolv1.InvocationContext)
	return ic
}
