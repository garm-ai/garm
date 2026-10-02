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

// A tree with no manifest still builds from proto/, and identically.
//
// Every runbook and CI pipeline in the estate passes --proto today, and this is
// a minor release. The deprecated flag is the single-input case of a manifest —
// one `path:` entry — so it goes through the same composition and produces the
// same bytes as the manifest that replaces it.
func TestBuildFallsBackToTheProtoDirectory(t *testing.T) {
	dir := fixture(t)
	// `garm init` writes a manifest now, so the pre-manifest state has to be
	// made rather than assumed: with the file in place this test would compare a
	// manifest against a manifest and the fallback would go unexercised.
	if err := os.Remove(filepath.Join(dir, manifest.Filename)); err != nil {
		t.Fatal(err)
	}
	viaFallback := filepath.Join(t.TempDir(), "fallback.binpb")
	if _, _, err := buildIn(t, dir, "-o", viaFallback); err != nil {
		t.Fatalf("a tree with no manifest did not build: %v", err)
	}

	viaManifest := filepath.Join(t.TempDir(), "manifest.binpb")
	writeManifest(t, dir, "schema: v1\nname: proto\ninclude:\n  - path: proto\n")
	if _, _, err := buildIn(t, dir, "-o", viaManifest); err != nil {
		t.Fatalf("the equivalent manifest did not build: %v", err)
	}

	a, b := read(t, viaFallback), read(t, viaManifest)
	ab, err := proto.MarshalOptions{Deterministic: true}.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := proto.MarshalOptions{Deterministic: true}.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ab) != string(bb) {
		t.Fatal("--proto and a one-entry manifest produced different artifacts")
	}
}

// A manifest wins over the fallback whenever there is one, or a migrated tree
// would silently keep building the pre-migration way.
func TestAManifestWinsOverTheFallback(t *testing.T) {
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

// --proto in a tree that already has a catalogue.yaml is the same mistake as
// --manifest and --proto together, made implicitly: the tree has said what
// composes into its catalogue, and the flag asks for one directory of it to
// be judged as the whole. Refuse, naming both inputs, rather than silently
// building a subset and calling it the catalogue.
func TestBuildRefusesProtoWhenAManifestIsPresent(t *testing.T) {
	dir := fixture(t) // `garm init` already wrote catalogue.yaml here.
	out := filepath.Join(t.TempDir(), "c.binpb")
	_, _, err := buildIn(t, dir, "--proto", "proto", "-o", out)
	if err == nil {
		t.Fatal("--proto was accepted on a tree that already has a manifest")
	}
	if !strings.Contains(err.Error(), "two different inputs") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	for _, want := range []string{"proto", manifest.Filename} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// --proto and --manifest name two different inputs, and picking one silently
// would build something other than what was asked for.
func TestBuildRefusesAManifestAndAProtoDirectoryTogether(t *testing.T) {
	dir := fixture(t)
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")
	_, _, err := buildIn(t, dir, "-f", manifest.Filename, "--proto", "proto")
	if err == nil {
		t.Fatal("both input flags were accepted")
	}
	if !strings.Contains(err.Error(), "two different inputs") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

// Neither a manifest nor a proto directory: say so, rather than reporting a
// compiler error about a directory nobody asked for.
func TestBuildSaysSoWhenThereIsNoInputAtAll(t *testing.T) {
	_, _, err := buildIn(t, t.TempDir(), "-o", filepath.Join(t.TempDir(), "c.binpb"))
	if err == nil {
		t.Fatal("a directory with no input built a catalogue")
	}
	for _, want := range []string{manifest.Filename, "proto/"} {
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
