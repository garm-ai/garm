package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is `garm catalogue init`'s half of the manifest: reading a tree
// that predates one and describing it.
//
// Its real job is migration rather than scaffolding (design §1.1). Every
// deployment in the estate is in the copied state — adopted packages sitting in
// proto/, a matching exclude_paths entry keeping them out of Go generation, a
// drift gate watching them, and the versions pinned in go.mod — because a
// catalogue used to be assembled from one directory. So the manifest that
// describes such a tree already exists, spread across three files, and this is
// the code that reads it out of them.
//
// WHAT IT MUST NOT DO IS GUESS, and that is the whole design of this file.
// Every fact it writes comes from something the tree already states:
//
//   - which directories hold copies: buf.gen.yaml's `exclude_paths`, which is
//     the deployment's own declaration that those trees are somebody else's —
//     they are excluded precisely because the generated Go arrives through a
//     module instead.
//   - which proto package a copy declares: the compiled descriptor, not the
//     directory name. A directory renamed without its package is the drift this
//     whole design exists to catch, so the same rule holds here as in Packages.
//   - which module a copy came from: the copy's own `go_package`, resolved to a
//     module by `go list`. Not a guess from the package name, and not a scan of
//     the module graph hoping for a match.
//
// When any of those is missing, there is no entry and the report says why. A
// copied package the tree does not require admits no entry at all — §2's
// invariant has no version to pin it to — and a tree in that state is compiling
// against descriptors it does not depend on, which is a real defect and the
// report is the useful output.

// BufGenFilename is where a tree says which directories compose into its
// catalogue today, and which of them are copies.
const BufGenFilename = "buf.gen.yaml"

// BufGen is the part of a buf.gen.yaml this reads, and nothing else. Unknown
// keys are ignored deliberately: plugins, options and outputs are buf's
// business, and a manifest generator that refused an unfamiliar plugin entry
// would be refusing to read a file it does not own.
type BufGen struct {
	Inputs []BufInput `yaml:"inputs"`
}

// BufInput is one buf input. Only `directory` inputs are of interest — a
// `module` or `git_repo` input is not a directory in this tree and cannot
// become a `path:` entry.
type BufInput struct {
	Directory    string   `yaml:"directory"`
	ExcludePaths []string `yaml:"exclude_paths"`
}

// LoadBufGen reads dir's buf.gen.yaml, if there is one.
//
// A bool and not an error for absence, because a tree with no buf.gen.yaml is
// an ordinary case: a new project that has not been scaffolded yet, and one
// that has already migrated and deleted its exclude_paths.
func LoadBufGen(dir string) (*BufGen, bool, error) {
	p := filepath.Join(dir, BufGenFilename)
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", p, err)
	}
	var g BufGen
	if err := yaml.Unmarshal(b, &g); err != nil {
		return nil, false, fmt.Errorf("parsing %s: %w", p, err)
	}
	return &g, true, nil
}

// Directories are the input directories buf generates from, and the copied
// trees inside each.
//
// exclude_paths are relative to the workspace root rather than to the input, so
// they are returned as the file spells them and compared as such.
func (g *BufGen) Directories() []BufInput {
	var out []BufInput
	for _, in := range g.Inputs {
		if in.Directory == "" {
			continue
		}
		out = append(out, in)
	}
	return out
}

// Copied is one adopted proto package sitting in the working tree, as
// `catalogue init` found it.
type Copied struct {
	// Package is the proto package the copy declares, read off the compiled
	// descriptor.
	Package string

	// Dir is where it sits, relative to the manifest, for the report that names
	// what to delete.
	Dir string

	// GoImport is the copy's own `go_package` option, verbatim — the `;alias`
	// suffix is stripped here rather than by the caller, so the one place that
	// knows the option's format is the one place that reads it. It is the only
	// thing in a copied file that says where the file came from, which is what
	// makes the match a reading rather than a guess.
	GoImport string
}

// Adoption is a copy that became a module entry.
type Adoption struct {
	Copied
	Module  string
	Version string
}

