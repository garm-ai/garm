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
// already share through internal/compiler, and the manifest grew in FRONT of it
// exactly as that shape predicted: internal/manifest reads catalogue.yaml,
// resolves each module through the module graph, refuses a module the tree does
// not require and refuses two inputs declaring one proto package, and
// compile.Union compiles the lot as one set. Build still takes a compiled tree
// and knows nothing about where its files came from — except for the resolved
// inputs it stamps into provenance, which arrive as values and which it sorts
// into the order the contract specifies.
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
func Check(fds []protoreflect.FileDescriptor, promptsRoot string, tax *compiler.Taxonomy) Diagnostics {
	return Diagnostics(compiler.LintWith(fds, compiler.Options{
		PromptsRoot: promptsRoot,
		Taxonomy:    tax,
	}))
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

	// Taxonomy is the vocabulary the manifest declares, or nil for a tree that
	// declares none and still has it scraped from file-level proto options.
	//
	// It reaches the artifact on `Catalogue`'s existing fields 3 and 4 either
	// way, so a daemon's registry construction is untouched and this is not a
	// contract change — only a change of where the words come from.
	Taxonomy *compiler.Taxonomy

	// Origin names where these declarations came from, for the message a
	// refusal has to print: the manifest that composed them. Nothing here
	// opens it — this package reads no files.
	Origin string

	// PromptsRoot is the directory an agent's prompts.*.path resolves against,
	// which A2 needs and a descriptor set cannot carry. Answering "relative to
	// what" is the caller's job, because only the caller knows where the tree
	// came from.
	PromptsRoot string

	// Source is free-form provenance: a repository and commit, a pipeline id.
	// Never parsed.
	Source string

	// Inputs is every input the tree was composed from, which is what makes the
	// artifact a bill of materials: producer, compiler and source all describe
	// the BUILD and none of them says what went into it.
	//
	// Handed in rather than derived, because deciding what an input IS means
	// running `go list -m` and reading the module cache, and this package opens
	// no files. What this package owns is the ORDER, below.
	//
	// Empty is permitted and means "this builder did not say", which is what
	// every catalogue written before the field says. It never means "composed
	// from nothing": that is not a state, because a build with no declarations
	// has nothing to compile and is refused above.
	Inputs []*cataloguev1.Input

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
	diags := Check(fds, req.PromptsRoot, req.Taxonomy)
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

	// The vocabulary the artifact carries: the manifest's when it declares one,
	// and otherwise the union of the file-level proto options, which is what
	// every tree built before v0.22.0 relies on.
	//
	// It is the SAME source the lint gate above resolved names against, and that
	// is the property worth keeping: an artifact whose fields 3 and 4 disagreed
	// with the vocabulary its own tools were checked against would carry a word
	// no tool may use, or omit one every tool does.
	compartments, toolSets := compiler.DeclaredCompartments(fds), compiler.DeclaredSets(fds)
	if req.Taxonomy != nil {
		compartments, toolSets = req.Taxonomy.Compartments, req.Taxonomy.ToolSets
	}

	cat := &cataloguev1.Catalogue{
		AnnotationSchemaVersion: contracts.AnnotationSchemaVersion,
		Files:                   set,
		Compartments:            compartments,
		ToolSets:                toolSets,
		FieldDocs:               docs,
		DescriptorHashes:        hashes,
		Provenance: &cataloguev1.Provenance{
			Producer: req.Producer,
			Compiler: req.Compiler,
			Source:   req.Source,
			Inputs:   canonicalInputs(req.Inputs),
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

// canonicalInputs puts the resolved inputs in the order the artifact carries
// them, which is the order garm.catalogue.v1.Provenance.inputs specifies and not
// this package's preference.
//
// Order is part of the artifact rather than presentation. A catalogue is
// byte-reproducible by requirement, and the file the inputs were read from is a
// human's to reorder — so a builder that emitted declaration order would move
// the digest when somebody sorted a list alphabetically, and two people building
// the same commit would disagree about the bytes.
//
// The sort is written out here because the contract's comment is the shared
// specification and any reimplementation has to match it: ascending by kind with
// local before module, as the oneof numbers them; then bytewise by the identity,
// which is local.path or module.path; then bytewise over proto_packages, which
// is itself sorted. Two inputs may not contribute the same proto package — see
// manifest.Packages — so no two entries tie on all three and the order is total.
func canonicalInputs(inputs []*cataloguev1.Input) []*cataloguev1.Input {
	if len(inputs) == 0 {
		return nil
	}
	// Cloned, not aliased. These messages end up inside the artifact this
	// function returns, and a caller that mutated its own slice afterwards
	// would be editing a catalogue that has already been marshalled and
	// hashed — a Result whose Catalogue no longer describes its Body.
	out := make([]*cataloguev1.Input, 0, len(inputs))
	for _, in := range inputs {
		c := proto.Clone(in).(*cataloguev1.Input)
		sort.Strings(c.ProtoPackages)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ka, kb := inputKind(a), inputKind(b); ka != kb {
			return ka < kb
		}
		if pa, pb := inputPath(a), inputPath(b); pa != pb {
			return pa < pb
		}
		return lessStrings(a.GetProtoPackages(), b.GetProtoPackages())
	})
	return out
}

// inputKind is the oneof's field number, which is what "ascending by kind" means
// and what makes local sort before module without a second constant to keep in
// step with the contract.
func inputKind(in *cataloguev1.Input) int {
	switch in.GetOf().(type) {
	case *cataloguev1.Input_Local:
		return 1
	case *cataloguev1.Input_Module:
		return 2
	default:
		// An arm this binary does not know. Sorted last so that the entries it
		// does understand keep their order among themselves.
		return 3
	}
}

func inputPath(in *cataloguev1.Input) string {
	if l := in.GetLocal(); l != nil {
		return l.GetPath()
	}
	return in.GetModule().GetPath()
}

// lessStrings compares two sorted lists the way bytes compare: element by
// element, and the shorter list first when one is a prefix of the other.
func lessStrings(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
