package callctx_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/garm-ai/garm/contracts/callctx"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// full is an invocation with every arm of the envelope populated, because a
// round trip that only carries a call id proves nothing about the parts the
// chain actually decides on.
func full() *toolv1.InvocationContext {
	return &toolv1.InvocationContext{
		Attribution: &toolv1.CallContext{
			Tenant:        "acme",
			App:           "support-copilot",
			Feature:       "refunds",
			RunId:         "run-7",
			CorrelationId: "corr-7",
			Tags:          map[string]string{"env": "prod"},
		},
		Principal:       &toolv1.InvocationPrincipal{Subject: "ada@acme.example", Kind: toolv1.PrincipalKind_PRINCIPAL_KIND_USER},
		Act:             []*toolv1.Act{{Subject: "agent-3", Kind: toolv1.PrincipalKind_PRINCIPAL_KIND_AGENT}},
		Scopes:          []string{"accounts:read"},
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:      "acme=t61rcWkgMzE",
		Deadline:        timestamppb.New(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)),
		CallId:          "call-1",
		IdempotencyKey:  "idem-1",
		ContractVersion: "acme.accounts.v1@3",
	}
}

// TestTheEnvelopeSurvivesTheHop is the whole job of the package: what the
// caller attached is what the far side decides on. A field dropped here is a
// governance decision made on less than the caller sent — a lost act chain is
// a delegation the daemon never sees, a lost deadline is a call with none.
func TestTheEnvelopeSurvivesTheHop(t *testing.T) {
	want := full()
	s, err := callctx.Encode(want)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	got, err := callctx.Decode(s)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !proto.Equal(want, got) {
		t.Errorf("the envelope changed across the hop.\nsent %v\ngot  %v", want, got)
	}
}

// TestTheHeaderValueIsHeaderSafe. NATS headers are text, so the encoding has
// to survive being written into one: raw proto bytes in a header are
// truncated at the first NUL or newline and arrive as a context that parses
// into something shorter than it was sent.
func TestTheHeaderValueIsHeaderSafe(t *testing.T) {
	s, err := callctx.Encode(full())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.ContainsAny(s, "\x00\r\n") {
		t.Errorf("encoded header value %q carries bytes a header cannot hold", s)
	}
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		t.Errorf("encoded header value is not base64: %v", err)
	}
}

// TestEncodingRefusesNil. Encoding nil would produce a valid, empty header
// that decodes into a context with no call id and no principal — an
// unattributable call that looks well-formed to everything downstream.
func TestEncodingRefusesNil(t *testing.T) {
	if s, err := callctx.Encode(nil); err == nil {
		t.Fatalf("encoded a nil InvocationContext as %q", s)
	}
}

// TestDecodeRefusesAnythingItCannotFullyTrust.
//
// This input is attacker-reachable the moment NATS subject permissions are
// misconfigured, so every rejection has to be an error the caller handles and
// never a partially populated context: the chain treats what it is handed as
// the caller's identity, and a half-built one is an identity nobody asserted.
func TestDecodeRefusesAnythingItCannotFullyTrust(t *testing.T) {
	// A well-formed InvocationContext that names no call: attributable to
	// nothing, so no ledger row can be tied back to it.
	noCallID, err := proto.Marshal(&toolv1.InvocationContext{
		Principal: &toolv1.InvocationPrincipal{Subject: "ada@acme.example"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, value string }{
		{"no header at all", ""},
		{"not base64", "not-base64!!"},
		{"base64 of bytes that are not a proto", base64.StdEncoding.EncodeToString([]byte{0xff, 0xff, 0xff})},
		{"a valid proto carrying no call id", base64.StdEncoding.EncodeToString(noCallID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ic, err := callctx.Decode(tc.value)
			if err == nil {
				t.Fatalf("decoded %q into %v", tc.value, ic)
			}
			if ic != nil {
				t.Errorf("returned a context alongside the error: %v", ic)
			}
		})
	}
}

// TestFromContextOnAnUnservedRequestIsSafeToRead.
//
// A handler runs outside a served request all the time — a test, a warm-up, a
// background reconcile — and that is not a bug. FromContext returns nil there
// rather than a zero value so that code needing the call id notices its
// absence, which only works because the accessors are nil-safe: if reading
// one panicked, every handler would need a nil check before it could log.
func TestFromContextOnAnUnservedRequestIsSafeToRead(t *testing.T) {
	ic := callctx.FromContext(context.Background())
	if ic != nil {
		t.Fatalf("FromContext on a context that never carried one returned %v", ic)
	}
	if ic.GetCallId() != "" || ic.GetPrincipal().GetSubject() != "" ||
		ic.GetAttribution().GetTenant() != "" || ic.GetDeadline() != nil ||
		len(ic.GetScopes()) != 0 || len(ic.GetAct()) != 0 {
		t.Error("the nil accessors returned something")
	}
}

// TestNewContextCarriesTheEnvelope, including past a derived context: the
// handler that reads it is several WithValue and WithCancel calls below the
// one that attached it.
func TestNewContextCarriesTheEnvelope(t *testing.T) {
	want := full()
	ctx, cancel := context.WithCancel(callctx.NewContext(context.Background(), want))
	defer cancel()
	if got := callctx.FromContext(ctx); got != want {
		t.Fatalf("FromContext = %v, want the attached envelope %v", got, want)
	}

	// An inner call attenuates by attaching its own; the outer one must not
	// leak back out when the inner scope is the one being served.
	inner := &toolv1.InvocationContext{CallId: "call-2"}
	if got := callctx.FromContext(callctx.NewContext(ctx, inner)); got != inner {
		t.Errorf("the inner envelope did not shadow the outer one: got %v", got)
	}
}
