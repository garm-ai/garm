package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/garm/internal/catalogue"
	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
	"github.com/garm-ai/garm/internal/manifest"
)

func newCatalogueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalogue",
		Short: "Build and inspect the artifact a daemon serves",
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newCatalogueInitCmd(), newCatalogueBuildCmd(),
		newCatalogueDiffCmd(), newCataloguePublishCmd())
	return cmd
}

func newCatalogueBuildCmd() *cobra.Command {
	var manifestPath, promptsRoot, out, source string
	var stampTime bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Compose a catalogue from the inputs catalogue.yaml declares",
		Long: "build reads catalogue.yaml, composes every input it declares, and\n" +
			"writes the artifact a daemon loads at boot.\n\n" +
			"An input is a directory in this tree or proto packages from a Go\n" +
			"module the tree requires. The manifest names WHAT to include and\n" +
			"go.mod says WHICH VERSION: a module that is not a requirement is\n" +
			"refused, and a `version:` key is readability that has to agree with\n" +
			"what the module graph resolves. Nothing is fetched that `go mod\n" +
			"download` would not fetch — the protos are read from the module\n" +
			"cache, so this works offline once it is warm.\n\n" +
			"Two inputs declaring one proto package is refused, naming both. The\n" +
			"order of `include` is presentation: the inputs are sorted before\n" +
			"they are stamped, so the digest does not move when somebody\n" +
			"reorders the file.\n\n" +
			"It lints first and refuses on any error. A catalogue that does not\n" +
			"lint cannot be built — the alternative is an artifact that fails at\n" +
			"the daemon's mount check instead, where the author is not present\n" +
			"and the failure is an outage rather than a build error.\n\n" +
			"Compilation is in process, against a compiler this binary pins. The\n" +
			"digest identifies the catalogue, so it must not move because someone\n" +
			"upgraded a CLI on their laptop.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCatalogueBuild(cmd, buildFlags{
				inputFlags:  inputFlags{manifest: manifestPath},
				promptsRoot: promptsRoot,
				out:         out,
				source:      source,
				stampTime:   stampTime,
			})
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "f", "",
		"The manifest to compose from (default: "+manifest.Filename+" in the working directory)")
	cmd.Flags().StringVar(&promptsRoot, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the manifest's `prompts:`, "+
			"or the manifest's own directory)")
	cmd.Flags().StringVarP(&out, "out", "o", "catalogue.binpb", "Where to write the artifact")
	cmd.Flags().StringVar(&source, "source", "",
		"Free-form provenance: a repository and commit, a pipeline id. Overrides the manifest's `source:`")
	cmd.Flags().BoolVar(&stampTime, "stamp-time", false,
		"Record the build time. Breaks byte-reproducibility: two builds of the same source will differ")
	return cmd
}

// inputFlags is how a command was told what to compose: a named manifest, or
// none, in which case findManifest looks for one by convention.
//
// Its own type because `catalogue build` and `lint` take the same flag and
// must resolve it the same way. They used not to — lint took one directory
// and only the builder read a manifest — and the consequence was that the
// rules needing the whole assembled set (A3's allowlist, and P1's required
// platform package) saw a manifest's inputs from the builder and never from the
// linter. So `garm lint` over a composed tree checked the deployment's own
// protos and not what it adopts, which makes the linter's promise — that it
// cannot pass and then fail at build time — false for exactly the rules that
// are hardest to diagnose later.
type inputFlags struct {
	manifest string
}

// buildFlags is what the user typed, gathered so that resolving it into inputs
// is one function with no cobra in it.
type buildFlags struct {
	inputFlags
	promptsRoot string
	out         string
	source      string
	stampTime   bool
}

// runCatalogueBuild is the command's half of `catalogue build`: it finds the
// inputs, compiles them, and prints what internal/catalogue decided.
//
// Every judgement lives elsewhere — the module invariant and the collision rules
// in internal/manifest, the lint gate and the refusal of a tree with no tools
// and the order the cards are synthesised in relative to the hashes in
// internal/catalogue. What is left here is what a command is for: which input
// the flags name, where the artifact goes, which stream each line belongs on,
// and the exit code.
func runCatalogueBuild(cmd *cobra.Command, f buildFlags) error {
	m, dir, origin, err := findManifest(f.inputFlags)
	if err != nil {
		return err
	}

	source := f.source
	if source == "" {
		source = m.Source
	}
	set, fds, inputs, err := compose(cmd.Context(), dir, m, source)
	if err != nil {
		return err
	}

	req := catalogue.Request{
		Set:         set,
		Descriptors: fds,
		Origin:      origin,
		PromptsRoot: promptsRoot(f.promptsRoot, m, dir),
		Source:      source,
		Inputs:      manifest.Provenance(inputs),
		Taxonomy:    taxonomyOf(m),
		Producer:    "garm/" + version(),
		Compiler:    compile.Version(),
	}
	// The build time is off by default; the flag's help says what it costs and
	// Request.BuiltAt says why.
	if f.stampTime {
		req.BuiltAt = time.Now().UTC()
	}

	res, diags, err := catalogue.Build(req)
	// Printed whether the build succeeded or not, and never counted here: the
	// refusal is Build's, not this command's.
	for _, d := range diags {
		fmt.Fprintln(cmd.ErrOrStderr(), d.String())
	}
	if err != nil {
		return err
	}

	if err := os.WriteFile(f.out, res.Body, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", f.out, err)
	}

	cat := res.Catalogue
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", f.out)
	fmt.Fprintf(cmd.OutOrStdout(), "  %d tool(s) in %d package(s), %d file(s), %d documented field(s), schema v%d\n",
		len(res.ToolNames), len(cat.GetDescriptorHashes()), len(cat.GetFiles().GetFile()),
		len(cat.GetFieldDocs()), cat.GetAnnotationSchemaVersion())
	fmt.Fprintf(cmd.OutOrStdout(), "  %d synthesised card endpoint(s)\n", res.SynthesisedCards)
	fmt.Fprintf(cmd.OutOrStdout(), "  digest %s\n", res.Digest)
	for _, n := range res.ToolNames {
		fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", n)
	}
	return nil
}

