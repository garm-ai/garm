// Package plugin is the protoc plugin that emits garm tool bindings and
// fails the build on any error-level lint diagnostic.
//
// This is the piece that makes "unlabeled data fails the build, not the
// request" true: every other package here is a library that could be wired
// up correctly or incorrectly. This is what refuses to produce output at
// all when a schema violates garm tool policy.
package plugin

import (
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"path"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/garm-ai/garm/internal/compiler"
)

// Run is the plugin entry point, speaking protoc's stdin/stdout protocol.
//
// Two binaries call it. cmd/protoc-gen-garm-go exists so that `go install`
// produces the conventionally named binary people expect to put on PATH;
// the garm CLI exposes the same entry point as `garm protoc-gen-go`, so a
// buf.gen.yaml can name the CLI directly and never install a second thing.
//
// One implementation, one module, one tag: the annotations and the
// generator that reads them cannot drift apart, which is the whole reason
// for bundling — kept without giving up the conventional name.
func Run() {
	var flagSet flag.FlagSet
	// package_suffix mirrors protoc-gen-connect-go's own flag of the same
	// name: empty (the default) colocates the generated registration file
	// with the base .pb.go package, exactly as the design calls for. A
	// non-empty suffix puts it in a sibling package instead — needed for
	// toolpolicy/testdata, whose base package is also imported as a test
	// fixture by toolplane's own (internal, white-box) tests. Colocating
	// there would make testdata import toolplane while toolplane's tests
	// import testdata: a real package cycle, not a generation choice.
	packageSuffix := flagSet.String("package_suffix", "", "Generate into a "+
		"sibling package with this suffix instead of the base .pb.go package.")
	// emit picks which of the two audiences this invocation writes for:
	// "server" (the default, today's unchanged behaviour) is garm's OWN
	// wiring (var Tools, Mount(*toolplane.Core, ...), RegisterXService) —
	// a tool author never calls it, and it imports toolplane, so it must
	// stay in the parent module. "toolsdk" is what a tool author
	// implements against (Handler, ServeX, and XAsConnect when
	// connect_adapter is set) — it registers
	// against toolbind.Registrar rather than importing a runtime, so it
	// can live in the contracts module with no dependency on garm's own
	// enforcement package. See internal/toolgen/emit_micro.go's doc
	// comment for the full reasoning.
	emit := flagSet.String("emit", "server", `Which output to generate: `+
		`"server" (garm's own wiring; default) or "toolsdk" (the tool-side `+
		`micro binding).`)
	// contract_version stamps the tool-side binding's ContractVersion
	// constant. It defaults to "dev" because no git tag exists yet on this
	// branch; a real build passes the git tag here.
	contractVersion := flagSet.String("contract_version", "dev",
		"The ContractVersion stamped into a toolsdk binding's generated constant.")
	// connect_adapter is off by default, and that default is a decision
	// rather than caution. Emitting XAsConnect makes every tool module run a
	// second plugin, inherit connectrpc.com/connect, and generate into a
	// sibling package to dodge an import cycle with its own connect sibling.
	// It was unconditional because garm once had a connect surface to mount
	// the result on; it has none, and cannot build one from generated
	// handlers while it dispatches on catalogue descriptors.
	connectAdapter := flagSet.Bool("connect_adapter", false,
		"Also emit XAsConnect, for a tool service that serves connect directly.")

	protogen.Options{ParamFunc: flagSet.Set}.Run(func(gen *protogen.Plugin) error {
		return generate(gen, os.Stderr, genOptions{
			packageSuffix:   *packageSuffix,
			emit:            *emit,
			contractVersion: *contractVersion,
			connectAdapter:  *connectAdapter,
		})
	})
}

// genOptions bundles generate's plugin-parameter-derived options. A struct
// rather than more positional parameters: emit and contract_version are
// both new alongside package_suffix, and a fourth boolean-shaped parameter
// down the line should not have to renumber every existing call site
// (including every test's).
type genOptions struct {
	packageSuffix   string
	emit            string
	contractVersion string
	connectAdapter  bool
}

