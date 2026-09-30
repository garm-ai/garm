package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/manifest"
)

func newCatalogueInitCmd() *cobra.Command {
	var name, source, protoDir string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Write the catalogue.yaml that describes this tree",
		Long: "init writes " + manifest.Filename + ", the file naming everything that\n" +
			"composes into this deployment's catalogue.\n\n" +
			"Its real job is migration rather than scaffolding. Every deployment\n" +
			"that predates the manifest is in the copied state: adopted packages\n" +
			"sitting in proto/, a matching exclude_paths entry keeping them out of\n" +
			"Go generation, a drift gate watching them, and the versions pinned in\n" +
			"go.mod. The manifest describing such a tree already exists, spread\n" +
			"across three files, and this command reads it out of them — a `path:`\n" +
			"entry for the deployment's own protos and a `module:` entry for each\n" +
			"copy it can match to a requirement in the module graph. Then the\n" +
			"copies can be deleted, along with the exclude_paths and the drift gate\n" +
			"that exist only because of them.\n\n" +
			"WHAT IT DOES NOT DO IS GUESS. Which directories hold copies comes from\n" +
			"buf.gen.yaml's exclude_paths, which is the deployment's own statement\n" +
			"that those trees are somebody else's. Which proto package each copy\n" +
			"declares comes from the compiled descriptor, not from the directory\n" +
			"name. Which module it came from comes from the copy's own go_package,\n" +
			"resolved by `go list`. When any of those is missing there is no entry,\n" +
			"the copy is left alone, and the report says why — a copied package the\n" +
			"tree does not require is a deployment compiling against descriptors it\n" +
			"does not depend on, which is a defect and not an entry to invent.\n\n" +
			"A hand-edited manifest is the authority and a generator is not, so an\n" +
			"existing " + manifest.Filename + " is never overwritten without --force.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCatalogueInit(cmd, dir, initFlags{
				name: name, source: source, protoDir: protoDir, force: force,
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "",
		"The deployment this catalogue is for (default: the directory's name)")
	cmd.Flags().StringVar(&source, "source", "",
		"Free-form provenance: a repository, a pipeline id. Never parsed")
	cmd.Flags().StringVar(&protoDir, "proto", "",
		"The deployment's own proto tree, when there is no "+manifest.BufGenFilename+
			" to read it from (default: proto)")
	cmd.Flags().BoolVar(&force, "force", false,
		"Overwrite an existing "+manifest.Filename)
	return cmd
}

type initFlags struct {
	name     string
	source   string
	protoDir string
	force    bool
}

// runCatalogueInit is the command's half: it reads the tree, hands the facts to
// internal/manifest, writes what comes back and prints the report.
//
// The judgement is all next door — which copy resolves to which module, which
// one admits no entry and why, what the file says. What is here is reading
// buf.gen.yaml, compiling the tree, the refusal to overwrite, and the report's
// wording.
func runCatalogueInit(cmd *cobra.Command, dir string, f initFlags) error {
	out := filepath.Join(dir, manifest.Filename)
	if _, err := os.Stat(out); err == nil && !f.force {
		return fmt.Errorf("%s already exists. A hand-edited manifest is the authority on "+
			"what composes into this catalogue and a generator is not, so it is not "+
			"overwritten: read it, or pass --force to replace it", out)
	}

	inputs, err := localInputs(dir, f.protoDir)
	if err != nil {
		return err
	}

	req := manifest.DescribeRequest{
		Name:   f.name,
		Source: f.source,
		// The manifest's directory, which is where a deployment laid out as
		// proto/ beside prompts/ keeps them — the same answer the historical
		// default (the parent of --proto) gives. Written explicitly because a
		// composed build has no --proto to derive it from.
		Prompts: ".",
	}
	if req.Name == "" {
		req.Name = deploymentName(dir)
	}
	for _, in := range inputs {
		req.Paths = append(req.Paths, in.dir)
	}

	// One compilation over every input directory together, through the same
	// compiler the builder uses. Not a scan for `package x.y;` with a regular
	// expression: which package a file declares is what the compiler says it
	// declares, and this command's whole discipline is that every fact it writes
	// was read from something rather than inferred.
	roots := make([]compile.Root, 0, len(inputs))
	for _, in := range inputs {
		roots = append(roots, compile.Root{Path: filepath.Join(dir, in.dir), Files: in.files})
	}
	set, _, err := compile.Union(cmd.Context(), roots)
	if err != nil {
		return err
	}
	declared, err := declaredPackages(inputs, set)
	if err != nil {
		return err
	}
	for _, d := range declared {
		if d.copied {
			req.Copies = append(req.Copies, d.Copied)
			continue
		}
		req.LocalPackages = append(req.LocalPackages, d.Package)
	}

	plan, err := manifest.Describe(cmd.Context(), dir, req)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, manifest.Render(plan.Manifest), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	report(cmd.OutOrStdout(), out, plan)
	if len(plan.Unpinned) > 0 {
		// Non-zero, because the report IS the output in this case and a
		// migration script that ignored it would delete a copy nothing pins.
		// The manifest is still written: it describes every input that could be
		// described, and the one that could not is named.
		return fmt.Errorf("%d copied proto package(s) have no entry: nothing in this tree "+
			"pins them", len(plan.Unpinned))
	}
	return nil
}

