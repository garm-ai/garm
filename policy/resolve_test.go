package policy_test

import (
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
	"github.com/garm-ai/garm/policy/testdata/testdatagarm"
)

func TestResolveDeniesOnlyWhatFails(t *testing.T) {
	plan := compileProfile(t)
	reg := testRegistry(t)
	pii, err := reg.Set([]string{"pii-contact"})
	if err != nil {
		t.Fatal(err)
	}

	cache := policy.NewCache()
	r := cache.Resolve(plan, policy.Shape{
		Clearance:    toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Compartments: pii,
	})

	denied := map[string]bool{}
	for _, a := range r.Deny {
		denied[a.Name] = true
	}
	// Passes: PUBLIC and INTERNAL fields, and email (CONFIDENTIAL + pii-contact).
	for _, n := range []string{"id", "locale", "email", "tags"} {
		if denied[n] {
			t.Fatalf("%s denied but should pass", n)
		}
	}
	// Denied: billing needs `financial`; national_id needs RESTRICTED.
	for _, n := range []string{"billing", "national_id"} {
		if !denied[n] {
			t.Fatalf("%s passed but should be denied", n)
		}
	}
}

func TestResolvePrunesBelowADeniedSubtree(t *testing.T) {
	plan := compileProfile(t)
	r := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_INTERNAL,
	})
	for _, a := range r.Deny {
		if a.Name == "billing.iban" || a.Name == "billing.card_last4" {
			t.Fatalf("%s listed although its parent subtree is already denied; "+
				"the sanitizer would walk into a branch it is about to delete", a.Name)
		}
	}
}

func TestResolveRecordsDisclosedAuditFields(t *testing.T) {
	plan := compileProfile(t)
	r := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_RESTRICTED,
	})
	found := false
	for _, n := range r.Disclosed {
		if n == "national_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit_on_read field not recorded as disclosed: %v", r.Disclosed)
	}
}

func TestResolveHashIsStableAndShapeSensitive(t *testing.T) {
	plan := compileProfile(t)
	cache := policy.NewCache()
	a := cache.Resolve(plan, policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})
	b := cache.Resolve(plan, policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL})
	c := cache.Resolve(plan, policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_RESTRICTED})

	if a != b {
		t.Fatal("cache returned a different object for the same shape")
	}
	if a.Hash != b.Hash {
		t.Fatal("hash unstable across calls")
	}
	if a.Hash == c.Hash {
		t.Fatal("different shapes produced the same plan hash")
	}
}

// TestResolveDoesNotOverPruneOnNamePrefix proves that underAny matches on
// path segments ("billing.") and not on a raw string prefix ("billing").
// "billing_summary" starts with the denied subtree's name as a string but
// is not beneath it as a path, so it must still be evaluated on its own
// terms — and here it fails on its own (RESTRICTED), so it must still
// appear in Deny rather than being silently skipped as "already covered".
func TestResolveDoesNotOverPruneOnNamePrefix(t *testing.T) {
	plan := compileProfile(t)
	r := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_INTERNAL, // fails billing (subtree) and billing_summary (RESTRICTED)
	})
	denied := map[string]bool{}
	for _, a := range r.Deny {
		denied[a.Name] = true
	}
	if !denied["billing"] {
		t.Fatal("billing should be denied at INTERNAL clearance (needs CONFIDENTIAL+financial)")
	}
	if !denied["billing_summary"] {
		t.Fatal("billing_summary was skipped as though it were beneath the denied " +
			"\"billing\" subtree, but \"billing.\" is not a prefix of \"billing_summary\"; " +
			"it fails on its own terms (RESTRICTED) and must be denied independently")
	}
}

// TestResolveEmptyCompartmentPassesForSatisfiedClearance confirms that a
// field with no compartment requirement at all is not denied for a caller
// whose clearance satisfies the read requirement, regardless of what
// compartments that caller holds (here: none).
func TestResolveEmptyCompartmentPassesForSatisfiedClearance(t *testing.T) {
	plan := compileProfile(t)
	r := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_INTERNAL, // satisfies "locale" (inherits INTERNAL, no compartments)
	})
	for _, a := range r.Deny {
		if a.Name == "locale" {
			t.Fatal("locale has no compartment requirement and clearance is satisfied; must not be denied")
		}
	}
}

// TestResolveDisclosedExcludesDeniedAuditFields asserts the negative
// counterpart to the brief's disclosure test: a caller who is denied an
// audit_on_read field must not have it listed in Disclosed. An
// implementation that recorded every audit field unconditionally would
// pass the brief's positive test and fail this one.
func TestResolveDisclosedExcludesDeniedAuditFields(t *testing.T) {
	plan := compileProfile(t)
	r := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_INTERNAL, // does not satisfy national_id (needs RESTRICTED)
	})
	for _, n := range r.Disclosed {
		if n == "national_id" {
			t.Fatalf("national_id is denied at this clearance but was recorded as disclosed: %v", r.Disclosed)
		}
	}
	denied := false
	for _, a := range r.Deny {
		if a.Name == "national_id" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("test setup invalid: national_id expected to be denied at INTERNAL clearance")
	}
}

