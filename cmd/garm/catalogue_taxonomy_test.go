package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool requiring a word, and a manifest declaring it. Nothing in the tree's
// protos declares a vocabulary at all — which is the end state §7 is after, and
// the state a deployment reaches by deleting the taxonomy proto it was carrying
// only so that `buf generate` would pass.
func scopedFixture(t *testing.T, set string) string {
	t.Helper()
	dir := fixture(t)
	svc := filepath.Join(dir, "proto", "acme", "v1", "calc.proto")
	b, err := os.ReadFile(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(b),
		"verb: VERB_READ min_clearance: CLEARANCE_PUBLIC",
		"verb: VERB_READ min_clearance: CLEARANCE_PUBLIC sets: [\""+set+"\"]", 1)
	if body == string(b) {
		t.Fatal("the fixture did not change; the replace missed")
	}
	if err := os.WriteFile(svc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The end-to-end case the whole of §7 is for, and the one step 4 could not
// deliver: a tool names `research`, no proto in the tree declares it, and the
// catalogue builds because `catalogue.yaml` declares it.
func TestACatalogueBuildsOnAVocabularyOnlyTheManifestDeclares(t *testing.T) {
	dir := scopedFixture(t, "research")
	writeManifest(t, dir, `schema: v1
name: acme
include:
  - path: proto
taxonomy:
  compartments:
    - name: internet
      description: Content fetched from the public internet.
  tool_sets:
    - name: research
      description: Reading public sources.
`)
	out := filepath.Join(t.TempDir(), "c.binpb")
	if _, errs, err := buildIn(t, dir, "-o", out); err != nil {
		t.Fatalf("a catalogue whose vocabulary is declared in its manifest was refused: %v\n%s",
			err, errs)
	}

	// And the artifact carries it, on Catalogue's existing fields 3 and 4, which
	// is what makes this not a contract change: garmd's registry construction is
	// untouched and only the source of the words moved.
	cat := read(t, out)
	if len(cat.GetToolSets()) != 1 || cat.GetToolSets()[0].GetName() != "research" {
		t.Errorf("field 4 carries %v; the manifest's tool sets have not reached the artifact, "+
			"so a daemon would build its registry without them", cat.GetToolSets())
	}
	if len(cat.GetCompartments()) != 1 || cat.GetCompartments()[0].GetName() != "internet" {
		t.Errorf("field 3 carries %v", cat.GetCompartments())
	}
	// The description travels too. It is what somebody reads when deciding which
	// roles hold the compartment, and losing it here would lose it everywhere.
	if got := cat.GetCompartments()[0].GetDescription(); !strings.Contains(got, "public internet") {
		t.Errorf("the compartment reached the artifact with description %q", got)
	}
}

// And the refusal that makes declaring worth anything: a word the tool requires
// and the deployment has not declared. This is the consent gate — an adopted tool
// naming a compartment nobody agreed to is an error the deployment resolves
// deliberately, by declaring the word or by not adopting the tool.
func TestACatalogueIsRefusedForAWordTheDeploymentHasNotDeclared(t *testing.T) {
	dir := scopedFixture(t, "research")
	writeManifest(t, dir, `schema: v1
name: acme
include:
  - path: proto
taxonomy:
  compartments: []
  tool_sets:
    - name: payments
      description: Initiating and inspecting payments.
`)
	out := filepath.Join(t.TempDir(), "c.binpb")
	_, errs, err := buildIn(t, dir, "-o", out)
	if err == nil {
		t.Fatal("a tool requiring `research` built against a manifest that declares only " +
			"`payments`. Nothing then stops an adopted tool from bringing its own " +
			"vocabulary, which is the defect the key exists to close")
	}
	if !strings.Contains(errs, "L20") || !strings.Contains(errs, "research") {
		t.Errorf("the refusal does not name the rule and the word: %s", errs)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("an artifact was written for a tree that does not lint")
	}
}

// A tree that declares no taxonomy keeps the old behaviour exactly, and this is
// the compatibility assertion the migration rests on: every catalogue in the
// estate declares its vocabulary in proto options today, including the one the
// live plane runs.
func TestATreeWithNoTaxonomyKeyStillScrapesItsProtos(t *testing.T) {
	dir := scopedFixture(t, "research")
	// The vocabulary where it has always been: a file-level option.
	tax := filepath.Join(dir, "proto", "acme", "v1", "taxonomy.proto")
	if err := os.WriteFile(tax, []byte(`syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
option (garm.tool.v1.tool_sets) = {
  declared: [{ name: "research" description: "Reading public sources." }]
};
`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "schema: v1\nname: acme\ninclude:\n  - path: proto\n")

	out := filepath.Join(t.TempDir(), "c.binpb")
	if _, errs, err := buildIn(t, dir, "-o", out); err != nil {
		t.Fatalf("a tree declaring its vocabulary in proto options, with no `taxonomy:` "+
			"key, must build exactly as it did before v0.22.0: %v\n%s", err, errs)
	}
	if sets := read(t, out).GetToolSets(); len(sets) != 1 || sets[0].GetName() != "research" {
		t.Errorf("the scraped vocabulary did not reach the artifact: %v", sets)
	}
}

// Declaring a taxonomy REPLACES the scrape. The adopted file still declares
// `research` and the deployment does not, so the build is refused — otherwise
// adopting a tool would go on extending the vocabulary silently and the key would
// be decoration.
func TestDeclaringATaxonomyOverridesWhatTheProtosDeclare(t *testing.T) {
	dir := scopedFixture(t, "research")
	tax := filepath.Join(dir, "proto", "acme", "v1", "taxonomy.proto")
	if err := os.WriteFile(tax, []byte(`syntax = "proto3";
package acme.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/acme/gen/acme/v1;acmev1";
option (garm.tool.v1.tool_sets) = {
  declared: [{ name: "research" description: "Arrived with somebody else's tool." }]
};
`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, `schema: v1
name: acme
include:
  - path: proto
taxonomy:
  compartments: []
  tool_sets:
    - name: payments
      description: Initiating and inspecting payments.
`)
	out := filepath.Join(t.TempDir(), "c.binpb")
	if _, errs, err := buildIn(t, dir, "-o", out); err == nil {
		t.Errorf("the proto's own declaration of `research` was still honoured beside a "+
			"manifest that declares only `payments`, so the vocabulary is a UNION and a "+
			"deployment still cannot say which words govern it.\n%s", errs)
	}
}

// `claims check` compares a claims policy against a catalogue's vocabulary, and
// it used to get that vocabulary by re-scraping the descriptors' file options.
// That silently stopped being right the moment a deployment could declare one in
// its manifest: such a catalogue has the words on fields 3 and 4 and in no proto
// option at all, so the command would have refused every migrated catalogue for
// "declaring no compartments and no tool sets" — the vacuity gate firing on a
// vocabulary that was right there in the bytes it was handed.
//
// It reads fields 3 and 4 now, which is also the better reading for a tree that
// has not migrated: those are the words garmd builds its registry from, so a
// policy is checked against what the daemon will actually honour.
func TestClaimsCheckReadsAVocabularyDeclaredInTheManifest(t *testing.T) {
	dir := scopedFixture(t, "research")
	writeManifest(t, dir, `schema: v1
name: acme
include:
  - path: proto
taxonomy:
  compartments:
    - name: internet
      description: Content fetched from the public internet.
  tool_sets:
    - name: research
      description: Reading public sources.
`)
	cat := filepath.Join(t.TempDir(), "c.binpb")
	if _, errs, err := buildIn(t, dir, "-o", cat); err != nil {
		t.Fatalf("build: %v\n%s", err, errs)
	}

	policy := writePolicy(t, `
roles:
  analyst: { compartments: [internet], tool_sets: [research] }
`)
	stdout, stderr, err := execClaimsCheck(t, policy, "--against", cat)
	if err != nil {
		t.Fatalf("claims check refused a policy naming exactly the words the manifest "+
			"declares: %v (stderr: %s)", err, stderr)
	}
	if stderr != "" {
		t.Errorf("findings on a policy that names only declared vocabulary: %q", stderr)
	}
	if !strings.HasPrefix(stdout, "ok —") {
		t.Errorf("stdout = %q", stdout)
	}

	// And it still finds an undeclared one, so reading the artifact's own fields
	// has not turned the gate into a rubber stamp.
	bad := writePolicy(t, `
roles:
  analyst: { compartments: [internet], tool_sets: [payments] }
`)
	if _, stderr, err := execClaimsCheck(t, bad, "--against", cat); err == nil {
		t.Errorf("accepted a role naming the undeclared tool set `payments`: %s", stderr)
	} else if !strings.Contains(stderr, "payments") {
		t.Errorf("the finding does not name the undeclared word: %s", stderr)
	}
}
