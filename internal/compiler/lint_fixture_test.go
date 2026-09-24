package compiler_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// methodSpec describes one RPC to attach to a lint fixture's service.
//
// A nil tool means the method carries NO (garm.tool.v1.tool) option at all —
// Tools() skips it entirely, so it is never a tool. That is only ever what
// an L27 fixture wants: every other fixture here exercises a rule that
// fires on an annotated tool, and a nil tool would silently make it inert.
type methodSpec struct {
	name      string // RPC method name; drives the derived tool name (SnakeCase)
	tool      *toolv1.ToolPolicy
	streaming bool
}

// lintFixture builds a minimal, self-contained FileDescriptorProto and
// resolves it with protodesc.NewFile, following the pattern already proven in
// loader_test.go: build descriptors in Go and set custom options with
// proto.SetExtension, rather than shelling out to buf. That keeps these
// fixtures out of the buf workspace entirely (no third module, no extra lint
// exceptions, make gen stays untouched) at the cost of not exercising buf's
// own parser — Task 11's build-gate verification covers that end-to-end.
//
// The message under test, M, carries a field named `secret`, whose policy is
// what varies per rule, plus a second field ("visible") that always passes
// every rule and is always resolvable at CLEARANCE_PUBLIC. The second field
// exists purely so that a fixture built to trip an L1/L5/L6/L7/L9/L10
// violation on `secret` does not also, as a side effect of the message
// having only one field, trip L11 ("every field of the output is
// invisible") — that would make a failing fixture ambiguous about which
// rule actually fired. Use lintFixtureNoCanary for the one fixture (L11
// itself) that needs a message where the field under test really is the
// only field.
func lintFixture(
	t *testing.T,
	name string,
	fieldPolicy *toolv1.FieldPolicy,
	msgDefault *toolv1.FieldPolicy,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()
	return buildLintFixture(t, name, fieldPolicy, msgDefault,
		fixtureOpts{withCanary: true, leaf: validField()}, methods...)
}

// fixtureOpts are the knobs that vary the SHAPE of the fixture message
// graph, as opposed to the policy on the field under test.
type fixtureOpts struct {
	// withCanary adds the always-valid companion fields (see canaryFields).
	withCanary bool
	// recursive adds a self-referencing field to M, making the message graph
	// reachable from the tool contain a cycle — the shape L26 rejects.
	recursive bool
	// wellKnown adds a google.protobuf.Timestamp field ("at") to M. Its own
	// fields (seconds, nanos) carry no garm annotation and never can, so a
	// walk that descends into it reports them unlabeled and fails the build
	// — which is exactly what both walks did until they learned to stop at
	// google.protobuf.*.
	wellKnown bool
	// wellKnownPolicy is the policy on that field, when wellKnown is set.
	wellKnownPolicy *toolv1.FieldPolicy
	// leaf is the policy on N.leaf, the field reachable ONLY through the
	// message-shaped canaries. Setting it to nil leaves N.leaf unlabeled,
	// which is how a test proves the walk actually descends a given route:
	// an L1 diagnostic whose path names that route can only come from there.
	leaf *toolv1.FieldPolicy
}

// lintFixtureRecursive builds a fixture whose message graph contains a
// cycle (M has a field of type M), for L26.
func lintFixtureRecursive(
	t *testing.T,
	name string,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()
	return buildLintFixture(t, name, validField(), nil,
		fixtureOpts{withCanary: true, recursive: true, leaf: validField()}, methods...)
}

// lintFixtureUnlabeledLeaf builds a fixture where N.leaf carries no policy
// at all. N is reachable only through the message-shaped canaries, so every
// L1 diagnostic it produces names the route the walk took to get there.
func lintFixtureUnlabeledLeaf(
	t *testing.T,
	name string,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()
	return buildLintFixture(t, name, validField(), nil,
		fixtureOpts{withCanary: true, leaf: nil}, methods...)
}

