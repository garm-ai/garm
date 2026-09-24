package contracts_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestContractsDependencyGraphStaysThin asserts the contracts module does not
// pull garm's server dependencies into a tool author's build (spec §1.2).
//
// A tool repo depends on contracts + garmtool and nothing else. If garm's
// graph leaks in, a small Go service starts compiling an embedded NATS
// server, DuckDB and minio — and, worse, becomes able to import the package
// that enforces the chain.
func TestContractsDependencyGraphStaysThin(t *testing.T) {
	forbidden := []string{
		"github.com/nats-io/nats-server",
		"github.com/marcboeker/go-duckdb",
		"github.com/minio/minio-go",
		"github.com/garm-ai/garm/toolplane",
		"github.com/garm-ai/garm/meter",
		"github.com/garm-ai/garm/policy",
	}
	// GOWORK=off: this test exists to guarantee what a CONSUMER of the
	// published contracts module sees, not what a developer working
	// inside this repo's go.work sees. Left to the ambient environment,
	// `go list` here resolves through go.work and go.work.sum, which
	// substitute for this module's own go.sum — masking exactly the class
	// of gap (a missing go.sum entry, an unbuildable standalone module)
	// this test's own existence is supposed to catch. Forcing it off here,
	// on the subprocess itself, makes that true regardless of whether the
	// caller happens to run this from inside or outside the workspace.
	cmd := exec.Command("go", "list", "-deps", "./...")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -deps failed: %v\n%s\n"+
			"If you just added an import to this module and it names toolplane, "+
			"toolpolicy or meter: that is the boundary working. Those packages enforce, "+
			"meter or store, and belong in the parent module (spec 1.2). Do NOT add a "+
			"require for github.com/garm-ai/garm to fix this — move the code instead.",
			err, stderr)
	}
	deps := string(out)
	for _, f := range forbidden {
		if strings.Contains(deps, f) {
			t.Fatalf("the contracts module depends on %q. Contracts carry messages and the "+
				"tool-side binding only; anything that enforces, meters or stores belongs "+
				"in the parent module (spec 1.2).", f)
		}
	}
}
