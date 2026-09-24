package compiler_test

import (
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// renderMicro runs the micro emitter over the demo accounts package and
// returns the generated source. Implemented alongside the existing helper in
// emit_test.go; see that file for how a *protogen.Plugin is built from the
// testdata descriptor set.
func renderMicro(t *testing.T) string { t.Helper(); return renderMicroFor(t, "garm.demo.v1beta1") }

func TestMicroHandlerHasOneMethodPerToolAndNoUnimplementedEmbed(t *testing.T) {
	src := renderMicro(t)

	for _, want := range []string{
		"type AccountsServiceHandler interface {",
		"GetAccountSummary(context.Context, *demov1beta1.GetAccountSummaryRequest) (*demov1beta1.GetAccountSummaryResponse, error)",
		"SearchTransactions(context.Context, *demov1beta1.SearchTransactionsRequest) (*demov1beta1.SearchTransactionsResponse, error)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated micro binding is missing:\n  %s", want)
		}
	}

	// The whole value of the interface is that a new tool fails to compile.
	// An Unimplemented embed would turn that into a runtime Unimplemented
	// answered to a real caller — the exact failure emitMount refuses.
	if strings.Contains(src, "UnimplementedAccountsServiceHandler") {
		t.Error("the micro binding emits an Unimplemented embed; a missing handler must be " +
			"a compile error, not a runtime Unimplemented (spec 2.1)")
	}
}

func TestMicroServeRegistersAgainstToolbindRegistrar(t *testing.T) {
	src := renderMicro(t)
	if !strings.Contains(src, "func ServeAccountsService(r toolbind.Registrar, h AccountsServiceHandler) error {") {
		t.Error("ServeAccountsService must take a toolbind.Registrar, not a concrete runtime: " +
			"contracts must not depend on garmtool (spec 1.2)")
	}
	if strings.Contains(src, "plaenen/garm/garmtool") {
		t.Error("the generated binding imports garmtool; that inverts the dependency and " +
			"fails the contracts thin-graph test")
	}
}

// A handler returning (nil, nil) must be named, not dereferenced. connect's
// own unary handler panics on this; here it would be a nil deref one line on.
func TestMicroResolverRefusesANilResponse(t *testing.T) {
	src := renderMicro(t)
	want := `"garm.demo.v1beta1.AccountsService.GetAccountSummary: handler returned no response and no error"`
	if !strings.Contains(src, want) {
		t.Errorf("generated resolver does not refuse a nil response by name; want a message containing:\n  %s", want)
	}
}

func TestMicroEmitsContractIdentityAndToolRefs(t *testing.T) {
	src := renderMicro(t)
	for _, want := range []string{
		"const ContractVersion =",
		"const DescriptorHash =",
		"var AccountsServiceTools = []toolbind.ToolRef{",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated micro binding is missing:\n  %s", want)
		}
	}
	// gofmt re-aligns a struct literal's colons to the longest key in the
	// block, so the exact run of spaces after "Subject:" shifts whenever a
	// longer field (ContractVersion, DescriptorHash) is added anywhere in
	// the same literal — match on the field/value pair with whitespace
	// tolerant of that, not on one fixed byte-for-byte spacing.
	if !subjectFieldRE.MatchString(src) {
		t.Errorf("generated micro binding is missing a ToolRef Subject field for "+
			"garm.demo.v1beta1.AccountsService.GetAccountSummary:\n%s", src)
	}
}

var subjectFieldRE = regexp.MustCompile(`Subject:\s*"garm\.demo\.v1beta1\.AccountsService\.GetAccountSummary"`)

// TestMicroToolRefsCarryContractIdentity is addition 3's own pin: a
// runtime learns ContractVersion/DescriptorHash from the ToolRef it
// already receives via Endpoint, not from a second ServeX parameter, so
// §7.1's drift detection has something to compare without a signature
// change.
func TestMicroToolRefsCarryContractIdentity(t *testing.T) {
	src := renderMicro(t)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`ContractVersion:\s*ContractVersion,`),
		regexp.MustCompile(`DescriptorHash:\s*DescriptorHash,`),
	} {
		if !re.MatchString(src) {
			t.Errorf("generated ToolRef literal does not thread the package's own contract "+
				"identity (addition 3): no match for %s\n%s", re, src)
		}
	}
}

