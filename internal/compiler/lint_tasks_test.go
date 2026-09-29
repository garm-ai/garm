package compiler_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
	"github.com/garm-ai/garm/internal/contractsrepo"
)

// garm.tasks.v1 is a contract garm-ai/contracts publishes and another
// repository serves, so it is held to the rules every other tool declaration
// is held to. A platform contract that could not pass this linter would be the
// clearest possible signal that the linter is wrong about something.
//
// The protos used to be here and are not: they went with the contract, and
// this repository depends on the module rather than holding it. A descriptor
// linked from that module would not do — lint reads proto SOURCE for the
// comments a field's documentation comes from — so the check reads a checkout.
// See internal/contractsrepo for what happens when there is none.
func TestTheTasksContractLints(t *testing.T) {
	root := filepath.Join(contractsrepo.Require(t), "proto")
	_, fds, err := compile.Tree(t.Context(), root)
	if err != nil {
		t.Fatalf("compiling the contract's proto tree at %s: %v", root, err)
	}

	var bad []string
	for _, d := range compiler.Lint(fds) {
		if d.Warn {
			continue
		}
		bad = append(bad, d.String())
	}
	if len(bad) > 0 {
		t.Fatalf("the contract's proto tree does not lint:\n%s", strings.Join(bad, "\n"))
	}

	// And the tools are the eight the design names, at the audiences it
	// names. The audience is the half a set cannot express, and getting one
	// of these wrong would hand a model a person's decision.
	tools, err := compiler.Tools(fds)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"create_task":   "[AUDIENCE_RUNNER]",
		"list_tasks":    "[AUDIENCE_PERSON AUDIENCE_AGENT]",
		"get_task":      "[AUDIENCE_PERSON AUDIENCE_AGENT]",
		"approval_card": "[AUDIENCE_PERSON]",
		"claim_task":    "[AUDIENCE_PERSON AUDIENCE_AGENT]",
		"release_task":  "[AUDIENCE_PERSON AUDIENCE_AGENT]",
		"decide_task":   "[AUDIENCE_PERSON]",
		"triage_task":   "[AUDIENCE_AGENT AUDIENCE_PERSON]",
	}
	got := map[string]string{}
	for _, tl := range tools {
		if tl.Method.ParentFile().Package() != "garm.tasks.v1" {
			continue
		}
		names := make([]string, 0, 3)
		for _, a := range compiler.ResolvedAudience(tl.Policy) {
			names = append(names, a.String())
		}
		got[tl.Name] = "[" + strings.Join(names, " ") + "]"
	}
	if len(got) != len(want) {
		t.Errorf("garm.tasks.v1 declares %d tools, want %d: %v", len(got), len(want), got)
	}
	for name, audience := range want {
		if got[name] != audience {
			t.Errorf("%s audience = %s, want %s", name, got[name], audience)
		}
	}
}
