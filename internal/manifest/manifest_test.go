package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/manifest"
)

// The manifest in §1 of the design, read as written. If the shape in the
// specification does not parse, everything below is testing something else.
func TestParsesTheSpecifiedShape(t *testing.T) {
	m, err := manifest.Parse([]byte(`
schema: v1
name: bank
source: garm-ai/examples/bank

include:
  - path: proto

  - module: github.com/garm-ai/contracts
    packages: [garm.tasks.v1]

  - module: github.com/garm-ai/tools/web
    version: v0.2.0
    packages: [web.v1]

  - module: github.com/garm-ai/agents
    packages: [agents.support.v1]
    prompts: prompts

prompts: .
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "bank" || m.Source != "garm-ai/examples/bank" || m.Prompts != "." {
		t.Fatalf("header: %+v", m)
	}
	if len(m.Include) != 4 {
		t.Fatalf("want 4 inputs, got %d", len(m.Include))
	}
	if m.Include[0].IsModule() || m.Include[0].Path != "proto" {
		t.Errorf("first entry should be the local tree: %+v", m.Include[0])
	}
	if !m.Include[1].IsModule() || m.Include[1].Packages[0] != "garm.tasks.v1" {
		t.Errorf("second entry should be a module: %+v", m.Include[1])
	}
	if m.Include[2].Version != "v0.2.0" {
		t.Errorf("a version key is permitted for readability: %+v", m.Include[2])
	}
	// Carried, not yet consumed: a composed agent's prompts live in its module
	// and `catalogue publish` resolves them per input.
	if m.Include[3].Prompts != "prompts" {
		t.Errorf("an entry's prompts root should survive parsing: %+v", m.Include[3])
	}
}

func TestRefusesAManifestThatCannotMeanWhatItSays(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{
			name: "no schema",
			yaml: "name: x\ninclude: [{path: proto}]\n",
			want: "schema: v1",
		}, {
			name: "a schema this binary does not read",
			yaml: "schema: v2\nname: x\ninclude: [{path: proto}]\n",
			want: "this binary reads v1",
		}, {
			name: "no name",
			yaml: "schema: v1\ninclude: [{path: proto}]\n",
			want: "names the deployment",
		}, {
			name: "nothing to compose",
			yaml: "schema: v1\nname: x\ninclude: []\n",
			want: "include is empty",
		}, {
			// A misspelled key is the failure this refusal is for: `pacakges:`
			// would leave a module entry contributing nothing, and a catalogue
			// quietly built without the task queue is the outage of 2026-09-29.
			name: "a misspelled key",
			yaml: "schema: v1\nname: x\ninclude: [{module: m, pacakges: [a.v1]}]\n",
			want: "pacakges",
		}, {
			name: "an entry naming no input",
			yaml: "schema: v1\nname: x\ninclude: [{prompts: p}]\n",
			want: "neither `path:` nor `module:`",
		}, {
			name: "an entry that is both",
			yaml: "schema: v1\nname: x\ninclude: [{path: proto, module: m, packages: [a.v1]}]\n",
			want: "never both",
		}, {
			name: "a module with no packages",
			yaml: "schema: v1\nname: x\ninclude: [{module: github.com/a/b}]\n",
			want: "names no packages",
		}, {
			name: "a directory where a package name belongs",
			yaml: "schema: v1\nname: x\ninclude: [{module: github.com/a/b, packages: [web/v1]}]\n",
			want: "is not a proto package name",
		}, {
			name: "a path entry selecting packages",
			yaml: "schema: v1\nname: x\ninclude: [{path: proto, packages: [a.v1]}]\n",
			want: "`packages:` on a path entry",
		}, {
			name: "a path entry with a version",
			yaml: "schema: v1\nname: x\ninclude: [{path: proto, version: v1.0.0}]\n",
			want: "`version:` on a path entry",
		}, {
			name: "a path outside the tree",
			yaml: "schema: v1\nname: x\ninclude: [{path: ../other/proto}]\n",
			want: "leaves the tree",
		}, {
			name: "one module twice",
			yaml: "schema: v1\nname: x\ninclude: [{module: m, packages: [a.v1]}, {module: m, packages: [b.v1]}]\n",
			want: "included twice",
		}, {
			name: "one path twice",
			yaml: "schema: v1\nname: x\ninclude: [{path: proto}, {path: ./proto}]\n",
			want: "included twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("parsed a manifest that should have been refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message should contain %q:\n%v", tc.want, err)
			}
		})
	}
}

// Found by convention, so `garm catalogue build` needs no flag in the common
// case. A bool and not an error, because "no manifest here" is how a tree that
// has not migrated still builds from --proto.
func TestFindByConvention(t *testing.T) {
	dir := t.TempDir()
	if _, ok := manifest.Find(dir); ok {
		t.Fatal("found a manifest in an empty directory")
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte("schema: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, ok := manifest.Find(dir)
	if !ok || path != filepath.Join(dir, manifest.Filename) {
		t.Fatalf("Find = %q, %v", path, ok)
	}
}

// A package's name and its directory: the only place the two are joined, and
// the reason a module entry can name a package while the compiler needs files.
func TestPackageDir(t *testing.T) {
	if got := manifest.PackageDir("garm.tasks.v1"); got != "garm/tasks/v1" {
		t.Fatalf("PackageDir = %q", got)
	}
}
