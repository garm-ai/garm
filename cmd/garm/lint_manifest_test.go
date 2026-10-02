package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/manifest"
)

// `garm lint` takes the manifest, which is what makes a whole-set rule
// checkable from the linter (design §6.2).
//
// Until v0.21.0 it took one directory. The consequence was not that lint checked
// less — it was that the rules which can only be answered against the assembled
// catalogue (A3's agent allowlist, A9's audience, and now P1's required platform
// package) saw the builder's inputs and never the linter's. So the command whose
// whole promise is "this cannot pass and then fail later" was, for exactly those
// rules, checking the deployment's own protos and not what it adopts.

// lintIn runs `garm lint` with the working directory inside a tree, which is the
// only way to test the convention: the manifest is found in the working
// directory precisely so that the common case needs no flag.
func lintIn(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	var out, errs bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"lint"}, args...))
	root.SetOut(&out)
	root.SetErr(&errs)
	err := root.Execute()
	return out.String(), errs.String(), err
}

// grantModeTool is a tool promising a human approval, which requires
// garm.tasks.v1 in the same catalogue.
const grantModeTool = `syntax = "proto3";
package acme.v2;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v2;acmev2";
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

// The proof that lint reading the manifest matters: one entry decides whether
// the tree lints.
//
// The declaration never changes. What changes is whether the manifest includes
// the module that serves the queue its approval parks on — so a linter that
// could not see the manifest could not answer the question at all, and would
// pass a tree `catalogue build` then refuses.
func TestLintEnforcesAWholeSetRuleAcrossTheManifestsInputs(t *testing.T) {
	dir := fixture(t)
	withModuleGraph(t, dir)
	copyInto(t, dir, "acme/v2/pay.proto", grantModeTool)

	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	_, errOut, err := lintIn(t, dir)
	if err == nil {
		t.Fatal("lint passed a MODE_GRANT tool with no task queue in the catalogue")
	}
	if !strings.Contains(errOut, "error: P1: acme.v2.Payments.Pay") {
		t.Fatalf("stderr does not carry the refusal:\n%s", errOut)
	}

	writeManifest(t, dir, `schema: v1
name: acme
include:
  - path: proto
  - module: github.com/garm-ai/contracts
    packages: [garm.tasks.v1]
