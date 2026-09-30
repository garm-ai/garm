package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/manifest"
)

// `garm catalogue init`, which is a migration command wearing an init command's
// name (design §1.1).
//
// The fixtures resolve against a real module graph: withModuleGraph copies this
// repository's own go.mod and go.sum into the tree, so
// github.com/garm-ai/contracts is a genuine requirement and
// github.com/garm-ai/tools/web is genuinely not one. That pair is the whole
// distinction the command draws, and a stub for either half would be this test
// agreeing with the code.

// withModuleGraph gives a scaffolded fixture a module graph to be asked about.
func withModuleGraph(t *testing.T, dir string) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Dir(root) // cmd/garm -> cmd -> the module root
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// copyInto writes a copied proto package into a tree and excludes it from Go
// generation, which is the state every pre-manifest deployment is in.
func copyInto(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, "proto", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// exclude adds buf.gen.yaml's statement that a subtree is somebody else's.
func exclude(t *testing.T, dir string, paths ...string) {
	t.Helper()
	p := filepath.Join(dir, "buf.gen.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	lines.WriteString("    exclude_paths:\n")
	for _, path := range paths {
		lines.WriteString("      - " + path + "\n")
	}
	s := strings.Replace(string(b), "  - directory: proto\n",
		"  - directory: proto\n"+lines.String(), 1)
	if s == string(b) {
		t.Fatal("the scaffold's buf.gen.yaml no longer has a `- directory: proto` input")
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The copied tasks contract, at the path a deployment copies it to. The compiler
// answers that path from the LINKED registry rather than from this file, which is
// exactly what happens in the bank — so the package name and the go_package the
// command reads are the contract's own, not this fixture's guess at them.
const copiedTasks = `syntax = "proto3";
package garm.tasks.v1;
option go_package = "github.com/garm-ai/contracts/garm/tasks/v1;tasksv1";
`

// A copy of a package the tree does not require: the bank's proto/web, which is
// the case §1.1 exists to report.
const copiedWeb = `syntax = "proto3";
package web.v1;
import "garm/tool/v1/tool.proto";
option go_package = "github.com/garm-ai/tools/web/gen/web/v1;webv1";
message FetchRequest {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string url = 1;
}
`

func initIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"catalogue", "init", dir}, args...))
	root.SetOut(&out)
	root.SetErr(&out)
	// Executed before the buffer is read: `return out.String(), root.Execute()`
	// evaluates left to right and would return the buffer as it was before the
	// command ran, which is empty.
	err := root.Execute()
	return out.String(), err
}

// The migration in one test: a tree with two copies, one pinned and one not.
//
// The pinned one becomes a module entry at the version the module graph
// resolves. The unpinned one becomes a report and no entry at all, because §2's
// invariant has no version to pin it to — and the copy is left exactly where it
// was, since a tree compiling against descriptors it does not depend on is a
// defect to fix rather than a state to encode.
func TestCatalogueInitAdoptsWhatIsPinnedAndReportsWhatIsNot(t *testing.T) {
	dir := fixture(t)
	withModuleGraph(t, dir)
	copyInto(t, dir, "garm/tasks/v1/tasks.proto", copiedTasks)
	copyInto(t, dir, "web/v1/web.proto", copiedWeb)
	exclude(t, dir, "proto/garm", "proto/web")

	out, err := initIn(t, dir, "--force", "--name", "acme")
	if err == nil {
		t.Fatal("init exited 0 with a copy nothing pins; the report is the output and a " +
			"migration script that ignored it would delete a copy the tree does not require")
	}
	if !strings.Contains(err.Error(), "no entry") {
		t.Errorf("the error does not say what happened: %v", err)
	}

	m, loadErr := manifest.Load(filepath.Join(dir, manifest.Filename))
	if loadErr != nil {
		t.Fatalf("init wrote a manifest this binary cannot read: %v", loadErr)
	}
	if len(m.Include) != 2 {
		t.Fatalf("include = %v, want the local tree and the one pinned module", m.Include)
	}
	if m.Include[0].Path != "proto" {
		t.Errorf("include[0] = %v, want the deployment's own tree", m.Include[0])
	}
	if m.Include[1].Module != "github.com/garm-ai/contracts" ||
		strings.Join(m.Include[1].Packages, ",") != "garm.tasks.v1" {
		t.Errorf("include[1] = %v, want the contract module and garm.tasks.v1", m.Include[1])
	}
	if m.Name != "acme" {
		t.Errorf("name = %q", m.Name)
	}

	// The report names the unpinned copy usefully: which directory, which
	// package, which import path, and that this is a defect in the tree.
	//
	// Single tokens and not phrases: the prose is wrapped for a terminal, so a
	// multi-word assertion here would be asserting the column width. What the
	// prose says is held, unwrapped, by internal/manifest's own tests.
	for _, want := range []string{
		"proto/web/v1", "web.v1", "github.com/garm-ai/tools/web/gen/web/v1",
		"have NO entry", "defect in the tree",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
		}
	}
	// And it names what to delete for the copy it DID adopt, because until the
	// copy is gone the manifest it wrote describes a tree in two states at once.
	for _, want := range []string{"proto/garm/tasks/v1", "exclude_paths", "drift gate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
		}
	}
	// The copies are still there. This command reads a tree and writes one file.
	for _, rel := range []string{"proto/web/v1/web.proto", "proto/garm/tasks/v1/tasks.proto"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was removed: %v", rel, err)
		}
	}
}

