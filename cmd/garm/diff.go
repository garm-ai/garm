package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"

	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/policydiff"
)

func newCatalogueDiffCmd() *cobra.Command {
	var failOnWidening bool

	cmd := &cobra.Command{
		Use:   "diff <before.binpb> <after.binpb>",
		Short: "Report what changed about policy between two catalogues",
		Long: "diff reads two catalogues and reports every change to POLICY —\n" +
			"clearances, compartments, approval, audit, redactions — with the\n" +
			"direction it moved.\n\n" +
			"It exists because the annotations ARE the policy, so lowering a\n" +
			"min_clearance is a security decision that arrives as an ordinary proto\n" +
			"diff. A domain owner reviewing a pull request is not a security\n" +
			"reviewer, and at any real scale nobody reads every diff. The linter\n" +
			"catches policy that is malformed; it cannot catch policy that is wrong.\n\n" +
			"Shape changes are deliberately absent. `buf breaking` covers those, and\n" +
			"mixing them in would bury the handful of lines that need a human.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd.OutOrStdout(), args[0], args[1], failOnWidening)
		},
	}
	cmd.Flags().BoolVar(&failOnWidening, "fail-on-widening", false,
		"Exit non-zero if anything widened. For a CI gate, where a deliberate "+
			"widening is approved by a human rather than by a green build")
	return cmd
}

func runDiff(out io.Writer, beforePath, afterPath string, failOnWidening bool) error {
	before, beforeDigest, err := readCatalogue(beforePath)
	if err != nil {
		return err
	}
	after, afterDigest, err := readCatalogue(afterPath)
	if err != nil {
		return err
	}

	changes, err := policydiff.Diff(before, after)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "policy diff\n  before %s\n  after  %s\n\n", beforeDigest, afterDigest)
	if len(changes) == 0 {
		fmt.Fprintln(out, "no policy changes.")
		return nil
	}

	widening := 0
	for _, d := range []policydiff.Direction{
		policydiff.Widening, policydiff.Unclear, policydiff.Narrowing,
	} {
		var group []policydiff.Change
		for _, c := range changes {
			if c.Direction == d {
				group = append(group, c)
			}
		}
		if len(group) == 0 {
			continue
		}
		if d == policydiff.Widening {
			widening = len(group)
		}
		fmt.Fprintf(out, "%s (%d)\n", header(d), len(group))
		for _, c := range group {
			fmt.Fprintf(out, "  %s\n      %s\n      %s\n", c.Subject, c.What, c.Why)
		}
		fmt.Fprintln(out)
	}

	if widening > 0 && failOnWidening {
		return fmt.Errorf("%d widening change(s): a person has to agree to these, "+
			"not a build", widening)
	}
	return nil
}

func header(d policydiff.Direction) string {
	switch d {
	case policydiff.Widening:
		return "WIDENING — more callers, or less recorded"
	case policydiff.Narrowing:
		return "NARROWING — fewer callers, or more recorded"
	default:
		return "UNCLEAR — a real change with no direction; read it"
	}
}

// readCatalogue loads an artifact and rebuilds its registry.
//
// Unmarshalled with the default resolver so the annotations come back as their
// generated types rather than as unknown fields — the producer round-trips
// them through the same registry on the way out, which is what makes this
// symmetric.
func readCatalogue(path string) ([]protoreflect.FileDescriptor, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading catalogue: %w", err)
	}
	var msg cataloguev1.Catalogue
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return nil, "", fmt.Errorf("parsing %s: %w", path, err)
	}
	files, err := protodesc.NewFiles(msg.GetFiles())
	if err != nil {
		return nil, "", fmt.Errorf("rebuilding the registry from %s: %w", path, err)
	}
	var fds []protoreflect.FileDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		fds = append(fds, fd)
		return true
	})
	sum := sha256.Sum256(raw)
	return fds, "sha256:" + hex.EncodeToString(sum[:]), nil
}
