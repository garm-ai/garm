package claimscheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/claimscheck"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadPolicyReadsTheSTSClaimsShape(t *testing.T) {
	refs, err := claimscheck.ReadPolicy(write(t, `
roles:
  support-desk:  { clearance: INTERNAL,   compartments: [support], verbs: [READ], tool_sets: [self-service] }
  payments-desk: { clearance: RESTRICTED, compartments: [financial, pii-contact], verbs: [READ, WRITE] }
segments:
  retail-vip: { kind: customer, roles: [support-desk] }
agents:
  order-assistant: { roles: [support-desk] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(refs.Compartments, []string{"support", "financial", "pii-contact"}) {
		t.Fatalf("compartments = %v", refs.Compartments)
	}
	if !sameSet(refs.ToolSets, []string{"self-service"}) {
		t.Fatalf("toolSets = %v", refs.ToolSets)
	}
	if refs.Roles != 2 {
		t.Fatalf("Roles = %d, want 2", refs.Roles)
	}
}

func TestReadPolicyReadsThePersonasShape(t *testing.T) {
	// devkit's file. Different assignment block, same vocabulary location.
	refs, err := claimscheck.ReadPolicy(write(t, `
roles:
  support-agent: { clearance: INTERNAL, compartments: [pii-contact], verbs: [READ] }
users:
  alice: { subject: "user:alice", roles: [support-agent] }
agents:
  triage-bot: { subject: "agent:triage-bot", roles: [support-agent], may_act_for: [alice] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(refs.Compartments, []string{"pii-contact"}) {
		t.Fatalf("compartments = %v", refs.Compartments)
	}
	if refs.Roles != 1 {
		t.Fatalf("Roles = %d", refs.Roles)
	}
}

// Review Focus 1. The failure this command exists to prevent, applied to itself.
func TestReadPolicyRejectsAFileThatIsNotAPolicy(t *testing.T) {
	for name, body := range map[string]string{
		"no roles block":                "segments:\n  retail-vip: { kind: customer }\n",
		"empty roles block":             "roles: {}\n",
		"a different document entirely": "apiVersion: v1\nkind: ConfigMap\n",
		"not yaml at all":               "{{{\n",
	} {
		if _, err := claimscheck.ReadPolicy(write(t, body)); err == nil {
			t.Errorf("%s: accepted; a file yielding no roles references no compartments "+
				"and would report clean, which is the failure this command exists to catch", name)
		}
	}
}

func TestReadPolicyDeduplicatesAndSorts(t *testing.T) {
	// Two roles naming the same compartment must report it once, and the
	// output must be stable so a CI diff is readable.
	refs, err := claimscheck.ReadPolicy(write(t, `
roles:
  b: { compartments: [zeta, alpha] }
  a: { compartments: [alpha] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(refs.Compartments, ","); got != "alpha,zeta" {
		t.Fatalf("compartments = %q, want %q (deduped and sorted)", got, "alpha,zeta")
	}
}

func TestReadPolicyReportsAMissingFile(t *testing.T) {
	if _, err := claimscheck.ReadPolicy("does/not/exist.yaml"); err == nil {
		t.Fatal("a missing policy file must be an error, not an empty result")
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}
