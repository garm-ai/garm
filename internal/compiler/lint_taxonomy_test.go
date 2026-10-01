package compiler_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// The vocabulary a deployment's tools are checked against moved out of file-level
// proto options and into `catalogue.yaml` in v0.22.0, and these are the claims
// that move makes.
//
// The reason is consent rather than tidiness. Unioning a file-level option across
// every file in the set means adopting a tool EXTENDS the deployment's
// access-control vocabulary: adopt the web fetcher and `internet` becomes a
// compartment of your bank, because the tool's own proto declares it. The
// deployment never said yes to the word, it said yes to the tool — and for the
// vocabulary that governs who may see what, that is the wrong direction of
// consent.

// declared is the shape Options.Taxonomy takes: the contract's own Decl, so
// nothing converts these on the way to the artifact's fields 3 and 4.
func declared(names ...string) []*toolv1.Decl {
	out := make([]*toolv1.Decl, 0, len(names))
	for _, n := range names {
		out = append(out, &toolv1.Decl{Name: n, Description: "declared by the deployment."})
	}
	return out
}

// namesSet is a tool naming one tool set and nothing else unusual.
func namesSet(set string) *toolv1.ToolPolicy {
	tool := validTool("get")
	tool.Sets = []string{set}
	return tool
}

// The case the bank was blocked on. `research` is declared by the DEPLOYMENT and
// by no proto in the set, and the tool that names it must lint clean — which is
// the whole point of moving the vocabulary, and until it holds the bank cannot
// delete its copy of somebody else's `tools/taxonomy`.
func TestAToolSetDeclaredOnlyByTheManifestIsDeclared(t *testing.T) {
	fds := lintFixture(t, "manifest_set", validField(), nil,
		methodSpec{name: "Get", tool: namesSet("research")})

	// The control first: with no vocabulary at all, `research` is undeclared.
	// Without this the assertion below would pass on a rule that never fires.
	if !hasRule(compiler.Lint(fds), "L20", false) {
		t.Fatal("the control failed: with no declared vocabulary anywhere, `research` " +
			"must be an undeclared tool set")
	}

	diags := compiler.LintWith(fds, compiler.Options{
		Taxonomy: &compiler.Taxonomy{ToolSets: declared("research")},
	})
	for _, d := range diags {
		if d.Rule == "L20" {
			t.Errorf("a tool set the manifest declares is reported undeclared: %s.\n"+
				"This is the blocker: L20 firing on a set declared outside the protos is "+
				"what forced a deployment to keep a copy of an adopted taxonomy proto in "+
				"its own tree so that `buf generate` would pass", d)
		}
	}
}

// The same for a compartment, which travels a different path: through
// policy.NewRegistry and out as L7 rather than through the tool-set map.
func TestACompartmentDeclaredOnlyByTheManifestIsDeclared(t *testing.T) {
	field := validField()
	field.Compartments = []string{"internet"}
	fds := lintFixture(t, "manifest_compartment", field, nil,
		methodSpec{name: "Get", tool: validTool("get")})

	if !hasRule(compiler.Lint(fds), "L7", false) {
		t.Fatal("the control failed: `internet` must be an undeclared compartment " +
			"when nothing declares it")
	}

	diags := compiler.LintWith(fds, compiler.Options{
		Taxonomy: &compiler.Taxonomy{Compartments: declared("internet")},
	})
	if hasRule(diags, "L7", false) || hasRule(diags, "L7", true) {
		t.Errorf("a compartment the manifest declares is reported undeclared: %v", diags)
	}
}

