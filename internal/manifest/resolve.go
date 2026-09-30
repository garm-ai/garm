package manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/garm-ai/garm/internal/compile"
)

// Kind is whether an input was read from the tree being built or from a pinned
// module.
//
// The two are not interchangeable and a reader deciding whether to trust a
// catalogue has to tell them apart: one is reproducible from a module path and
// a version, the other is whatever was on somebody's disk. The values are the
// contract's oneof field numbers, because the canonical order of a catalogue's
// inputs is "ascending by kind, local before module" and that order must not
// depend on how this package happens to spell its enum.
type Kind int

const (
	// KindLocal is a directory in the tree being built.
	KindLocal Kind = 1
	// KindModule is proto packages read from the Go module cache.
	KindModule Kind = 2
)

// Input is one resolved entry: where its protos actually are, and which of them
// to compile.
type Input struct {
	// Entry is the manifest line this came from, kept so a refusal can quote
	// what a human wrote rather than what the resolver made of it.
	Entry Entry

	Kind Kind

	// Identity is what provenance records: the path as the manifest wrote it
	// for a local input, the module path for a module one. Deliberately not the
	// absolute directory — that would make the artifact describe the machine
	// that built it.
	Identity string

	// Version is what the module graph RESOLVED to, recorded and never
	// requested. Empty for a local input, which has no version; that absence
	// is the honest record of a local build.
	Version string

	// Source is free-form provenance for a local input.
	Source string

	// Root is the import path for this input, on this machine.
	Root string

	// Files are this input's own protos, relative to Root. A file reached only
	// as an import is not here: it is a dependency of the set, not a
	// declaration of whoever imported it.
	Files []string

	// Packages is which proto packages this input turned out to declare, filled
	// in by Packages once the union has been compiled. For a module entry it is
	// checked against what the manifest asked for.
	Packages []string
}

// Resolve turns a manifest into inputs, and is where the one invariant lives.
//
// A module entry is resolved through the module graph of the tree at dir, and
// **refused if the tree does not require it**. That refusal is the point of the
// whole design: go.mod already pins every module whose generated Go this
// deployment compiles against, so taking the descriptor version from anywhere
// else would let a tree compile against one tag and declare another. Nothing is
// fetched that `go mod download` would not fetch, no registry is involved, and
// composition works offline once the cache is warm — which is the objection
// that ruled a schema registry out in the first place.
//
// source is the build's free-form provenance, already resolved from --source or
// the manifest, and is what a local input records.
func Resolve(ctx context.Context, dir string, m *Manifest, source string) ([]*Input, error) {
	mods, err := listModules(ctx, dir, modulePaths(m))
	if err != nil {
		return nil, err
	}

	inputs := make([]*Input, 0, len(m.Include))
	for _, e := range m.Include {
		var in *Input
		if e.IsModule() {
			in, err = resolveModule(ctx, dir, e, mods[e.Module])
		} else {
			in, err = resolveLocal(dir, e, source)
		}
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, in)
	}
	return inputs, nil
}

// Roots is what to hand compile.Union: every input's import path, in manifest
// order.
//
// Every root is on the import path for every file, which is what makes a
// cross-input import resolve — a bank proto importing `web/v1/web.proto` finds
// it in the module's root the same way buf finds it in a workspace. Order here
// is only which root wins if two offer the same relative path, and Packages
// refuses that rather than letting it be decided by position.
func Roots(inputs []*Input) []compile.Root {
	roots := make([]compile.Root, 0, len(inputs))
	for _, in := range inputs {
		roots = append(roots, compile.Root{Path: in.Root, Files: in.Files})
	}
	return roots
}

func modulePaths(m *Manifest) []string {
	var out []string
	for _, e := range m.Include {
		if e.IsModule() {
			out = append(out, e.Module)
		}
	}
	return out
}

// modInfo is the part of `go list -m -json` this needs.
type modInfo struct {
	Path    string
	Version string
	Dir     string
	Error   *struct{ Err string }
}

