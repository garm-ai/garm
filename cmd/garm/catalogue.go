package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	garm "github.com/garm-ai/garm"
	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

func newCatalogueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalogue",
		Short: "Build and inspect the artifact a daemon serves",
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newCatalogueBuildCmd(), newCatalogueDiffCmd())
	return cmd
}

func newCatalogueBuildCmd() *cobra.Command {
	var protoDir, out, source string
	var stampTime bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Compile a proto tree into a catalogue",
		Long: "build compiles the proto tree and writes the artifact a daemon loads\n" +
			"at boot.\n\n" +
			"It lints first and refuses on any error. A catalogue that does not\n" +
			"lint cannot be built — the alternative is an artifact that fails at\n" +
			"the daemon's mount check instead, where the author is not present\n" +
			"and the failure is an outage rather than a build error.\n\n" +
			"Compilation is in process, against a compiler this binary pins. The\n" +
			"digest identifies the catalogue, so it must not move because someone\n" +
			"upgraded a CLI on their laptop.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCatalogueBuild(cmd, protoDir, out, source, stampTime)
		},
	}
	cmd.Flags().StringVar(&protoDir, "proto", "proto", "Root of the proto tree")
	cmd.Flags().StringVarP(&out, "out", "o", "catalogue.binpb", "Where to write the artifact")
	cmd.Flags().StringVar(&source, "source", "", "Free-form provenance: a repository and commit, a pipeline id")
	cmd.Flags().BoolVar(&stampTime, "stamp-time", false,
		"Record the build time. Breaks byte-reproducibility: two builds of the same source will differ")
	return cmd
}

func runCatalogueBuild(cmd *cobra.Command, protoDir, out, source string, stampTime bool) error {
	set, fds, err := compile.Tree(cmd.Context(), protoDir)
	if err != nil {
		return err
	}

	// Lint before building, never after. See the command's Long.
	diags := compiler.Lint(fds)
	errs := 0
	for _, d := range diags {
		fmt.Fprintln(cmd.ErrOrStderr(), d.String())
		if !d.Warn {
			errs++
		}
	}
	if errs > 0 {
		return fmt.Errorf("refusing to build a catalogue with %d policy error(s)", errs)
	}

	tools, err := compiler.Tools(fds)
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		return fmt.Errorf("no tools declared under %s: a catalogue with nothing in it "+
			"would start a daemon that serves nothing, which is a deployment nobody meant", protoDir)
	}

	// Comments come out of the descriptors and into a flat table before the
	// artifact is written. SourceCodeInfo carries spans and paths for every
	// token in every file; a projected schema only ever needed the prose.
	// Measured on a 10,000-tool catalogue: 9.7 MB and 180 MB retained becomes
	// 3.7 MB and 93 MB.
	docs := compiler.FieldDocs(fds)
	for _, f := range set.GetFile() {
		f.SourceCodeInfo = nil
	}

	// One hash per proto package, matching how the generator emits them: a
	// binding is per package, so a service serves one package and advertises
	// one digest.
	byPkg := map[string][]compiler.Tool{}
	for _, t := range tools {
		pkg := string(t.Method.ParentFile().Package())
		byPkg[pkg] = append(byPkg[pkg], t)
	}
	hashes := make(map[string]string, len(byPkg))
	for pkg, ts := range byPkg {
		hashes[pkg] = compiler.DescriptorHash(ts)
	}

	cat := &cataloguev1.Catalogue{
		AnnotationSchemaVersion: garm.AnnotationSchemaVersion,
		Files:                   set,
		Compartments:            compiler.DeclaredCompartments(fds),
		ToolSets:                compiler.DeclaredSets(fds),
		FieldDocs:               docs,
		DescriptorHashes:        hashes,
		Provenance: &cataloguev1.Provenance{
			Producer: "garm/" + version(),
			Compiler: compile.Version(),
			Source:   source,
		},
	}

	// The build time is off by default, and that is the whole reason this
	// artifact is reproducible.
	//
	// A digest that moves on every rebuild of identical source identifies the
	// BUILD, not the content — so redeploying the same catalogue would look
	// like a change, and "are these two deployments serving the same tools"
	// would be unanswerable. A wall clock buys little that `source` does not
	// already carry, so it is opt-in and says what it costs.
	if stampTime {
		cat.Provenance.BuiltAt = timestamppb.New(time.Now().UTC())
	}

	// Deterministic marshalling: field order is already stable for a given
	// binary, and this pins map ordering too, which matters the moment any
	// option carries a map.
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(cat)
	if err != nil {
		return fmt.Errorf("marshalling the catalogue: %w", err)
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}

	sum := sha256.Sum256(body)
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
	fmt.Fprintf(cmd.OutOrStdout(), "  %d tool(s) in %d package(s), %d file(s), %d documented field(s), schema v%d\n",
		len(tools), len(hashes), len(set.GetFile()), len(docs), garm.AnnotationSchemaVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "  digest sha256:%s\n", hex.EncodeToString(sum[:]))
	// The FQN is proto package + resolved tool name, split at the last dot
	// by everything that consumes it. Built here rather than read off Tool
	// because the generator composes it the same way at emit time.
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		pkg := t.Method.ParentFile().Package()
		names = append(names, fmt.Sprintf("%s.%s", pkg, t.Name))
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", n)
	}
	return nil
}
