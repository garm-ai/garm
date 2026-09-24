package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	garm "github.com/garm-ai/garm"
)

// bufYAML and bufGenYAML are what `garm init` writes beside the vendored
// annotations. They name protoc-gen-garm-go as a PATH-discovered local
// plugin — the shape people expect — rather than the `[garm, protoc-gen-go]`
// form, which works too but reads as unusual in a file someone else will
// maintain.
const bufYAML = `version: v2
modules:
  - path: proto
lint:
  use: [STANDARD]
breaking:
  use: [FILE]
`

const bufGenYAML = `version: v2
inputs:
  - directory: proto
plugins:
  # Messages and services.
  - remote: buf.build/protocolbuffers/go
    out: gen
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: gen
    opt: paths=source_relative
  # The governed tool binding. Install with:
  #   go install github.com/garm-ai/garm/cmd/protoc-gen-garm-go@latest
  # or replace this entry with:
  #   - local: [garm, protoc-gen-go]
  # to use the CLI you already have.
  - local: protoc-gen-garm-go
    out: gen
    opt: paths=source_relative,emit=toolsdk
`

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Scaffold a proto tree with the garm annotations vendored in",
		Long: "init writes the garm annotations into your repository, along with\n" +
			"a buf workspace configured to compile them.\n\n" +
			"The annotations are vendored rather than fetched: they are small,\n" +
			"they are worth reading by whoever writes schemas against them, and\n" +
			"a vendored file is one fewer host your build has to be allowed to\n" +
			"reach. They are written verbatim, so checking for drift later is a\n" +
			"byte comparison.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runInit(cmd, dir, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Overwrite files that already exist")
	return cmd
}

func runInit(cmd *cobra.Command, dir string, force bool) error {
	files := []struct {
		path string
		body []byte
	}{
		{garm.AnnotationsPath, garm.AnnotationsProto},
		{"buf.yaml", []byte(bufYAML)},
		{"buf.gen.yaml", []byte(bufGenYAML)},
	}

	// Check every target before writing any of them. A half-scaffolded tree
	// is worse than an untouched one: the error would name one file while
	// leaving others already changed, and the reader cannot tell which.
	if !force {
		var clashes []string
		for _, f := range files {
			p := filepath.Join(dir, f.path)
			if _, err := os.Stat(p); err == nil {
				clashes = append(clashes, f.path)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("checking %s: %w", p, err)
			}
		}
		if len(clashes) > 0 {
			return fmt.Errorf("refusing to overwrite %v in %s (pass --force)", clashes, dir)
		}
	}

	for _, f := range files {
		p := filepath.Join(dir, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, f.body, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", p, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", p)
	}

	fmt.Fprintf(cmd.OutOrStdout(), `
Next:
  1. Write a service in proto/ and annotate it: (garm.tool.v1.tool)
  2. go install github.com/garm-ai/garm/cmd/protoc-gen-garm-go@latest
  3. garm lint && garm gen
`)
	return nil
}
