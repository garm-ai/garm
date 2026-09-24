// Package compile turns proto source into descriptors, in process.
//
// Not by shelling out to buf. Two reasons, and the second is the important
// one.
//
// An author should be able to check a declaration without installing a
// second tool. `garm gen` still wraps buf, because code generation is what
// buf's plugin orchestration is for and teams already have pipelines built on
// it — but nothing garm must do for itself should require it.
//
// And the descriptor bytes must depend on a compiler this module pins rather
// than on whichever buf version happens to be on someone's PATH. A catalogue
// is identified by the digest of its bytes; if that digest moves when a
// contributor upgrades a CLI, it identifies nothing. The agent plane reached
// the same conclusion for bundles, for the same reason.
package compile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Tree compiles every .proto under root.
//
// It returns both shapes callers need: a FileDescriptorSet, which is what a
// catalogue carries and what a digest is taken over, and the resolved
// descriptors the lint rules walk.
//
// SourceInfo is retained deliberately. Proto comments are where a tool's field
// documentation comes from, and a projected schema without them is typed
// fields with no indication of what they mean — a large accuracy loss for the
// one reader the schema exists for.
func Tree(ctx context.Context, root string) (*descriptorpb.FileDescriptorSet, []protoreflect.FileDescriptor, error) {
	paths, err := protoPaths(root)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no .proto files under %s", root)
	}

	compiled, err := (&protocompile.Compiler{
		Resolver:       resolver(root),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}).Compile(ctx, paths...)
	if err != nil {
		return nil, nil, err
	}

	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var collect func(fd protoreflect.FileDescriptor)
	collect = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		// Dependencies first: a FileDescriptorSet has to be topologically
		// ordered for anything to rebuild it.
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			collect(imports.Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, f := range compiled {
		collect(f)
	}

	set, err = resolveOptions(set)
	if err != nil {
		return nil, nil, err
	}

	reg, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, nil, fmt.Errorf("rebuilding the descriptor set: %w", err)
	}
	var fds []protoreflect.FileDescriptor
	reg.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		fds = append(fds, fd)
		return true
	})
	sort.Slice(fds, func(i, j int) bool { return fds[i].Path() < fds[j].Path() })
	return set, fds, nil
}

// resolveOptions re-parses every options message so that garm's annotations
// arrive as their generated Go types.
//
// protocompile stores options as dynamic messages whatever the import
// resolver does, so proto.GetExtension(opts, toolv1.E_Tool) is handed a
// *dynamicpb.Message where it wants a *toolv1.ToolPolicy — and panics, because
// those are different types that happen to describe the same field.
//
// Marshalling and unmarshalling through GlobalTypes fixes it at the root: the
// bytes are identical either way, and the second parse knows the extension
// types this binary links. Doing it over the whole set rather than per-option
// keeps one code path, and the set is what the caller wanted anyway.
func resolveOptions(set *descriptorpb.FileDescriptorSet) (*descriptorpb.FileDescriptorSet, error) {
	b, err := proto.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("marshalling the descriptor set: %w", err)
	}
	out := &descriptorpb.FileDescriptorSet{}
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("re-parsing options against the linked types: %w", err)
	}
	return out, nil
}

// resolver prefers descriptors this binary already links over the same file
// compiled from source.
//
// Without it, a tree that vendors garm/tool/v1/tool.proto — which is exactly
// what `garm init` writes — gets that file compiled afresh, and every
// annotation on it resolves to a dynamicpb value rather than to the generated
// Go extension type. proto.GetExtension then panics, because the extension it
// was handed and the one it was asked for are different types that happen to
// describe the same field.
//
// Resolving the annotations from the linked registry also makes this binary's
// own copy authoritative: a stale vendored file cannot quietly change what a
// declaration means. Checking whether it HAS gone stale is a separate job,
// and a byte comparison, which is why `garm init` writes it verbatim.
func resolver(root string) protocompile.Resolver {
	return protocompile.WithStandardImports(protocompile.CompositeResolver{
		protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
			if err != nil {
				// Not linked here; fall through to source.
				return protocompile.SearchResult{}, protoregistry.NotFound
			}
			return protocompile.SearchResult{Desc: fd}, nil
		}),
		&protocompile.SourceResolver{ImportPaths: []string{root}},
	})
}

// protoPaths lists every .proto under root, as paths relative to root —
// which is what the compiler wants, and what makes an import of
// "garm/tool/v1/tool.proto" resolve the same way it does in a buf workspace.
func protoPaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// Version reports the proto compiler this binary pins.
//
// Recorded in a catalogue's provenance because identical source compiled by
// different compilers can produce different descriptor bytes, and therefore a
// different digest. Without it a digest mismatch is a mystery; with it, it is
// a diff.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "protocompile/unknown"
	}
	for _, d := range info.Deps {
		if d.Path == "github.com/bufbuild/protocompile" {
			return "protocompile/" + d.Version
		}
	}
	return "protocompile/unknown"
}