// listModules asks the go command what the tree resolves each module to.
//
// `go list -m` and not a parse of go.mod, because go.mod is a set of
// requirements and not an answer: minimal version selection, a replace, a
// workspace and an indirect requirement all decide the version that is actually
// built, and reimplementing that here would be a second module resolver that is
// wrong in ways nobody notices until the versions disagree. The go command is
// the only correct answer to "which version does this tree use".
//
// One invocation for every module, with -e so that a module the tree does not
// require comes back as a structured error on its own entry instead of aborting
// the batch — the refusal is per entry and its message names the line to fix.
func listModules(ctx context.Context, dir string, paths []string) (map[string]modInfo, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if !inAModule(dir) {
		return nil, fmt.Errorf("no go.mod in %s or any directory above it, and the manifest "+
			"includes %d module(s): a module entry names WHAT to include and go.mod says "+
			"WHICH VERSION, so a tree with no module graph has nothing to pin the "+
			"descriptors to", dir, len(paths))
	}
	out, err := run(ctx, dir, append([]string{"go", "list", "-m", "-e", "-json"}, paths...)...)
	if err != nil {
		return nil, err
	}
	mods := map[string]modInfo{}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var info modInfo
		if err := dec.Decode(&info); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading `go list -m -json` output: %w", err)
		}
		mods[info.Path] = info
	}
	return mods, nil
}

// inAModule reports whether dir is inside a Go module, by walking up for a
// go.mod the way the go command itself does.
//
// Up and not just in dir, because a deployment is not always its own module. The
// bank is one directory of `garm-ai/examples`, whose go.mod is at the repository
// root and whose requirements are the pins for every example in it — so a
// manifest beside the bank's protos is the normal case, not an odd one, and
// stopping at dir would refuse every module entry it wrote.
//
// This check only decides which MESSAGE a tree with no module graph gets: `go
// list -m` walks up on its own, so the resolution below is unaffected either
// way. It is here because "no go.mod, and the manifest names modules" is worth
// saying plainly rather than passing the go command's own wording through.
func inAModule(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if fi, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil && !fi.IsDir() {
			return true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false
		}
		abs = parent
	}
}

func resolveLocal(dir string, e Entry, source string) (*Input, error) {
	// An absolute path cannot come from a manifest — validate refuses one,
	// because an input outside the tree has no version and belongs behind a
	// `module:` entry. It can only arrive from the deprecated --proto, which
	// several pipelines pass an absolute directory to, and joining it to dir
	// would quietly turn "/tmp/x/proto" into "tmp/x/proto".
	root := e.Path
	if !filepath.IsAbs(root) {
		root = filepath.Join(dir, e.Path)
	}
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a directory. A path entry is an import root, so an "+
			"import inside it resolves the way it does in a buf workspace; a single file has "+
			"no root to resolve against", e)
	}
	files, err := compile.ProtoPaths(root)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no .proto files under %s", e, root)
	}
	return &Input{
		Entry:    e,
		Kind:     KindLocal,
		Identity: filepath.ToSlash(filepath.Clean(e.Path)),
		Source:   source,
		Root:     root,
		Files:    files,
	}, nil
}

func resolveModule(ctx context.Context, dir string, e Entry, info modInfo) (*Input, error) {
	if info.Error != nil || info.Version == "" {
		return nil, notRequired(e, info)
	}
	// A version key is readability, not a pin. Checked and never preferred:
	// if this file could name a version the module graph does not, a
	// deployment would be one careless edit away from compiling its Go
	// against one tag and declaring the descriptors of another.
	if e.Version != "" && e.Version != info.Version {
		return nil, fmt.Errorf("module %s: the manifest says %s and this tree resolves %s.\n\n"+
			"The manifest names WHAT to include; go.mod says WHICH VERSION, because the "+
			"generated Go already comes from there. A disagreement is not a precedence "+
			"question — one of the two is wrong. Either drop the `version:` key, correct it "+
			"to %s, or move the requirement:\n\n    go get %s@%s",
			e.Module, e.Version, info.Version, info.Version, e.Module, e.Version)
	}

	modDir := info.Dir
	if modDir == "" {
		// Required but not extracted: a fresh checkout where nothing has been
		// built yet. `go mod download` is the mapping from a version to a
		// directory and it is the same one the Go build uses, so ask it rather
		// than telling the author to.
		var err error
		if modDir, err = download(ctx, dir, e.Module, info.Version); err != nil {
			return nil, err
		}
	}

	// The import root, by the convention every tree in this estate follows: a
	// module's protos are under proto/, and its generated Go beside them. A
	// module that keeps them at its root still works, which is the fallback.
	root := filepath.Join(modDir, "proto")
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		root = modDir
	}

	in := &Input{
		Entry:    e,
		Kind:     KindModule,
		Identity: e.Module,
		Version:  info.Version,
		Root:     root,
	}
	for _, pkg := range sortedCopy(e.Packages) {
		pkgDir := filepath.Join(root, PackageDir(pkg))
		names, err := protosIn(pkgDir)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			rel := filepath.ToSlash(strings.TrimPrefix(pkgDir, modDir+string(os.PathSeparator)))
			return nil, fmt.Errorf("module %s@%s declares no %s.\n\n"+
				"A proto package lives in the directory its name spells, so it was looked "+
				"for at %s inside the module and there is no .proto file there. Either the "+
				"package moved between versions, or the name is a typo, or this module "+
				"never had it", e.Module, info.Version, pkg, rel)
		}
		for _, n := range names {
			in.Files = append(in.Files, filepath.ToSlash(filepath.Join(PackageDir(pkg), n)))
		}
	}
	return in, nil
}