// Unpinned is a copy that could not become one, and why not.
type Unpinned struct {
	Copied
	Why string
}

// DescribeRequest is the tree, as values. Nothing here compiles protos or reads
// buf.gen.yaml — the command does both and hands the answers in, the same seam
// Build sits behind.
type DescribeRequest struct {
	// Name is the deployment the catalogue is for.
	Name string

	// Source is free-form provenance. Omitted from the manifest when empty.
	Source string

	// Prompts is the manifest's prompts root, relative to it.
	Prompts string

	// Paths are the local input directories, relative to the manifest, in the
	// order buf.gen.yaml lists them. The builder sorts, so the order is
	// presentation.
	Paths []string

	// LocalPackages is every proto package the path entries contribute — the
	// deployment's own authorship, which is what is left once the copies are
	// taken out. Reported, never written: a path entry names no packages.
	LocalPackages []string

	// Copies is every adopted package the tree carries.
	Copies []Copied
}

// Plan is what `catalogue init` would write, and what it will not.
type Plan struct {
	// Manifest describes the tree as it will be once the adopted copies are
	// deleted. It does NOT describe the tree as it stands: while a copy sits
	// beside the module entry meant to replace it, two inputs declare one proto
	// package and `catalogue build` refuses that by name — which is the check
	// working, and why the report says what to delete.
	Manifest *Manifest

	// LocalPackages is what the path entries contribute, for the report.
	LocalPackages []string

	// Adopted and Unpinned are every copy, partitioned. Unpinned is the output
	// that matters: it is a defect report, not a warning about this command.
	Adopted  []Adoption
	Unpinned []Unpinned
}