// TestResolveCacheConcurrentUse calls Resolve from many goroutines for the
// same shape and for different shapes, to be run with -race. A data race in
// the cache would corrupt policy decisions under concurrent load — the
// worst possible failure mode, and invisible to single-threaded tests.
func TestResolveCacheConcurrentUse(t *testing.T) {
	plan := compileProfile(t)
	cache := policy.NewCache()
	reg := testRegistry(t)
	pii, err := reg.Set([]string{"pii-contact"})
	if err != nil {
		t.Fatal(err)
	}
	both, err := reg.Set([]string{"pii-contact", "financial"})
	if err != nil {
		t.Fatal(err)
	}

	// Chosen so each shape's Deny set has a different size (6, 4, 3, 0
	// fields respectively), guaranteeing distinct hashes regardless of the
	// hash function's internals.
	shapes := []policy.Shape{
		{Clearance: toolv1.Clearance_CLEARANCE_PUBLIC},
		{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL},
		{Clearance: toolv1.Clearance_CLEARANCE_CONFIDENTIAL, Compartments: pii},
		{Clearance: toolv1.Clearance_CLEARANCE_RESTRICTED, Compartments: both},
	}

	const goroutinesPerShape = 20
	var wg sync.WaitGroup
	results := make([][]*policy.Resolved, len(shapes))
	for i := range results {
		results[i] = make([]*policy.Resolved, goroutinesPerShape)
	}

	for si, shape := range shapes {
		for g := 0; g < goroutinesPerShape; g++ {
			wg.Add(1)
			go func(si, g int, shape policy.Shape) {
				defer wg.Done()
				results[si][g] = cache.Resolve(plan, shape)
			}(si, g, shape)
		}
	}
	wg.Wait()

	for si, shape := range shapes {
		first := results[si][0]
		for g, r := range results[si] {
			if r != first {
				t.Fatalf("shape %v: goroutine %d got a different *Resolved than goroutine 0 "+
					"(identity should be stable per shape under concurrent Resolve)", shape, g)
			}
		}
	}
	// Different shapes must still resolve to distinct, internally consistent
	// results even when computed concurrently with each other.
	for i := 0; i < len(shapes); i++ {
		for j := i + 1; j < len(shapes); j++ {
			if results[i][0].Hash == results[j][0].Hash {
				t.Fatalf("shapes %v and %v produced the same hash", shapes[i], shapes[j])
			}
		}
	}
}

// TestCacheKeysOnDescriptorIdentityNotFullName is a regression test for a
// review finding on Task 7: the cache used to key on p.Desc.FullName() (a
// string), while Sanitize compares descriptor identity. Two distinct
// descriptors sharing a full name — a dynamicpb message alongside a
// generated type, or a runtime-loaded FileDescriptorSet — would collide in
// the cache, and Sanitize would then return the message completely
// unredacted for one of them: fail-open on data while looking clean, since
// Sanitize has no error return to distinguish that from "nothing needed
// redacting".
//
// This builds a second, standalone MessageDescriptor with the exact same
// full name as the fixture's Profile ("policy.testdata.Profile") but a
// different identity (a different Go value, built via protodesc.NewFile
// rather than generated code), and asserts the two get separate cache
// entries.
func TestCacheKeysOnDescriptorIdentityNotFullName(t *testing.T) {
	plan := compileProfile(t)
	dupDesc := duplicateProfileDescriptor(t)

	if plan.Desc.FullName() != dupDesc.FullName() {
		t.Fatalf("test setup invalid: full names differ: %s vs %s", plan.Desc.FullName(), dupDesc.FullName())
	}
	if plan.Desc == dupDesc {
		t.Fatal("test setup invalid: descriptors are the same identity")
	}

	dupPlan := &policy.Plan{Desc: dupDesc}
	cache := policy.NewCache()
	shape := policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL}

	r1 := cache.Resolve(plan, shape)
	r2 := cache.Resolve(dupPlan, shape)

	if r1 == r2 {
		t.Fatal("two distinct descriptors sharing a full name collided in the cache; " +
			"Sanitize would silently no-op (Desc mismatch) or worse, redact against the wrong plan")
	}
	if r1.Desc == r2.Desc {
		t.Fatal("resolved results carry the same Desc identity despite distinct source descriptors")
	}
}

// duplicateProfileDescriptor builds a standalone MessageDescriptor whose full
// name is identical to the fixture's real Profile ("policy.testdata.Profile")
// but whose identity is a distinct Go value: a fresh, minimal message built
// with protodesc.NewFile from a different source file, rather than the
// generated type. This stands in for the two real-world collision cases
// named in the amendment: a dynamicpb message alongside a generated type, or
// a second copy of a schema loaded from a runtime FileDescriptorSet.
func duplicateProfileDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()

	fieldOpts := &descriptorpb.FieldOptions{}
	proto.SetExtension(fieldOpts, toolv1.E_FieldPolicy, &toolv1.FieldPolicy{
		Read: toolv1.Clearance_CLEARANCE_PUBLIC,
	})

	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("toolpolicy/testdata/duplicate.proto"),
		Package: proto.String("policy.testdata"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Profile"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("id"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("id"),
				Options:  fieldOpts,
			}},
		}},
	}

	// protoregistry.GlobalFiles is used only to resolve this file's own
	// dependencies (there are none); protodesc.NewFile does not register the
	// result into it, so this coexists with the real, generated
	// policy.testdata.Profile without conflict.
	fd, err := protodesc.NewFile(fdProto, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return fd.Messages().Get(0)
}

func testRegistry(t *testing.T) *policy.Registry {
	t.Helper()
	reg, err := policy.NewRegistry(testdatagarm.Compartments)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