// generate is main's logic, pulled out of the protogen.Options.Run closure
// so it can be driven directly in tests against a hand-built *protogen.Plugin
// instead of through protoc's stdin/stdout protocol.
func generate(gen *protogen.Plugin, diagOut io.Writer, opts genOptions) error {
	gen.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)

	packageSuffix := opts.packageSuffix
	if packageSuffix != "" && !token.IsIdentifier(packageSuffix) {
		return fmt.Errorf("package_suffix %q is not a valid Go identifier", packageSuffix)
	}
	switch opts.emit {
	case "server", "toolsdk":
	default:
		return fmt.Errorf(`emit %q is not "server" or "toolsdk"`, opts.emit)
	}

	var fds []protoreflect.FileDescriptor
	for _, f := range gen.Files {
		fds = append(fds, f.Desc)
	}

	// Lint the whole input set, not only the files being generated: a
	// compartment declared in a dependency is still in scope, and a
	// duplicate tool name across files must be caught.
	diags := dedupeDiags(compiler.Lint(fds))
	errs := 0
	for _, d := range diags {
		fmt.Fprintln(diagOut, d.String())
		if !d.Warn {
			errs++
		}
	}
	if errs > 0 {
		return fmt.Errorf("%d garm tool policy error(s)", errs)
	}

	compartments := compiler.DeclaredCompartments(fds)
	sets := compiler.DeclaredSets(fds)

	// Group generated files by Go import path, not proto file: var Tools and
	// the compartment/set constants are package-scoped declarations. Two
	// proto files that share a go_package (a common layout — see
	// proto/garm/v1's control.proto and generation.proto) but are emitted
	// one _garm.pb.go each would redeclare the same symbols twice the
	// moment both carry tool annotations, a redeclaration error `go build`
	// would catch but `make gen` itself never would. gen.Files is already
	// in deterministic (topological) order, so grouping preserves it.
	var pkgOrder []string
	pkgFiles := map[string][]*protogen.File{}
	for _, f := range gen.Files {
		if !f.Generate {
			continue
		}
		key := string(f.GoImportPath)
		if _, seen := pkgFiles[key]; !seen {
			pkgOrder = append(pkgOrder, key)
		}
		pkgFiles[key] = append(pkgFiles[key], f)
	}

	for _, key := range pkgOrder {
		files := pkgFiles[key]
		var tools []compiler.Tool
		for _, f := range files {
			ts, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
			if err != nil {
				return err
			}
			tools = append(tools, ts...)
		}
		if len(tools) == 0 {
			// A package with no tools produces no generated file at all —
			// not an empty one that would just sit there as noise.
			continue
		}

		f0 := files[0]
		outPkg := f0.GoPackageName
		outImportPath := f0.GoImportPath
		// The filename is keyed off the Go package name, not any one proto
		// file's name: <pkg>_garm.pb.go for emit=server, <pkg>_micro.pb.go
		// for emit=toolsdk (below), per the design's own naming convention,
		// living in the base package's directory (or its package_suffix
		// sibling below).
		outDir := path.Dir(f0.GeneratedFilenamePrefix)
		if packageSuffix != "" {
			outPkg = f0.GoPackageName + protogen.GoPackageName(packageSuffix)
			outImportPath = protogen.GoImportPath(path.Join(string(f0.GoImportPath), string(outPkg)))
			outDir = path.Join(outDir, string(outPkg))
		}
		switch opts.emit {
		case "toolsdk":
			// contracts/garm/demo/v1beta1/demov1beta1micro/demov1beta1_micro.pb.go,
			// alongside emit=server's own <pkg>_garm.pb.go: same directory
			// layout, same per-package (not per-proto-file) grouping — see
			// EmitMicro's doc comment for why ContractVersion and
			// DescriptorHash being package-scoped constants forces that.
			filename := path.Join(outDir, string(f0.GoPackageName)) + "_micro.pb.go"
			g := gen.NewGeneratedFile(filename, outImportPath)
			if err := compiler.EmitMicro(g, files, outPkg, tools, opts.contractVersion,
				compiler.MicroOptions{ConnectAdapter: opts.connectAdapter}); err != nil {
				return err
			}
		default: // "server"
			filename := path.Join(outDir, string(f0.GoPackageName)) + "_garm.pb.go"
			g := gen.NewGeneratedFile(filename, outImportPath)
			if err := compiler.Emit(g, files, outPkg, tools, compartments, sets); err != nil {
				return err
			}
		}
	}
	return nil
}

// dedupeDiags collapses diagnostics that are identical in rule, path and
// message before they are printed.
//
// Lint walks a tool's Input and Output independently; a schema where two
// tools' request/response graphs converge on the same field (the same
// message type reached the same way) can otherwise report the same finding
// more than once. Harmless in a unit test that inspects one tool at a time,
// but noisy — and easy to mistake for N distinct problems — in a real
// build's stderr.
func dedupeDiags(diags []compiler.Diag) []compiler.Diag {
	seen := make(map[compiler.Diag]bool, len(diags))
	out := make([]compiler.Diag, 0, len(diags))
	for _, d := range diags {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
