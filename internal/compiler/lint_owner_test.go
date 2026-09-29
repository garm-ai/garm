package compiler_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// O1 — every tool service and every agent names its owner. A warning in
// v0.15.0: every catalogue built before this release has no owners, and a
// gate nobody can pass on the day it appears is a gate people disable.

func ownerSrc(serviceOptions string) map[string]string {
	return map[string]string{"acme/v1/calc.proto": `syntax = "proto3";
package acme.v1;
import "garm/meta/v1/meta.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/v1;acmev1";
message AddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double a = 1;
}
message AddResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double sum = 1;
}
service Calculator {
  ` + serviceOptions + `
  rpc Add(AddRequest) returns (AddResponse) {
    option (garm.tool.v1.tool) = {
      name: "add" title: "Add" description: "Add two numbers."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`}
}

func o1Diags(ds []compiler.Diag) []compiler.Diag {
	var out []compiler.Diag
	for _, d := range ds {
		if d.Rule == "O1" {
			out = append(out, d)
		}
	}
	return out
}

// hasWarning is hasDiag for a warning: same rule, path and message check,
// opposite severity.
func hasWarning(ds []compiler.Diag, rule, path, contains string) bool {
	for _, d := range ds {
		if d.Rule == rule && d.Warn && d.Path == path && strings.Contains(d.Msg, contains) {
			return true
		}
	}
	return false
}

func TestO1WarnsOnAToolServiceWithNoOwner(t *testing.T) {
	diags := compiler.Lint(compileSource(t, ownerSrc("")))
	if !hasWarning(diags, "O1", "acme.v1.Calculator", "carries no (garm.meta.v1.owner)") {
		t.Errorf("a tool service with no owner produced no O1 warning:\n%s", render(diags))
	}
}

func TestO1WarnsOnAnOwnerWithNoTeam(t *testing.T) {
	for _, opt := range []string{
		`option (garm.meta.v1.owner) = { contact: "#calc" };`,
		`option (garm.meta.v1.owner) = { team: "  " contact: "#calc" };`,
	} {
		diags := compiler.Lint(compileSource(t, ownerSrc(opt)))
		if !hasWarning(diags, "O1", "acme.v1.Calculator", "has an empty team") {
			t.Errorf("%s produced no O1 warning:\n%s", opt, render(diags))
		}
	}
}

func TestO1AcceptsAnOwnerWithATeam(t *testing.T) {
	diags := compiler.Lint(compileSource(t, ownerSrc(
		`option (garm.meta.v1.owner) = { team: "calc-platform" contact: "#calc" on_call: "calc-primary" };`)))
	if got := o1Diags(diags); len(got) != 0 {
		t.Errorf("an owned service produced O1:\n%s", render(got))
	}
}

// Agents are services, so the same annotation names an agent's owner.
func TestO1WarnsOnAnAgentWithNoOwner(t *testing.T) {
	diags := compiler.Lint(compileSource(t, agentSrc(fullPolicy)))
	if !hasWarning(diags, "O1", "bank.v1.SupportAssistant", "carries no (garm.meta.v1.owner)") {
		t.Errorf("an agent with no owner produced no O1 warning:\n%s", render(diags))
	}
}

// A service with nothing governed on it has no card and needs no owner.
func TestO1IgnoresAServiceWithNoToolsAndNoAgent(t *testing.T) {
	src := ownerSrc("")
	src["acme/v1/calc.proto"] = strings.Replace(src["acme/v1/calc.proto"], `    option (garm.tool.v1.tool) = {
      name: "add" title: "Add" description: "Add two numbers."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
`, "", 1)
	if got := o1Diags(compiler.Lint(compileSource(t, src))); len(got) != 0 {
		t.Errorf("a plain RPC service produced O1:\n%s", render(got))
	}
}

// The release note in one assertion: O1 never fails a build in v0.15.0.
func TestO1IsAWarningInThisRelease(t *testing.T) {
	for _, d := range o1Diags(compiler.Lint(compileSource(t, ownerSrc("")))) {
		if !d.Warn {
			t.Errorf("O1 reported as an error; existing catalogues have no owners and must still build: %s", d)
		}
	}
}
