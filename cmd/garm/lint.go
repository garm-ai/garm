package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

func newLintCmd() *cobra.Command {
	var protoDir, promptsRoot string
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Check tool declarations without generating anything",
		Long: "lint compiles the proto tree in process and applies garm's tool rules\n" +
			"to it, reporting what `garm gen` would refuse.\n\n" +
			"The same rules run inside the generator, so this cannot pass and then\n" +
			"fail at generation time. It needs no buf and no plugins: checking\n" +
			"whether a declaration is acceptable should not require installing a\n" +
			"toolchain, and a linter people cannot run easily is one they skip.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, fds, err := compile.Tree(cmd.Context(), protoDir)
			if err != nil {
				return err
			}
			diags := compiler.LintWith(fds, compiler.Options{
				PromptsRoot: resolvePromptsRoot(promptsRoot, protoDir),
			})
			errs := 0
			for _, d := range diags {
				fmt.Fprintln(cmd.ErrOrStderr(), d.String())
				if !d.Warn {
					errs++
				}
			}
			if errs > 0 {
				return fmt.Errorf("%d garm tool policy error(s)", errs)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok — %d file(s), no errors\n", len(fds))
			return nil
		},
	}
	cmd.Flags().StringVar(&protoDir, "proto", "proto", "Root of the proto tree")
	cmd.Flags().StringVar(&promptsRoot, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the parent of --proto)")
	return cmd
}

// resolvePromptsRoot answers "relative to what" for an agent's prompt paths.
//
// The directory CONTAINING the proto tree, so that a checkout laid out as
// proto/ beside prompts/ — which is how the bank example is laid out and how
// the declaration `prompts/support-assistant.md` reads — needs no flag at all.
// `garm catalogue publish`, when it lands, has no proto tree and defaults to
// the working directory, which is the same place.
func resolvePromptsRoot(promptsRoot, protoDir string) string {
	if promptsRoot != "" {
		return promptsRoot
	}
	return filepath.Dir(protoDir)
}
