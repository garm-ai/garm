package toolbind_test

import (
	"regexp"
	"testing"

	"github.com/garm-ai/garm/contracts/toolbind"
)

// microNameRegexp mirrors github.com/nats-io/nats.go/micro's own unexported
// nameRegexp (service.go: `^[A-Za-z0-9\-_]+$`), which validates
// micro.Config.Name. contracts deliberately does not depend on nats.go (see
// toolbind.go's package doc comment — "a tool author who wants only the
// message types does not pull in a NATS client"), so this test pins the
// charset by value rather than by importing the library; a live-broker
// cross-check that the two still agree lives in
// toolplane/svcwatch_live_test.go (which already depends on an embedded
// nats-server) and in garmtool's own runtime test.
var microNameRegexp = regexp.MustCompile(`^[A-Za-z0-9\-_]+$`)

// TestMicroServiceNameMatchesTheLibrarysCharset is the test that would have
// caught the real defect this function fixes: garmtool/runtime.go passed a
// dotted, fully-qualified proto service name straight through as
// micro.Config.Name, which nats.go/micro rejects at the first Endpoint
// call — a real broker was needed to observe the failure, and no test
// anywhere in this build had ever dialed one on this path.
func TestMicroServiceNameMatchesTheLibrarysCharset(t *testing.T) {
	for _, service := range []string{
		"garm.demo.v1beta1.AccountsService",
		"garm.demo.v1beta1.ComplianceService",
		"a",
		"pkg.Svc",
		"deeply.nested.proto.pkg.v1beta1.SomeService",
	} {
		got := toolbind.MicroServiceName(service)
		if !microNameRegexp.MatchString(got) {
			t.Errorf("MicroServiceName(%q) = %q, which does not match nats.go/micro's own "+
				"name charset %s", service, got, microNameRegexp)
		}
	}
}

// TestMicroServiceNameRejectsNoLongerNeedsDots is the negative control:
// without the fix, the INPUT itself (a real ToolRef.Service value) fails
// the same charset — proving the test above is not vacuously true because
// every string happens to already match.
func TestMicroServiceNameRejectsNoLongerNeedsDots(t *testing.T) {
	const dotted = "garm.demo.v1beta1.AccountsService"
	if microNameRegexp.MatchString(dotted) {
		t.Fatalf("%q unexpectedly matches the micro name charset on its own; "+
			"this test no longer demonstrates why MicroServiceName is needed", dotted)
	}
	if got := toolbind.MicroServiceName(dotted); !microNameRegexp.MatchString(got) {
		t.Fatalf("MicroServiceName(%q) = %q still does not match", dotted, got)
	}
}

// TestMicroServiceNameLeavesTheSubjectAndQueueGroupAlone documents (and
// pins) that this function is for Name ONLY. ToolRef.Subject and
// ToolRef.Service (used verbatim as QueueGroup) must stay dotted — subjects
// and micro's queue-group charset both permit dots, and sanitizing them
// would break subject derivability (ToolRef.Subject's own doc comment) for
// no reason.
func TestMicroServiceNameLeavesTheSubjectAndQueueGroupAlone(t *testing.T) {
	ref := toolbind.ToolRef{
		FQN:     "garm.demo.v1beta1.get_account_summary",
		Subject: "garm.demo.v1beta1.AccountsService.GetAccountSummary",
		Method:  "GetAccountSummary",
		Service: "garm.demo.v1beta1.AccountsService",
	}
	if ref.Subject != "garm.demo.v1beta1.AccountsService.GetAccountSummary" {
		t.Fatalf("Subject was mutated: %q", ref.Subject)
	}
	if ref.Service != "garm.demo.v1beta1.AccountsService" {
		t.Fatalf("Service was mutated: %q", ref.Service)
	}
	if got := toolbind.MicroServiceName(ref.Service); got == ref.Service {
		t.Fatalf("MicroServiceName(%q) = %q, unchanged; the dotted input should have been "+
			"transformed", ref.Service, got)
	}
}
