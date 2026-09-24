package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newGenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gen [-- buf args...]",
		Short: "Generate code from the proto tree",
		Long: "gen runs `buf generate` against the buf configuration in the\n" +
			"current directory.\n\n" +
			"It is a thin wrapper on purpose. buf is the toolchain a proto\n" +
			"repository already has, and hiding it behind a garm-shaped\n" +
			"interface would mean anyone with an existing buf pipeline has to\n" +
			"choose between the two. Arguments after -- are passed straight\n" +
			"through.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuf(cmd, append([]string{"generate"}, args...))
		},
	}
}

// runBuf shells out to buf, streaming its output rather than capturing it:
// buf's diagnostics are the useful part, and reformatting them would only
// make them harder to match against buf's own documentation.
func runBuf(cmd *cobra.Command, args []string) error {
	bin, err := exec.LookPath("buf")
	if err != nil {
		return fmt.Errorf("buf is not on PATH: %w\n"+
			"garm uses buf to compile protos. Install it with `mise install`, "+
			"or see https://buf.build/docs/installation", err)
	}
	c := exec.Command(bin, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		// buf has already said what was wrong on stderr; wrapping its exit
		// status in a second message would be noise.
		return fmt.Errorf("buf %s: %w", args[0], err)
	}
	return nil
}
