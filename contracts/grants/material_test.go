package grants_test

import (
	"strings"
	"testing"

	agentv1 "github.com/garm-ai/garm/contracts/garm/agent/v1"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/contracts/grant"
	"github.com/garm-ai/garm/contracts/grants"
)

// Materialise is the other half of the agreement: the issuer digests the map
// it was handed, and the verifying side rebuilds the map from the actual
// message. If the two disagree about how a value becomes text, every approval
// is refused — so what each kind reads as is pinned here.
func TestMaterialiseReadsTheCanonicalText(t *testing.T) {
	// A message with a scalar, an enum, an absent optional and a nested
	// message, which between them are every shape a material path may take.
	msg := &agentv1.AgentPolicy{
		Mode:      agentv1.Mode_MODE_REACT,
		Principal: &agentv1.Principal{Clearance: toolv1.Clearance_CLEARANCE_RESTRICTED},
	}

	if _, err := grants.Materialise(msg.ProtoReflect(), []string{"principal.no_such_field"}); err == nil {
		t.Fatal("a path naming no field was accepted; the catalogue and the message " +
			"disagreeing about shape must refuse, not skip")
	}

	got, err := grants.Materialise(msg.ProtoReflect(), []string{"mode", "principal.clearance"})
	if err != nil {
		t.Fatalf("materialising: %v", err)
	}
	// The enum's NAME, not its number: an approver was shown a name, and a
	// renumbered proto must not silently change a digest.
	for path, want := range map[string]string{
		"mode":                "MODE_REACT",
		"principal.clearance": "CLEARANCE_RESTRICTED",
	} {
		if got[path] != want {
			t.Errorf("%s = %q, want %q", path, got[path], want)
		}
	}
}

// An absent field anywhere on a path yields Unset for the whole path, because
// a message that omits `principal` has omitted `principal.clearance` too, and
// the human approving that and the side checking it must agree.
func TestAnAbsentPathIsUnsetAndNotEmpty(t *testing.T) {
	got, err := grants.Materialise((&agentv1.AgentPolicy{}).ProtoReflect(),
		[]string{"principal.clearance"})
	if err != nil {
		t.Fatalf("materialising: %v", err)
	}
	if got["principal.clearance"] != grant.Unset {
		t.Fatalf("an absent path read as %q, want %q: absence is a value, so approving "+
			"a request with a field and sending one without must not digest the same",
			got["principal.clearance"], grant.Unset)
	}
	// And that it actually changes the digest, which is the only reason the
	// distinction is worth keeping.
	if grant.Digest(got) == grant.Digest(map[string]string{"principal.clearance": ""}) {
		t.Error("unset and empty digest the same")
	}
}

// A repeated field has no single text a human could approve, and descending
// into one is a panic waiting to happen. The linter refuses such a path at
// build; this is the runtime's own guard.
func TestARepeatedPathIsRefused(t *testing.T) {
	msg := &agentv1.AgentPolicy{Tools: []*agentv1.ToolRef{{Fqn: "bank.v1.pay"}}}
	_, err := grants.Materialise(msg.ProtoReflect(), []string{"tools"})
	if err == nil {
		t.Fatal("a repeated material path was accepted")
	}
	if !strings.Contains(err.Error(), "repeated") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The round trip the two processes actually perform: a tool service stores a
// map when it opens a task, the issuer digests that map, and the verifying
// side rebuilds it from the request and compares.
func TestTheMapAndTheMessageDigestTheSame(t *testing.T) {
	msg := &agentv1.AgentPolicy{
		Mode:      agentv1.Mode_MODE_REACT,
		Principal: &agentv1.Principal{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL},
	}
	paths := []string{"mode", "principal.clearance"}

	stored, err := grants.Materialise(msg.ProtoReflect(), paths)
	if err != nil {
		t.Fatal(err)
	}
	resent, err := grants.Materialise(msg.ProtoReflect(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Digest(stored) != grant.Digest(resent) {
		t.Fatal("the same message materialised twice digests differently")
	}

	// Change one value and the digest moves, which is the whole mechanism.
	msg.Principal.Clearance = toolv1.Clearance_CLEARANCE_RESTRICTED
	changed, err := grants.Materialise(msg.ProtoReflect(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Digest(stored) == grant.Digest(changed) {
		t.Fatal("a changed material value digested the same")
	}
}
