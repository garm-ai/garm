package policy_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
	"github.com/garm-ai/garm/policy/redact"
	"github.com/garm-ai/garm/policy/testdata"
)

func fullProfile() *testdata.Profile {
	return &testdata.Profile{
		Id:         "u_8123",
		Locale:     "en-GB",
		Email:      proto.String("ada@corp.com"),
		NationalId: proto.String("NI-99-88-77"),
		Billing:    &testdata.Billing{CardLast4: "1111", Iban: "GB33BUKB20201555555555"},
		Tags:       map[string]string{"tier": "gold"},
	}
}

func TestSanitizeRedactsAndOmits(t *testing.T) {
	msg := fullProfile()
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	paths := policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if msg.GetEmail() != "***@corp.com" {
		t.Fatalf("email = %q, want the domain-only form", msg.GetEmail())
	}
	if msg.NationalId != nil {
		t.Fatalf("national_id present after omit: %q", msg.GetNationalId())
	}
	if msg.Billing != nil {
		t.Fatal("billing subtree survived a denied gate")
	}
	if msg.GetId() != "u_8123" || msg.GetLocale() != "en-GB" {
		t.Fatal("a permitted field was modified")
	}
	if len(paths) == 0 {
		t.Fatal("no redacted paths reported")
	}
}

func TestSanitizeNeverLeaksThroughTheSubtree(t *testing.T) {
	msg := fullProfile()
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})
	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	// The strongest assertion available: serialize and grep. A field reached by
	// a path the walker mishandles would still be in the bytes.
	blob, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"GB33BUKB20201555555555", "NI-99-88-77", "ada@corp.com"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("%q survived sanitization", secret)
		}
	}
}

func TestSanitizeWalksRepeatedMessages(t *testing.T) {
	resp := &testdata.SearchResponse{
		Total: 2,
		Profiles: []*testdata.Profile{
			{Id: "a", Email: proto.String("a@corp.com")},
			{Id: "b", Email: proto.String("b@corp.com")},
		},
	}
	plan := compilePlan(t, resp.ProtoReflect().Descriptor())
	r := policy.NewCache().Resolve(plan,
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	policy.Sanitize(resp, r, redact.Ctx{Key: []byte("k")})

	for i, p := range resp.GetProfiles() {
		if p.GetEmail() != "***@corp.com" {
			t.Fatalf("profiles[%d].email = %q; every element must be redacted", i, p.GetEmail())
		}
	}
}

// TestSanitizeRedactsMapValuesThroughRealPipeline exercises the map branch,
// which takes a different code path from lists and scalars, using a
// *Resolved obtained from the real Compile/Resolve pipeline rather than a
// hand-built Action — so a change to how Compile paths a map field would be
// caught here. The fixture's "attributes" field is a scalar-valued map with
// a non-omit (mask) redaction for exactly this purpose.
func TestSanitizeRedactsMapValuesThroughRealPipeline(t *testing.T) {
	msg := &testdata.Profile{
		Id:         "u1",
		Attributes: map[string]string{"tier": "gold", "region": "eu"},
	}
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if len(msg.Attributes) != 2 {
		t.Fatalf("mask redaction should retain every entry, got %v", msg.Attributes)
	}
	for k, v := range msg.Attributes {
		if v != "[REDACTED]" {
			t.Fatalf("attributes[%q] = %q, want the mask default", k, v)
		}
	}
}

// TestSanitizeRedactsMapValues is a unit-level companion to the pipeline
// test above: it builds a Resolved by hand with a mask transformer over the
// map field, pinning the map branch's behavior in isolation from Compile.
func TestSanitizeRedactsMapValues(t *testing.T) {
	msg := &testdata.Profile{
		Id:   "u1",
		Tags: map[string]string{"tier": "gold", "region": "eu"},
	}
	r := &policy.Resolved{
		Desc: msg.ProtoReflect().Descriptor(),
		Deny: []policy.Action{{
			Path:   []protoreflect.FieldNumber{6},
			Name:   "tags",
			OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}},
		}},
	}

	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if len(msg.Tags) == 0 {
		t.Fatal("map redaction dropped every entry; expected masked values to be retained")
	}
	for k, v := range msg.Tags {
		if v == "gold" || v == "eu" {
			t.Fatalf("tags[%q] = %q, original value survived redaction", k, v)
		}
	}
}

