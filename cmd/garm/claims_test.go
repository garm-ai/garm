package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTaxonomy adds a file-level declaration of compartments and tool sets
// to the fixture built by fixture() — no messages, no service, just the two
// DeclSet extensions, the shape a package's taxonomy commonly lives in.
// Passing a nil/empty slice for either omits that option entirely, so a
// caller can build a catalogue that declares nothing at all.
func withTaxonomy(t *testing.T, dir string, compartments, toolSets []string) {
	t.Helper()
	declList := func(names []string) string {
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = `{ name: "` + n + `" }`
		}
		return strings.Join(parts, ", ")
	}
	p := "syntax = \"proto3\";\n" +
		"package acme.v1;\n" +
		"import \"garm/tool/v1/tool.proto\";\n" +
		"option go_package = \"example.com/acme/gen/acme/v1;acmev1\";\n"
	if len(compartments) > 0 {
		p += "option (garm.tool.v1.compartments) = { declared: [" + declList(compartments) + "] };\n"
	}
	if len(toolSets) > 0 {
		p += "option (garm.tool.v1.tool_sets) = { declared: [" + declList(toolSets) + "] };\n"
	}
	path := filepath.Join(dir, "proto", "acme", "v1", "taxonomy.proto")
	if err := os.WriteFile(path, []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claims.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runClaimsCheck runs `garm claims check` and returns its stdout, stderr and
// error, without ever exiting the process.
func execClaimsCheck(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"claims", "check"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestClaimsCheckAcceptsAPolicyWhoseVocabularyIsDeclared(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial", "pii-contact"}, []string{"self-service"})
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  payments-desk: { compartments: [financial], tool_sets: [self-service] }
`)

	stdout, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err != nil {
		t.Fatalf("claims check: %v (stderr: %s)", err, stderr)
	}
	if stderr != "" {
		t.Errorf("expected no findings on stderr, got: %q", stderr)
	}
	if !strings.HasPrefix(stdout, "ok —") {
		t.Errorf("stdout = %q, want it to start with the lint-style \"ok —\" success line", stdout)
	}
}

// The finance/financial typo. Assert the error names the offending
// compartment AND the role that references it — an operator needs to know
// where to look, not just that something is wrong.
func TestClaimsCheckReportsAnUndeclaredCompartment(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial"}, nil)
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  payments-desk: { compartments: [finance] }
`)

	_, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err == nil {
		t.Fatal("expected an error: \"finance\" is not declared by the catalogue (it declares \"financial\")")
	}
	if !strings.Contains(stderr, "finance") {
		t.Errorf("stderr = %q, must name the offending compartment %q", stderr, "finance")
	}
	if !strings.Contains(stderr, "payments-desk") {
		t.Errorf("stderr = %q, must name the role %q that references it", stderr, "payments-desk")
	}
}

func TestClaimsCheckReportsAnUndeclaredToolSet(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial"}, []string{"self-service"})
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  support-desk: { compartments: [financial], tool_sets: [self-serve] }
`)

	_, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err == nil {
		t.Fatal("expected an error: \"self-serve\" is not declared by the catalogue (it declares \"self-service\")")
	}
	if !strings.Contains(stderr, "self-serve") {
		t.Errorf("stderr = %q, must name the offending tool set %q", stderr, "self-serve")
	}
	if !strings.Contains(stderr, "support-desk") {
		t.Errorf("stderr = %q, must name the role %q that references it", stderr, "support-desk")
	}
}

// Three bad names; assert all three appear. One round trip per typo is the
// difference between a usable gate and an annoying one.
func TestClaimsCheckReportsEveryOffenderNotJustTheFirst(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial"}, []string{"self-service"})
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  payments-desk: { compartments: [finance] }
  hr-desk:       { compartments: [personnel] }
  support-desk:  { tool_sets: [self-serve] }
`)

	_, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err == nil {
		t.Fatal("expected an error: three references are undeclared")
	}
	for _, want := range []string{"finance", "personnel", "self-serve"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, missing offender %q — every offender must be reported, not just the first", stderr, want)
		}
	}
}

// No --against: a usage error, never a default path.
func TestClaimsCheckRequiresAgainst(t *testing.T) {
	policy := writePolicy(t, `
roles:
  support-desk: { compartments: [financial] }
`)

	_, _, err := execClaimsCheck(t, policy)
	if err == nil {
		t.Fatal("expected a usage error when --against is omitted, not a silently-defaulted catalogue path")
	}
	if !strings.Contains(err.Error(), "against") {
		t.Errorf("err = %q, want it to name the missing --against flag", err.Error())
	}
}

// Review Focus 2. A catalogue with no declared compartments or sets makes
// every reference invalid, so "0 references, all valid" is the only way
// this could report clean — and it must not. Fail, naming the catalogue.
func TestClaimsCheckRefusesACatalogueThatDeclaresNoVocabulary(t *testing.T) {
	dir := fixture(t)
	// No withTaxonomy call: this catalogue declares nothing.
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  payments-desk: { compartments: [financial] }
`)

	_, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err == nil {
		t.Fatal("a catalogue declaring no vocabulary must fail, not pass vacuously")
	}
	if !strings.Contains(err.Error(), cat) {
		t.Errorf("err = %q, want it to name the catalogue %q", err.Error(), cat)
	}
	if strings.Contains(stderr, "referenced by role") {
		t.Errorf("expected no per-reference findings when the catalogue itself is the problem, got: %q", stderr)
	}
}

// The mirror of TestClaimsCheckRefusesACatalogueThatDeclaresNoVocabulary, on
// the policy side. `compartment:` (singular) is not a key this reader knows,
// and it must not be — it reads two policy shapes with one decoder and
// cannot tell a typo from a shape-specific key. So the file references
// nothing, and "0 compartment(s) and 0 tool set(s) ... all declared" would
// be a pass having checked nothing. Refuse instead.
func TestClaimsCheckRefusesAPolicyThatReferencesNoVocabulary(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial"}, []string{"self-service"})
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  payments-desk: { clearance: RESTRICTED, compartment: [financial], toolsets: [self-service] }
`)

	stdout, _, err := execClaimsCheck(t, policy, "--against", cat)
	if err == nil {
		t.Fatalf("a policy referencing nothing must fail, not pass vacuously (stdout: %q)", stdout)
	}
	if !strings.Contains(err.Error(), policy) {
		t.Errorf("err = %q, want it to name the policy %q", err.Error(), policy)
	}
	if strings.Contains(stdout, "ok —") {
		t.Errorf("stdout = %q, must not report success", stdout)
	}
}

// The guard above is about the file, not any one role. A role granting only
// a clearance and verbs — devkit's read-only persona is exactly that — is
// legal, and stays legal as long as something in the file names a
// compartment or a tool set.
func TestClaimsCheckAcceptsARoleThatGrantsOnlyClearanceAndVerbs(t *testing.T) {
	dir := fixture(t)
	withTaxonomy(t, dir, []string{"financial"}, nil)
	cat := filepath.Join(t.TempDir(), "c.binpb")
	build(t, dir, cat)

	policy := writePolicy(t, `
roles:
  read-only:     { clearance: INTERNAL, verbs: [READ] }
  payments-desk: { clearance: RESTRICTED, compartments: [financial], verbs: [READ, WRITE] }
`)

	stdout, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err != nil {
		t.Fatalf("claims check: %v (stderr: %s)", err, stderr)
	}
	if !strings.HasPrefix(stdout, "ok —") {
		t.Errorf("stdout = %q, want the success line", stdout)
	}
}
