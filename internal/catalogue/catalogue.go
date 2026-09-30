// Package catalogue assembles the artifact a daemon loads at boot.
//
// The seam is that this package DECIDES and its caller PRINTS. Build is handed
// a compiled proto tree, lints it, refuses what has to be refused, and returns
// the marshalled artifact along with the numbers that describe it. It opens no
// files, writes none, formats nothing for a terminal and knows nothing about
// the command that calls it. The command owns flags, streams, exit codes and
// I/O.
//
// That is the shape contracts/policy already has — Compile returns a plan and
// leaves applying it to the caller — and it is here for the same reason. While
// assembly lived inside a cobra RunE, the only way to test any of it was to
// drive the command and read its stderr, so the properties the catalogue
// actually depends on (a package's digest covers the author's declarations and
// nothing else; a tree with no tools is refused; a tree that does not lint is
// never built) were asserted, when they were asserted at all, through the
// output of a CLI.
//
// Assembly is also the one concern the builder and the protoc plugin do not
// already share through internal/compiler, and it is about to grow: a
// catalogue.yaml manifest, module resolution, input merging and collision
// detection all land in FRONT of Build, producing the Request it already
// takes. A Request that describes a compiled tree rather than a directory is
// what lets them.
package catalogue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/garm-ai/contracts"
	cataloguev1 "github.com/garm-ai/contracts/garm/catalogue/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// Diagnostics is one lint run's findings, in the order the rules produced
// them.
//
// A caller prints these. Only this package decides which of them is a
// refusal — Errors exists for Build and for this package's own tests, and not
// as the hook a caller uses to make that decision itself. See Build.
type Diagnostics []compiler.Diag

// Errors counts the diagnostics that are not warnings, which is the count that
// decides whether a catalogue may be built.
func (d Diagnostics) Errors() int {
	n := 0
	for _, diag := range d {
		if !diag.Warn {
			n++
		}
	}
	return n
}

// Check lints a compiled tree and reports what it found.
//
// This is what `garm lint` prints, and it gates nothing: the gate is inside
// Build, which lints the tree itself rather than trusting anything a caller
// hands it. There is deliberately no way to pass Build a clean bill of health.
func Check(fds []protoreflect.FileDescriptor, promptsRoot string) Diagnostics {
	return Diagnostics(compiler.LintWith(fds, compiler.Options{PromptsRoot: promptsRoot}))
}

// Request is what to build.
type Request struct {
	// Set and Descriptors are the two shapes of one compiled tree, exactly as
	// compile.Tree returns them. The set is what the catalogue carries and
	// what a package's descriptor hash is taken over; the descriptors are what
	// the lint rules and the tool loader walk. They must describe the same
	// files — a descriptor with no file in the set is refused rather than
	// silently producing a catalogue whose cards are missing from half of it.
	Set         *descriptorpb.FileDescriptorSet
	Descriptors []protoreflect.FileDescriptor

	// Origin names where these declarations came from, for the message a
	// refusal has to print. Today that is the --proto directory; once a
	// manifest composes several inputs it will be the manifest. Nothing here
	// opens it: this package reads no files.
	Origin string

	// PromptsRoot is the directory an agent's prompts.*.path resolves against,
	// which A2 needs and a descriptor set cannot carry. Answering "relative to
	// what" is the caller's job, because only the caller knows where the tree
	// came from.
	PromptsRoot string

	// Source is free-form provenance: a repository and commit, a pipeline id.
	// Never parsed.
	Source string

	// Producer and Compiler are stamped into the artifact's provenance. The
	// producer is the binary that built it and the compiler is the proto
	// compiler that binary pins — identical source compiled by different
	// compilers can produce different descriptor bytes and therefore a
	// different digest, so without it a digest mismatch is a mystery.
	Producer string
	Compiler string

	// BuiltAt, when non-zero, records the build time — and that is off by
	// default, which is the whole reason this artifact is reproducible.
	//
	// A digest that moves on every rebuild of identical source identifies the
	// BUILD, not the content — so redeploying the same catalogue would look
	// like a change, and "are these two deployments serving the same tools"
	// would be unanswerable. A wall clock buys little that Source does not
	// already carry, so it is opt-in and the flag that sets it says what it
	// costs.
	BuiltAt time.Time
}

