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
import _ "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