// lintFixtureWellKnown builds a fixture whose message M carries a
// google.protobuf.Timestamp field, `at`, alongside everything lintFixture
// normally carries. fieldPolicy is the policy on the ordinary `secret`
// field, so passing nil proves the walk still reports L1 for an unlabeled
// ORDINARY field in the very same message that holds the well-known type:
// the fix must NARROW the walk at one boundary, not switch it off.
func lintFixtureWellKnown(
	t *testing.T,
	name string,
	fieldPolicy *toolv1.FieldPolicy,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()
	return buildLintFixture(t, name, fieldPolicy, nil,
		fixtureOpts{
			withCanary:      true,
			leaf:            validField(),
			wellKnown:       true,
			wellKnownPolicy: dateGrainField(),
		}, methods...)
}

// dateGrainField is the policy a Timestamp field actually wants: a real
// clearance and date_grain, the only redaction whose transformer requires a
// well-known type. Using it here (rather than a bare omit) means the
// fixture also keeps L5's MessageKind branch honest.
func dateGrainField() *toolv1.FieldPolicy {
	return &toolv1.FieldPolicy{
		Read: toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_DateGrain{
			DateGrain: &toolv1.DateGrain{Grain: toolv1.Grain_GRAIN_YEAR}}},
	}
}

// lintFixtureNoCanary is lintFixture without the always-visible second
// field, for the one rule (L11) that is specifically about every field of
// the message being invisible at once.
func lintFixtureNoCanary(
	t *testing.T,
	name string,
	fieldPolicy *toolv1.FieldPolicy,
	msgDefault *toolv1.FieldPolicy,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()
	return buildLintFixture(t, name, fieldPolicy, msgDefault,
		fixtureOpts{withCanary: false, leaf: validField()}, methods...)
}

func buildLintFixture(
	t *testing.T,
	name string,
	fieldPolicy *toolv1.FieldPolicy,
	msgDefault *toolv1.FieldPolicy,
	o fixtureOpts,
	methods ...methodSpec,
) []protoreflect.FileDescriptor {
	t.Helper()

	var fieldOpts *descriptorpb.FieldOptions
	if fieldPolicy != nil {
		fieldOpts = &descriptorpb.FieldOptions{}
		proto.SetExtension(fieldOpts, toolv1.E_FieldPolicy, fieldPolicy)
	}

	var msgOpts *descriptorpb.MessageOptions
	if msgDefault != nil {
		msgOpts = &descriptorpb.MessageOptions{}
		proto.SetExtension(msgOpts, toolv1.E_DefaultFieldPolicy, msgDefault)
	}

	fields := []*descriptorpb.FieldDescriptorProto{
		{
			Name:     proto.String("secret"),
			Number:   proto.Int32(1),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String("secret"),
			Options:  fieldOpts,
		},
	}
	var oneofs []*descriptorpb.OneofDescriptorProto
	var nested []*descriptorpb.DescriptorProto
	if o.withCanary {
		fields = append(fields, canaryFields(&oneofs)...)
		nested = append(nested, mapEntry("MsgMapEntry", true), mapEntry("StrMapEntry", false))
	}
	if o.wellKnown {
		fields = append(fields, &descriptorpb.FieldDescriptorProto{
			Name:     proto.String("at"),
			Number:   proto.Int32(9),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".google.protobuf.Timestamp"),
			JsonName: proto.String("at"),
			Options:  fieldOptions(o.wellKnownPolicy),
		})
	}
	if o.recursive {
		fields = append(fields, &descriptorpb.FieldDescriptorProto{
			Name:     proto.String("self"),
			Number:   proto.Int32(8),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".compiler.testdata.lint.M"),
			JsonName: proto.String("self"),
			Options:  fieldOptions(validMessageField()),
		})
	}

	msg := &descriptorpb.DescriptorProto{
		Name:       proto.String("M"),
		Field:      fields,
		NestedType: nested,
		OneofDecl:  oneofs,
		Options:    msgOpts,
	}

	svcMethods := make([]*descriptorpb.MethodDescriptorProto, 0, len(methods))
	for _, m := range methods {
		var mOpts *descriptorpb.MethodOptions
		if m.tool != nil {
			mOpts = &descriptorpb.MethodOptions{}
			proto.SetExtension(mOpts, toolv1.E_Tool, m.tool)
		}
		svcMethods = append(svcMethods, &descriptorpb.MethodDescriptorProto{
			Name:            proto.String(m.name),
			InputType:       proto.String(".compiler.testdata.lint.M"),
			OutputType:      proto.String(".compiler.testdata.lint.M"),
			Options:         mOpts,
			ServerStreaming: proto.Bool(m.streaming),
		})
	}

	// The import is declared only when it is used: protodesc.NewFile
	// resolves it against protoregistry.GlobalFiles (timestamppb is linked
	// in by the blank-ish import at the top of this file), and an unused
	// dependency would be noise in every other fixture.
	var deps []string
	if o.wellKnown {
		deps = append(deps, timestamppb.File_google_protobuf_timestamp_proto.Path())
	}

	fdProto := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("compiler/testdata/lint/" + name + ".proto"),
		Package:     proto.String("compiler.testdata.lint"),
		Syntax:      proto.String("proto3"),
		Dependency:  deps,
		MessageType: []*descriptorpb.DescriptorProto{msg, leafMessage(o.leaf)},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: proto.String("S"), Method: svcMethods},
		},
	}

	fd, err := protodesc.NewFile(fdProto, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile(%s): %v", name, err)
	}
	return []protoreflect.FileDescriptor{fd}
}

