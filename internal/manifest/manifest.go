// Package manifest reads catalogue.yaml and resolves what it names.
//
// A catalogue used to be assembled from ONE directory, which meant everything
// a deployment did not author itself had to be copied into that directory.
// `examples/bank/proto` carried `web` and `tools` for exactly that reason, the
// copies had to be excluded from Go generation so the same descriptors were not
// registered twice, and a drift gate had to watch each copy because a copy
// drifts. On 2026-09-29 one had been five releases behind the module compiling
// it and the check meant to catch it compared the tree against a constant in
// its own config — it compared the tree to itself.
//
// The manifest replaces the copy with a reference. It names WHAT composes into
// a catalogue; `go.mod` says WHICH VERSION, because the generated Go already
// comes from there and a tree that compiled against one tag while declaring the
// descriptors of another would serve a tool whose wire shape does not match its
// own code. That is the descriptor mismatch that took the plane offline the
// same morning. So there is exactly one version per module and the builder
// refuses a module the tree does not require — see Resolve.
//
// The split of work here mirrors the rest of the repository: Parse takes bytes
// and returns values, Resolve is the half that runs `go list -m` and reads the
// module cache, and Packages takes a compiled set and decides which input
// contributed what. Nothing here compiles protos and nothing here writes a
// file.
package manifest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/policy"
)

// Filename is the manifest's name by convention, looked for in the working
// directory so that `garm catalogue build` needs no flag in the common case.
const Filename = "catalogue.yaml"

// Schema is the only schema version this binary reads.
//
// Named and checked rather than ignored: a manifest is a governance input, and
// a future key that changes what an existing key MEANS has to be refusable by
// an older binary rather than silently misread.
const Schema = "v1"

// Manifest is catalogue.yaml.
type Manifest struct {
	// Schema must be Schema.
	Schema string `yaml:"schema"`

	// Name is the deployment this catalogue is for. It appears in the messages
	// a refusal prints and identifies the manifest in a report; it is not
	// stamped into the artifact, which has no name field and is identified by
	// its digest.
	Name string `yaml:"name"`

	// Source is free-form provenance for this tree: a repository, a pipeline
	// id. Never parsed. `--source` overrides it, because CI knows a commit that
	// a checked-in file cannot.
	Source string `yaml:"source"`

	// Include is every input, in whatever order a human found readable. The
	// builder sorts, so this order is presentation: see catalogue.Build.
	Include []Entry `yaml:"include"`

	// Prompts is where an agent's prompts.*.path resolves against, relative to
	// the manifest's directory. Defaults to that directory.
	Prompts string `yaml:"prompts"`

	// Taxonomy is the access-control vocabulary of THIS deployment.
	//
	// Optional, and its absence is the pre-v0.22.0 behaviour: the vocabulary is
	// scraped from file-level proto options and unioned by name across every
	// file in the set. Declaring it here is how a deployment takes that back —
	// see Taxonomy.
	Taxonomy Taxonomy `yaml:"taxonomy"`
}

// Taxonomy is the compartments and tool sets a deployment declares.
//
// It exists because requiring a name and declaring one are two different acts
// that the same annotation conflated. A tool REQUIRES compartments by name on
// each method, which is the tool author's business and does not change. The
// vocabulary those names come from is the DEPLOYMENT's, and until v0.22.0 it
// was assembled by unioning a file-level proto option across every file in the
// set, first declaration winning, in traversal order.
//
// Two things were wrong with that, and the second is the one that matters. A
// deployment could not see its own access-control vocabulary in one place: the
// bank's seven compartments came from three protos with three different owners.
// And adopting a tool SILENTLY EXTENDED the vocabulary — adopt the web fetcher
// and `internet` becomes a compartment of your bank, because the tool's proto
// declares it. The deployment never said yes to the word; it said yes to the
// tool. For the vocabulary that governs who may see what, that is the wrong
// direction of consent.
//
// Declared here, an adopted tool naming a word this deployment has not declared
// is an error the deployment resolves deliberately: declare the word, or do not
// adopt the tool. It also catches a typo, which today is not an error but a
// silently distinct compartment — and a silently distinct compartment is a tool
// nobody can reach, refused for a reason nobody can see.
//
// It is NOT a contract change. The artifact carries these on `Catalogue`'s
// existing fields 3 and 4; they are sourced from here rather than scraped, and
// garmd's registry construction is untouched.
type Taxonomy struct {
	Compartments []Decl `yaml:"compartments"`
	ToolSets     []Decl `yaml:"tool_sets"`
}