// Result is a built catalogue.
//
// The counts a caller reports are read off the artifact itself rather than
// carried beside it, so what is printed describes what was written: the tools
// are ToolNames, the packages are Catalogue.DescriptorHashes, the files are
// Catalogue.Files and the documented fields are Catalogue.FieldDocs.
type Result struct {
	// Catalogue is the message Body encodes.
	Catalogue *cataloguev1.Catalogue

	// Body is the deterministic marshalling of Catalogue: the bytes to write,
	// and the bytes Digest describes.
	Body []byte

	// Digest is "sha256:" followed by hex, the same spelling `catalogue diff`
	// and `catalogue publish` print for an artifact they read.
	Digest string

	// ToolNames is every tool's fully qualified name, sorted. The FQN is proto
	// package plus resolved tool name, split at the last dot by everything
	// that consumes it; it is composed here rather than read off a Tool
	// because the generator composes it the same way at emit time.
	ToolNames []string

	// SynthesisedCards is how many card endpoints were added to the set after
	// the author's own declarations were linted and hashed.
	SynthesisedCards int
}

// Build lints a compiled tree and assembles the catalogue it describes.
//
// The diagnostics come back whether the build succeeded or not, because a
// caller reports warnings either way. They come back to be PRINTED and never
// to be counted: by the time Build returns, the decision a count would make
// has already been made — err is non-nil and the Result is nil when any
// diagnostic was an error. So lint is not a step a caller can skip, reorder,
// or forget to act on. A catalogue that does not lint cannot be built, because
// the alternative is an artifact that fails at the daemon's mount check
// instead, where the author is not present and the failure is an outage rather
// than a build error.
func Build(req Request) (*Result, Diagnostics, error) {
	set, fds := req.Set, req.Descriptors

	// Lint before building, never after.
	diags := Check(fds, req.PromptsRoot)
	if errs := diags.Errors(); errs > 0 {
		return nil, diags, fmt.Errorf("refusing to build a catalogue with %d policy error(s)", errs)
	}

	tools, err := compiler.Tools(fds)
	if err != nil {
		return nil, diags, err
	}
	if len(tools) == 0 {
		return nil, diags, fmt.Errorf("no tools declared under %s: a catalogue with nothing in it "+
			"would start a daemon that serves nothing, which is a deployment nobody meant", req.Origin)
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

	// Every tool's card endpoints, added to the set after the author's own
	// declarations have been linted and hashed.
	//
	// After the hash on purpose: a package's DescriptorHash is what a tool
	// service advertises and the daemon compares at mount, and a service
	// computes it from the tools its author declared. Hashing the cards here
	// would move every existing service's digest without a single wire shape
	// having changed.
	//
	// What makes that true is that `tools` above was resolved from the
	// DESCRIPTORS and synthesiseCards amends the SET, so the hashes below are
	// taken over the author's tool list however far down this function they
	// are computed. A tidier-looking sequence that hashed the amended set
	// instead would move every digest in the estate, so do not reorder these
	// and do not re-resolve `tools` after this call.
	synthesised, err := synthesiseCards(set, fds)
	if err != nil {
		return nil, diags, err
	}
	if err := rebuildable(set); err != nil {
		return nil, diags, err
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
		AnnotationSchemaVersion: contracts.AnnotationSchemaVersion,
		Files:                   set,
		Compartments:            compiler.DeclaredCompartments(fds),
		ToolSets:                compiler.DeclaredSets(fds),
		FieldDocs:               docs,
		DescriptorHashes:        hashes,
		Provenance: &cataloguev1.Provenance{
			Producer: req.Producer,
			Compiler: req.Compiler,
			Source:   req.Source,
		},
	}
	if !req.BuiltAt.IsZero() {
		cat.Provenance.BuiltAt = timestamppb.New(req.BuiltAt)
	}

	// Deterministic marshalling: field order is already stable for a given
	// binary, and this pins map ordering too, which matters the moment any
	// option carries a map.
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(cat)
	if err != nil {
		return nil, diags, fmt.Errorf("marshalling the catalogue: %w", err)
	}
	sum := sha256.Sum256(body)

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		pkg := t.Method.ParentFile().Package()
		names = append(names, fmt.Sprintf("%s.%s", pkg, t.Name))
	}
	sort.Strings(names)

	return &Result{
		Catalogue:        cat,
		Body:             body,
		Digest:           "sha256:" + hex.EncodeToString(sum[:]),
		ToolNames:        names,
		SynthesisedCards: synthesised,
	}, diags, nil
}