// TestSanitizeRedactsInsideMessageValuedMap proves Compile now recurses into
// a message-valued map's value type, and that Sanitize reaches fields nested
// inside it: the "billing_by_id" gate passes for this shape, its value's
// card_last4 passes on its own policy, but its iban still needs RESTRICTED
// and must be redacted without disturbing the rest of the entry.
func TestSanitizeRedactsInsideMessageValuedMap(t *testing.T) {
	msg := &testdata.Profile{
		Id: "u1",
		BillingById: map[string]*testdata.Billing{
			"acct1": {CardLast4: "1111", Iban: "GB33BUKB20201555555555"},
		},
	}
	reg := testRegistry(t)
	need, err := reg.Set([]string{"financial"})
	if err != nil {
		t.Fatal(err)
	}
	r := policy.NewCache().Resolve(compileProfile(t), policy.Shape{
		Clearance:    toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Compartments: need,
	})

	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	acct, ok := msg.BillingById["acct1"]
	if !ok {
		t.Fatal("billing_by_id gate should pass at CONFIDENTIAL+financial; the entry vanished")
	}
	if acct.GetCardLast4() != "1111" {
		t.Fatalf("card_last4 changed even though both the gate and its own policy pass: %q", acct.GetCardLast4())
	}
	if acct.GetIban() == "GB33BUKB20201555555555" {
		t.Fatal("iban inside a message-valued map value was not redacted; " +
			"Compile is not recursing into map values")
	}
}

// TestSanitizeClearsWholeMessageValuedMapOnDeniedGate proves a message-valued
// map is treated as a subtree gate: when the map field itself is denied, the
// whole map is cleared structurally, exactly like a non-map message field.
func TestSanitizeClearsWholeMessageValuedMapOnDeniedGate(t *testing.T) {
	msg := &testdata.Profile{
		Id: "u1",
		BillingById: map[string]*testdata.Billing{
			"acct1": {CardLast4: "1111", Iban: "GB33BUKB20201555555555"},
		},
	}
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if len(msg.BillingById) != 0 {
		t.Fatalf("billing_by_id survived a denied gate: %v", msg.BillingById)
	}
}

// TestSanitizeAbsentFieldsAreNoop proves the walker does not panic on a
// partially populated message and reports nothing redacted for fields that
// were never set in the first place.
func TestSanitizeAbsentFieldsAreNoop(t *testing.T) {
	msg := &testdata.Profile{Id: "u1", Locale: "en-GB"}
	// email, billing, national_id, billing_summary are all unset.
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	paths := policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if msg.Email != nil {
		t.Fatal("email became non-nil after sanitizing an absent field")
	}
	if msg.Billing != nil {
		t.Fatal("billing became non-nil after sanitizing an absent field")
	}
	if msg.NationalId != nil {
		t.Fatal("national_id became non-nil after sanitizing an absent field")
	}
	for _, p := range paths {
		if p == "email" || p == "billing" || p == "national_id" || p == "billing_summary" {
			t.Fatalf("reported %q as redacted, but the field was absent to begin with", p)
		}
	}
}

// TestSanitizeLeavesPermittedFieldsUntouched asserts byte-for-byte that a
// field the caller IS allowed to see comes out identical, including a
// nested field inside a subtree gate that itself passes.
func TestSanitizeLeavesPermittedFieldsUntouched(t *testing.T) {
	msg := fullProfile()
	orig := proto.Clone(msg).(*testdata.Profile)

	reg := testRegistry(t)
	need, err := reg.Set([]string{"financial", "pii-contact"})
	if err != nil {
		t.Fatal(err)
	}
	r := policy.NewCache().Resolve(compileProfile(t), policy.Shape{
		Clearance:    toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Compartments: need,
	})

	policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})

	if msg.GetId() != orig.GetId() {
		t.Fatalf("id changed: got %q want %q", msg.GetId(), orig.GetId())
	}
	if msg.GetLocale() != orig.GetLocale() {
		t.Fatalf("locale changed: got %q want %q", msg.GetLocale(), orig.GetLocale())
	}
	if msg.GetEmail() != orig.GetEmail() {
		t.Fatalf("email changed even though CONFIDENTIAL+pii-contact grants access: got %q want %q",
			msg.GetEmail(), orig.GetEmail())
	}
	if msg.Billing == nil {
		t.Fatal("billing subtree removed even though CONFIDENTIAL+financial grants access")
	}
	if msg.Billing.GetCardLast4() != orig.Billing.GetCardLast4() {
		t.Fatalf("billing.card_last4 changed: got %q want %q",
			msg.Billing.GetCardLast4(), orig.Billing.GetCardLast4())
	}
	// Sanity check that this shape actually exercises redaction rather than
	// passing everything: iban still needs RESTRICTED and must have changed.
	if msg.Billing.GetIban() == orig.Billing.GetIban() {
		t.Fatal("test setup invalid: iban should still be redacted at CONFIDENTIAL")
	}
}

// TestSanitizeDoesNotMutateResolved guards the constraint carried from Task
// 6's review: *Resolved is shared across every caller with the same shape,
// so Sanitize must never write through r.Deny or r.Disclosed, and must
// produce identical results on repeated calls against the same *Resolved.
func TestSanitizeDoesNotMutateResolved(t *testing.T) {
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	if len(r.Deny) == 0 {
		t.Fatal("test setup invalid: expected a non-empty Deny list")
	}
	wantLen := len(r.Deny)
	wantFirstName := r.Deny[0].Name
	wantHash := r.Hash

	msg1 := fullProfile()
	policy.Sanitize(msg1, r, redact.Ctx{Key: []byte("k")})

	if len(r.Deny) != wantLen {
		t.Fatalf("len(r.Deny) changed after Sanitize: got %d want %d", len(r.Deny), wantLen)
	}
	if r.Deny[0].Name != wantFirstName {
		t.Fatalf("r.Deny[0].Name changed after Sanitize: got %q want %q", r.Deny[0].Name, wantFirstName)
	}
	if r.Hash != wantHash {
		t.Fatalf("r.Hash changed after Sanitize: got %q want %q", r.Hash, wantHash)
	}

	msg2 := fullProfile()
	policy.Sanitize(msg2, r, redact.Ctx{Key: []byte("k")})

	if !proto.Equal(msg1, msg2) {
		t.Fatalf("two sanitizations of identical inputs against the same *Resolved diverged:\n%v\nvs\n%v", msg1, msg2)
	}
}