// notRequired is the refusal that makes the invariant real, and the message has
// to teach because the fix is not obvious in one case.
//
// The obvious case is an adopted tool whose Go this deployment links: `go get`
// and the requirement is there. The case that catches people is a tool the
// deployment DECLARES but does not SERVE — the bank declares the web fetcher
// while webd, somebody else's process, answers it. No Go here calls that
// module, so `go mod tidy` removes the requirement, and an entry that exists
// only to pin a proto version is deleted by housekeeping. The answer is the
// ordinary Go idiom for a dependency you pin without calling: an explicit blank
// import. So the message says so, because the alternative is that somebody
// reads §2's invariant as a bug.
func notRequired(e Entry, info modInfo) error {
	detail := "this tree does not require it"
	if info.Error != nil {
		detail = info.Error.Err
	}
	return fmt.Errorf("module %s: %s.\n\n"+
		"The manifest names WHAT to include and go.mod says WHICH VERSION, so a module "+
		"that is not a requirement of this tree cannot be an input: there would be no one "+
		"version, and a catalogue that declares descriptors the tree does not compile "+
		"against is the mismatch this manifest exists to prevent. Require it:\n\n"+
		"    go get %s@<version>\n\n"+
		"If this deployment DECLARES the tool but does not serve it — another process "+
		"does, and no Go here calls it — that requirement will not survive `go mod tidy`, "+
		"which removes what nothing imports. Pin it with an explicit blank import of the "+
		"module's generated package, which is the ordinary Go idiom for a dependency you "+
		"depend on without calling (the package's `go_package` option names the import "+
		"path):\n\n"+
		"    import _ \"%s/gen/%s\"",
		e.Module, detail, e.Module, e.Module, PackageDir(e.Packages[0]))
}

func download(ctx context.Context, dir, path, version string) (string, error) {
	out, err := run(ctx, dir, "go", "mod", "download", "-json", path+"@"+version)
	if err != nil {
		return "", fmt.Errorf("module %s@%s is required but not in the module cache, and "+
			"downloading it failed: %w", path, version, err)
	}
	var info modInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return "", fmt.Errorf("reading `go mod download -json %s@%s` output: %w", path, version, err)
	}
	if info.Dir == "" {
		return "", fmt.Errorf("module %s@%s downloaded but reports no directory", path, version)
	}
	return info.Dir, nil
}

// protosIn lists the .proto files directly in dir, and not below it: a proto
// package is one directory, and a subdirectory is a different package that a
// manifest entry has to name for itself.
func protosIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []string
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".proto") {
			out = append(out, ent.Name())
		}
	}
	return sortedCopy(out), nil
}

// run executes a go command in dir and returns its stdout.
//
// Shelling out to `go` rather than linking golang.org/x/mod: the question is
// not what go.mod says but what the go command RESOLVES, and only the go
// command on this machine — with this GOFLAGS, this GOPROXY, this workspace —
// can answer that. `garm gen` already shells out to buf for the same kind of
// reason.
func run(ctx context.Context, dir string, argv ...string) (string, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", strings.Join(argv, " "), msg)
	}
	return stdout.String(), nil
}
