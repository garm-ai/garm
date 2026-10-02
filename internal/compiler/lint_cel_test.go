package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/garm-ai/celenv"

	"github.com/garm-ai/garm/internal/compile"
)

// TestTheCLIsCELEnvironmentAgreesWithCelenv pins the whole point of this
// unit's convergence: guardEnv — the CLI's own route to a CEL environment,
// now built in terms of celenv.EnvVars (lint_cel.go) — and celenv.Env called
// directly must compile the identical set of expressions the identical way,
// over the SAME registry and the SAME message. Before this file existed
// that was an assumption; a future change that lets the two drift apart —
// an extra EnvOption added to one and not the other, say — now fails here,
// at `go test`, rather than at 3am when a guard that lints clean in this
// repository fails to load at agentd or the STS.
//
// One of the handful is deliberately a field reached through an IMPORT
// (google.protobuf.Timestamp) rather than only fields declared directly on
// the request message: that is exactly where `celEnvFor`'s hand-rolled,
// per-call import walk and celenv's cel.TypeDescs(files) could have
// disagreed, and did not, before this test could have told anyone either
// way.
func TestTheCLIsCELEnvironmentAgreesWithCelenv(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("payments/v1/payments.proto", `syntax = "proto3";
package payments.v1;
import "garm/tool/v1/tool.proto";
import "google/protobuf/timestamp.proto";
option go_package = "example.com/payments/v1;paymentsv1";
message Pay {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 amount_minor_units = 1;
  optional google.protobuf.Timestamp requested_at = 2;
}
message Paid {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string payment_id = 1;
}
service Payments {
  rpc InitiatePayment(Pay) returns (Paid) {
    option (garm.tool.v1.tool) = {
      name: "initiate_payment" title: "Pay" description: "Move money."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`)
	_, fds, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	files, err := filesRegistry(fds)
	if err != nil {
		t.Fatalf("filesRegistry: %v", err)
	}
	tools, err := Tools(fds)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	md := tools[0].Method.Input()

	cliEnv, cliErr := guardEnv(md, files)
	if cliErr != nil {
		t.Fatalf("guardEnv: %v", cliErr)
	}
	celenvEnv, celenvErr := celenv.Env(files, md)
	if celenvErr != nil {
		t.Fatalf("celenv.Env: %v", celenvErr)
	}

	for _, tc := range []struct {
		name, expr string
		compiles   bool
	}{
		{"the request's own field", "args.amount_minor_units <= 500000", true},
		{"a field reached through an import", "args.requested_at > timestamp('2020-01-01T00:00:00Z')", true},
		{"a field the request does not have", "args.nope == 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cliAst, cliIss := cliEnv.Compile(tc.expr)
			celAst, celIss := celenvEnv.Compile(tc.expr)

			cliOK := cliIss == nil || cliIss.Err() == nil
			celOK := celIss == nil || celIss.Err() == nil
			if cliOK != tc.compiles {
				t.Errorf("the CLI's own environment: compiles = %v, want %v (issues: %v)",
					cliOK, tc.compiles, cliIss)
			}
			if celOK != tc.compiles {
				t.Errorf("celenv's environment: compiles = %v, want %v (issues: %v)",
					celOK, tc.compiles, celIss)
			}
			if cliOK != celOK {
				t.Fatalf("the two environments disagree on %q: CLI compiles=%v, celenv compiles=%v",
					tc.expr, cliOK, celOK)
			}
			if cliOK {
				if got, want := cliAst.OutputType().String(), celAst.OutputType().String(); got != want {
					t.Errorf("output types disagree on %q: CLI=%s, celenv=%s", tc.expr, got, want)
				}
			}
		})
	}
}
