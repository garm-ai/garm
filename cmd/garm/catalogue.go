package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/catalogue"
	"github.com/garm-ai/garm/internal/compile"
)

func newCatalogueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalogue",
		Short: "Build and inspect the artifact a daemon serves",
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newCatalogueBuildCmd(), newCatalogueDiffCmd(), newCataloguePublishCmd())
	return cmd
}

func newCatalogueBuildCmd() *cobra.Command {
	var protoDir, promptsRoot, out, source string
	var stampTime bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Compile a proto tree into a catalogue",
		Long: "build compiles the proto tree and writes the artifact a daemon loads\n" +
			"at boot.\n\n" +
			"It lints first and refuses on any error. A catalogue that does not\n" +
			"lint cannot be built — the alternative is an artifact that fails at\n" +
			"the daemon's mount check instead, where the author is not present\n" +
			"and the failure is an outage rather than a build error.\n\n" +
			"Compilation is in process, against a compiler this binary pins. The\n" +
			"digest identifies the catalogue, so it must not move because someone\n" +
			"upgraded a CLI on their laptop.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCatalogueBuild(cmd, protoDir, promptsRoot, out, source, stampTime)
		},
	}
	cmd.Flags().StringVar(&protoDir, "proto", "proto", "Root of the proto tree")
	cmd.Flags().StringVar(&promptsRoot, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the parent of --proto)")
	cmd.Flags().StringVarP(&out, "out", "o", "catalogue.binpb", "Where to write the artifact")
	cmd.Flags().StringVar(&source, "source", "", "Free-form provenance: a repository and commit, a pipeline id")
	cmd.Flags().BoolVar(&stampTime, "stamp-time", false,
		"Record the build time. Breaks byte-reproducibility: two builds of the same source will differ")
	return cmd
}

// runCatalogueBuild is the command's half of `catalogue build`: it resolves
// flags, compiles the tree, and prints what internal/catalogue decided.
//
// Every judgement lives over there — the lint gate, the refusal of a tree with
// no tools, the order the cards are synthesised in relative to the hashes. What
// is left here is what a command is for: where the tree is, where the artifact
// goes, which stream each line belongs on, and the exit code.
func runCatalogueBuild(cmd *cobra.Command, protoDir, promptsRoot, out, source string, stampTime bool) error {
	set, fds, err := compile.Tree(cmd.Context(), protoDir)
	if err != nil {
		return err
	}

	req := catalogue.Request{
		Set:         set,
		Descriptors: fds,
		Origin:      protoDir,
		PromptsRoot: resolvePromptsRoot(promptsRoot, protoDir),
		Source:      source,
		Producer:    "garm/" + version(),
		Compiler:    compile.Version(),
	}
	// The build time is off by default; the flag's help says what it costs and
	// Request.BuiltAt says why.
	if stampTime {
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

	if err := os.WriteFile(out, res.Body, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}

	cat := res.Catalogue
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
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