// Describe resolves each copy to the module that pins it and assembles the
// manifest the tree describes.
//
// dir is the tree the manifest belongs to, which is what the module graph is
// asked about.
func Describe(ctx context.Context, dir string, req DescribeRequest) (*Plan, error) {
	p := &Plan{LocalPackages: sortedCopy(req.LocalPackages)}

	// A tree with copies and no module graph cannot pin any of them, and saying
	// that once is better than letting the go command say "go.mod file not
	// found" per copy. It is a real state: a proto tree in a repository whose Go
	// lives somewhere else.
	if len(req.Copies) > 0 && !inAModule(dir) {
		for _, c := range req.Copies {
			p.Unpinned = append(p.Unpinned, Unpinned{Copied: c, Why: fmt.Sprintf(
				"there is no go.mod in %s or any directory above it, so this tree has no "+
					"module graph and nothing can pin these descriptors. The manifest names "+
					"WHAT to include and go.mod says WHICH VERSION; with no go.mod there is "+
					"no second half, and %s was left exactly as it is.", dir, c.Dir)})
		}
		req.Copies = nil
	}

	// Group by module first, so one module contributing two packages produces
	// one entry — validate() refuses two entries for one module, and two
	// provenance records for one version is what that rule is about.
	byModule := map[string][]Copied{}
	var order []string
	for _, c := range req.Copies {
		imp := goPackageImport(c.GoImport)
		if imp == "" {
			p.Unpinned = append(p.Unpinned, Unpinned{Copied: c, Why: fmt.Sprintf(
				"The copy declares no `go_package`, so nothing in it says which module its "+
					"generated Go comes from. A module entry names WHAT to include and go.mod "+
					"says WHICH VERSION; with no import path there is nothing to ask the "+
					"module graph about, and %s was left exactly as it is.", c.Dir)})
			continue
		}
		// The version go list reports here is discarded: Resolve below is the
		// authority on what the module graph pins, and asking twice would be
		// two answers to one question.
		mod, _, err := goModuleOf(ctx, dir, imp)
		if err != nil {
			return nil, err
		}
		if mod == "" {
			p.Unpinned = append(p.Unpinned, Unpinned{Copied: c, Why: fmt.Sprintf(
				"This tree requires no module providing %s, the import path its own "+
					"`go_package` names. §2's invariant admits no entry without one — the "+
					"manifest names WHAT to include and go.mod says WHICH VERSION — so there "+
					"is no version to pin these descriptors to, and the copy was left exactly "+
					"as it is.\n\n"+
					"That state is the defect this report exists for: the tree compiles its "+
					"declarations against descriptors it does not depend on, so the copy and "+
					"the module it came from can differ by any amount and nothing says so. "+
					"Require it and run this again:\n\n"+
					"    go get %s\n\n"+
					"If this deployment DECLARES the tool but does not serve it — another "+
					"process does, and no Go here calls it — that requirement will not "+
					"survive `go mod tidy`, which removes what nothing imports. Pin it with "+
					"an explicit blank import of the same package, which is the ordinary Go "+
					"idiom for a dependency you depend on without calling.", imp, imp)})
			continue
		}
		if _, seen := byModule[mod]; !seen {
			order = append(order, mod)
		}
		byModule[mod] = append(byModule[mod], c)
	}

	sort.Strings(order)
	var entries []Entry
	for _, mod := range order {
		copies := byModule[mod]
		var pkgs []string
		for _, c := range copies {
			pkgs = append(pkgs, c.Package)
		}
		e := Entry{Module: mod, Packages: sortedCopy(pkgs)}

		// Validated through the same resolver the builder uses, one entry at a
		// time so a failure names the copy it came from. It is what keeps this
		// command from writing a manifest that `catalogue build` then refuses:
		// the module is required, the packages are where their names spell, and
		// the version is the module graph's.
		in, err := resolveOne(ctx, dir, e)
		if err != nil {
			for _, c := range copies {
				p.Unpinned = append(p.Unpinned, Unpinned{Copied: c, Why: err.Error()})
			}
			continue
		}
		entries = append(entries, e)
		for _, c := range copies {
			p.Adopted = append(p.Adopted, Adoption{Copied: c, Module: mod, Version: in.Version})
		}
	}

	include := make([]Entry, 0, len(req.Paths)+len(entries))
	for _, path := range req.Paths {
		include = append(include, Entry{Path: path})
	}
	include = append(include, entries...)

	p.Manifest = &Manifest{
		Schema:  Schema,
		Name:    req.Name,
		Source:  req.Source,
		Include: include,
		Prompts: req.Prompts,
	}
	sort.Slice(p.Unpinned, func(i, j int) bool { return p.Unpinned[i].Dir < p.Unpinned[j].Dir })
	return p, nil
}

// resolveOne runs one candidate entry through Resolve, which is the only way to
// be sure the entry would build: it is the code the builder runs.
func resolveOne(ctx context.Context, dir string, e Entry) (*Input, error) {
	inputs, err := Resolve(ctx, dir, &Manifest{
		Schema: Schema, Name: e.Module, Include: []Entry{e},
	}, "")
	if err != nil {
		return nil, err
	}
	return inputs[0], nil
}

// goPackageImport strips the `;alias` suffix a go_package option may carry.
//
// `option go_package = "github.com/garm-ai/tools/web/gen/web/v1;webv1"` names an
// import path and a package name, and only the first half is a thing `go list`
// can resolve.
func goPackageImport(goPackage string) string {
	if i := strings.IndexByte(goPackage, ';'); i >= 0 {
		goPackage = goPackage[:i]
	}
	return strings.TrimSpace(goPackage)
}

// goModuleOf asks the go command which module provides an import path, and
// reports an empty path when the tree requires none.
//
// `go list` and not a search of the module graph for a matching prefix: an
// import path maps to a module by the same rules a build uses — the longest
// requirement that is a prefix, a replace, a workspace — and reimplementing
// that here would be the second module resolver CLAUDE.md's third invariant
// forbids. -e so that "no required module provides this" comes back as data on
// the package rather than as a failed command: that answer is the whole point
// of the call, and it is a fact about the tree rather than an error here.
func goModuleOf(ctx context.Context, dir, importPath string) (string, string, error) {
	out, err := run(ctx, dir, "go", "list", "-e", "-json=ImportPath,Module,Error", importPath)
	if err != nil {
		return "", "", err
	}
	var info struct {
		Module *struct{ Path, Version string }
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return "", "", fmt.Errorf("reading `go list -e -json %s` output: %w", importPath, err)
	}
	if info.Module == nil {
		return "", "", nil
	}
	return info.Module.Path, info.Module.Version, nil
}

