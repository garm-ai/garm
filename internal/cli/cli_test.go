package cli_test

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/garm-ai/garm/internal/cli"
)

func runnable(use string) *cobra.Command {
	return &cobra.Command{Use: use, RunE: func(*cobra.Command, []string) error { return nil }}
}

func root(t *testing.T) *cobra.Command {
	t.Helper()
	r := &cobra.Command{Use: "whatever"}
	sub := &cobra.Command{Use: "run"}
	sub.Flags().String("config", "x.yaml", "")
	r.AddCommand(sub)
	return r
}

// The suggested command line names the binary it was given. A helper shared
// by three binaries that hardcoded one of their names would print confident,
// wrong advice in the other two.
func TestCheckSingleDashNamesTheCallingBinary(t *testing.T) {
	err := cli.CheckSingleDash(root(t), []string{"run", "-config", "x.yaml"}, "garm-control")
	if err == nil {
		t.Fatal("single-dash long flag not caught")
	}
	if !strings.Contains(err.Error(), "garm-control run --config x.yaml") {
		t.Fatalf("suggestion does not name the calling binary:\n%s", err)
	}
	if strings.Contains(err.Error(), "  garm run") {
		t.Fatalf("suggestion names the wrong binary:\n%s", err)
	}
}

// Everything the scan does not recognise stays pflag's to report.
func TestCheckSingleDashLeavesEverythingElseAlone(t *testing.T) {
	for name, args := range map[string][]string{
		"correct long form": {"run", "--config", "x.yaml"},
		"unknown flag":      {"run", "-nosuch"},
		"bare dash":         {"run", "-"},
		"after terminator":  {"run", "--", "-config"},
		"value like a flag": {"run", "--config", "-config"},
		"no args":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			if err := cli.CheckSingleDash(root(t), args, "x"); err != nil {
				t.Fatalf("CheckSingleDash(%q) = %v; want nil", args, err)
			}
		})
	}
}

// A parent with no subcommand must fail, not print help and exit 0 — a CI
// step whose variable is unset would otherwise pass having done nothing.
func TestRequireSubcommandRefusesAndNamesTheOptions(t *testing.T) {
	// Real subcommands have a RunE; cobra's IsAvailableCommand — which
	// RequireSubcommand uses to skip hidden commands — also treats a command
	// with nothing to run as unavailable, so the stubs need one.
	parent := &cobra.Command{Use: "forward"}
	parent.AddCommand(runnable("usage"), runnable("audit"))
	cli.RequireSubcommand(parent)

	if parent.RunE == nil {
		t.Fatal("no RunE: the parent would print help and exit 0")
	}
	err := parent.RunE(parent, nil)
	if err == nil {
		t.Fatal("parent with no subcommand succeeded")
	}
	for _, want := range []string{"audit", "usage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name subcommand %q: %v", want, err)
		}
	}
	// A misspelling is the same failure wearing a different hat.
	if err := parent.RunE(parent, []string{"usag"}); err == nil {
		t.Fatal("misspelled subcommand succeeded")
	} else if !strings.Contains(err.Error(), "usag") {
		t.Errorf("error does not quote what was typed: %v", err)
	}
}

// RequireSubcommand must not advertise hidden commands. `garm` keeps
// tombstones for the verbs that moved to garmdev; listing them as options
// would send the reader straight back to the error that told them the
// command had moved.
func TestRequireSubcommandSkipsHiddenCommands(t *testing.T) {
	parent := &cobra.Command{Use: "garm"}
	parent.AddCommand(runnable("serve"))
	hidden := runnable("gen")
	hidden.Hidden = true
	parent.AddCommand(hidden)
	cli.RequireSubcommand(parent)

	err := parent.RunE(parent, nil)
	if err == nil {
		t.Fatal("parent with no subcommand succeeded")
	}
	if !strings.Contains(err.Error(), "serve") {
		t.Errorf("error does not offer the real subcommand: %v", err)
	}
	if strings.Contains(err.Error(), "gen") {
		t.Errorf("error advertises a hidden tombstone: %v", err)
	}
}