// Decl is one declared name and what it means.
//
// The description is required rather than optional, because the point of moving
// the vocabulary here is that a deployment declares a word deliberately, and a
// name with no description is the same act of omission in a new file. It
// travels: into the artifact, into `garm claims check`'s report, and in front of
// whoever is deciding which compartments a role should hold.
type Decl struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Declared reports whether this manifest declares a vocabulary at all.
//
// One bool for both lists, deliberately. A manifest that declared compartments
// and no tool sets would otherwise mean "my tool sets still come from protos",
// and a vocabulary half in one place and half in another is worse than either
// whole. So declaring either takes over both, and a deployment with no tool
// sets writes `tool_sets: []` to say so — which reads as a decision, where an
// absent key reads as an oversight.
func (t Taxonomy) Declared() bool { return t.Compartments != nil || t.ToolSets != nil }

// Entry is one line of `include`: a directory in this tree, or proto packages
// from a module the tree requires.
//
// One struct with two shapes rather than a tagged union, because YAML has no
// good spelling for one and the validation is a single check. Which shape an
// entry is, is Module being set.
type Entry struct {
	// Path is a directory in the working tree, relative to the manifest.
	Path string `yaml:"path"`

	// Module is a Go module path.
	Module string `yaml:"module"`

	// Version is permitted on a module entry FOR READABILITY and must equal
	// what the module graph resolves to. A disagreement is an error and never a
	// precedence rule: if this file could pin a version go.mod does not, the
	// invariant the manifest exists to hold would be one careless edit from
	// gone.
	Version string `yaml:"version"`

	// Packages names the proto packages to take from the module. Required on a
	// module entry, forbidden on a path entry — a path entry contributes
	// whatever the tree holds, which is the deployment's own authorship and
	// not a selection from somebody else's.
	Packages []string `yaml:"packages"`

	// Prompts is where this input's agent prompts live, relative to the
	// module's root. A composed agent's prompts live in ITS module, so one
	// --prompts-root cannot find them. Parsed and carried here; `catalogue
	// publish` resolves them, and until it does the value is recorded and
	// unused.
	Prompts string `yaml:"prompts"`
}

// validate refuses a vocabulary that cannot mean what it says.
//
// The name format is contracts' own ValidateDeclName rather than a second
// implementation, because a declared name becomes four things at once — a Go
// constant, a JWT claim value, a ledger field and a bit position — and the
// exclusions it enforces are failure modes rather than style. Checking it HERE
// is the improvement on checking it in a lint rule: the refusal points at the
// line of YAML somebody typed.
func (t Taxonomy) validate(name string) error {
	if !t.Declared() {
		return nil
	}
	for _, l := range []struct {
		kind  string
		key   string
		decls []Decl
	}{
		{"compartment", "compartments", t.Compartments},
		{"tool set", "tool_sets", t.ToolSets},
	} {
		seen := map[string]int{}
		for i, d := range l.decls {
			where := fmt.Sprintf("taxonomy.%s[%d]", l.key, i)
			if err := policy.ValidateDeclName(d.Name); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			if prev, dup := seen[d.Name]; dup {
				return fmt.Errorf("%s: %s %q is declared twice (also taxonomy.%s[%d]). "+
					"One entry per name: two would be one word with two meanings, and "+
					"the second is the one nobody reads",
					where, l.kind, d.Name, l.key, prev)
			}
			seen[d.Name] = i
			if strings.TrimSpace(d.Description) == "" {
				return fmt.Errorf("%s: %s %q has no description. The point of declaring a "+
					"vocabulary here is that a deployment says yes to a word deliberately, "+
					"and a name with nothing beside it is the same omission in a new file — "+
					"somebody has to decide which roles hold %q and the description is what "+
					"they read", where, l.kind, d.Name, d.Name)
			}
		}
	}
	return nil
}

// IsModule answers which shape this entry is.
func (e Entry) IsModule() bool { return e.Module != "" }

// String names the entry the way an error message should, so that "two inputs
// declare web.v1" can print both and a reader knows which line to edit.
func (e Entry) String() string {
	if e.IsModule() {
		if e.Version != "" {
			return fmt.Sprintf("module %s@%s", e.Module, e.Version)
		}
		return "module " + e.Module
	}
	return "path " + e.Path
}

// Find reports the manifest in dir, if there is one.
//
// A bool and not an error, because "there is no manifest here" is an ordinary
// answer: it is how `catalogue build` knows to fall back to the deprecated
// --proto and still build a tree that has not migrated.
func Find(dir string) (string, bool) {
	p := filepath.Join(dir, Filename)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, true
	}
	return "", false
}

