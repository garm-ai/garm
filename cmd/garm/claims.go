package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/claimscheck"
	"github.com/garm-ai/garm/internal/compiler"
)

func newClaimsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "Work with claims policies",
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newClaimsCheckCmd())
	return cmd
}

func newClaimsCheckCmd() *cobra.Command {
	var against string
	cmd := &cobra.Command{
		Use:   "check <policy>",
		Short: "Check that a claims policy's vocabulary is declared by a catalogue",
		Long: "check reads the compartments and tool sets a claims policy's roles\n" +
			"name and compares them against what a tool catalogue declares in its\n" +
			"protobuf annotations.\n\n" +
			"Today, a name a policy references but no catalogue declares is not an\n" +
			"error anywhere else: the verifying daemon drops an unknown compartment\n" +
			"rather than refusing the token, deliberately, since rejecting would\n" +
			"turn a typo into an outage. That means `finance` where the catalogue\n" +
			"declares `financial` costs that caller access silently, surfacing only\n" +
			"as a ledger anomaly nobody watches. This is the gate that catches it\n" +
			"before it ships.\n\n" +
			"Only references the catalogue does not declare are reported. A\n" +
			"catalogue declaring a name no policy uses is normal — most policies\n" +
			"use a subset — and flagging that would make the gate unusable.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClaimsCheck(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], against)
		},
	}
	cmd.Flags().StringVar(&against, "against", "", "Catalogue artifact to check the policy against (required)")
	if err := cmd.MarkFlagRequired("against"); err != nil {
		panic(err)
	}
	return cmd
}

func runClaimsCheck(out, errOut io.Writer, policyPath, cataloguePath string) error {
	refs, err := claimscheck.ReadPolicy(policyPath)
	if err != nil {
		return err
	}

	fds, _, err := readCatalogue(cataloguePath)
	if err != nil {
		return err
	}

	declaredCompartments := declaredNames(compiler.DeclaredCompartments(fds))
	declaredSets := declaredNames(compiler.DeclaredSets(fds))

	// A catalogue declaring nothing makes every reference undeclared, which
	// is indistinguishable from "0 problems" only if this command does
	// nothing. That is the exact failure this gate exists to catch, so it is
	// refused up front rather than reported as a clean run.
	if len(declaredCompartments) == 0 && len(declaredSets) == 0 {
		return fmt.Errorf(
			"claims check: %s declares no compartments and no tool sets; every "+
				"reference in %s would be undeclared, so this catalogue cannot be "+
				"checked against (a vacuous \"0 problems\" would mean the check did nothing)",
			cataloguePath, policyPath)
	}

	// The same reasoning, mirrored onto the policy side. A policy that
	// references zero compartments AND zero tool sets gives this command
	// nothing to compare, and "0 and 0, all declared" reads exactly like a
	// clean run. The way that happens in practice is a misspelled key —
	// `compartment:` for `compartments:`, `toolsets:` for `tool_sets:` —
	// which the reader ignores by design (see internal/claimscheck: it must
	// stay permissive to read two policy shapes with one decoder), so this
	// is the layer that has to notice. Note the guard is about the file, not
	// any one role: a role granting only a clearance and verbs is legal and
	// stays legal, as long as *something* in the file names a compartment or
	// a tool set.
	if len(refs.Compartments) == 0 && len(refs.ToolSets) == 0 {
		return fmt.Errorf(
			"claims check: %s references no compartments and no tool sets across "+
				"its %d role(s); there is nothing to check against %s (a vacuous "+
				"\"0 problems\" would mean the check did nothing). If its roles do "+
				"grant a compartment or a tool set, check the spelling of the "+
				"compartments: and tool_sets: keys",
			policyPath, refs.Roles, cataloguePath)
	}

	problems := 0
	problems += reportUndeclared(errOut, "compartment", refs.Compartments, declaredCompartments, refs.CompartmentRoles)
	problems += reportUndeclared(errOut, "tool set", refs.ToolSets, declaredSets, refs.ToolSetRoles)

	if problems > 0 {
		return fmt.Errorf("%d reference(s) in %s not declared by %s", problems, policyPath, cataloguePath)
	}

	fmt.Fprintf(out, "ok — %s: %d compartment(s) and %d tool set(s) across %d role(s), all declared by %s\n",
		policyPath, len(refs.Compartments), len(refs.ToolSets), refs.Roles, cataloguePath)
	return nil
}

// reportUndeclared writes one line per name in refs that declared does not
// contain, naming the role(s) that reference it, and returns how many it
// found.
func reportUndeclared(w io.Writer, kind string, refs []string, declared map[string]bool, roles map[string][]string) int {
	n := 0
	for _, name := range refs {
		if declared[name] {
			continue
		}
		fmt.Fprintf(w, "undeclared %s %q referenced by role(s): %s\n", kind, name, strings.Join(roles[name], ", "))
		n++
	}
	return n
}

func declaredNames(decls []*toolv1.Decl) map[string]bool {
	out := make(map[string]bool, len(decls))
	for _, d := range decls {
		out[d.GetName()] = true
	}
	return out
}
