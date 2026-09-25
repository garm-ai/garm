package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lint runs `garm lint` against a scaffolded tree and reports what the author
// sees: the error the command exits on, and everything it printed.
func lint(t *testing.T, dir string) (error, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRoot()
	root.SetArgs([]string{"lint", "--proto", filepath.Join(dir, "proto")})
	root.SetOut(&out)
	root.SetErr(&errOut)
	return root.Execute(), out.String(), errOut.String()
}

// append adds a declaration to the fixture's one proto file.
func appendProto(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, "proto", "acme", "v1", "calc.proto")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

// TestLintAcceptsACleanTree is the case that has to work without buf
// installed, because that is the command's entire reason to exist: an author
// checking a declaration should not first have to install a toolchain, and a
// linter people cannot run easily is one they skip.
func TestLintAcceptsACleanTree(t *testing.T) {
	err, out, errOut := lint(t, fixture(t))
	if err != nil {
		t.Fatalf("lint on a clean tree: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("stdout = %q, want a line saying the tree is clean", out)
	}
}

// TestLintFailsOnAPolicyError pins the exit status, not just the message.
//
// `garm lint` in a pre-commit hook or a CI step is judged by whether it
// returns non-zero; one that prints errors and exits 0 is a check everyone
// believes is running and nothing is enforced by.
func TestLintFailsOnAPolicyError(t *testing.T) {
	dir := fixture(t)
	appendProto(t, dir, `
message NukeRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  string id = 1;
}
message NukeResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  string id = 1;
}
service Danger {
  rpc Nuke(NukeRequest) returns (NukeResponse) {
    option (garm.tool.v1.tool) = {
      name: "nuke" title: "Nuke" description: "Irreversible."
      verb: VERB_DESTRUCTIVE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`)
	err, _, errOut := lint(t, dir)
	if err == nil {
		t.Fatal("lint accepted a destructive tool that declares no supervision")
	}
	if !strings.Contains(errOut, "acme.v1.Danger.Nuke") {
		t.Errorf("stderr = %q, want the offending method named", errOut)
	}
}

// TestLintReportsAWarningWithoutFailing. Warnings exist for declarations that
// are legal and usually a mistake; failing on them would make the only way
// past a judgement call an edit to the declaration, and a linter that cannot
// say "look at this" without blocking ends up saying nothing.
func TestLintReportsAWarningWithoutFailing(t *testing.T) {
	dir := fixture(t)
	appendProto(t, dir, `
message OddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string note = 1 [(garm.tool.v1.field_policy) = {
    read: CLEARANCE_CONFIDENTIAL write: CLEARANCE_PUBLIC on_deny: { omit: {} }
  }];
}
service Odd {
  rpc Look(OddRequest) returns (OddRequest) {
    option (garm.tool.v1.tool) = {
      name: "look" title: "Look" description: "Read a note."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`)
	err, _, errOut := lint(t, dir)
	if err != nil {
		t.Fatalf("lint failed on a warning-only tree: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "warning: L10") {
		t.Errorf("stderr = %q, want the L10 warning reported even though it does not fail", errOut)
	}
}

// TestLintReportsAnUncompilableTree separately from a policy error: the
// author has a syntax error, and a linter that answers "0 file(s), no errors"
// to a tree it could not read is the worst of both.
func TestLintReportsAnUncompilableTree(t *testing.T) {
	dir := fixture(t)
	appendProto(t, dir, "message Unclosed {\n")
	err, out, _ := lint(t, dir)
	if err == nil {
		t.Fatalf("lint accepted a tree that does not compile; it printed %q", out)
	}
	if !strings.Contains(err.Error(), "calc.proto") {
		t.Errorf("error = %v, want the offending file named", err)
	}
}