// TestSanitizeConcurrentSharingResolved runs many goroutines, each with its
// own message, all sanitizing against one shared *Resolved from the cache.
// Run with -race: a first call that mutated r.Deny in place would corrupt
// policy for every concurrent caller of that shape.
func TestSanitizeConcurrentSharingResolved(t *testing.T) {
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan string, n*3)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := fullProfile()
			policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")})
			if msg.GetEmail() != "***@corp.com" {
				errs <- fmt.Sprintf("email = %q, want domain-only form", msg.GetEmail())
			}
			if msg.Billing != nil {
				errs <- "billing subtree survived a denied gate"
			}
			if msg.NationalId != nil {
				errs <- "national_id present after omit"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestSanitizeRefusesMismatchedDescriptor guards against pairing a message
// with a *Resolved computed for a different message type: without this
// check, Sanitize would redact whatever field happens to sit at the same
// field numbers in the wrong type.
//
// It must PANIC, not return nil. Sanitize's only signal is the list of
// paths it changed, so a silent nil is indistinguishable from "matched
// fine, nothing needed redacting" — a caller would ship the unredacted
// message and every log line would say the call was clean. The interceptor
// used to compensate with a guardedSanitize wrapper, which fixed one call
// site and left the primitive fail-open for the next; Plan B's projection
// and Plan D's descriptor loading both call Sanitize directly.
func TestSanitizeRefusesMismatchedDescriptor(t *testing.T) {
	r := policy.NewCache().Resolve(compileProfile(t),
		policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})

	wrong := &testdata.Billing{CardLast4: "1111", Iban: "GB33BUKB20201555555555"}

	func() {
		defer func() {
			rec := recover()
			if rec == nil {
				t.Fatal("Sanitize accepted a Resolved for a different message type; " +
					"a silent no-op here is indistinguishable from a clean call, so the " +
					"caller would return unredacted data believing it was sanitized")
			}
			msg, _ := rec.(string)
			// The panic has to name BOTH types: an operator reading a stack
			// trace needs to know which pairing went wrong, not merely that
			// one did.
			if !strings.Contains(msg, "policy.testdata.Profile") ||
				!strings.Contains(msg, "policy.testdata.Billing") {
				t.Fatalf("panic does not name both descriptors: %v", rec)
			}
		}()
		policy.Sanitize(wrong, r, redact.Ctx{Key: []byte("k")})
	}()

	if wrong.GetIban() != "GB33BUKB20201555555555" {
		t.Fatal("a Resolved for a different message type mutated this message")
	}
}

// TestSanitizeRefusesMismatchedDescriptorEvenWithAnEmptyDenyList pins the
// ORDER of the two early checks. The descriptor guard has to run before the
// len(r.Deny) == 0 shortcut: a mispaired Resolved that happens to deny
// nothing is still mispaired, and checking Deny first would make the guard's
// coverage depend on the caller's clearance — the higher the clearance, the
// emptier Deny, the likelier a mismatch slips through unseen.
func TestSanitizeRefusesMismatchedDescriptorEvenWithAnEmptyDenyList(t *testing.T) {
	// A Resolved for Profile with nothing denied — what a fully-cleared
	// principal gets.
	r := &policy.Resolved{Desc: (&testdata.Profile{}).ProtoReflect().Descriptor()}

	defer func() {
		if recover() == nil {
			t.Fatal("an empty deny list let a mismatched descriptor through")
		}
	}()
	policy.Sanitize(&testdata.Billing{}, r, redact.Ctx{Key: []byte("k")})
}

// TestSanitizeSkipsZeroLengthPath guards the panic in "a.Path[:len(a.Path)-1]"
// for a zero-length Path. Compile never produces one, but Action is exported
// with exported fields, and a hand-built Action (as in this package's own
// tests) can.
func TestSanitizeSkipsZeroLengthPath(t *testing.T) {
	msg := fullProfile()
	r := &policy.Resolved{
		Desc: msg.ProtoReflect().Descriptor(),
		Deny: []policy.Action{{Name: "no-path"}}, // Path is nil
	}

	paths := policy.Sanitize(msg, r, redact.Ctx{Key: []byte("k")}) // must not panic

	if len(paths) != 0 {
		t.Fatalf("a zero-length Path must not report anything redacted, got %v", paths)
	}
}
