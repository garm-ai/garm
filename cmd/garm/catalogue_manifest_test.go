package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	cataloguev1 "github.com/garm-ai/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/manifest"
)

// buildIn runs `catalogue build` with the working directory inside a tree, which
// is the only way to test the convention: the manifest is found in the working
// directory precisely so that the common case needs no flag.
func buildIn(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	var out, errs bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"catalogue", "build"}, args...))
	root.SetOut(&out)
	root.SetErr(&errs)
	err := root.Execute()
	return out.String(), errs.String(), err
}

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) *cataloguev1.Catalogue {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cat := &cataloguev1.Catalogue{}
	if err := proto.Unmarshal(b, cat); err != nil {
		t.Fatal(err)
	}
	return cat
}

// `garm catalogue build`, no arguments. That is the target of the whole design:
// the manifest is the primary input and a flag naming a directory is not needed
// to find it.
func TestBuildFindsTheManifestByConvention(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, `schema: v1
name: acme
source: garm-ai/garm/cmd/garm
include:
  - path: proto
`)
	out := filepath.Join(t.TempDir(), "c.binpb")
	stdout, _, err := buildIn(t, dir, "-o", out)
	if err != nil {
		t.Fatalf("catalogue build with no input flag: %v", err)
	}
	if !strings.Contains(stdout, "acme.v1.add") {
		t.Errorf("the tool is missing from the output:\n%s", stdout)
	}

	// And the artifact records what it was composed from, which is the fact a
	// catalogue built from one directory could not state.
	inputs := read(t, out).GetProvenance().GetInputs()
	if len(inputs) != 1 {
		t.Fatalf("provenance records %d input(s), want 1", len(inputs))
	}
	local := inputs[0].GetLocal()
	if local == nil || local.GetPath() != "proto" || local.GetSource() != "garm-ai/garm/cmd/garm" {
		t.Errorf("recorded input: %v", inputs[0])
	}
	if got := strings.Join(inputs[0].GetProtoPackages(), ","); got != "acme.v1" {
		t.Errorf("recorded packages %q, want acme.v1", got)
	}
}

// The manifest's own `include:` entries are what compose, not whatever
// happens to sit in a conventionally named directory beside it.
func TestTheManifestsOwnEntriesCompose(t *testing.T) {
	dir := fixture(t)
	// A second tree the manifest names instead of proto/.
	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(filepath.Join(other, "acme", "v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "proto", "acme", "v1", "calc.proto"))
	if err != nil {
		t.Fatal(err)
	}
	// The same service in a second package, so which tool appears says which
	// input was composed.
	src := strings.ReplaceAll(string(b), "package acme.v1;", "package acme.v2;")
	src = strings.ReplaceAll(src, "acme/gen/acme/v1;acmev1", "acme/gen/acme/v2;acmev2")
	if err := os.WriteFile(filepath.Join(other, "acme", "v2", "calc.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: other\n")

	out := filepath.Join(t.TempDir(), "c.binpb")
	stdout, _, err := buildIn(t, dir, "-o", out)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(stdout, "acme.v2.add") || strings.Contains(stdout, "acme.v1.add") {
		t.Fatalf("the manifest's input was not the one composed:\n%s", stdout)
	}
}

// Neither a manifest named by flag nor one found by convention: say so,
// rather than reporting a compiler error about a directory nobody asked for —
// and name the directory it looked in and what to run.
func TestBuildSaysSoWhenThereIsNoInputAtAll(t *testing.T) {
	dir := t.TempDir()
	_, _, err := buildIn(t, dir, "-o", filepath.Join(t.TempDir(), "c.binpb"))
	if err == nil {
		t.Fatal("a directory with no input built a catalogue")
	}
	for _, want := range []string{manifest.Filename, dir, "garm catalogue init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// -f takes a manifest elsewhere, and paths inside it resolve against the
// manifest rather than against the working directory — a manifest is the
// authority on where its own inputs are.
func TestBuildTakesAManifestByFlag(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	out := filepath.Join(t.TempDir(), "c.binpb")
	stdout, _, err := buildIn(t, t.TempDir(), "-f", filepath.Join(dir, manifest.Filename), "-o", out)
	if err != nil {
		t.Fatalf("build -f: %v", err)
	}
	if !strings.Contains(stdout, "acme.v1.add") {
		t.Errorf("the tool is missing:\n%s", stdout)
	}
}

// --source overrides the manifest, because a pipeline knows a commit that a
// checked-in file cannot.
func TestSourceFlagOverridesTheManifest(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\nsource: from-the-file\ninclude:\n  - path: proto\n")
	out := filepath.Join(t.TempDir(), "c.binpb")
	if _, _, err := buildIn(t, dir, "-o", out, "--source", "from-the-flag"); err != nil {
		t.Fatalf("build: %v", err)
	}
	cat := read(t, out)
	if got := cat.GetProvenance().GetSource(); got != "from-the-flag" {
		t.Errorf("provenance source is %q", got)
	}
	if got := cat.GetProvenance().GetInputs()[0].GetLocal().GetSource(); got != "from-the-flag" {
		t.Errorf("the local input's source is %q", got)
	}
}