// localInput is one input directory and the .proto files under it.
type localInput struct {
	dir      string
	files    []string
	excluded []string
}

// localInputs reads which directories compose into the catalogue today, and
// which trees inside them are copies.
//
// buf.gen.yaml is the source, because it is where a tree already states both:
// `inputs[].directory` is what the catalogue is built from and
// `exclude_paths` is the deployment's own declaration that those subtrees are
// somebody else's — they are excluded precisely because their generated Go
// arrives through a module instead, and a second copy of the same descriptors
// panics a binary at init.
//
// With no buf.gen.yaml there is nothing to read and nothing to infer, so the
// tree is one directory with no copies: a new project, or one that has already
// migrated and deleted its exclusions.
func localInputs(dir, protoFlag string) ([]localInput, error) {
	gen, found, err := manifest.LoadBufGen(dir)
	if err != nil {
		return nil, err
	}
	var out []localInput
	if found && protoFlag == "" {
		for _, in := range gen.Directories() {
			out = append(out, localInput{dir: in.Directory, excluded: in.ExcludePaths})
		}
	}
	if len(out) == 0 {
		d := protoFlag
		if d == "" {
			d = "proto"
		}
		out = []localInput{{dir: d}}
	}
	for i := range out {
		files, err := compile.ProtoPaths(filepath.Join(dir, out[i].dir))
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("no .proto files under %s: there is no tree here to "+
				"describe. Name the deployment's own protos with --proto, or write the "+
				"manifest by hand", filepath.Join(dir, out[i].dir))
		}
		out[i].files = files
	}
	return out, nil
}

// declaration is one proto package the tree declares, and whether the tree says
// it is a copy.
type declaration struct {
	manifest.Copied
	copied bool
}

// declaredPackages reads each package off the compiled set and decides, per
// package, whether it is the deployment's own or an adopted copy.
//
// Per PACKAGE and not per file, because a manifest entry names packages: two
// files of one package must agree about which input they belong to, and a
// package split across an excluded directory and a kept one is a tree nobody
// can describe — so it is refused here rather than half-migrated.
func declaredPackages(inputs []localInput, set *descriptorpb.FileDescriptorSet) ([]declaration, error) {
	// Which input owns each compiled file, and whether that file sits under an
	// exclusion. Files the compiler pulled in as imports are not in any input
	// and are skipped: a dependency of the set is not a declaration of it.
	type origin struct {
		dir    string
		copied bool
	}
	// Keyed by the path relative to the input's own root, which is the spelling a
	// descriptor carries. Two inputs offering the same relative path would
	// collide here and the last would win — a state manifest.Packages refuses by
	// name at build time, and one buf itself will not generate from, so it is not
	// worth a second refusal in a command that reads.
	owner := map[string]origin{}
	for _, in := range inputs {
		for _, rel := range in.files {
			// exclude_paths are relative to the workspace root, so they carry
			// the input directory's own prefix: `proto/web`, not `web`. That is
			// buf's spelling and the comparison is made in it.
			full := path.Join(filepath.ToSlash(in.dir), rel)
			owner[rel] = origin{dir: path.Dir(full), copied: underAny(full, in.excluded)}
		}
	}

	byPkg := map[string]*declaration{}
	var order []string
	for _, f := range set.GetFile() {
		o, ok := owner[f.GetName()]
		if !ok || f.GetPackage() == "" {
			continue
		}
		pkg := f.GetPackage()
		d, seen := byPkg[pkg]
		if !seen {
			d = &declaration{
				Copied: manifest.Copied{
					Package:  pkg,
					Dir:      o.dir,
					GoImport: f.GetOptions().GetGoPackage(),
				},
				copied: o.copied,
			}
			byPkg[pkg] = d
			order = append(order, pkg)
			continue
		}
		if d.copied != o.copied {
			return nil, fmt.Errorf("proto package %s is declared both inside and outside "+
				"this tree's exclude_paths (%s and %s). A package is one input's: either it "+
				"is the deployment's own or it is adopted, and a manifest cannot say half "+
				"of each", pkg, d.Dir, o.dir)
		}
	}

	sort.Strings(order)
	out := make([]declaration, 0, len(order))
	for _, pkg := range order {
		out = append(out, *byPkg[pkg])
	}
	return out, nil
}