// A hand-edited manifest is the authority and a generator is not.
func TestCatalogueInitRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := fixture(t)
	body := "schema: v1\nname: mine\ninclude:\n  - path: proto\n"
	writeManifest(t, dir, body)

	if _, err := initIn(t, dir); err == nil {
		t.Fatal("init overwrote an existing manifest")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal does not name the way past it: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != body {
		t.Error("the manifest was rewritten by a command that said it would not")
	}

	if _, err := initIn(t, dir, "--force"); err != nil {
		t.Fatalf("--force did not overwrite: %v", err)
	}
	b, err = os.ReadFile(filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == body {
		t.Error("--force left the file alone")
	}
}

// A tree with no copies: the manifest is one path entry, and the catalogue it
// composes is the one the tree already built.
func TestCatalogueInitOnATreeWithNothingAdopted(t *testing.T) {
	dir := fixture(t)
	if _, err := initIn(t, dir, "--force"); err != nil {
		t.Fatalf("init on a clean tree: %v", err)
	}
	out := filepath.Join(t.TempDir(), "c.binpb")
	stdout, _, err := buildIn(t, dir, "-o", out)
	if err != nil {
		t.Fatalf("the manifest init wrote does not build: %v", err)
	}
	if !strings.Contains(stdout, "acme.v1.add") {
		t.Errorf("the catalogue is missing the tree's tool:\n%s", stdout)
	}
}

// `garm init` writes one too, so a new deployment starts with the file rather
// than discovering later that it needed it.
//
// The platform packages are in it as a COMMENTED block and not as live entries,
// which is a deliberate departure from §1.1: a module entry is refused unless the
// tree requires the module, and a tree this command has just scaffolded has no
// go.mod at all. A live entry would be a manifest that cannot build on the first
// `garm catalogue build`.
func TestInitScaffoldsAManifestThatBuilds(t *testing.T) {
	dir := fixture(t)
	m, err := manifest.Load(filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatalf("garm init wrote no readable %s: %v", manifest.Filename, err)
	}
	if len(m.Include) != 1 || m.Include[0].Path != "proto" {
		t.Fatalf("include = %v, want the one tree it scaffolded", m.Include)
	}
	b, err := os.ReadFile(filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"garm.tasks.v1", "go get github.com/garm-ai/contracts", "blank import"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the scaffolded manifest does not mention %q:\n%s", want, b)
		}
	}
	out := filepath.Join(t.TempDir(), "c.binpb")
	if _, _, err := buildIn(t, dir, "-o", out); err != nil {
		t.Fatalf("the scaffolded manifest does not build: %v", err)
	}
}
