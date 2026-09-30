package manifest_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/manifest"
)

// `taxonomy:` is where a deployment declares its own access-control vocabulary,
// and the refusals below are the reason it is validated HERE rather than in a
// lint rule: the message can name the line of YAML somebody typed.
//
// The base manifest every case below varies.
const taxBase = "schema: v1\nname: bank\ninclude: [{path: proto}]\n"

func TestAManifestDeclaresItsOwnVocabulary(t *testing.T) {
	m, err := manifest.Parse([]byte(taxBase + `taxonomy:
  compartments:
    - name: financial
      description: Money movement and balances.
    - name: pii-contact
      description: A customer's contact details.
  tool_sets:
    - name: payments
      description: Initiating and inspecting payments.
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !m.Taxonomy.Declared() {
		t.Fatal("a manifest with two compartments and a tool set does not report a " +
			"declared taxonomy, so the builder would scrape the protos instead")
	}
	compartments, toolSets := m.Taxonomy.Decls()
	if len(compartments) != 2 || len(toolSets) != 1 {
		t.Fatalf("Decls() = %d compartments, %d tool sets; want 2 and 1",
			len(compartments), len(toolSets))
	}
	// The description travels. It ends up on the artifact and in front of
	// whoever is deciding which compartments a role should hold.
	if compartments[0].GetName() != "financial" ||
		compartments[0].GetDescription() != "Money movement and balances." {
		t.Errorf("the first compartment came through as %v", compartments[0])
	}
}

// Order is presentation, not bit order, and this pins it so that nobody has to
// rediscover it from the registry.
//
// The composition design's §7.3 says the manifest's list order IS the bit order
// and is authoritative. It is not: `policy.NewRegistry` sorts the names before
// assigning bits, and its own doc comment says so — "Names are sorted first so
// the assignment is deterministic for a given declaration set." The conclusion
// §7.3 wanted still holds, by a better mechanism: bit assignment is deterministic
// no matter what order anybody writes the YAML in, so reordering the file cannot
// move a bit. The bitset is per build and never persisted either way — tokens
// carry compartment NAMES and the ledger records names.
func TestReorderingTheVocabularyIsNotAChangeOfMeaning(t *testing.T) {
	one, err := manifest.Parse([]byte(taxBase + `taxonomy:
  compartments:
    - {name: alpha, description: A.}
    - {name: beta, description: B.}
`))
	if err != nil {
		t.Fatal(err)
	}
	two, err := manifest.Parse([]byte(taxBase + `taxonomy:
  compartments:
    - {name: beta, description: B.}
    - {name: alpha, description: A.}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := one.Taxonomy.Decls()
	b, _ := two.Taxonomy.Decls()
	if len(a) != len(b) {
		t.Fatalf("two spellings of one vocabulary produced %d and %d names", len(a), len(b))
	}
	// Carried in the order written — the artifact reads the way the file does —
	// while the bits come out of a sort either way.
	if a[0].GetName() != "alpha" || b[0].GetName() != "beta" {
		t.Errorf("Decls() reorders the list; it should carry it as written: %v, %v", a, b)
	}
}

// An empty list is a decision and an absent key is not.
//
// Declaring either list takes over BOTH, because a vocabulary half in the
// manifest and half in the protos is worse than either whole. So a deployment
// with no tool sets writes `tool_sets: []`, which reads as an answer, where
// leaving the key out reads as an oversight.
func TestAnEmptyListIsDeclaredAndAnAbsentKeyIsNot(t *testing.T) {
	declaredEmpty, err := manifest.Parse([]byte(taxBase +
		"taxonomy:\n  compartments: []\n  tool_sets: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !declaredEmpty.Taxonomy.Declared() {
		t.Error("`compartments: []` does not count as declared, so a deployment that " +
			"means to have no vocabulary silently gets the protos' one instead")
	}

	absent, err := manifest.Parse([]byte(taxBase))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Taxonomy.Declared() {
		t.Error("a manifest with no `taxonomy:` key reports a declared taxonomy, which " +
			"would break every tree that has not migrated")
	}
	c, s := absent.Taxonomy.Decls()
	if c != nil || s != nil {
		t.Errorf("Decls() on an undeclared taxonomy returned %v, %v; nil is what tells "+
			"the builder to scrape the file options", c, s)
	}
}

func TestRefusesAVocabularyThatCannotMeanWhatItSays(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{
			// The failure mode is not cosmetic: the generator treats `-` and `_`
			// identically, so `pii_contact` and `pii-contact` are two
			// compartments with two bits that generate ONE Go constant.
			name: "an underscore in a name",
			yaml: "taxonomy:\n  compartments: [{name: pii_contact, description: x.}]\n",
			want: "pii_contact",
		}, {
			// Two names differing only in case get two bits and nothing
			// notices, and SetLenient silently drops the one it does not know.
			name: "an upper-case name",
			yaml: "taxonomy:\n  compartments: [{name: PII, description: x.}]\n",
			want: "lower-case",
		}, {
			name: "a name declared twice",
			yaml: "taxonomy:\n  compartments: [{name: fin, description: a.}, {name: fin, description: b.}]\n",
			want: "declared twice",
		}, {
			name: "a tool set declared twice",
			yaml: "taxonomy:\n  tool_sets: [{name: pay, description: a.}, {name: pay, description: b.}]\n",
			want: "declared twice",
		}, {
			// A word nobody explained is the same act of omission the key exists
			// to end, performed in a new file.
			name: "a name with no description",
			yaml: "taxonomy:\n  compartments: [{name: fin}]\n",
			want: "no description",
		}, {
			name: "a description that is only whitespace",
			yaml: "taxonomy:\n  compartments: [{name: fin, description: \"   \"}]\n",
			want: "no description",
		}, {
			name: "a misspelled key inside the taxonomy",
			yaml: "taxonomy:\n  compartmnets: [{name: fin, description: x.}]\n",
			want: "compartmnets",
		}, {
			name: "a misspelled key on an entry",
			yaml: "taxonomy:\n  compartments: [{name: fin, descriptoin: x.}]\n",
			want: "descriptoin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(taxBase + tc.yaml))
			if err == nil {
				t.Fatalf("accepted:\n%s", tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not contain %q, so it does not point at what to fix:\n  %v",
					tc.want, err)
			}
		})
	}
}

// The name and the index, so a reader of the refusal knows which line to edit.
func TestARefusalNamesTheEntryItRefuses(t *testing.T) {
	_, err := manifest.Parse([]byte(taxBase +
		"taxonomy:\n  tool_sets: [{name: ok, description: x.}, {name: Bad, description: y.}]\n"))
	if err == nil {
		t.Fatal("accepted an upper-case tool set name")
	}
	if !strings.Contains(err.Error(), "taxonomy.tool_sets[1]") {
		t.Errorf("the refusal does not say which entry is wrong: %v", err)
	}
}
