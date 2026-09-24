package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/garm/internal/compiler"
)

func newLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint",
		Short: "Check tool declarations without generating anything",
		Long: "lint compiles the proto tree and applies garm's tool rules to it,\n" +
			"reporting what `garm gen` would refuse.\n\n" +
			"The same rules run inside the generator, so this cannot pass and\n" +
			"then fail at generation time. It exists because the answer to\n" +
			"\"is this declaration acceptable\" is worth having without writing\n" +
			"files, and because a linter that needs a code generator to run is\n" +
			"one people skip.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLint(cmd) },
	}
}

func runLint(cmd *cobra.Command) error {
	fds, err := buildDescriptorSet()
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
}

// buildDescriptorSet asks buf for a compiled image of the workspace.
//
// `buf build -o -` writes a FileDescriptorSet, which is exactly what the
// lint rules want: they walk descriptors, not source. Reimplementing proto
// parsing here to avoid the subprocess would be a second compiler that has
// to agree with buf about every edge of the language.
func buildDescriptorSet() ([]protoreflect.FileDescriptor, error) {
	bin, err := exec.LookPath("buf")
	if err != nil {
		return nil, fmt.Errorf("buf is not on PATH: %w\n"+
			"garm uses buf to compile protos. Install it with `mise install`, "+
			"or see https://buf.build/docs/installation", err)
	}
	c := exec.Command(bin, "build", "-o", "-")
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("buf build: %w", err)
	}

	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(out, set); err != nil {
		return nil, fmt.Errorf("parsing buf's image: %w", err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, fmt.Errorf("resolving the descriptor set: %w", err)
	}

	// Lint the whole set, not only the files the caller authored: a
	// compartment declared in a dependency is still in scope, and a
	// duplicate tool name across files must be caught. Sorted by path so
	// that diagnostics come out in the same order on every run — RangeFiles
	// does not promise one.
	var fds []protoreflect.FileDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		fds = append(fds, fd)
		return true
	})
	sort.Slice(fds, func(i, j int) bool { return fds[i].Path() < fds[j].Path() })
	return fds, nil
}
