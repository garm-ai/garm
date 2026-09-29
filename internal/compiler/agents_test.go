package compiler_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

// compileSource writes a set of .proto files into a temp tree and compiles it,
// returning the resolved descriptors. Real source rather than hand-built
// descriptors: what is under test is how an option written by a schema author
// arrives, and assembling the descriptor by hand would test the assembly.
func compileSource(t *testing.T, files map[string]string) []protoreflect.FileDescriptor {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, fds, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return fds
}

// agentSrc is one well-formed agent service, parameterised by the parts the
// tests vary. The prompts map uses the real protobuf text format for a map
// field — `{ key: ... value: {...} }`. The abbreviated form that appears in
// the design document does not compile.
func agentSrc(policyBody string) map[string]string {
	return map[string]string{"bank/v1/agent.proto": `syntax = "proto3";
package bank.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
message Ask {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
service SupportAssistant {
  option (garm.agent.v1.agent) = {` + policyBody + `};
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant" title: "Support" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "support_assistant_run" title: "Support run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
}
`, "bank/v1/taxonomy.proto": `syntax = "proto3";
package bank.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/bank/v1;bankv1";
option (garm.tool.v1.tool_sets) = { declared: [{ name: "support" description: "Support desk." }] };
option (garm.tool.v1.compartments) = { declared: [{ name: "financial" description: "Money." }] };
`}
}

const fullPolicy = `
    mode: MODE_REACT
    principal: { clearance: CLEARANCE_CONFIDENTIAL compartments: ["financial"] }
    model: { alias: "fast" }
    bounds: { max_steps: 12 max_tokens: 200000 max_tool_calls: 30 timeout: { seconds: 600 } }
    prompts: { key: "system" value: { path: "prompts/support.md" sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" } }
    tools: [{ fqn: "bank.v1.support_assistant" }]
`

func TestAgentsFindsTheServiceAndItsTwoMethods(t *testing.T) {
	agents := compiler.Agents(compileSource(t, agentSrc(fullPolicy)))
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	a := agents[0]
	if got := string(a.Service.FullName()); got != "bank.v1.SupportAssistant" {
		t.Errorf("Service = %s, want bank.v1.SupportAssistant", got)
	}
	if a.Policy.GetMode() != 1 { // MODE_REACT
		t.Errorf("Mode = %v, want MODE_REACT", a.Policy.GetMode())
	}
	if a.Policy.GetPrincipal().GetClearance() != toolv1.Clearance_CLEARANCE_CONFIDENTIAL {
		t.Errorf("clearance = %v, want CONFIDENTIAL", a.Policy.GetPrincipal().GetClearance())
	}
	if a.Invoke == nil || string(a.Invoke.Name()) != "Invoke" {
		t.Errorf("Invoke = %v, want the Invoke method", a.Invoke)
	}
	if a.GetRun == nil || string(a.GetRun.Name()) != "GetRun" {
		t.Errorf("GetRun = %v, want the GetRun method", a.GetRun)
	}
}