func TestMicroEmitsConnectAdapter(t *testing.T) {
	src := renderMicro(t)
	if !strings.Contains(src, "func AccountsServiceAsConnect(h AccountsServiceHandler) demov1beta1connect.AccountsServiceHandler {") {
		t.Error("the micro binding must emit a connect adapter so one implementation stays " +
			"servable three ways (tools-server-design 2.5)")
	}
}

// descriptorHashRE pulls DescriptorHash's value out of generated source.
// EmitMicro is the only thing that computes the hash, so this is the only
// surface a test can observe it through.
var descriptorHashRE = regexp.MustCompile(`const DescriptorHash = "([0-9a-f]{64})"`)

func mustDescriptorHash(t *testing.T, src string) string {
	t.Helper()
	m := descriptorHashRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("generated source has no const DescriptorHash = \"<64 hex chars>\" line:\n%s", src)
	}
	return m[1]
}

// TestDescriptorHashExcludesPolicyAndComments is the property the brief
// calls load-bearing: DescriptorHash must NOT move when a field_policy
// annotation or a comment changes, because both change redaction — garm's
// business, applied after the response returns — not what can be
// unmarshalled off the wire. `make gen` run twice cannot catch a
// regression here: a legitimate field_policy edit legitimately changes the
// generated registry, so that diff-based check passes whether or not the
// HASH itself should have moved. If descriptorHash were ever "simplified"
// into marshalling a whole FileDescriptorProto, this is the only thing
// that would still fail.
func TestDescriptorHashExcludesPolicyAndComments(t *testing.T) {
	const pkg = "compiler.testdata.hash"
	bare := hashFixture(pkg, 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, false, "")
	withPolicy := hashFixture(pkg, 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, true, "")
	withComment := hashFixture(pkg, 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, false,
		"a completely different comment that says nothing about the wire shape")

	hBare := mustDescriptorHash(t, renderMicroFromFD(t, bare))
	hPolicy := mustDescriptorHash(t, renderMicroFromFD(t, withPolicy))
	hComment := mustDescriptorHash(t, renderMicroFromFD(t, withComment))

	if hBare != hPolicy {
		t.Errorf("DescriptorHash moved when only a field_policy annotation was added "+
			"(%s vs %s); a read: clearance change would then mark every service "+
			"contract-incompatible over a change that cannot break unmarshalling", hBare, hPolicy)
	}
	if hBare != hComment {
		t.Errorf("DescriptorHash moved when only a comment changed (%s vs %s)", hBare, hComment)
	}
}

// TestDescriptorHashChangesOnWireShapeChanges is
// TestDescriptorHashExcludesPolicyAndComments' other half: a test that only
// proved the hash is stable would pass just as well if descriptorHash
// always returned a constant. Pin that it actually reacts to the two
// dimensions the brief names: field number and field kind.
func TestDescriptorHashChangesOnWireShapeChanges(t *testing.T) {
	const pkg = "compiler.testdata.hash"
	base := hashFixture(pkg, 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, false, "")
	diffNumber := hashFixture(pkg, 2, descriptorpb.FieldDescriptorProto_TYPE_STRING, false, "")
	diffKind := hashFixture(pkg, 1, descriptorpb.FieldDescriptorProto_TYPE_INT64, false, "")

	hBase := mustDescriptorHash(t, renderMicroFromFD(t, base))
	hNumber := mustDescriptorHash(t, renderMicroFromFD(t, diffNumber))
	hKind := mustDescriptorHash(t, renderMicroFromFD(t, diffKind))

	if hBase == hNumber {
		t.Errorf("DescriptorHash did not change when the field's NUMBER changed (both %s)", hBase)
	}
	if hBase == hKind {
		t.Errorf("DescriptorHash did not change when the field's KIND changed (both %s)", hBase)
	}
	if hNumber == hKind {
		t.Errorf("the field-number and field-kind variants coincidentally hashed the same (%s); "+
			"that would hide either dimension breaking independently", hNumber)
	}
}
