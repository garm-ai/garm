package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/catalogue"
	"github.com/garm-ai/garm/internal/manifest"
)

func newLintCmd() *cobra.Command {
	var manifestPath, promptsDir string
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Check tool declarations without generating anything",
		Long: "lint composes the inputs catalogue.yaml declares, compiles them in\n" +
			"process, and applies garm's tool rules to the result — reporting what\n" +
			"`garm gen` and `garm catalogue build` would refuse.\n\n" +
			"It takes the same inputs the builder does, and for the same reason.\n" +
			"Several rules are questions about the ASSEMBLED catalogue rather than\n" +
			"about one declaration: whether an agent's allowlist names a tool that\n" +
			"exists, whether that tool may be offered to a model, and whether a\n" +
			"tool declaring a human approval has a task queue to open one on. A\n" +
			"linter that saw only the deployment's own directory would pass a tree\n" +
			"the build then refuses, which would make this command's promise false\n" +
			"for exactly the rules that are hardest to diagnose afterwards.\n\n" +
			"The same rules run inside the generator, so this cannot pass and then\n" +
			"fail at generation time. It needs no buf and no plugins: checking\n" +
			"whether a declaration is acceptable should not require installing a\n" +
			"toolchain, and a linter people cannot run easily is one they skip.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := inputFlags{manifest: manifestPath}
			m, dir, _, err := findManifest(f)
			if err != nil {
				return err
			}
			// The manifest's own `source:` and nothing from a flag: lint stamps
			// no artifact, so provenance has nowhere to go. It is passed at all
			// because a local input records it and Resolve takes it as a value.
			_, fds, _, err := compose(cmd.Context(), dir, m, m.Source)
			if err != nil {
				return err
			}
			// The same lint the catalogue's build gate runs, through the same
			// function, so there is one implementation of which diagnostics
			// are refusals. This command only reports: there is no artifact
			// here to gate.
			diags := catalogue.Check(fds, promptsRoot(promptsDir, m, dir), taxonomyOf(m))
			for _, d := range diags {
				fmt.Fprintln(cmd.ErrOrStderr(), d.String())
			}
			if errs := diags.Errors(); errs > 0 {
				return fmt.Errorf("%d garm tool policy error(s)", errs)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok — %d file(s), no errors\n", len(fds))
			return nil
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "f", "",
		"The manifest to compose from (default: "+manifest.Filename+" in the working directory)")
	cmd.Flags().StringVar(&promptsDir, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the manifest's "+
			"`prompts:`, or the manifest's own directory)")
	return cmd
}

// resolvePromptsRoot answers "relative to what" for an agent's prompt paths.
//
// The directory CONTAINING the proto tree, so that a checkout laid out as
// proto/ beside prompts/ — which is how the bank example is laid out and how
// the declaration `prompts/support-assistant.md` reads — needs no flag at all.
// `garm catalogue publish` has no proto tree and defaults to
// the working directory, which is the same place.
func resolvePromptsRoot(promptsRoot, protoDir string) string {
	if promptsRoot != "" {
		return promptsRoot
	}
	return filepath.Dir(protoDir)
}