// canaryFields are the always-valid fields every fixture (except L11's)
// carries alongside `secret`.
//
// They are not decoration. Until the final branch review this fixture's
// message was two flat string fields, and that thinness is what let an L5
// false positive on message-valued maps ship, and what kept the lint walk's
// disagreement with policy.Compile over maps invisible. Each field here
// covers one shape the walk treats differently:
//
//	visible     singular scalar, the original L11 canary
//	nested      singular message — the walk must descend
//	many        repeated message — cardinality must not change the rules
//	msg_map     message-valued map — the walk must descend through MapValue
//	str_map     scalar-valued map — L5 must judge MapValue().Kind(), not the
//	            synthetic MapEntry's MessageKind
//	opt_scalar  proto3 `optional` scalar — the presence half of L6
//
// Every one carries a policy that is valid under every rule, so the good
// fixture must produce ZERO diagnostics, warnings included.
func canaryFields(oneofs *[]*descriptorpb.OneofDescriptorProto) []*descriptorpb.FieldDescriptorProto {
	scalarOpts := fieldOptions(validField())
	// A message-typed field cannot carry mask (L5: mask accepts strings), so
	// the message-shaped canaries use omit, which is structural and legal on
	// any kind.
	msgOpts := fieldOptions(validMessageField())

	// proto3 `optional` is encoded as a synthetic one-field oneof named
	// "_<field>"; protodesc rejects Proto3Optional without it. This is the
	// only way to get a scalar with real presence into the fixture, and
	// presence is exactly what L6 turns on.
	oneofIdx := int32(len(*oneofs))
	*oneofs = append(*oneofs, &descriptorpb.OneofDescriptorProto{
		Name: proto.String("_opt_scalar"),
	})

	return []*descriptorpb.FieldDescriptorProto{
		{
			Name:     proto.String("visible"),
			Number:   proto.Int32(2),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String("visible"),
			Options:  scalarOpts,
		},
		{
			Name:     proto.String("nested"),
			Number:   proto.Int32(3),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".compiler.testdata.lint.N"),
			JsonName: proto.String("nested"),
			Options:  msgOpts,
		},
		{
			Name:     proto.String("many"),
			Number:   proto.Int32(4),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".compiler.testdata.lint.N"),
			JsonName: proto.String("many"),
			Options:  msgOpts,
		},
		{
			Name:     proto.String("msg_map"),
			Number:   proto.Int32(5),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".compiler.testdata.lint.M.MsgMapEntry"),
			JsonName: proto.String("msgMap"),
			Options:  msgOpts,
		},
		{
			Name:     proto.String("str_map"),
			Number:   proto.Int32(6),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".compiler.testdata.lint.M.StrMapEntry"),
			JsonName: proto.String("strMap"),
			Options:  scalarOpts,
		},
		{
			Name:           proto.String("opt_scalar"),
			Number:         proto.Int32(7),
			Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName:       proto.String("optScalar"),
			Proto3Optional: proto.Bool(true),
			OneofIndex:     proto.Int32(oneofIdx),
			Options:        fieldOptions(validOmitField()),
		},
	}
}

