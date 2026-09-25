package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildInto compiles a one-tool tree and writes the artifact, so a test can
// diff two real catalogues rather than two hand-built structs.
func buildInto(t *testing.T, out, clearance string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "d", "v1", "d.proto")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `syntax = "proto3";
package d.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/gen/d_v1;x";
message In  { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } }; optional string id = 1; }
message Out { option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_INTERNAL on_deny: { omit: {} } }; optional string v = 1; }
service S {
  rpc Get(In) returns (Out) {
    option (garm.tool.v1.tool) = {
      name: "get" title: "T" description: "d."
      verb: VERB_READ min_clearance: ` + clearance + `
    };
  }
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newCatalogueBuildCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--proto", dir, "-o", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("building the fixture catalogue: %v", err)
	}
	return out
}

func TestDiffReportsAWideningAndSaysWhatItMeans(t *testing.T) {
	dir := t.TempDir()
	before := buildInto(t, filepath.Join(dir, "a.binpb"), "CLEARANCE_CONFIDENTIAL")
	after := buildInto(t, filepath.Join(dir, "b.binpb"), "CLEARANCE_INTERNAL")

	var out bytes.Buffer
	if err := runDiff(&out, before, after, false); err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	s := out.String()
	for _, want := range []string{"WIDENING", "min_clearance", "d.v1.get"} {
		if !strings.Contains(s, want) {
			t.Errorf("output does not mention %q:\n%s", want, s)
		}
	}
	// Both digests, so a reviewer can tell which artifacts were compared. A
	// diff whose inputs are unidentifiable is not evidence of anything.
	if strings.Count(s, "sha256:") != 2 {
		t.Errorf("output does not name both catalogues:\n%s", s)
	}
}

// The gate. Without the flag a widening is reported and the build stays green,
// which is right for a report and wrong for a gate; with it, a person has to
// agree.
func TestOnlyTheFlagTurnsAWideningIntoAFailure(t *testing.T) {
	dir := t.TempDir()
	before := buildInto(t, filepath.Join(dir, "a.binpb"), "CLEARANCE_CONFIDENTIAL")
	after := buildInto(t, filepath.Join(dir, "b.binpb"), "CLEARANCE_INTERNAL")

	if err := runDiff(&bytes.Buffer{}, before, after, false); err != nil {
		t.Errorf("reporting a widening failed the build without the flag: %v", err)
	}
	err := runDiff(&bytes.Buffer{}, before, after, true)
	if err == nil {
		t.Fatal("--fail-on-widening did not fail on a widening")
	}
	if !strings.Contains(err.Error(), "widening") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// Identical catalogues must pass the gate, or the gate blocks every pull
// request and is switched off within a week.
func TestAnUnchangedPolicyPassesTheGate(t *testing.T) {
	dir := t.TempDir()
	c := buildInto(t, filepath.Join(dir, "a.binpb"), "CLEARANCE_CONFIDENTIAL")

	var out bytes.Buffer
	if err := runDiff(&out, c, c, true); err != nil {
		t.Fatalf("an unchanged policy failed the gate: %v", err)
	}
	if !strings.Contains(out.String(), "no policy changes") {
		t.Errorf("output does not say nothing changed:\n%s", out.String())
	}
}

func TestDiffRejectsSomethingThatIsNotACatalogue(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.binpb")
	if err := os.WriteFile(junk, []byte("not a catalogue"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := buildInto(t, filepath.Join(dir, "a.binpb"), "CLEARANCE_INTERNAL")

	if err := runDiff(&bytes.Buffer{}, junk, good, false); err == nil {
		t.Error("garbage was accepted as a catalogue")
	}
}
