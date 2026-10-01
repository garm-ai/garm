// The migration tests: a tree in the copied state, described.
//
// They resolve against a real module graph for the same reason the composition
// tests do — the question this code answers is "which module does the go command
// say provides this import path", and a stub would be this package agreeing with
// itself. `tree` copies this repository's own go.mod and go.sum, so
// github.com/garm-ai/contracts is a genuine requirement and
// github.com/garm-ai/tools/web is genuinely not one, which is exactly the pair of
// cases §1.1 distinguishes.
package manifest_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/manifest"
)

// tasksGoImport is the import path the bank's copied garm/tasks/v1/tasks.proto
// names in its own go_package, alias suffix included — the copy is read verbatim
// and the suffix is stripped where the format is known.
const tasksGoImport = "github.com/garm-ai/contracts/garm/tasks/v1;tasksv1"

// webGoImport is the bank's copied proto/web, whose module examples does not
// require. It is the live case §1.1 exists for.
const webGoImport = "github.com/garm-ai/tools/web/gen/web/v1;webv1"

func describe(t *testing.T, dir string, req manifest.DescribeRequest) *manifest.Plan {
	t.Helper()
	p, err := manifest.Describe(t.Context(), dir, req)
	if err != nil {
		t.Fatalf("describing %s: %v", dir, err)
	}
	return p
}

