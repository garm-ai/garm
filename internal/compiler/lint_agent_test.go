package compiler_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/garm/internal/compiler"
)

// hasDiag is hasError plus a path assertion. Rule and severity alone do not
// say WHICH service or method a diagnostic is about, and a catalogue built
// from many trees needs that answered from the diagnostic itself — so an A1
// test pins the exact path, not just the rule id and message substring.
func hasDiag(ds []compiler.Diag, rule, path, contains string) bool {
	for _, d := range ds {
		if d.Rule == rule && !d.Warn && d.Path == path && strings.Contains(d.Msg, contains) {
			return true
		}
	}
	return false
}

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
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant.Cancel", "is a third method") {
		t.Errorf("a third method was accepted:\n%s", render(diags))
	}
}

func TestA1RefusesAnAgentWithNoInvoke(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		"rpc Invoke(Ask)", "rpc Start(Ask)", 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant", "must declare exactly one rpc named Invoke") {
		t.Errorf("an agent with no Invoke was accepted:\n%s", render(diags))
	}
	// Exactly two A1 diagnostics, not three: the missing-Invoke error on the
	// service, and one saying Start is neither Invoke nor GetRun — never "a
	// third method", which would be factually wrong here. There are only two
	// RPCs on this service (Start, GetRun); Start is not a third anything.
	var got []compiler.Diag
	for _, d := range diags {
		if d.Rule == "A1" {
			got = append(got, d)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d A1 diagnostics, want 2:\n%s", len(got), render(got))
	}
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant.Start", "is neither Invoke nor GetRun") {
		t.Errorf("Start should be reported as neither Invoke nor GetRun, not as a third method:\n%s", render(diags))
	}
	if hasDiag(diags, "A1", "bank.v1.SupportAssistant.Start", "is a third method") {
		t.Errorf("Start was reported as a third method, but the service only has two RPCs:\n%s", render(diags))
	}
}

func TestA1RefusesTheWrongMessagesOnEitherDoor(t *testing.T) {
	for _, tc := range []struct{ name, from, to, path, want string }{
		{"Invoke returning something else",
			"rpc Invoke(Ask) returns (garm.agent.v1.RunRef)",
			"rpc Invoke(Ask) returns (Ask)",
			"bank.v1.SupportAssistant.Invoke",
			"Invoke must return garm.agent.v1.RunRef"},
		{"GetRun taking something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(Ask) returns (garm.agent.v1.RunStatus)",
			"bank.v1.SupportAssistant.GetRun",
			"GetRun must take garm.agent.v1.RunRef"},
		{"GetRun returning something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(garm.agent.v1.RunRef) returns (Ask)",
			"bank.v1.SupportAssistant.GetRun",
			"GetRun must return garm.agent.v1.RunStatus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := agentSrc(fullPolicy)
			files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"], tc.from, tc.to, 1)
			diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
			if !hasDiag(diags, "A1", tc.path, tc.want) {
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

// a2Tree writes a fixture whose prompts root is the temp tree itself, so the
// declared path "prompts/system.md" resolves the way it does in a real
// checkout: proto/ and prompts/ as siblings.
func a2Tree(t *testing.T, promptPath, sha, body string) (fds []protoreflect.FileDescriptor, root string) {
	t.Helper()
	root = t.TempDir()
	if body != "" {
		p := filepath.Join(root, "prompts", "system.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	policy := strings.Replace(fullPolicy,
		`prompts: { key: "system" value: { path: "prompts/support.md" sha256: "`+emptySHA+`" } }`,
		`prompts: { key: "system" value: { path: "`+promptPath+`" sha256: "`+sha+`" } }`, 1)
	return compileSource(t, agentSrc(policy)), root
}

const helloPrompt = "You are a support assistant. Answer from the tools you are given.\n"
const helloSHA = "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a"

func TestA2AcceptsAPromptThatExistsAndHashes(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", helloSHA, helloPrompt)
	for _, d := range compiler.LintWith(fds, compiler.Options{PromptsRoot: root}) {
		if d.Rule == "A2" {
			t.Errorf("a correct prompt produced an A2: %s", d.String())
		}
	}
}

func TestA2RefusesAPromptThatHashesDifferently(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", emptySHA, helloPrompt)
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
	if !hasError(diags, "A2", "but the declaration says") {
		t.Errorf("a drifted prompt was accepted:\n%s", render(diags))
	}
}

func TestA2RefusesAPromptThatIsNotThere(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", helloSHA, "")
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
	if !hasError(diags, "A2", "does not exist under the prompts root") {
		t.Errorf("a missing prompt was accepted:\n%s", render(diags))
	}
}

// Review Focus 3 — a declared path that leaves the tree. The linter must
// refuse the declaration rather than read, hash and bless whatever is there.
func TestA2RefusesAPromptPathThatEscapesTheRoot(t *testing.T) {
	for _, p := range []string{"../secrets.md", "prompts/../../secrets.md", "/etc/passwd"} {
		fds, root := a2Tree(t, p, helloSHA, helloPrompt)
		diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
		if !hasError(diags, "A2", "prompts root") {
			t.Errorf("the path %q was accepted:\n%s", p, render(diags))
		}
	}
}

// Review Focus 4 — the digest is lowercase hex with no prefix (program plan
// §3.1). An uppercase one never matches a computed digest and would also be a
// different object key on S3, so it is refused as malformed rather than
// reported as a mismatch.
func TestA2RefusesASha256ThatIsNotLowercaseHex(t *testing.T) {
	for _, tc := range []struct{ sha, want string }{
		{strings.ToUpper(helloSHA), "lowercase"},
		{"sha256:" + helloSHA, "no prefix"},
		{helloSHA[:32], "must be 64"},
	} {
		fds, root := a2Tree(t, "prompts/system.md", tc.sha, helloPrompt)
		diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
		if !hasError(diags, "A2", tc.want) {
			t.Errorf("the sha256 %q was accepted:\n%s", tc.sha, render(diags))
		}
	}
}

// The "system" key is required (program plan §3.1). An agent with no system
// prompt has no instructions, and the runner would hand the model an empty
// system message rather than refusing.
func TestA2RequiresASystemPrompt(t *testing.T) {
	policy := strings.Replace(fullPolicy, `key: "system"`, `key: "critic"`, 1)
	fds := compileSource(t, agentSrc(policy))
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: t.TempDir()})
	if !hasError(diags, "A2", `no "system" entry`) {
		t.Errorf("an agent with no system prompt was accepted:\n%s", render(diags))
	}
}

// With no prompts root there is nothing to resolve against. A2 says so, as a
// warning, rather than silently passing — a rule enforced only where nobody
// is looking is worse than no rule.
func TestA2WarnsWhenThereIsNoPromptsRoot(t *testing.T) {
	fds, _ := a2Tree(t, "prompts/system.md", helloSHA, helloPrompt)
	var warned bool
	for _, d := range compiler.LintWith(fds, compiler.Options{}) {
		if d.Rule == "A2" {
			if !d.Warn {
				t.Errorf("A2 is an error with no prompts root: %s", d.String())
			}
			warned = true
		}
	}
	if !warned {
		t.Error("A2 said nothing at all with no prompts root")
	}
}