// Load reads and validates a manifest.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	m, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// Parse decodes a manifest and refuses one that cannot mean what it says.
//
// Unknown fields are an error. A misspelled key in a file that governs which
// tools a deployment may call is not a harmless extra: `packages:` typed
// `pacakges:` would leave a module entry with no packages, and the difference
// between "refused" and "quietly built without the task queue" is the outage
// of 2026-09-29.
func Parse(b []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	switch m.Schema {
	case Schema:
	case "":
		return fmt.Errorf("no schema: a catalogue manifest declares `schema: %s`", Schema)
	default:
		return fmt.Errorf("schema %q: this binary reads %s, so either the manifest "+
			"is newer than the tool or the value is a typo", m.Schema, Schema)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("no name: a manifest names the deployment it composes a catalogue for")
	}
	if len(m.Include) == 0 {
		return fmt.Errorf("%s: include is empty, so there is nothing to compose. A catalogue "+
			"with nothing in it would start a daemon that serves nothing", m.Name)
	}

	if err := m.Taxonomy.validate(m.Name); err != nil {
		return err
	}

	seenPath := map[string]Entry{}
	seenModule := map[string]Entry{}
	for i, e := range m.Include {
		where := fmt.Sprintf("include[%d]", i)
		switch {
		case e.Path == "" && e.Module == "":
			return fmt.Errorf("%s: neither `path:` nor `module:`, so it names no input", where)
		case e.Path != "" && e.Module != "":
			return fmt.Errorf("%s: both `path: %s` and `module: %s`. An input is a directory "+
				"in this tree or proto packages from a module, never both — split it in two",
				where, e.Path, e.Module)
		}
		if e.IsModule() {
			if len(e.Packages) == 0 {
				return fmt.Errorf("%s: module %s names no packages. A module is not an input on "+
					"its own: `packages:` says which proto packages to take from it, and taking "+
					"all of them would mean adopting whatever the module adds next",
					where, e.Module)
			}
			for _, pkg := range e.Packages {
				if !validPackage(pkg) {
					return fmt.Errorf("%s: %q is not a proto package name", where, pkg)
				}
			}
			if prev, dup := seenModule[e.Module]; dup {
				return fmt.Errorf("%s: module %s is included twice (also %s). One entry per "+
					"module, with every package it contributes — two entries would be two "+
					"provenance records for one version", where, e.Module, prev)
			}
			seenModule[e.Module] = e
			continue
		}
		if len(e.Packages) > 0 {
			return fmt.Errorf("%s: `packages:` on a path entry. A directory in this tree "+
				"contributes what it declares; naming a subset would mean the tree holds "+
				"declarations the catalogue leaves out, which is a tree to fix rather than "+
				"a list to filter", where)
		}
		if e.Version != "" {
			return fmt.Errorf("%s: `version:` on a path entry. A working tree has no version — "+
				"that is what makes it local, and why provenance records it as unpinned", where)
		}
		clean := filepath.ToSlash(filepath.Clean(e.Path))
		if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(e.Path) {
			return fmt.Errorf("%s: path %s leaves the tree. A local input is the deployment's "+
				"own declarations, and anything outside this tree is somebody else's and "+
				"belongs behind a `module:` entry that pins a version", where, e.Path)
		}
		if prev, dup := seenPath[clean]; dup {
			return fmt.Errorf("%s: path %s is included twice (also %s)", where, e.Path, prev)
		}
		seenPath[clean] = e
	}
	return nil
}

// validPackage is the proto grammar for a package name and nothing more
// permissive: identifiers separated by dots. A module entry names packages, and
// a name that is really a directory ("web/v1") has to say so here rather than
// produce "no such directory" three steps later.
func validPackage(pkg string) bool {
	if pkg == "" {
		return false
	}
	for _, part := range strings.Split(pkg, ".") {
		if part == "" {
			return false
		}
		for i, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			case r >= '0' && r <= '9' && i > 0:
			default:
				return false
			}
		}
	}
	return true
}

// PackageDir is the directory a proto package lives in, under an import root.
//
// The mapping is the convention every proto linter enforces and every tree in
// this estate follows: `garm.tasks.v1` is `garm/tasks/v1`. It is written down
// because a module entry names a PACKAGE and the compiler needs FILES, and this
// is the only place the two are joined. A module that does not follow it is
// refused by Resolve with a message that says which directory was looked for,
// rather than silently contributing nothing.
func PackageDir(pkg string) string {
	return strings.ReplaceAll(pkg, ".", "/")
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// Decls is the taxonomy as the contract spells it, ready for the artifact's
// fields 3 and 4 and for the lint registry.
//
// nil, nil when nothing is declared, which is how every caller distinguishes
// "this deployment declares its vocabulary" from "scrape it from the protos" —
// the same distinction Declared answers, in the shape the consumer needs.
//
// The conversion lives here rather than in the command that wires it, because
// what a line of catalogue.yaml MEANS is this package's business, and a mapping
// in cmd/ would be judgement in the layer that is only allowed to plumb.
func (t Taxonomy) Decls() (compartments, toolSets []*toolv1.Decl) {
	if !t.Declared() {
		return nil, nil
	}
	conv := func(ds []Decl) []*toolv1.Decl {
		// Non-nil even when empty: `tool_sets: []` is a deployment saying it
		// has none, and an empty slice is how that survives into an artifact
		// whose field 4 then reads as declared-and-empty rather than absent.
		out := make([]*toolv1.Decl, 0, len(ds))
		for _, d := range ds {
			out = append(out, &toolv1.Decl{Name: d.Name, Description: d.Description})
		}
		return out
	}
	return conv(t.Compartments), conv(t.ToolSets)
}