// A service with no agent option is not an agent. Most services are not.
func TestAgentsIgnoresAnUnannotatedService(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["plain/v1/plain.proto"] = `syntax = "proto3";
package plain.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/plain/v1;plainv1";
message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string a = 1;
}
service Plain {
  rpc Go(In) returns (In) {
    option (garm.tool.v1.tool) = {
      name: "go" title: "Go" description: "A plain tool."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if got := len(compiler.Agents(compileSource(t, files))); got != 1 {
		t.Fatalf("found %d agents, want 1: a service with no agent option is not an agent", got)
	}
}

// GetRun is optional (design §2.2, A1 says "at most one"), so the loader must
// return the agent with a nil GetRun rather than skipping it — otherwise A1
// could never report anything about such a service.
func TestAgentsReturnsANilGetRunWhenTheServiceDeclaresNone(t *testing.T) {
	files := agentSrc(fullPolicy)
	src := files["bank/v1/agent.proto"]
	cut := strings.Index(src, "  rpc GetRun")
	files["bank/v1/agent.proto"] = src[:cut] + "}\n"
	agents := compiler.Agents(compileSource(t, files))
	if len(agents) != 1 {
		t.Fatalf("found %d agents, want 1", len(agents))
	}
	if agents[0].GetRun != nil {
		t.Error("GetRun is not nil for a service that declares none")
	}
	if agents[0].Invoke == nil {
		t.Error("Invoke went missing")
	}
}

func TestPromptRefsCarriesTheKeyThePathAndTheHash(t *testing.T) {
	refs := compiler.PromptRefs(compileSource(t, agentSrc(fullPolicy)))
	if len(refs) != 1 {
		t.Fatalf("found %d prompt refs, want 1", len(refs))
	}
	got := refs[0]
	want := compiler.PromptRef{
		Agent:  "bank.v1.SupportAssistant",
		Key:    "system",
		Path:   "prompts/support.md",
		SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	if got != want {
		t.Errorf("PromptRefs()[0] = %+v, want %+v", got, want)
	}
}

// A map iterates in random order in Go, and a golden file cannot exist against
// a random order. Two prompts, sorted by key, every time.
func TestPromptRefsIsOrderedByKey(t *testing.T) {
	policy := strings.Replace(fullPolicy,
		`tools: [{ fqn: "bank.v1.support_assistant" }]`,
		`tools: [{ fqn: "bank.v1.support_assistant" }]
    prompts: { key: "critic" value: { path: "prompts/critic.md" sha256: "`+emptySHA+`" } }`, 1)
	refs := compiler.PromptRefs(compileSource(t, agentSrc(policy)))
	if len(refs) != 2 {
		t.Fatalf("found %d prompt refs, want 2", len(refs))
	}
	if refs[0].Key != "critic" || refs[1].Key != "system" {
		t.Errorf("keys are %q, %q; want critic then system — a map's iteration "+
			"order is random and a golden file cannot exist against one",
			refs[0].Key, refs[1].Key)
	}
}

// emptySHA is sha256 of the empty string: a well-formed digest that is
// almost certainly not the digest of any prompt anyone wrote.
const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestValidatePromptPathRefusesWhatEscapesTheRoot(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"", "is empty"},
		{"/etc/passwd", "must be relative"},
		{"../secrets.md", "escapes"},
		{"prompts/../../secrets.md", "escapes"},
		{`prompts\system.md`, "must use forward slashes"},
	} {
		err := compiler.ValidatePromptPath(tc.path)
		if err == nil {
			t.Errorf("ValidatePromptPath(%q) = nil; a prompt path is resolved "+
				"against a root and must stay inside it", tc.path)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidatePromptPath(%q) = %v, want it to mention %q", tc.path, err, tc.want)
		}
	}
	for _, ok := range []string{"prompts/system.md", "a/b/c.md", "system.md", "./system.md"} {
		if err := compiler.ValidatePromptPath(ok); err != nil {
			t.Errorf("ValidatePromptPath(%q) = %v, want nil", ok, err)
		}
	}
}

func TestValidatePromptSHA256InsistsOnLowercaseHex(t *testing.T) {
	upper := strings.ToUpper(emptySHA)
	for _, tc := range []struct{ in, want string }{
		{"", "is empty"},
		{"sha256:" + emptySHA, "no prefix"},
		{upper, "lowercase"},
		{emptySHA[:63], "64"},
		{emptySHA[:63] + "z", "hex"},
	} {
		err := compiler.ValidatePromptSHA256(tc.in)
		if err == nil {
			t.Errorf("ValidatePromptSHA256(%q) = nil, want an error about %s", tc.in, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidatePromptSHA256(%q) = %v, want it to mention %q", tc.in, err, tc.want)
		}
	}
	if err := compiler.ValidatePromptSHA256(emptySHA); err != nil {
		t.Errorf("ValidatePromptSHA256(%q) = %v, want nil", emptySHA, err)
	}
}
