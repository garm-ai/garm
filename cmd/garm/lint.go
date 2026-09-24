package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

func newLintCmd() *cobra.Command {
	var protoDir string
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
			diags := compiler.Lint(fds)
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
	return cmd
}