// And the other half of the same claim: a manifest that declares a vocabulary
// REPLACES the scrape rather than adding to it. A union would preserve exactly
// the silent extension the key exists to end — the adopted tool's own word would
// still be in the deployment's vocabulary, and the deployment would still never
// have said yes to it.
func TestADeclaredVocabularyReplacesTheProtosRatherThanJoiningThem(t *testing.T) {
	// A tool naming `research`, beside an adopted file declaring it — which is
	// the bank's shape exactly: the word arrives with the tool.
	tool := lintFixture(t, "adopted_tool", validField(), nil,
		methodSpec{name: "Get", tool: namesSet("research")})
	adopted := declFile(t, "compiler/testdata/lint/adopted_taxonomy.proto",
		toolv1.E_ToolSets, &toolv1.Decl{Name: "research", Description: "Reading public sources."})
	fds := append(append([]protoreflect.FileDescriptor{}, tool...), adopted)

	// Today's behaviour, and the defect: the adopted file's declaration is
	// enough, so the deployment's vocabulary silently grew a word.
	if hasRule(compiler.Lint(fds), "L20", false) {
		t.Fatal("the control failed: an adopted file declaring `research` should make " +
			"the tool lint clean when the protos ARE the vocabulary")
	}

	diags := compiler.LintWith(fds, compiler.Options{
		Taxonomy: &compiler.Taxonomy{ToolSets: declared("payments")},
	})
	if !hasRule(diags, "L20", false) {
		t.Errorf("a manifest declaring only `payments` left `research` declared, so the "+
			"vocabulary is a UNION of the manifest and the protos. Adopting a tool would "+
			"still extend the deployment's vocabulary without anyone agreeing to it, "+
			"which is the whole defect this key exists to fix. Got: %v", diags)
	}
}

// The plugin can never see a manifest: buf invokes it once per directory with a
// descriptor set and nothing else. So L20 and L7's reference half join A3, A9 and
// P1 in the group PartialSet turns into warnings, and the warning names the
// commands that do check.
//
// This is a real loss of checking and it is the right trade. Before the move, a
// tree whose taxonomy proto sat beside its tools had a typo caught by `garm gen`;
// it no longer will. What the old behaviour cost was worse — a tree that could
// not delete a copy of an adopted taxonomy without `mise run gen` failing on two
// L20 errors, while `catalogue build` was perfectly content.
func TestAnUndeclaredNameWarnsWhereTheVocabularyIsNotVisible(t *testing.T) {
	withGhostSet := lintFixture(t, "partial_set", validField(), nil,
		methodSpec{name: "Get", tool: namesSet("ghost-set")})

	ghostCompartment := validField()
	ghostCompartment.Compartments = []string{"ghost-compartment"}
	withGhostCompartment := lintFixture(t, "partial_compartment", ghostCompartment, nil,
		methodSpec{name: "Get", tool: validTool("get")})

	for _, tc := range []struct {
		what string
		rule string
		fds  []protoreflect.FileDescriptor
	}{
		{"an undeclared tool set", "L20", withGhostSet},
		{"an undeclared compartment", "L7", withGhostCompartment},
	} {
		t.Run(tc.what, func(t *testing.T) {
			// A full run refuses it: `garm lint` and `garm catalogue build` see
			// the whole catalogue and the manifest, so an undeclared name there
			// is a name declared nowhere.
			if !hasRule(compiler.Lint(tc.fds), tc.rule, false) {
				t.Fatalf("the control failed: %s is not an error in a full run", tc.rule)
			}

			diags := compiler.LintWith(tc.fds, compiler.Options{PartialSet: true})
			if hasRule(diags, tc.rule, false) {
				t.Errorf("%s is still an ERROR under PartialSet. buf runs the plugin over "+
					"one directory and cannot see catalogue.yaml, so an undeclared name "+
					"there is indistinguishable from one declared elsewhere — and refusing "+
					"it is what forces a tree to keep a copy of an adopted taxonomy",
					tc.rule)
			}
			if !hasRule(diags, tc.rule, true) {
				t.Fatalf("%s produced no warning under PartialSet either. A rule that is "+
					"skipped wherever nobody is looking is not a rule: it has to say it did "+
					"not check. Got: %v", tc.rule, diags)
			}
			for _, name := range []string{"garm lint", "catalogue build"} {
				if !hasRuleWithMsg(diags, tc.rule, true, name) {
					t.Errorf("the %s warning does not name %q, so a reader is told a rule "+
						"was skipped and not where it is enforced: %v", tc.rule, name, diags)
				}
			}
		})
	}
}