// mapEntry builds the synthetic MapEntry message protobuf uses to represent
// a map field. msgValue selects map<string, N> over map<string, string>.
func mapEntry(name string, msgValue bool) *descriptorpb.DescriptorProto {
	value := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("value"),
		Number:   proto.Int32(2),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: proto.String("value"),
	}
	if msgValue {
		value.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
		value.TypeName = proto.String(".compiler.testdata.lint.N")
	}
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("key"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("key"),
			},
			value,
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

// leafMessage is N, the message type the message-shaped canaries point at.
// Its single field carries a fully valid policy, so reaching N contributes
// no diagnostics — what it proves is that the walk reaches it at all.
func leafMessage(leaf *toolv1.FieldPolicy) *descriptorpb.DescriptorProto {
	var opts *descriptorpb.FieldOptions
	if leaf != nil {
		opts = fieldOptions(leaf)
	}
	return &descriptorpb.DescriptorProto{
		Name: proto.String("N"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("leaf"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("leaf"),
				Options:  opts,
			},
		},
	}
}

func fieldOptions(fp *toolv1.FieldPolicy) *descriptorpb.FieldOptions {
	o := &descriptorpb.FieldOptions{}
	proto.SetExtension(o, toolv1.E_FieldPolicy, fp)
	return o
}

// validMessageField is validField for a field whose kind is not a string:
// omit is structural and accepted on every kind, where mask is not (L5).
func validMessageField() *toolv1.FieldPolicy {
	return &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_Omit{Omit: &toolv1.Omit{}}},
	}
}

// validOmitField is omit on a scalar — legal only where the scalar has
// explicit presence, which is the point of the opt_scalar canary.
func validOmitField() *toolv1.FieldPolicy { return validMessageField() }

// validField is a field policy with no violation of any rule under test:
// public read, a non-omit redaction that accepts a string field, and no
// compartments. Fixtures that are not about the field itself start here and
// vary exactly the one thing they test.
func validField() *toolv1.FieldPolicy {
	return &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_PUBLIC,
		OnDeny: maskRedaction(),
	}
}

func maskRedaction() *toolv1.Redaction {
	return &toolv1.Redaction{Kind: &toolv1.Redaction_Mask{Mask: &toolv1.Mask{}}}
}

// validTool is a tool policy with no violation of any rule under test: a
// legal name, a description, a read verb and a public minimum clearance.
func validTool(name string) *toolv1.ToolPolicy {
	return &toolv1.ToolPolicy{
		Name:         name,
		Description:  "d",
		Verb:         toolv1.Verb_VERB_READ,
		MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	}
}

func hasRule(ds []compiler.Diag, rule string, warn bool) bool {
	for _, d := range ds {
		if d.Rule == rule && d.Warn == warn {
			return true
		}
	}
	return false
}

// hasRuleWithMsg is hasRule plus a substring check on Diag.Msg. It exists
// alongside hasRule (not as a replacement) so the fifteen existing table rows
// in TestLintRules, which only care that the right rule+warn fired, stay
// unchanged. Use this where a mutation could narrow a check in a way that
// still fires the right Rule/Warn but for the wrong reason — hasRule alone
// cannot tell those apart.
func hasRuleWithMsg(ds []compiler.Diag, rule string, warn bool, msgContains string) bool {
	for _, d := range ds {
		if d.Rule == rule && d.Warn == warn && strings.Contains(d.Msg, msgContains) {
			return true
		}
	}
	return false
}
