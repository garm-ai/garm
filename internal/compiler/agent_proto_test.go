package compiler_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

// garm.agent.v1's own messages are a governed tool's request and response, so
// every L-rule that walks a tool message walks them. An agent declaration must
// not fail the build on garm's own contract: that would make the annotation
// unusable and the failure would name the schema author's file, not this one.
//
// This is the test that pins disagreement D2. If RunRef.run_id loses its
// `optional`, L6 fires here rather than in a bank's catalogue at 3am.
func TestTheAgentContractMessagesPassGarmsOwnToolRules(t *testing.T) {
	const src = `syntax = "proto3";
package agentproto.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/agentproto/v1;x";
message StartRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
service Assistant {
  rpc Invoke(StartRequest) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "assistant" title: "Assistant" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "assistant_run" title: "Assistant run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "agentproto", "v1", "a.proto")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, fds, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	for _, d := range compiler.Lint(fds) {
		if !d.Warn {
			t.Errorf("garm.agent.v1's own messages fail garm's tool rules: %s", d.String())
		}
	}
}
