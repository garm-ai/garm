package compiler

import (
	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/celenv"
)

// This file is where the CEL side of A4, A6, A7, A11 and A13 converges on
// `github.com/garm-ai/celenv` — the module agentd and the STS build their own
// environments from, so a guard, a workflow expression or a consent caveat
// is checked by exactly one dialect rather than by this package's own copy of
// one. Before this file existed, `celEnvFor` (gone now) built a
// `cel.dev/cel-go/common/types.Registry` by hand and walked a variable's
// parent file plus its transitive imports (`registerFileAndImports`, also
// gone) — a second implementation of the same idea celenv.EnvVars already
// has, and the two could disagree: a guard touching a message outside its
// own file's import closure could lint clean here and fail to load at
// agentd, or the reverse, with nothing testing it.
//
// celenv wants a *protoregistry.Files (cel.TypeDescs(files), resolving a
// type by name across the whole generation) rather than the
// []protoreflect.FileDescriptor LintWith receives. internal/compile already
// builds one of these on the way to producing that slice (compile.go:162)
// and discards it, so filesRegistry rebuilds it here from the fds a lint run
// already has — cheap next to compiling the tree in the first place, and it
// keeps LintWith's signature, and every external caller's, untouched. See
// the unit's report for why this reads better than threading the registry in
// through internal/compile and Options.

// filesRegistry rebuilds a *protoregistry.Files spanning every file reachable
// from fds. For every caller of LintWith this already IS the whole
// generation: the whole catalogue for `garm lint` and `catalogue build`, or
// one buf-invoked directory plus its dependencies for the protoc plugin.
//
// This is the behavioural change the unit's report and KNOWN-GAPS.md record:
// celenv.EnvVars resolves a type by name across everything in `files`, which
// is wider than `celEnvFor` ever was — a guard, or a workflow expression,
// touching a message outside its own file's transitive imports now compiles
// where it previously failed. That is intentional: a lint gate stricter than
// the runtime rejects a declaration that would have worked, and celenv is
// also what the runtime resolves against.
func filesRegistry(fds []protoreflect.FileDescriptor) (*protoregistry.Files, error) {
	return filesFrom(fds...)
}

// filesFrom builds a *protoregistry.Files from an explicit seed list of file
// descriptors, closed transitively over their imports — the same walk
// internal/compile.Union's own `collect` performs on the way to a
// FileDescriptorSet, reused here because rebuilding one from descriptors
// that are already resolved (every fd here came from a protodesc-built
// registry) cannot fail the way compiling from source can.
func filesFrom(seeds ...protoreflect.FileDescriptor) (*protoregistry.Files, error) {
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if fd == nil || seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			add(imports.Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range seeds {
		add(fd)
	}
	return protodesc.NewFiles(set)
}

// filesFromMessages is filesFrom, seeded by a set of variables' own parent
// files rather than by file descriptors directly — what a CEL environment is
// usually declared from. Used where a function's signature is pinned by an
// existing test and so cannot take the whole generation's registry: it falls
// back to the transitive import closure of whichever descriptors it already
// has, which is exactly the scope `celEnvFor` gave every caller before this
// file existed.
func filesFromMessages(mds ...protoreflect.MessageDescriptor) (*protoregistry.Files, error) {
	seeds := make([]protoreflect.FileDescriptor, 0, len(mds))
	for _, md := range mds {
		if md != nil {
			seeds = append(seeds, md.ParentFile())
		}
	}
	return filesFrom(seeds...)
}

// envFiles declares vars over files, falling back to each variable's own
// import closure (filesFromMessages) when the caller has no wider registry
// to offer — nil is exactly what a direct call from a test, unable to pass
// one in because its signature predates this file, leaves behind.
func envFiles(files *protoregistry.Files, vars map[string]protoreflect.MessageDescriptor) (*cel.Env, error) {
	if files == nil {
		mds := make([]protoreflect.MessageDescriptor, 0, len(vars))
		for _, md := range vars {
			mds = append(mds, md)
		}
		var err error
		files, err = filesFromMessages(mds...)
		if err != nil {
			return nil, err
		}
	}
	return celenv.EnvVars(files, vars)
}
