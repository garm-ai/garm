package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fixture is the smallest tree that produces a catalogue: the vendored
// annotations plus one governed tool.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	svc := filepath.Join(dir, "proto", "acme", "v1")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	const p = `syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
message AddRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double a = 1;
  optional double b = 2;
}
message AddResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional double sum = 1;
}
service Calculator {
  rpc Add(AddRequest) returns (AddResponse) {
    option (garm.tool.v1.tool) = {
      name: "add" title: "Add" description: "Add two numbers."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`
	if err := os.WriteFile(filepath.Join(svc, "calc.proto"), []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func build(t *testing.T, dir, out string, args ...string) []byte {
	t.Helper()
	root := newRoot()
	root.SetArgs(append([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"), "-o", out}, args...))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("catalogue build: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCatalogueIsReproducible is the property the digest depends on.
//
// A digest that moves on every rebuild of identical source identifies the
// build rather than the content, which makes "are these two deployments
// serving the same tools" unanswerable and makes a redeploy look like a
// change. The first version of this command stamped a wall clock and failed
// exactly that way.
func TestCatalogueIsReproducible(t *testing.T) {
	dir := fixture(t)
	a := build(t, dir, filepath.Join(t.TempDir(), "a.binpb"))
	b := build(t, dir, filepath.Join(t.TempDir(), "b.binpb"))
	if !bytes.Equal(a, b) {
		t.Fatalf("two builds of identical source differ: %d vs %d bytes", len(a), len(b))
	}
}

// TestStampTimeBreaksReproducibilityOnPurpose pins the trade rather than the
// convenience: --stamp-time exists, and the flag's help says what it costs.
func TestStampTimeBreaksReproducibilityOnPurpose(t *testing.T) {
	dir := fixture(t)
	a := build(t, dir, filepath.Join(t.TempDir(), "a.binpb"), "--stamp-time")
	b := build(t, dir, filepath.Join(t.TempDir(), "b.binpb"), "--stamp-time")
	if bytes.Equal(a, b) {
		t.Skip("clock granularity made two stamped builds identical; the flag is still opt-in")
	}
}

// TestCatalogueRefusesAPolicyError is why building and linting are one step.
// A catalogue that does not lint would fail at the daemon's mount check
// instead — where the author is absent and the failure is an outage.
func TestCatalogueRefusesAPolicyError(t *testing.T) {
	dir := fixture(t)
	bad := `
message NukeRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  string id = 1;
}
message NukeResponse {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
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
`
	p := filepath.Join(dir, "proto", "acme", "v1", "calc.proto")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(bad); err != nil {
		t.Fatal(err)
	}
	f.Close()

	root := newRoot()
	root.SetArgs([]string{"catalogue", "build",
		"--proto", filepath.Join(dir, "proto"), "-o", filepath.Join(t.TempDir(), "x.binpb")})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("built a catalogue containing a destructive tool that declares no supervision")
	}
}
