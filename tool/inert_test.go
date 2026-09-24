package tool_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDefIsInert is the reason this package exists.
//
// A Def describes a tool; it must not be able to decide anything about one.
// The moment this package can reach a policy engine, a resolver or a
// transport, "the declaration is data" stops being a fact about the build and
// becomes a thing someone has to remember — and the daemon and the generator
// both depend on it being a fact.
//
// Checked by dependency graph rather than by reading imports, because a
// transitive reach is exactly as fatal as a direct one and much easier to
// introduce by accident.
func TestDefIsInert(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	// Everything this package may reach, beyond the standard library and the
	// protobuf runtime: the generated annotations, and nothing else.
	const allowed = "github.com/garm-ai/garm/contracts/"

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(dep, "github.com/garm-ai/garm/") {
			continue // stdlib, protobuf, and other modules are not this test's business
		}
		if !strings.HasPrefix(dep, allowed) {
			t.Errorf("tool reaches %s; a declaration must not depend on anything that acts on it", dep)
		}
	}
}
