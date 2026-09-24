package redact_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy/redact"
)

func str(s string) protoreflect.Value { return protoreflect.ValueOfString(s) }

func TestApplyOmitVariants(t *testing.T) {
	for name, r := range map[string]*toolv1.Redaction{
		"explicit omit": {Kind: &toolv1.Redaction_Omit{Omit: &toolv1.Omit{}}},
		"nil redaction": nil,
		"empty oneof":   {},
		"unknown custom": {Kind: &toolv1.Redaction_Custom{
			Custom: &toolv1.Custom{Name: "nope"}}},
	} {
		got, omit := redact.Apply(redact.Ctx{}, r, protoreflect.StringKind, str("secret"))
		if !omit {
			t.Fatalf("%s: expected omit, got a value", name)
		}
		if got.IsValid() {
			t.Fatalf("%s: expected zero value on omit, got %v", name, got)
		}
	}
}

func TestMaskAndHash(t *testing.T) {
	got, omit := redact.Apply(redact.Ctx{},
		&toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
		protoreflect.StringKind, str("ada@corp.com"))
	if omit || got.String() != "[REDACTED]" {
		t.Fatalf("mask = %q omit=%v", got.String(), omit)
	}

	r := &toolv1.Redaction{Kind: &toolv1.Redaction_Hash{Hash: &toolv1.Hash{}}}
	ctx := redact.Ctx{Key: []byte("k1")}
	a, _ := redact.Apply(ctx, r, protoreflect.StringKind, str("ada@corp.com"))
	b, _ := redact.Apply(ctx, r, protoreflect.StringKind, str("ada@corp.com"))
	c, _ := redact.Apply(ctx, r, protoreflect.StringKind, str("bob@corp.com"))
	if a.String() != b.String() {
		t.Fatal("hash is not deterministic; pseudonyms would not join")
	}
	if a.String() == c.String() {
		t.Fatal("hash collides on distinct inputs")
	}
	if a.String() == "ada@corp.com" {
		t.Fatal("hash returned the plaintext")
	}
}

func TestPerTenantSaltSeparatesTenants(t *testing.T) {
	r := &toolv1.Redaction{Kind: &toolv1.Redaction_Hash{
		Hash: &toolv1.Hash{PerTenantSalt: true}}}
	a, _ := redact.Apply(redact.Ctx{Key: []byte("k1"), Tenant: "acme"}, r, protoreflect.StringKind, str("x"))
	b, _ := redact.Apply(redact.Ctx{Key: []byte("k1"), Tenant: "globex"}, r, protoreflect.StringKind, str("x"))
	if a.String() == b.String() {
		t.Fatal("per-tenant salt did not separate tenants")
	}
}

func TestTransformerErrorOmits(t *testing.T) {
	redact.Register(failing{})
	r := &toolv1.Redaction{Kind: &toolv1.Redaction_Custom{
		Custom: &toolv1.Custom{Name: "failing"}}}
	got, omit := redact.Apply(redact.Ctx{}, r, protoreflect.StringKind, str("sensitive"))
	if !omit {
		t.Fatalf("failing transformer returned %q instead of omitting", got.String())
	}
}

// TestApplyOmitsOnKindMismatch guards the defence-in-depth check added to
// Apply: a schema that pairs a transformer with a field kind it was never
// built for (e.g. keep_last, a string-only transformer, on an int64 field)
// must omit rather than reach Transform. Transform would return a string
// Value for an int64 field, and the caller's protoreflect.Message.Set would
// panic on that type mismatch — the build-time lint that should catch this
// schema error does not exist yet, so this is the last line of defence in
// the serving path itself.
func TestApplyOmitsOnKindMismatch(t *testing.T) {
	r := &toolv1.Redaction{Kind: &toolv1.Redaction_KeepLast{
		KeepLast: &toolv1.KeepLast{N: 4}}}
	got, omit := redact.Apply(redact.Ctx{}, r, protoreflect.Int64Kind, protoreflect.ValueOfInt64(123456))
	if !omit {
		t.Fatalf("expected omit for a kind mismatch, got a value: %v", got)
	}
	if got.IsValid() {
		t.Fatalf("expected the zero Value on a kind mismatch, got %v", got)
	}
}

type failing struct{}

func (failing) Name() string                 { return "failing" }
func (failing) Accepts() []protoreflect.Kind { return []protoreflect.Kind{protoreflect.StringKind} }
func (failing) Transform(redact.Ctx, protoreflect.Value) (protoreflect.Value, error) {
	return protoreflect.Value{}, errBoom
}

var errBoom = errTest("boom")

type errTest string

func (e errTest) Error() string { return string(e) }
