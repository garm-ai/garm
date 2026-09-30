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
	var manifestPath, protoDir, promptsRoot, out, source string
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
				inputFlags: inputFlags{
					manifest: manifestPath,
					protoDir: protoDir,
					protoSet: cmd.Flags().Changed("proto"),
				},
				promptsRoot: promptsRoot,
				out:         out,
				source:      source,
				stampTime:   stampTime,
			})
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "f", "",
		"The manifest to compose from (default: "+manifest.Filename+" in the working directory)")
	cmd.Flags().StringVar(&protoDir, "proto", "proto",
		"DEPRECATED: build from this one directory instead of a manifest, as a manifest with a "+
			"single `path:` entry. Used only when there is no "+manifest.Filename+", or when given "+
			"explicitly. Write a manifest instead: it is the input that can name a module")
	cmd.Flags().StringVar(&promptsRoot, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the manifest's `prompts:`, "+
			"or the parent of --proto)")
	cmd.Flags().StringVarP(&out, "out", "o", "catalogue.binpb", "Where to write the artifact")
	cmd.Flags().StringVar(&source, "source", "",
		"Free-form provenance: a repository and commit, a pipeline id. Overrides the manifest's `source:`")
	cmd.Flags().BoolVar(&stampTime, "stamp-time", false,
		"Record the build time. Breaks byte-reproducibility: two builds of the same source will differ")
	return cmd
}

// inputFlags is how a command was told what to compose: the manifest, or the
// deprecated single directory.
//
// Its own type because `catalogue build` and `lint` take the same three flags
// and must resolve them the same way. They used not to — lint took one
// directory and only the builder read a manifest — and the consequence was that
// the rules needing the whole assembled set (A3's allowlist, and P1's required
// platform package) saw a manifest's inputs from the builder and never from the
// linter. So `garm lint` over a composed tree checked the deployment's own
// protos and not what it adopts, which makes the linter's promise — that it
// cannot pass and then fail at build time — false for exactly the rules that
// are hardest to diagnose later.
type inputFlags struct {
	manifest string
	protoDir string
	// protoSet records whether --proto was given, which is what distinguishes
	// "this tree has not migrated" from "somebody asked for the old behaviour".
	// A default value cannot answer that, and getting it wrong would mean a
	// tree with a manifest silently built from proto/ instead.
	protoSet bool
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
		PromptsRoot: promptsRoot(f.promptsRoot, f.inputFlags, m, dir),
		Source:      source,
		Inputs:      manifest.Provenance(inputs),
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

// findManifest decides what a command composes: the manifest a flag named, the
// one convention found, or the single directory --proto names. Shared by
// `catalogue build` and `garm lint`, which must agree about it.
//
// The precedence is the one that keeps `garm catalogue build` with no arguments
// right in both worlds. A manifest is the primary input, so it wins whenever
// there is one; a tree that has not migrated still builds from proto/, because
// every runbook and CI pipeline in the estate passes --proto and none of them
// should break on a minor release. What is NOT allowed is both at once: --proto
// and --manifest name two different inputs, and picking one silently would build
// something other than what was asked for.
//
// It returns the manifest, the directory paths inside it resolve against, and
// the origin a refusal names.
func findManifest(f inputFlags) (*manifest.Manifest, string, string, error) {
	switch {
	case f.manifest != "" && f.protoSet:
		return nil, "", "", fmt.Errorf("--manifest %s and --proto %s name two different inputs. "+
			"A manifest already says which directories compose into the catalogue, so pass one "+
			"or the other", f.manifest, f.protoDir)

	case f.manifest != "":
		m, err := manifest.Load(f.manifest)
		if err != nil {
			return nil, "", "", err
		}
		return m, filepath.Dir(f.manifest), f.manifest, nil

	case !f.protoSet:
		if path, ok := manifest.Find("."); ok {
			m, err := manifest.Load(path)
			if err != nil {
				return nil, "", "", err
			}
			return m, ".", path, nil
		}
		if _, err := os.Stat(f.protoDir); err != nil {
			return nil, "", "", fmt.Errorf("no %s here and no %s/ either: a catalogue is "+
				"composed from the inputs a manifest declares, so there is nothing to "+
				"compose. Write one with `garm catalogue init`, or name a directory with "+
				"--proto", manifest.Filename, f.protoDir)
		}
	}
	// The single-input case, spelled as what it is: a manifest with one path
	// entry. Composition is then one code path rather than two, so the
	// deprecated flag cannot drift away from the supported input — and the
	// artifact records its one local input the same way a composed one does.
	return singleTree(f.protoDir), ".", f.protoDir, nil
}

func singleTree(dir string) *manifest.Manifest {
	return &manifest.Manifest{
		Schema:  manifest.Schema,
		Name:    dir,
		Include: []manifest.Entry{{Path: dir}},
	}
}

// promptsRoot answers "relative to what" for an agent's prompts.*.path.
//
// --prompts-root wins because it always did. Then the manifest's `prompts:`,
// relative to the manifest — which is the deployment's own answer and the one a
// composed build needs, since the tree's root is no longer derivable from a
// --proto directory. Then the historical default: the parent of --proto, because
// `--proto proto` means the prompts are beside it and not inside it.
//
// A prompt that a COMPOSED agent pins lives in ITS module and is not reachable
// from any of these; `catalogue publish` resolves those per input, and the entry
// key that says where is parsed and carried already.
func promptsRoot(flag string, f inputFlags, m *manifest.Manifest, dir string) string {
	switch {
	case flag != "":
		return flag
	case m.Prompts != "":
		return filepath.Join(dir, m.Prompts)
	default:
		return resolvePromptsRoot("", f.protoDir)
	}
}