// A copy the tree requires becomes a module entry, at the version the module
// graph resolves and with no `version:` key written.
//
// The version is deliberately absent from the file: it would be the same fact in
// two places, and §2 makes a disagreement between them an error rather than a
// precedence rule. It is reported instead, because a person reading the
// migration wants to know which tag they just pinned their descriptors to.
func TestDescribeAdoptsACopyTheTreeRequires(t *testing.T) {
	dir := tree(t, nil)
	p := describe(t, dir, manifest.DescribeRequest{
		Name:          "acme",
		Paths:         []string{"proto"},
		LocalPackages: []string{"acme.v1"},
		Copies: []manifest.Copied{{
			Package: "garm.tasks.v1", Dir: "proto/garm/tasks/v1", GoImport: tasksGoImport,
		}},
	})
	if len(p.Unpinned) != 0 {
		t.Fatalf("a required module was reported as unpinned: %v", p.Unpinned)
	}
	if len(p.Adopted) != 1 || p.Adopted[0].Module != contractsModule {
		t.Fatalf("adopted = %v, want one entry for %s", p.Adopted, contractsModule)
	}
	if p.Adopted[0].Version == "" {
		t.Error("the adoption records no version; the report is how somebody learns which " +
			"tag their descriptors are now pinned to")
	}
	want := []manifest.Entry{
		{Path: "proto"},
		{Module: contractsModule, Packages: []string{"garm.tasks.v1"}},
	}
	got := p.Manifest.Include
	if len(got) != len(want) {
		t.Fatalf("include = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Module != want[i].Module ||
			strings.Join(got[i].Packages, ",") != strings.Join(want[i].Packages, ",") {
			t.Errorf("include[%d] = %v, want %v", i, got[i], want[i])
		}
		if got[i].Version != "" {
			t.Errorf("include[%d] writes version %q; §2 makes the module graph the only "+
				"answer, so a generator must not restate it", i, got[i].Version)
		}
	}
}

// The case this command exists to report: a copy whose module the tree does not
// require. No entry, the copy untouched, and a message that says the tree is
// defective rather than that this command is limited.
//
// `examples/bank` is exactly this for `web`: it carries proto/web and requires no
// github.com/garm-ai/tools/web, so it compiles its declarations against
// descriptors it does not depend on and nothing makes the two agree.
func TestDescribeRefusesToInventAnEntryForACopyNothingPins(t *testing.T) {
	dir := tree(t, nil)
	p := describe(t, dir, manifest.DescribeRequest{
		Name:  "acme",
		Paths: []string{"proto"},
		Copies: []manifest.Copied{{
			Package: "web.v1", Dir: "proto/web/v1", GoImport: webGoImport,
		}},
	})
	if len(p.Adopted) != 0 {
		t.Fatalf("an entry was invented for a module nothing requires: %v", p.Adopted)
	}
	if len(p.Unpinned) != 1 {
		t.Fatalf("unpinned = %v, want the one copy reported", p.Unpinned)
	}
	u := p.Unpinned[0]
	if u.Package != "web.v1" || u.Dir != "proto/web/v1" {
		t.Errorf("the report names %s at %s", u.Package, u.Dir)
	}
	for _, want := range []string{
		"requires no module providing",
		"github.com/garm-ai/tools/web/gen/web/v1",
		"WHAT to include",
		"WHICH VERSION",
		"left exactly as it is",
		"go get github.com/garm-ai/tools/web/gen/web/v1",
		"blank import",
	} {
		if !strings.Contains(u.Why, want) {
			t.Errorf("the report does not say %q:\n%s", want, u.Why)
		}
	}
	// And the manifest it wrote is still valid and still composes the rest: a
	// tree with one defect is not a tree with no describable inputs.
	if len(p.Manifest.Include) != 1 || p.Manifest.Include[0].Path != "proto" {
		t.Errorf("include = %v, want the local tree alone", p.Manifest.Include)
	}
}

// Two packages from one module are one entry. Two entries for one module is
// refused by validate, and two provenance records for one version is why.
func TestDescribeGroupsPackagesByModule(t *testing.T) {
	dir := tree(t, nil)
	p := describe(t, dir, manifest.DescribeRequest{
		Name:  "acme",
		Paths: []string{"proto"},
		Copies: []manifest.Copied{
			{Package: "garm.tasks.v1", Dir: "proto/garm/tasks/v1", GoImport: tasksGoImport},
			{Package: "garm.ledger.v1", Dir: "proto/garm/ledger/v1",
				GoImport: "github.com/garm-ai/contracts/garm/ledger/v1;ledgerv1"},
		},
	})
	if len(p.Unpinned) != 0 {
		t.Fatalf("unpinned = %v", p.Unpinned)
	}
	var mods []manifest.Entry
	for _, e := range p.Manifest.Include {
		if e.IsModule() {
			mods = append(mods, e)
		}
	}
	if len(mods) != 1 {
		t.Fatalf("include has %d module entries, want 1: %v", len(mods), mods)
	}
	if got := strings.Join(mods[0].Packages, ","); got != "garm.ledger.v1,garm.tasks.v1" {
		t.Errorf("packages = %q, want both, sorted", got)
	}
}

// A copy with no go_package says nothing about where it came from, and that is
// reported rather than guessed at from the package name.
func TestDescribeSaysSoWhenACopyNamesNoGoPackage(t *testing.T) {
	dir := tree(t, nil)
	p := describe(t, dir, manifest.DescribeRequest{
		Name:   "acme",
		Paths:  []string{"proto"},
		Copies: []manifest.Copied{{Package: "web.v1", Dir: "proto/web/v1"}},
	})
	if len(p.Adopted) != 0 || len(p.Unpinned) != 1 {
		t.Fatalf("adopted = %v, unpinned = %v", p.Adopted, p.Unpinned)
	}
	if !strings.Contains(p.Unpinned[0].Why, "no `go_package`") {
		t.Errorf("the report does not say why:\n%s", p.Unpinned[0].Why)
	}
}

// What Render writes, Parse reads — including the commented platform block,
// which has to be a comment and not a key.
//
// The round trip is the property that matters: a generator whose output its own
// parser refuses would be worse than no generator, and the comments are half the
// file so they are not incidental to the test.
func TestRenderWritesAManifestThisBinaryReads(t *testing.T) {
	src := &manifest.Manifest{
		Schema: manifest.Schema,
		Name:   "bank",
		Source: "garm-ai/examples/bank",
		Include: []manifest.Entry{
			{Path: "proto"},
			{Module: contractsModule, Packages: []string{"garm.tasks.v1"}},
		},
		Prompts: ".",
	}
	body := manifest.Render(src)
	got, err := manifest.Parse(body)
	if err != nil {
		t.Fatalf("this binary cannot read what it wrote: %v\n%s", err, body)
	}
	if got.Schema != src.Schema || got.Name != src.Name || got.Source != src.Source ||
		got.Prompts != src.Prompts || len(got.Include) != 2 {
		t.Fatalf("round trip lost something: %+v\n%s", got, body)
	}
	if got.Include[0].Path != "proto" || got.Include[1].Module != contractsModule ||
		strings.Join(got.Include[1].Packages, ",") != "garm.tasks.v1" {
		t.Errorf("include = %v\n%s", got.Include, body)
	}
	// A manifest that already declares the queue gets no block suggesting it.
	if strings.Contains(string(body), "# - module:") {
		t.Errorf("the commented suggestion was written into a manifest that already "+
			"declares garm.tasks.v1:\n%s", body)
	}

	// And one that does not gets the block, still as a comment.
	bare := manifest.Render(&manifest.Manifest{
		Schema: manifest.Schema, Name: "new", Include: []manifest.Entry{{Path: "proto"}},
	})
	if !strings.Contains(string(bare), "# - module: "+contractsModule) {
		t.Errorf("a manifest with no queue does not suggest one:\n%s", bare)
	}
	m, err := manifest.Parse(bare)
	if err != nil {
		t.Fatalf("the suggestion is not a comment: %v\n%s", err, bare)
	}
	if len(m.Include) != 1 {
		t.Errorf("the commented suggestion parsed as an entry: %+v", m.Include)
	}
}
