package policy_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

func TestFieldPolicyExtensionRoundTrips(t *testing.T) {
	opts := &descriptorpb.FieldOptions{}
	want := &toolv1.FieldPolicy{
		Read:         toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Compartments: []string{"pii-contact"},
		OnDeny: &toolv1.Redaction{
			Kind: &toolv1.Redaction_EmailDomain{EmailDomain: &toolv1.EmailDomain{}},
		},
	}
	proto.SetExtension(opts, toolv1.E_FieldPolicy, want)

	got, ok := proto.GetExtension(opts, toolv1.E_FieldPolicy).(*toolv1.FieldPolicy)
	if !ok || got == nil {
		t.Fatalf("extension not readable: %#v", got)
	}
	if got.GetRead() != toolv1.Clearance_CLEARANCE_CONFIDENTIAL {
		t.Fatalf("read = %v", got.GetRead())
	}
	if got.GetOnDeny().GetEmailDomain() == nil {
		t.Fatalf("on_deny lost its variant: %v", got.GetOnDeny())
	}
}

func TestClearanceOrdinalsAreSpacedAndStable(t *testing.T) {
	// The enum number IS the ordinal. Spacing leaves room to insert a level
	// without renumbering, which would silently reclassify every field.
	for name, want := range map[toolv1.Clearance]int32{
		toolv1.Clearance_CLEARANCE_UNSPECIFIED:  0,
		toolv1.Clearance_CLEARANCE_PUBLIC:       10,
		toolv1.Clearance_CLEARANCE_INTERNAL:     20,
		toolv1.Clearance_CLEARANCE_CONFIDENTIAL: 30,
		toolv1.Clearance_CLEARANCE_RESTRICTED:   40,
	} {
		if int32(name) != want {
			t.Fatalf("%v = %d, want %d", name, int32(name), want)
		}
	}
}