// underAny reports whether a file sits under one of buf's exclude_paths.
//
// Prefix on a path SEGMENT boundary, so `proto/web` excludes `proto/web/v1` and
// never `proto/website`.
func underAny(file string, excluded []string) bool {
	for _, ex := range excluded {
		ex = strings.TrimSuffix(filepath.ToSlash(ex), "/")
		if ex == "" {
			continue
		}
		if file == ex || strings.HasPrefix(file, ex+"/") {
			return true
		}
	}
	return false
}

// deploymentName is the manifest's `name:` when nobody passed one: the
// directory's own name, which for every deployment in this estate is what the
// deployment is called.
func deploymentName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "catalogue"
	}
	base := filepath.Base(abs)
	if base == "" || base == string(filepath.Separator) || base == "." {
		return "catalogue"
	}
	return base
}

// report prints what was written, what it adopted, and what it refused to
// invent.
//
// The last of those is the reason this command is worth running against a tree
// nobody intends to migrate today: a copied package the tree does not require is
// a deployment whose declarations and whose generated code can differ by any
// amount, with nothing watching. The report is the useful output.
func report(w io.Writer, out string, p *manifest.Plan) {
	fmt.Fprintf(w, "wrote %s\n", out)
	for _, e := range p.Manifest.Include {
		if e.IsModule() {
			fmt.Fprintf(w, "  module %s  %s\n", e.Module, strings.Join(e.Packages, ", "))
			continue
		}
		fmt.Fprintf(w, "  path   %s  %s\n", e.Path, strings.Join(p.LocalPackages, ", "))
	}

	if len(p.Adopted) > 0 {
		fmt.Fprintf(w, "\n%d copied package(s) are now module entries and the copies can go:\n",
			len(p.Adopted))
		for _, a := range p.Adopted {
			fmt.Fprintf(w, "  %s  (%s)  <-  %s@%s\n", a.Dir, a.Package, a.Module, a.Version)
		}
		fmt.Fprintf(w, "\nDelete those directories, and the %s exclude_paths entries that kept\n"+
			"them out of Go generation, and whatever drift gate was watching them: each of\n"+
			"the three exists only because the copy did. Until the copies are gone,\n"+
			"`garm catalogue build` refuses the tree by name — two inputs declaring one\n"+
			"proto package — which is that check working.\n", manifest.BufGenFilename)
	}

	if len(p.Unpinned) > 0 {
		fmt.Fprintf(w, "\n%d copied package(s) have NO entry, and this is the part to read.\n"+
			"A copy the tree does not pin is not a gap in this command's output — it is a\n"+
			"defect in the tree, and naming it is the useful work:\n", len(p.Unpinned))
		for _, u := range p.Unpinned {
			fmt.Fprintf(w, "\n  %s declares %s.\n", u.Dir, u.Package)
			fmt.Fprint(w, indent(wrap(u.Why, 74), "  "))
		}
	}
}

// wrap fills a paragraph to width columns, leaving a line that is already
// indented alone.
//
// Presentation, so it is here and not in internal/manifest: the refusal that
// package composes is one string of prose, and how wide a terminal is has
// nothing to do with why a copy admits no entry. An indented line is a command
// to type and is emitted verbatim — rewrapping a `go get` would break it.
func wrap(text string, width int) string {
	var out strings.Builder
	for i, para := range strings.Split(text, "\n\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		if strings.HasPrefix(para, " ") {
			out.WriteString(strings.TrimRight(para, " ") + "\n")
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				out.WriteString(line + "\n")
				line = word
			}
		}
		if line != "" {
			out.WriteString(line + "\n")
		}
	}
	return out.String()
}

// indent prefixes every non-empty line.
func indent(text, prefix string) string {
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			out.WriteString("\n")
			continue
		}
		out.WriteString(prefix + line + "\n")
	}
	return out.String()
}
