package compiler_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// A1 — the governed door is exactly two RPCs. A third method on an agent
// service is a tool the runner does not know how to serve and garmd would
// mount anyway: a governed call that reaches nothing.
func TestA1RefusesAThirdMethod(t *testing.T) {
	files := agentSrc(fullPolicy)
	// The anchor is the service's closing brace, which is the only place two
	// closing braces sit on consecutive lines in this fixture.
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		"  }\n}\n", `  }
  rpc Cancel(garm.agent.v1.RunRef) returns (Ask) {
    option (garm.tool.v1.tool) = {
      name: "support_cancel" title: "Cancel" description: "Cancel a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
}
`, 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasError(diags, "A1", "is a third method") {
		t.Errorf("a third method was accepted:\n%s", render(diags))
	}
}

func TestA1RefusesAnAgentWithNoInvoke(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		"rpc Invoke(Ask)", "rpc Start(Ask)", 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasError(diags, "A1", "must declare exactly one rpc named Invoke") {
		t.Errorf("an agent with no Invoke was accepted:\n%s", render(diags))
	}
}

func TestA1RefusesTheWrongMessagesOnEitherDoor(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"Invoke returning something else",
			"rpc Invoke(Ask) returns (garm.agent.v1.RunRef)",
			"rpc Invoke(Ask) returns (Ask)",
			"Invoke must return garm.agent.v1.RunRef"},
		{"GetRun taking something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(Ask) returns (garm.agent.v1.RunStatus)",
			"GetRun must take garm.agent.v1.RunRef"},
		{"GetRun returning something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(garm.agent.v1.RunRef) returns (Ask)",
			"GetRun must return garm.agent.v1.RunStatus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := agentSrc(fullPolicy)
			files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"], tc.from, tc.to, 1)
			diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
			if !hasError(diags, "A1", tc.want) {
				t.Errorf("accepted:\n%s", render(diags))
			}
		})
	}
}

// An agent that declares no GetRun is legal: design §2.2 says "at most one".
func TestA1AcceptsAnAgentWithNoGetRun(t *testing.T) {
	files := agentSrc(fullPolicy)
	src := files["bank/v1/agent.proto"]
	files["bank/v1/agent.proto"] = src[:strings.Index(src, "  rpc GetRun")] + "}\n"
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A1" && !d.Warn {
			t.Errorf("an agent with no GetRun was refused: %s", d.String())
		}
	}
}

// And the well-formed one produces no A1 at all.
func TestA1AcceptsTheWellFormedAgent(t *testing.T) {
	for _, d := range compiler.LintWith(compileSource(t, agentSrc(fullPolicy)), compiler.Options{}) {
		if d.Rule == "A1" {
			t.Errorf("the well-formed agent produced an A1: %s", d.String())
		}
	}
}
