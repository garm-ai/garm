package callctx_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/garm-ai/garm/contracts/callctx"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

func sample() *toolv1.InvocationContext {
	return &toolv1.InvocationContext{
		Attribution: &toolv1.CallContext{
			Tenant:        "acme",
			App:           "support-bot",
			CorrelationId: "c1",
			CausationId:   "g1",
			Tags:          map[string]string{"env": "prod"},
		},
		Principal:       &toolv1.InvocationPrincipal{Subject: "agent:support-bot", Kind: toolv1.PrincipalKind_PRINCIPAL_KIND_AGENT},
		Scopes:          []string{"partner:read"},
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Deadline:        timestamppb.New(time.Unix(1790000000, 0)),
		CallId:          "t1",
		ContractVersion: "contracts/v1.5.0",
	}
}

func TestEncodeDecodeRoundTrips(t *testing.T) {
	in := sample()
	s, err := callctx.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := callctx.Decode(s)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.GetCallId() != "t1" {
		t.Errorf("call_id = %q, want %q", out.GetCallId(), "t1")
	}
	if out.GetAttribution().GetCorrelationId() != "c1" {
		t.Errorf("correlation_id = %q, want %q", out.GetAttribution().GetCorrelationId(), "c1")
	}
	if out.GetAttribution().GetCausationId() != "g1" {
		t.Errorf("causation_id = %q, want %q", out.GetAttribution().GetCausationId(), "g1")
	}
	if got := out.GetDeadline().AsTime().Unix(); got != 1790000000 {
		t.Errorf("deadline = %d, want 1790000000", got)
	}
}

// TestDecodeRejectsGarbage: a header a caller controls must never panic or
// yield a half-built context the chain then trusts. All three inputs here
// are rejected before Decode ever looks at call_id: "" by the empty-header
// check, "!!!not base64!!!" by base64 decoding, and "aGVsbG8=" (valid
// base64, but not a valid wire-format message) by proto.Unmarshal itself.
// See TestDecodeRejectsMissingCallID for the case that actually exercises
// the call_id check.
func TestDecodeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "!!!not base64!!!", "aGVsbG8="} {
		if _, err := callctx.Decode(s); err == nil {
			t.Errorf("Decode(%q) succeeded; want an error", s)
		}
	}
}

// TestDecodeRejectsMissingCallID exercises the call_id check specifically:
// a WELL-FORMED InvocationContext with call_id unset marshals and
// base64-encodes cleanly, and proto.Unmarshal accepts it without error, so
// only the explicit call_id check in Decode can reject it. garm always sets
// call_id; its absence means the message did not come from garm (or came
// from a broken one), and nothing downstream can ledger, cancel or
// deduplicate a call it cannot name.
func TestDecodeRejectsMissingCallID(t *testing.T) {
	ic := sample()
	ic.CallId = ""
	b, err := proto.Marshal(ic)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	s := base64.StdEncoding.EncodeToString(b)
	if _, err := callctx.Decode(s); err == nil {
		t.Errorf("Decode(%q) succeeded for an InvocationContext with no call_id; want an error", s)
	}
}

func TestFromContextReturnsNilWhenAbsent(t *testing.T) {
	if got := callctx.FromContext(context.Background()); got != nil {
		t.Fatalf("FromContext(empty) = %v, want nil", got)
	}
}

func TestNewContextRoundTrips(t *testing.T) {
	ctx := callctx.NewContext(context.Background(), sample())
	got := callctx.FromContext(ctx)
	if got == nil {
		t.Fatal("FromContext returned nil after NewContext")
	}
	if got.GetCallId() != "t1" {
		t.Errorf("call_id = %q, want %q", got.GetCallId(), "t1")
	}
}

// TestInvocationContextCarriesNoClearance is a design guard, not a behaviour
// test. Clearance and compartments must never travel to a tool: sending them
// is an invitation to write the second, unreviewed copy of the policy that
// the spec refuses in three separate places (spec §4.4).
//
// It walks every message type reachable from InvocationContext — attribution
// (CallContext), principal (InvocationPrincipal), act (Act), and whatever
// those in turn reach — not just InvocationContext's own top-level fields.
// The realistic way this constraint gets violated is someone extending a
// nested message like InvocationPrincipal, not InvocationContext itself, so
// a guard that only checked the top level would pass vacuously on exactly
// the case that matters.
//
// This is still a name-based check: a field carrying the same information
// but spelled, say, "level" or "access_class" would pass undetected. That
// residual gap is accepted, not closed, by this test.
func TestInvocationContextCarriesNoClearance(t *testing.T) {
	visited := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if visited[md.FullName()] {
			return
		}
		visited[md.FullName()] = true

		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			switch name := string(f.Name()); name {
			case "clearance", "compartments":
				t.Fatalf("%s declares %q. The tool returns the full message and garm "+
					"redacts; a tool that can see clearance is a tool that will filter "+
					"(spec 4.4).", md.FullName(), name)
			}
			if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
				walk(f.Message())
			}
		}
	}
	walk(sample().ProtoReflect().Descriptor())
}

// ServiceHealth.gauges must stay numeric. A free-form string map on the tool
// side is a place a classified value lands, and it flows to a bucket, a
// stream and a dashboard through a path none of this design's protections
// reach: toolpolicy runs in garm on responses, and the slog handler elides
// protos in logs. Neither sees a health value. A float64 cannot carry a
// customer identifier; a string can (spec §3.6).
func TestServiceHealthGaugesAreNumeric(t *testing.T) {
	fields := (&toolv1.ServiceHealth{}).ProtoReflect().Descriptor().Fields()
	f := fields.ByName("gauges")
	if f == nil {
		t.Fatal("ServiceHealth declares no gauges field")
	}
	if !f.IsMap() {
		t.Fatalf("gauges is %s, want a map", f.Kind())
	}
	if got := f.MapValue().Kind(); got != protoreflect.DoubleKind {
		t.Fatalf("gauges value kind = %s, want double. A string map would let a tool "+
			"publish a customer identifier to the health bucket, the transitions stream "+
			"and a dashboard, through a path redaction never reaches (spec 3.6).", got)
	}
}
