package compile_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
)

// A tree that declares a constraint must compile.
//
// Without protovalidate linked, `buf/validate/validate.proto: not found`
// refuses any tree that validates anything — which is every tree worth
// governing, and it fails at the producer, so no catalogue could ever carry a
// constraint the daemon is perfectly able to enforce. That is a whole feature
// unreachable through the only path that builds an artifact.
//
// Linked rather than vendored for the reason the annotations are: this
// binary's copy is authoritative, so a stale file in somebody's third_party
// cannot quietly change what a constraint means — and producer and consumer
// stay pinned to one version of the constraint schema.
func TestATreeThatDeclaresConstraintsCompiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "v/v1/v.proto"), `syntax = "proto3";
package v.v1;
import "buf/validate/validate.proto";
option go_package = "example.com/gen/v_v1;x";
message M {
  string id = 1 [(buf.validate.field).string.min_len = 5];
  option (buf.validate.message).cel = {
    id: "m.rule"
    message: "id must not be the literal 'admin'"
    expression: "this.id != 'admin'"
  };
}
`)
	set, _, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("a tree using buf.validate did not compile: %v", err)
	}
	if len(set.GetFile()) == 0 {
		t.Fatal("no files in the descriptor set")
	}

	// The import must be IN the set, not merely resolvable. A catalogue whose
	// descriptors reference a file it does not carry cannot be rebuilt into a
	// registry by the daemon.
	var haveValidate bool
	for _, f := range set.GetFile() {
		if f.GetName() == "buf/validate/validate.proto" {
			haveValidate = true
		}
	}
	if !haveValidate {
		t.Error("buf/validate/validate.proto resolved but was not carried in the " +
			"descriptor set; the daemon would not be able to rebuild the registry")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
