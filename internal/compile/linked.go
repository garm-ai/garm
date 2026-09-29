package compile

// Descriptors this binary links so that a proto tree can import them without
// vendoring a copy.
//
// The resolver in compile.go answers from protoregistry.GlobalFiles before it
// falls through to source, so anything registered by a linked package is
// importable by its real path. garm's own annotations arrive that way already,
// as a side effect of contracts being in this module. protovalidate does not,
// and without this import `buf/validate/validate.proto: not found` refuses any
// tree that declares a constraint — which is every tree that validates
// anything.
//
// Linking rather than vendoring, for the reason the annotations are linked:
// this binary's copy becomes authoritative, so a stale vendored file cannot
// quietly change what a constraint means. It also pins producer and consumer
// to one version of the constraint schema — garmd evaluates these at runtime
// against its own linked protovalidate, and a catalogue compiled against an
// older validate.proto could carry a constraint the evaluator reads
// differently. Same module, same version, one meaning.
//
// Blank import: nothing here calls protovalidate. The package's init registers
// the descriptors, which is the whole purpose.
import (
	_ "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"

	// garm.agent.v1 is this module's own contract, but it is not reached
	// transitively the way garm.tool.v1 is (the tool annotations arrive
	// through internal/compiler). Linking it here makes
	// "garm/agent/v1/agent.proto" resolvable by its real path in any tree
	// this binary compiles, vendored or not — which is what `garm init`
	// writes and what a conformance case imports.
	_ "github.com/garm-ai/contracts/garm/agent/v1"
	// garm.card.v1 and garm.meta.v1 for the same reason: a template or an
	// owner is an option on a service the tree declares, and the file that
	// defines the extension has to resolve for the option to be parsed.
	_ "github.com/garm-ai/contracts/garm/card/v1"
	_ "github.com/garm-ai/contracts/garm/meta/v1"
	// garm.tasks.v1 is a SERVICE contract rather than a vocabulary, and it is
	// linked for a different reason from the three above: a deployment that
	// wants the tasks tools in its catalogue imports this file from its own
	// tree, and the import has to resolve without vendoring eight hundred
	// lines of somebody else's service. Importing it is also what puts its
	// tools in that catalogue — the compiler collects a file's imports into
	// the descriptor set, and every annotated method in the set is a tool.
	_ "github.com/garm-ai/contracts/garm/tasks/v1"
)
