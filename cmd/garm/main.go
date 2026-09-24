// Command garm is the garm command line tool: it scaffolds a proto tree,
// compiles governed tool declarations, and refuses the bad ones.
//
// It is not the server. The server is garmd, and nothing here depends on it.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/cli"
	"github.com/garm-ai/garm/internal/plugin"
)

func main() {
	root := newRoot()
	if err := cli.CheckSingleDash(root, os.Args[1:], "garm"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := cli.RequireSubcommand(&cobra.Command{
		Use:   "garm",
		Short: "Author, compile and check governed tool contracts",
		Long: "garm is the command line side of the tool plane: it scaffolds a\n" +
			"proto tree with the annotations vendored in, compiles tool\n" +
			"declarations, and refuses the ones that declare governance the\n" +
			"build cannot honour.\n\n" +
			"The server is garmd, a separate program. Nothing here talks to it.",
		SilenceUsage: true,
	})
	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newGenCmd(),
		newLintCmd(),
		newPluginCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version())
			return nil
		},
	}
}

// version reports the module version stamped by the Go toolchain, which is
// the tag for an installed build and "(devel)" for one built from a working
// tree. It is not a variable set by -ldflags: the annotations and this
// binary ship as one module, so the module version IS the contract version,
// and a separately injected string could disagree with it.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "dev"
	}
	return info.Main.Version
}

// newPluginCmd exposes the protoc plugin as a subcommand, so a buf.gen.yaml
// can say `local: [garm, protoc-gen-go]` and a consumer installs one binary.
//
// Hidden because it is not for typing: protoc speaks to it over stdin and
// stdout, and a human running it by hand gets a hang, not an error. The
// conventionally named standalone binary is cmd/protoc-gen-garm-go, for
// anyone who prefers PATH discovery.
func newPluginCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "protoc-gen-go",
		Short:  "Run as protoc-gen-garm-go (invoked by protoc, not by you)",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run:    func(*cobra.Command, []string) { plugin.Run() },
	}
}
