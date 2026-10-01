package compiler_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// P1 — a required platform package (composition design §6).
//
// These read proto SOURCE rather than the synthetic descriptors the rest of this
// package's fixtures build, because the rule's subject is which PACKAGES are in
// the set: satisfying it means putting a second package there, and the only
// honest way to do that is to import the real garm.tasks.v1. It resolves from
// the linked registry, so there is no file on disk and nothing to keep in step.

// grantTool is one tool declaring a human approval, with the imports as a
// parameter so the same declaration can be compiled with and without the queue
// it depends on.
func grantTool(imports string) string {
	return `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
` + imports + `option go_package = "example.com/acme/gen/acme/v1;acmev1";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string reference = 1;
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
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_INTERNAL
                  max_grant_age_seconds: 900 material_fields: ["reference"] }
    };
  }
}
`
}

// noApproval is the same service with no approval block at all: a tool that
// promises nobody a decision requires no queue to deliver one.
const noApproval = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message PayRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string reference = 1;
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
      approval: { mode: MODE_NOTIFY notes: "recorded, nobody is asked" }
    };
  }
}
`

// payTree is one file at the path its package spells, which is what
// compileSource wants.
func payTree(body string) map[string]string {
	return map[string]string{"acme/v1/pay.proto": body}
}

func diagsFor(diags []compiler.Diag, rule string) []compiler.Diag {
	var out []compiler.Diag
	for _, d := range diags {
		if d.Rule == rule {
			out = append(out, d)
		}
	}
	return out
}

// The refusal itself, and the reason it exists: a catalogue that promises a
// human approval no queue can deliver.
//
// The message is asserted clause by clause rather than by a substring, because
// what it has to do is TEACH. Somebody tripping this is looking at a build
// error about a package they have never heard of, and the failure it replaces —
// a tool that parks, waits out its window and reports that no approval arrived,
// indistinguishable from a person declining to act — cost a day to diagnose on
// 2026-09-29. If the message stops saying that, the rule is worth much less and
// this test is what says so.
func TestP1RefusesAGrantModeToolWithNoTaskQueue(t *testing.T) {
	got := diagsFor(compiler.Lint(compileSource(t, payTree(grantTool("")))), "P1")
	if len(got) != 1 {
		t.Fatalf("P1 produced %d diagnostic(s), want 1: %v", len(got), got)
	}
	d := got[0]
	if d.Warn {
		t.Error("P1 is a warning; §6.3 makes it a refusal, because a catalogue promising " +
			"an approval nobody can deliver is the same claim the daemon refuses at mount")
	}
	if d.Path != "acme.v1.Payments.Pay" {
		t.Errorf("P1 reported against %q, want the method that declares the approval", d.Path)
	}
	for _, want := range []string{
		"MODE_GRANT",
		"garm.tasks.v1",
		"nobody can open, decide or see",
		"waits out the approval window",
		"indistinguishable from a person declining to act",
		"module: github.com/garm-ai/contracts",
		"packages: [garm.tasks.v1]",
		"MODE_NOTIFY",
	} {
		if !strings.Contains(d.Msg, want) {
			t.Errorf("the message does not say %q:\n%s", want, d.Msg)
		}
	}
}

// And it passes the moment the queue is in the set. The same declaration, one
// import different — which is what makes this the rule and not a ban on
// MODE_GRANT.
func TestP1IsSatisfiedByTheQueueBeingInTheSet(t *testing.T) {
	fds := compileSource(t, payTree(grantTool("import \"garm/tasks/v1/tasks.proto\";\n")))
	if got := diagsFor(compiler.Lint(fds), "P1"); len(got) != 0 {
		t.Fatalf("P1 fired with garm.tasks.v1 in the set: %v", got)
	}
	// And for the right reason: the queue's own tools are in the catalogue, so
	// there is something for the daemon to dispatch an approval to.
	tools, err := compiler.Tools(fds)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools {
		if tl.Name == "create_task" {
			found = true
		}
	}
	if !found {
		t.Error("the set carries no create_task, so the import satisfied P1 without " +
			"putting a queue in the catalogue — which would make the rule vacuous")
	}
}

// An approval mode that opens no task needs no queue. MODE_NOTIFY records the
// call and asks nobody.
func TestP1IgnoresAnApprovalThatOpensNoTask(t *testing.T) {
	if got := diagsFor(compiler.Lint(compileSource(t, payTree(noApproval))), "P1"); len(got) != 0 {
		t.Fatalf("P1 fired on a MODE_NOTIFY tool: %v", got)
	}
}

// Under PartialSet it says it did not check, and names what does.
//
// §6.2: the trigger is per declaration but the question is about the assembled
// catalogue, and buf invokes the protoc plugin once per directory. A rule that
// quietly passed there would be enforced only where somebody had already run the
// command that enforces it.
func TestP1WarnsUnderAPartialSetInsteadOfPassingSilently(t *testing.T) {
	fds := compileSource(t, payTree(grantTool("")))
	got := diagsFor(compiler.LintWith(fds, compiler.Options{PartialSet: true}), "P1")
	if len(got) != 1 {
		t.Fatalf("P1 produced %d diagnostic(s) under PartialSet, want 1: %v", len(got), got)
	}
	if !got[0].Warn {
		t.Error("P1 is an error under PartialSet; one directory cannot answer a question " +
			"about the whole catalogue, so it must say it did not check")
	}
	for _, want := range []string{"NOT checked", "garm lint", "garm catalogue build"} {
		if !strings.Contains(got[0].Msg, want) {
			t.Errorf("the warning does not say %q:\n%s", want, got[0].Msg)
		}
	}
}

// A partial run that can SEE the provider says nothing, because that one
// inference is sound: presence is monotone, so a package present in a subset of
// the catalogue's files is present in the catalogue. Only the negative direction
// fails to hold, and only the negative direction warns.
func TestP1DoesNotWarnUnderAPartialSetThatCanSeeTheQueue(t *testing.T) {
	fds := compileSource(t, payTree(grantTool("import \"garm/tasks/v1/tasks.proto\";\n")))
	if got := diagsFor(compiler.LintWith(fds, compiler.Options{PartialSet: true}), "P1"); len(got) != 0 {
		t.Fatalf("P1 warned about a question the partial set had already answered: %v", got)
	}
}
