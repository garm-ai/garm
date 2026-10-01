package main

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/garm-ai/contracts"
	cataloguev1 "github.com/garm-ai/contracts/garm/catalogue/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
)

// FieldPolicy.source is additive: a catalogue that carries it still stamps
// schema v1, and a daemon built before the field existed — garmd v0.2.0, whose
// window is v1 back to v1 — loads it and reads every policy it already knew.
//
// That is the compatibility a plane running tonight depends on, so this test
// pins it two ways rather than trusting the comment on AnnotationSchemaVersion:
//
//  1. the version stamped is literally 1, not merely "whatever the constant
//     says" — the constant is what this test guards;
//  2. a reader whose FieldPolicy has no field 6 (the v0.15.0 descriptor,
//     rebuilt here by deleting the field) decodes the marked field's policy
//     with read, on_deny intact and the source preserved as unknown bytes,
//     which is exactly what proto.GetExtension does inside an older garmd.
//
// If either half fails, the catalogue is NOT loadable by garmd v0.2.0 and the
// schema version must move — which means a daemon upgrade before the
// catalogue can ship. Stop and say so; do not bump the version to make the
// test pass.
func TestASourceRunnerFieldLoadsOnASchemaV1Daemon(t *testing.T) {
	dir := fixture(t)
	const p = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string reference = 1;
  string idempotency_key = 2 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_INTERNAL on_deny: { mask: {} } source: SOURCE_RUNNER }];
}
message PayResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service Payments {
  rpc Pay(PayRequest) returns (PayResponse) {
    option (garm.tool.v1.tool) = {
      name: "pay" title: "Pay" description: "Pay someone."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "proto", "acme", "v1", "pay.proto"), []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}

	var cat cataloguev1.Catalogue
	if err := proto.Unmarshal(build(t, dir, filepath.Join(t.TempDir(), "c.binpb")), &cat); err != nil {
		t.Fatal(err)
	}

	// 1. The version is v1 — the literal a v0.2.0 daemon's window admits.
	if got := cat.GetAnnotationSchemaVersion(); got != 1 {
		t.Fatalf("annotation_schema_version = %d; a catalogue carrying source: SOURCE_RUNNER "+
			"must still stamp v1 or garmd v0.2.0 refuses it at boot", got)
	}
	if contracts.AnnotationSchemaVersion != 1 {
		t.Fatalf("AnnotationSchemaVersion = %d; this release must not move it", contracts.AnnotationSchemaVersion)
	}

	// The set resolves the way garmd's Load resolves it, before it reads a
	// single annotation.
	files, err := protodesc.NewFiles(cat.GetFiles())
	if err != nil {
		t.Fatalf("the descriptor set does not resolve: %v", err)
	}
	fd, err := files.FindDescriptorByName("acme.v1.PayRequest.idempotency_key")
	if err != nil {
		t.Fatal(err)
	}
	opts := fd.(protoreflect.FieldDescriptor).Options().(*descriptorpb.FieldOptions)
	fp, _ := proto.GetExtension(opts, toolv1.E_FieldPolicy).(*toolv1.FieldPolicy)
	if fp.GetSource() != toolv1.FieldPolicy_SOURCE_RUNNER {
		t.Fatalf("this build reads source = %v, want SOURCE_RUNNER", fp.GetSource())
	}
	wire, err := proto.Marshal(fp)
	if err != nil {
		t.Fatal(err)
	}

	// 2. A v0.15.0 reader: FieldPolicy with no field 6 and no Source enum.
	old := protodesc.ToFileDescriptorProto(toolv1.File_garm_tool_v1_tool_proto)
	var policyMsg *descriptorpb.DescriptorProto
	for _, m := range old.GetMessageType() {
		if m.GetName() == "FieldPolicy" {
			policyMsg = m
		}
	}
	if policyMsg == nil {
		t.Fatal("no FieldPolicy in the tool descriptor")
	}
	var kept []*descriptorpb.FieldDescriptorProto
	for _, f := range policyMsg.GetField() {
		if f.GetName() != "source" {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(policyMsg.GetField()) {
		t.Fatal("FieldPolicy has no field named source; the fixture is not exercising the new field")
	}
	policyMsg.Field = kept
	policyMsg.EnumType = nil
	oldFile, err := protodesc.NewFile(old, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("rebuilding the v0.15.0 descriptor: %v", err)
	}
	asOld := dynamicpb.NewMessage(oldFile.Messages().ByName("FieldPolicy"))
	if err := proto.Unmarshal(wire, asOld); err != nil {
		t.Fatalf("a reader without field 6 cannot decode the policy: %v", err)
	}
	read := asOld.Get(asOld.Descriptor().Fields().ByName("read")).Enum()
	if read != protoreflect.EnumNumber(toolv1.Clearance_CLEARANCE_INTERNAL) {
		t.Errorf("read clearance = %d through a v0.15.0 reader, want %d",
			read, toolv1.Clearance_CLEARANCE_INTERNAL)
	}
	if !asOld.Has(asOld.Descriptor().Fields().ByName("on_deny")) {
		t.Error("on_deny was lost through a v0.15.0 reader")
	}
	if len(asOld.GetUnknown()) == 0 {
		t.Error("the source survived as nothing at all; it should be unknown bytes an older " +
			"daemon carries and ignores")
	}
}
