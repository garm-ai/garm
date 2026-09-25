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

const bufYAML = `version: v2
modules:
  # Your protos. The only module anything is generated from.
  - path: proto
  # The garm annotations, vendored by ` + "`garm init`" + `.
  #
  # A module of their own so they can be imported by their real path —
  # "garm/tool/v1/tool.proto" — while staying OUT of what is generated. Every
  # module in a buf v2 workspace is an input, so annotations living under
  # proto/ would have Go generated for them; that output is never usable,
  # because the real one already exists in github.com/garm-ai/garm/contracts
  # and two packages registering one proto file panic at init.
  #
  # BASIC lint because the rules meant for a contract you publish have no
  # business judging a file you vendored.
  - path: third_party/proto
    lint:
      use: [BASIC]
lint:
  use: [STANDARD]
breaking:
  use: [FILE]
`

const bufGenYAML = `version: v2
inputs:
  # Your module only. third_party is a dependency, not an input.
  - directory: proto
plugins:
  # Messages.
  - remote: buf.build/protocolbuffers/go
    out: gen
    opt: paths=source_relative
    include_imports: false

  # Connect handlers. Needed because the garm binding below emits an
  # AsConnect adapter, so the same handlers can also be served over HTTP.
  - remote: buf.build/connectrpc/go
    out: gen
    opt: paths=source_relative
    include_imports: false

  # The tool binding: a typed Handler interface, a Serve that registers it
  # against a runtime, and the contract version and descriptor hash a service
  # advertises so a daemon can tell whether it is running the contract the
  # catalogue declares.
  #
  # Install with:
  #   go install github.com/garm-ai/garm/cmd/protoc-gen-garm-go@latest
  # or replace this entry with:
  #   - local: [garm, protoc-gen-go]
  # to use the CLI you already have.
  - local: protoc-gen-garm-go
    out: gen
    # emit=toolsdk is the consumer half — what a tool author implements.
    # emit=server is garm's own wiring and belongs nowhere near a tool service.
    #
    # package_suffix puts the binding in a sibling package, and it is not
    # optional. Colocated, the binding references the connect Handler from its
    # own connect sibling, and that sibling imports the base package back for
    # message types: a two-package import cycle that nothing can break from
    # the outside.
    #
    # contract_version stamps what the service advertises. A build should pass
    # its own tag here.
    opt: paths=source_relative,emit=toolsdk,package_suffix=micro,contract_version=v0.0.0-dev
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
		{garm.VendoredAnnotationsPath, garm.AnnotationsProto},
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
  1. go install github.com/garm-ai/garm/cmd/protoc-gen-garm-go@latest
  2. Write a service in proto/ and annotate it: (garm.tool.v1.tool)
  3. garm lint
  4. garm gen            -> a typed Handler interface and a Serve for it
  5. garm catalogue build -> the artifact a daemon loads

Implement the generated Handler interface, then register it:

    svc := garmtool.New("my-service", version)
    if err := <pkg>micro.Serve<Service>(svc, myHandlers{}); err != nil { ... }
    return svc.Run(ctx, nc)
`)
	return nil
}