// compose resolves a manifest's inputs and compiles them as one set.
//
// Shared by `catalogue build` and `garm lint` so that the two see the same
// assembled catalogue. That is not tidiness: the rules that need the whole set —
// A3's agent allowlist, A9's audience and P1's required platform package —
// cannot be checked against one input, and a linter that saw a subset of what
// the builder composes would pass a tree the build then refuses. `garm lint`'s
// whole promise is that it cannot pass and then fail later.
//
// The three refusals in here are the manifest's own and they belong to both
// commands for the same reason: a module the tree does not require, two inputs
// declaring one proto package, and a module entry naming a package the module
// does not declare are all questions about the assembled set, and the linter is
// the command people run first.
func compose(ctx context.Context, dir string, m *manifest.Manifest, source string) (
	*descriptorpb.FileDescriptorSet, []protoreflect.FileDescriptor, []*manifest.Input, error) {
	inputs, err := manifest.Resolve(ctx, dir, m, source)
	if err != nil {
		return nil, nil, nil, err
	}
	set, fds, err := compile.Union(ctx, manifest.Roots(inputs))
	if err != nil {
		return nil, nil, nil, err
	}
	// After the union and not before: which package an input declares is a fact
	// about the descriptors, and a check against the manifest's own names would
	// only report the manifest back to itself.
	if err := manifest.Packages(inputs, set); err != nil {
		return nil, nil, nil, err
	}
	return set, fds, inputs, nil
}

// findManifest decides what a command composes: the manifest a flag named, or
// the one convention found. Shared by `catalogue build` and `garm lint`, which
// must agree about it.
//
// It returns the manifest, the directory paths inside it resolve against, and
// the origin a refusal names.
func findManifest(f inputFlags) (*manifest.Manifest, string, string, error) {
	if f.manifest != "" {
		m, err := manifest.Load(f.manifest)
		if err != nil {
			return nil, "", "", err
		}
		return m, filepath.Dir(f.manifest), f.manifest, nil
	}

	if path, ok := manifest.Find("."); ok {
		m, err := manifest.Load(path)
		if err != nil {
			return nil, "", "", err
		}
		return m, ".", path, nil
	}

	// The only refusal left, and it is the only thing standing between a user
	// and confusion: name the directory it looked in and say exactly what to
	// run.
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return nil, "", "", fmt.Errorf("no %s in %s: a catalogue is composed from the inputs a "+
		"manifest declares, so there is nothing to compose. Run `garm catalogue init` here, "+
		"or pass --manifest to name one elsewhere", manifest.Filename, dir)
}

// promptsRoot answers "relative to what" for an agent's prompts.*.path.
//
// --prompts-root wins because it always did. Then the manifest's `prompts:`,
// relative to the manifest. Then the manifest's own directory, which is what
// an absent `prompts:` key means — see Manifest.Prompts's doc.
//
// A prompt that a COMPOSED agent pins lives in ITS module and is not reachable
// from any of these; `catalogue publish` resolves those per input, and the entry
// key that says where is parsed and carried already.
func promptsRoot(flag string, m *manifest.Manifest, dir string) string {
	switch {
	case flag != "":
		return flag
	case m.Prompts != "":
		return filepath.Join(dir, m.Prompts)
	default:
		return dir
	}
}

// taxonomyOf is the manifest's declared vocabulary in the shape the compiler and
// the builder take, or nil when the manifest declares none.
//
// Nil rather than an empty Taxonomy, and the difference is the whole migration:
// nil means "scrape the file-level proto options", which is what every tree
// built before v0.22.0 depends on and still gets from a manifest that declares
// no `taxonomy:` block. A non-nil value replaces the scrape entirely.
func taxonomyOf(m *manifest.Manifest) *compiler.Taxonomy {
	compartments, toolSets := m.Taxonomy.Decls()
	if compartments == nil && toolSets == nil {
		return nil
	}
	return &compiler.Taxonomy{Compartments: compartments, ToolSets: toolSets}
}