`)
	_, errOut, err = lintIn(t, dir)
	if err != nil {
		t.Fatalf("lint refused a tree whose manifest declares the queue: %v\n%s", err, errOut)
	}
	if strings.Contains(errOut, "P1") {
		t.Errorf("P1 still fires with the module entry in place:\n%s", errOut)
	}
}

// A manifest is found by convention, so `garm lint` with no arguments is the
// command — the same input model `catalogue build` has.
func TestLintFindsTheManifestByConvention(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	stdout, errOut, err := lintIn(t, dir)
	if err != nil {
		t.Fatalf("lint with no arguments: %v\n%s", err, errOut)
	}
	if !strings.Contains(stdout, "ok") {
		t.Errorf("stdout = %q, want a line saying the tree is clean", stdout)
	}
}

// -f takes a manifest elsewhere, and its paths resolve against the manifest.
func TestLintTakesAManifestByFlag(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	stdout, errOut, err := lintIn(t, t.TempDir(), "-f", filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatalf("lint -f: %v\n%s", err, errOut)
	}
	if !strings.Contains(stdout, "ok") {
		t.Errorf("stdout = %q", stdout)
	}
}

// --proto and -f name two different inputs, and picking one silently would
// check something other than what was asked about.
func TestLintRefusesAManifestAndAProtoDirectoryTogether(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	_, _, err := lintIn(t, dir, "-f", manifest.Filename, "--proto", "proto")
	if err == nil {
		t.Fatal("both input flags were accepted")
	}
	if !strings.Contains(err.Error(), "two different inputs") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

// A tree with no manifest still lints from proto/, and --proto still names one
// directory. Every runbook and pre-commit hook in the estate passes it.
func TestLintFallsBackToTheProtoDirectory(t *testing.T) {
	dir := fixture(t)
	if err := os.Remove(filepath.Join(dir, manifest.Filename)); err != nil {
		t.Fatal(err)
	}
	stdout, errOut, err := lintIn(t, dir)
	if err != nil {
		t.Fatalf("lint on a tree with no manifest: %v\n%s", err, errOut)
	}
	if !strings.Contains(stdout, "ok") {
		t.Errorf("stdout = %q", stdout)
	}
}

// --proto in a tree that already has a catalogue.yaml is refused, naming both
// inputs — the lint-side half of the build-side refusal.
func TestLintRefusesProtoWhenAManifestIsPresent(t *testing.T) {
	dir := fixture(t) // `garm init` already wrote catalogue.yaml here.
	_, _, err := lintIn(t, dir, "--proto", "proto")
	if err == nil {
		t.Fatal("--proto was accepted on a tree that already has a manifest")
	}
	if !strings.Contains(err.Error(), "two different inputs") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	for _, want := range []string{"proto", manifest.Filename} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// The bug this fix closes, reproduced end to end.
//
// A tree composed from two inputs — the deployment's own directory plus one
// it adopts from elsewhere (a module in the real bank; a second `path:` entry
// here, which exercises the identical composition code and needs no module
// cache) — passes lint through its manifest: the adopted tool is in the
// composed set and the agent's allowlist names it validly. `garm lint
// --proto proto` used to turn the flag into a synthetic one-entry manifest of
// the deployment's OWN directory alone and run the full rule set over it, so
// A3 refused the allowlist for naming a tool that directory cannot see — the
// exact failure `garm-ai/examples`' research assistant hit on
// `web.v1.fetch_page`. The fix refuses `--proto` outright on a tree that has
// a manifest, so that false A3 can no longer fire at all: this test asserts
// the refusal replaces it, not merely that lint now fails for some reason.
func TestLintProtoNoLongerFalselyFailsAComposedTree(t *testing.T) {
	dir := fixture(t) // proto/acme/v1 with acme.v1.add; catalogue.yaml: path: proto.

	// The adopted tool: a second service, in a directory of its own — standing
	// in for a module's proto the way TestAManifestWinsOverTheFallback does.
	other := filepath.Join(dir, "other", "acme", "v2")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "proto", "acme", "v1", "calc.proto"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "package acme.v1;", "package acme.v2;")
	src = strings.ReplaceAll(src, "acme/gen/acme/v1;acmev1", "acme/gen/acme/v2;acmev2")
	// A distinct short name: MCP dispatches on it, so "add" in both packages
	// is a different error (L3) and would mask the one this test is about.
	src = strings.ReplaceAll(src, `name: "add"`, `name: "fetch"`)
	if err := os.WriteFile(filepath.Join(other, "calc.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// An agent in the deployment's own directory whose allowlist names the
	// adopted tool — the bank's research assistant naming web.v1.fetch_page.
	writeAgentFixture(t, dir, "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a")
	agentPath := filepath.Join(dir, "proto", "bank", "v1", "agent.proto")
	agentSrc, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	withAllowlist := strings.Replace(string(agentSrc),
		`prompts: { key: "system" value: { path: "prompts/system.md" sha256: "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a" } }`,
		`prompts: { key: "system" value: { path: "prompts/system.md" sha256: "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a" } }
    tools: [{ fqn: "acme.v2.fetch" }]`, 1)
	if withAllowlist == string(agentSrc) {
		t.Fatal("the agent fixture's prompts line was not found to patch")
	}
	if err := os.WriteFile(agentPath, []byte(withAllowlist), 0o644); err != nil {
		t.Fatal(err)
	}

	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n  - path: other\n")

	// Through the manifest, composing both inputs: the allowlisted tool exists
	// and the tree lints clean.
	stdout, errOut, err := lintIn(t, dir)
	if err != nil {
		t.Fatalf("lint through the manifest refused a tree whose allowlisted tool is adopted: %v\n%s", err, errOut)
	}
	if !strings.Contains(stdout, "ok") {
		t.Errorf("stdout = %q, want a line saying the tree is clean", stdout)
	}

	// Through --proto, composing only proto/: refused outright, naming both
	// inputs — never reaching A3, and never the false failure it used to be.
	_, errOut, err = lintIn(t, dir, "--proto", "proto")
	if err == nil {
		t.Fatal("--proto was accepted on a tree that already has a manifest")
	}
	if !strings.Contains(err.Error(), "two different inputs") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	if strings.Contains(errOut, "A3") || strings.Contains(err.Error(), "A3") {
		t.Fatalf("the old false A3 failure fired instead of the refusal:\nerr=%v\nstderr=%s", err, errOut)
	}
}

// A manifest wins over the fallback, or a migrated tree would silently keep
// linting the pre-migration way.
func TestLintPrefersTheManifestOverTheFallback(t *testing.T) {
	dir := fixture(t)
	other := filepath.Join(dir, "other", "acme", "v3")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	// A tool that does not lint, in a directory only the manifest names. If the
	// manifest were ignored, this would pass.
	body := strings.ReplaceAll(grantModeTool, "acme.v2", "acme.v3")
	body = strings.ReplaceAll(body, "acme/v2;acmev2", "acme/v3;acmev3")
	if err := os.WriteFile(filepath.Join(other, "pay.proto"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: other\n")
	_, errOut, err := lintIn(t, dir)
	if err == nil {
		t.Fatalf("lint checked proto/ instead of the input the manifest names:\n%s", errOut)
	}
	if !strings.Contains(errOut, "acme.v3.Payments.Pay") {
		t.Errorf("stderr does not name the manifest's input:\n%s", errOut)
	}
}
