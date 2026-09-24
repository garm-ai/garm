// Package cli holds the cobra conventions every garm binary shares.
//
// It exists because the third copy is where duplicated helpers start to
// drift: `garm` grew the argv pre-scan, `garmdev` grew the parent-command
// guard, and `garm-control` needs both.
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// knownFlags is what the argv scan below needs to know about a binary's
// flags: which long names exist, and which spellings consume the next
// argument as their value. The second half is what keeps the scan from
// mistaking a VALUE that happens to look like a flag for a flag.
type knownFlags struct {
	long       map[string]bool // "config"
	takesValue map[string]bool // "config", "c" — long names and shorthands
}

func collectFlags(cmd *cobra.Command) knownFlags {
	k := knownFlags{long: map[string]bool{}, takesValue: map[string]bool{}}
	add := func(f *pflag.Flag) {
		k.long[f.Name] = true
		// pflag sets NoOptDefVal on flags that may appear bare (bools);
		// everything else consumes the next argument.
		if f.NoOptDefVal == "" {
			k.takesValue[f.Name] = true
			if f.Shorthand != "" {
				k.takesValue[f.Shorthand] = true
			}
		}
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		// Cobra registers --help lazily, inside Execute. This scan runs
		// BEFORE Execute, so without asking for it here `-help` would not be
		// recognised — and -help is exactly the spelling someone reaches for
		// after hitting the -config break. stdlib flag accepted it.
		c.InitDefaultHelpFlag()
		c.Flags().VisitAll(add)
		c.PersistentFlags().VisitAll(add)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
	return k
}

// CheckSingleDash reports the stdlib-flag spellings that pflag no longer
// accepts, and prints the corrected command line rather than only the rule,
// so the fix can be copied straight out of the error.
//
// These binaries parsed flags with the standard library before they used
// cobra, and stdlib `flag` accepts -config and --config alike. pflag reads
// -config as a cluster of single-letter shorthands and fails with "unknown
// shorthand flag: 'c' in -config", which names nothing a reader can act on.
//
// It is not a second flag parser. It knows the binary's long flag names and
// which of them take a value, and everything it does not recognise is
// pflag's to report.
//
// binary is the name to print in the suggestion. Hardcoding one binary's
// name in a helper three of them share would print confident, wrong advice
// in the other two.
func CheckSingleDash(root *cobra.Command, args []string, binary string) error {
	k := collectFlags(root)
	corrected := make([]string, len(args))
	copy(corrected, args)
	var offenders, want []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break // everything after this is positional by definition
		}
		switch {
		case strings.HasPrefix(a, "--"):
			// Correct spelling. Step over its value so a value like
			// "-config" is never read as a flag.
			if name, _, hasValue := strings.Cut(a[2:], "="); !hasValue && k.takesValue[name] {
				i++
			}
		case a == "-" || !strings.HasPrefix(a, "-"):
			// Bare "-" (stdin by convention) and positionals.
		case len(a) == 2:
			// A genuine shorthand; pflag still accepts these.
			if k.takesValue[a[1:]] {
				i++
			}
		default:
			name, value, hasValue := strings.Cut(a[1:], "=")
			if !k.long[name] {
				continue // not ours; pflag reports it
			}
			offenders = append(offenders, "-"+name)
			want = append(want, "--"+name)
			corrected[i] = "--" + name
			if hasValue {
				corrected[i] += "=" + value
			} else if k.takesValue[name] {
				i++ // its value, not another flag
			}
		}
	}

	if len(offenders) == 0 {
		return nil
	}
	return fmt.Errorf("%s is no longer accepted; use %s\n\n  %s %s",
		strings.Join(offenders, ", "), strings.Join(want, ", "),
		binary, strings.Join(corrected, " "))
}

// RequireSubcommand makes a parent command FAIL rather than print help and
// exit 0 when it is invoked without a subcommand, or with one it does not
// have.
//
// Cobra's default is to print help and exit 0. That turns a CI step written
// as `garmdev manifest $MODE --manifest app.garm.yaml` into a green check
// that verified nothing the day $MODE is unset or misspelled — the same
// silent-pass shape as an empty tool index.
func RequireSubcommand(cmd *cobra.Command) *cobra.Command {
	cmd.RunE = func(c *cobra.Command, args []string) error {
		subs := c.Commands()
		names := make([]string, 0, len(subs))
		for _, sub := range subs {
			// Hidden commands are not options. `garm` keeps tombstones for
			// the verbs that moved to garmdev; offering them here would send
			// the reader straight back to the error telling them the command
			// had moved. IsAvailableCommand also drops cobra's own help and
			// completion entries.
			if !sub.IsAvailableCommand() {
				continue
			}
			names = append(names, sub.Name())
		}
		sort.Strings(names)
		if len(args) > 0 {
			return fmt.Errorf("unknown %s subcommand %q; expected one of: %s",
				c.Name(), args[0], strings.Join(names, ", "))
		}
		return fmt.Errorf("%s needs a subcommand: %s",
			c.CommandPath(), strings.Join(names, ", "))
	}
	return cmd
}
