package catalogue

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/garm-ai/contracts/cards"
)

// synthesiseCards adds every tool's card endpoints to the descriptor set a
// catalogue carries.
//
// The daemon mounts what the catalogue declares, and nothing else. So a card
// that is not in the file set is a card no viewer can fetch, whatever the
// tool service registered — which is why this runs over the SET rather than
// only over the resolved descriptors, and why it also adds the imports the
// new methods need. A file set that does not rebuild fails at the daemon's
// boot, and that is the wrong end of the pipe to discover it.
//
// It runs AFTER lint, deliberately. The rules judge what an author wrote; a
// synthesised method is not the author's, and reporting a diagnostic against
// one would name a method they cannot edit. The one rule that does look at
// them (C9, the collision) runs at lint time over the names this package
// would produce, not over the methods themselves.
func synthesiseCards(set *descriptorpb.FileDescriptorSet, fds []protoreflect.FileDescriptor) (int, error) {
	byPath := make(map[string]*descriptorpb.FileDescriptorProto, len(set.GetFile()))
	for _, f := range set.GetFile() {
		byPath[f.GetName()] = f
	}

	added := 0
	needFiles := map[string]bool{}

	for _, fd := range fds {
		fp := byPath[fd.Path()]
		if fp == nil {
			// A descriptor that is not in the set cannot be amended. This
			// cannot happen for a tree compiled by compile.Tree, which
			// collects every file it resolved; refusing beats silently
			// serving a catalogue whose cards are missing from half of it.
			return 0, fmt.Errorf("%s is linked but not in the descriptor set", fd.Path())
		}
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			synths := cards.ForService(svc)
			if len(synths) == 0 {
				continue
			}
			sp := serviceProto(fp, svc.Name())
			if sp == nil {
				return 0, fmt.Errorf("%s: service %s is not in the descriptor set", fd.Path(), svc.Name())
			}
			for _, s := range synths {
				md, err := s.Descriptor()
				if err != nil {
					return 0, fmt.Errorf("%s: %w", s, err)
				}
				sp.Method = append(sp.Method, md)
				added++
			}
			for _, imp := range cards.Imports(synths) {
				needFiles[imp] = true
				addDependency(fp, imp)
			}
		}
	}

	if added == 0 {
		return 0, nil
	}
	if err := ensureFiles(set, byPath, needFiles); err != nil {
		return 0, err
	}
	return added, nil
}

func serviceProto(fp *descriptorpb.FileDescriptorProto, name protoreflect.Name) *descriptorpb.ServiceDescriptorProto {
	for _, s := range fp.GetService() {
		if s.GetName() == string(name) {
			return s
		}
	}
	return nil
}

func addDependency(fp *descriptorpb.FileDescriptorProto, path string) {
	if fp.GetName() == path {
		return
	}
	for _, d := range fp.GetDependency() {
		if d == path {
			return
		}
	}
	fp.Dependency = append(fp.Dependency, path)
}

// ensureFiles puts the card vocabulary and google.protobuf.Empty in the set
// when a synthesised method made them reachable and the author's tree never
// imported them.
//
// They come from this binary's own linked registry, which is the same source
// the compiler resolved every other import from, so the bytes are the ones
// this build speaks. Prepended in dependency order: a FileDescriptorSet has
// to be topologically ordered or nothing can rebuild it.
func ensureFiles(
	set *descriptorpb.FileDescriptorSet,
	byPath map[string]*descriptorpb.FileDescriptorProto,
	need map[string]bool,
) error {
	paths := make([]string, 0, len(need))
	for p := range need {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var prepend []*descriptorpb.FileDescriptorProto
	var add func(path string) error
	add = func(path string) error {
		if byPath[path] != nil {
			return nil
		}
		fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
		if err != nil {
			return fmt.Errorf("a synthesised card needs %s and this build does not "+
				"carry it: %w", path, err)
		}
		fp := protodesc.ToFileDescriptorProto(fd)
		// Mark it present BEFORE recursing, so a diamond in the import graph
		// is visited once and a cycle cannot spin.
		byPath[path] = fp
		for _, dep := range fp.GetDependency() {
			if err := add(dep); err != nil {
				return err
			}
		}
		// After its own dependencies, so the slice stays topological.
		prepend = append(prepend, fp)
		return nil
	}
	for _, p := range paths {
		if err := add(p); err != nil {
			return err
		}
	}
	if len(prepend) == 0 {
		return nil
	}
	set.File = append(prepend, set.File...)
	return nil
}

// rebuildable re-resolves the amended set, so a catalogue that a daemon
// cannot load is refused here rather than at its boot.
func rebuildable(set *descriptorpb.FileDescriptorSet) error {
	if _, err := protodesc.NewFiles(proto.Clone(set).(*descriptorpb.FileDescriptorSet)); err != nil {
		return fmt.Errorf("the catalogue's descriptor set does not rebuild after the "+
			"card endpoints were added: %w", err)
	}
	return nil
}