// L29 judges the declarations written in PROTO OPTIONS. When the manifest
// declares the vocabulary, nothing reads those options — so the rule has no
// subject, which is a different thing from being switched off where nobody is
// looking. Refusing a deployment for a disagreement between two adopted files it
// does not read would be the linter enforcing a vocabulary nobody uses.
func TestConflictingProtoDeclarationsAreMootWhenTheManifestDeclares(t *testing.T) {
	fds := []protoreflect.FileDescriptor{
		declFile(t, "compiler/testdata/lint/tax_a.proto", toolv1.E_Compartments,
			&toolv1.Decl{Name: "shared", Description: "Alpha's meaning"}),
		declFile(t, "compiler/testdata/lint/tax_b.proto", toolv1.E_Compartments,
			&toolv1.Decl{Name: "shared", Description: "A COMPLETELY DIFFERENT meaning"}),
	}

	if !hasRule(compiler.Lint(fds), "L29", false) {
		t.Fatal("the control failed: two files declaring one compartment differently " +
			"must be an L29 error when the protos ARE the vocabulary")
	}

	diags := compiler.LintWith(fds, compiler.Options{
		Taxonomy: &compiler.Taxonomy{Compartments: declared("shared")},
	})
	for _, d := range diags {
		if d.Rule == "L29" || d.Rule == "L28" {
			t.Errorf("%s still fires when the vocabulary comes from the manifest. The "+
				"declarations it judges are read by nothing — not the registry, not the "+
				"tool-set check, not the artifact's fields 3 and 4 — so this refuses a "+
				"deployment over words it does not use: %s", d.Rule, d)
		}
	}
}

// A malformed name in a manifest is refused by manifest.Taxonomy.validate, at
// parse time, with a message that can name the line of YAML. L28 must not report
// it a second time — two messages for one mistake, and the worse one first.
func TestAMalformedManifestNameIsNotAlsoAnL28(t *testing.T) {
	fds := lintFixture(t, "bad_manifest_name", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")})

	diags := compiler.LintWith(fds, compiler.Options{
		Taxonomy: &compiler.Taxonomy{Compartments: declared("Not_A_Valid_Name")},
	})
	if hasRule(diags, "L28", false) || hasRule(diags, "L28", true) {
		t.Errorf("L28 reported a manifest-declared name. manifest.Taxonomy.validate "+
			"refuses it at parse time with the same contracts validator and can point at "+
			"the line somebody typed, so a lint diagnostic here is the second and worse "+
			"message for one mistake: %v", diags)
	}
	// The registry still refuses it, under L7, because a name that cannot become
	// a bit position is not a vocabulary this build can use at all — and that
	// path is reached whatever the source. A manifest can only get here by
	// bypassing Parse, which is a programming error rather than a typo.
	if !hasRule(diags, "L7", false) {
		t.Errorf("nothing refused a malformed declared name at all: %v", diags)
	}
}

// The default is unchanged, and this is the backwards-compatibility assertion:
// a nil Taxonomy means the vocabulary is scraped from file options exactly as it
// was before v0.22.0, which is what every tree that has not migrated relies on
// and what `--proto` will always mean, since a synthesised one-entry manifest has
// no taxonomy to declare.
func TestNoDeclaredTaxonomyKeepsTheScrape(t *testing.T) {
	tool := lintFixture(t, "scrape", validField(), nil,
		methodSpec{name: "Get", tool: namesSet("research")})
	adopted := declFile(t, "compiler/testdata/lint/scrape_taxonomy.proto",
		toolv1.E_ToolSets, &toolv1.Decl{Name: "research", Description: "Reading public sources."})
	fds := append(append([]protoreflect.FileDescriptor{}, tool...), adopted)

	for _, d := range compiler.Lint(fds) {
		if d.Rule == "L20" {
			t.Errorf("a tree that declares no taxonomy in a manifest must still have its "+
				"vocabulary read from file options; this one did not: %s", d)
		}
	}
}