// Render writes a manifest as the file a human then edits.
//
// Hand-written rather than yaml.Marshal because the comments are half the
// artifact. A generated manifest is read by whoever has to maintain it, and a
// bare `- module: github.com/garm-ai/contracts` says nothing about why the
// version is not beside it — which is the one thing about this file that
// surprises people.
func Render(m *Manifest) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `# %s — everything that composes into this deployment's catalogue.
#
# The manifest names WHAT to include; go.mod says WHICH VERSION. A module entry
# has no version here on purpose: the generated Go already comes from that
# requirement, so taking the descriptor version from anywhere else would let
# this tree compile against one tag while declaring the descriptors of another.
# A `+"`version:`"+` key is permitted for readability and must EQUAL what the module
# graph resolves; a disagreement is an error, never a precedence rule.
#
# Written by `+"`garm catalogue init`"+`. Edit it freely — it is the authority from
# here on, and the generator refuses to overwrite it without --force.
schema: %s
name: %s
`, Filename, m.Schema, m.Name)
	if m.Source != "" {
		fmt.Fprintf(&b, "source: %s\n", m.Source)
	}
	b.WriteString("\ninclude:\n")

	wroteLocal, wroteModule := false, false
	for _, e := range m.Include {
		if e.IsModule() {
			if !wroteModule {
				b.WriteString("\n  # Adopted from modules this tree already requires. The protos are read\n" +
					"  # out of the module cache, so nothing new is fetched and composition\n" +
					"  # works offline once `go mod download` has run.\n")
				wroteModule = true
			}
			fmt.Fprintf(&b, "  - module: %s\n    packages: [%s]\n",
				e.Module, strings.Join(e.Packages, ", "))
			continue
		}
		if !wroteLocal {
			b.WriteString("  # This deployment's own declarations.\n")
			wroteLocal = true
		}
		fmt.Fprintf(&b, "  - path: %s\n", e.Path)
	}

	if !declares(m, tasksPackage) {
		fmt.Fprintf(&b, `
  # The task queue, which every tool declaring `+"`approval { mode: MODE_GRANT }`"+`
  # needs: without it the call parks on a task nobody can open, decide or see,
  # and the runner reports that no approval arrived. Uncomment it once the tree
  # requires the module — an entry for a module go.mod does not require is
  # refused, because there would be no one version.
  #
  #     go get %s@<version>
  #
  # and, because no Go here calls it and `+"`go mod tidy`"+` removes what nothing
  # imports, a blank import to keep the requirement:
  #
  #     import _ "%s/%s"
  #
  # - module: %s
  #   packages: [%s]
`, tasksModule, tasksModule, PackageDir(tasksPackage), tasksModule, tasksPackage)
	}

	if m.Prompts != "" {
		fmt.Fprintf(&b, "\n# Where an agent's prompts.*.path resolves against, relative to this file.\nprompts: %s\n", m.Prompts)
	}
	return b.Bytes()
}

// tasksModule is where garm.tasks.v1 comes from. It is the platform package §1.1
// wants a new deployment to start with, and the only one that exists today —
// `garm.artefacts.v1` and the module serving it do not, so this file does not
// name them and pretend otherwise.
const tasksModule = "github.com/garm-ai/contracts"

// tasksPackage is spelled here as well as in internal/compiler because the two
// are answering different questions — one is a lint rule's requirement, the
// other is what a generated manifest suggests — and a shared constant between
// them would make internal/manifest import the linter to write a comment.
const tasksPackage = "garm.tasks.v1"

func declares(m *Manifest, pkg string) bool {
	for _, e := range m.Include {
		if containsString(e.Packages, pkg) {
			return true
		}
	}
	return false
}
