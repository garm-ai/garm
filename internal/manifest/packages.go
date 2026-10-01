package manifest

import (
	"fmt"
	"sort"
	"strings"

	cataloguev1 "github.com/garm-ai/contracts/garm/catalogue/v1"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Packages works out which proto packages each input contributed, and refuses
// the two ways that can go wrong.
//
// It runs AFTER the union is compiled and not before, because the answer is a
// property of the descriptors rather than of the manifest. A module entry names
// the packages it wants, but what a directory actually declares is in the file:
// a package that moved, or a directory that was renamed without its package
// being renamed, is exactly the drift this whole design exists to catch, and a
// resolver that trusted the manifest's names would report the manifest back to
// itself. A path entry names no packages at all, so for that shape there is
// nothing to trust in the first place.
//
// Two refusals, both from §2 of the design:
//
//   - Two inputs declaring one proto package is an error naming both. Never a
//     merge and never last-wins: a silent winner is the drift this replaces. It
//     is also what refuses a half-migrated tree, where a copied `proto/web`
//     survives beside the `module:` entry meant to replace it.
//   - A package a module entry named and the module does not declare is an
//     error naming both. Resolve catches the usual case — no such directory —
//     and this catches the subtler one, where the directory exists and the
//     files in it declare something else.
//
// The set's own files are the source of truth for which package a file belongs
// to, so a file reached only as an import is attributed to nobody: it is a
// dependency of the set, already carried in Catalogue.files, and not a
// declaration of whichever input happened to import it.
func Packages(inputs []*Input, set *descriptorpb.FileDescriptorSet) error {
	// Which input provided each path. Two inputs offering the same relative
	// path is refused here rather than in the compiler, which would silently
	// let the first import root win: only this layer knows they were separate
	// inputs and can say whose they were.
	owner := map[string]*Input{}
	for _, in := range inputs {
		for _, f := range in.Files {
			if prev, dup := owner[f]; dup {
				return fmt.Errorf("two inputs both provide %s: %s and %s.\n\n"+
					"One file is one input's. A tree that still carries a copy of a package "+
					"it now includes as a module is in both states at once, and the copy is "+
					"the one to delete", f, prev.Entry, in.Entry)
			}
			owner[f] = in
		}
	}

	declared := map[string]*Input{}
	seen := map[*Input]map[string]bool{}
	for _, in := range inputs {
		seen[in] = map[string]bool{}
	}
	for _, f := range set.GetFile() {
		in, ok := owner[f.GetName()]
		if !ok {
			continue
		}
		pkg := f.GetPackage()
		if pkg == "" {
			continue
		}
		if prev, dup := declared[pkg]; dup && prev != in {
			return fmt.Errorf("two inputs declare %s: %s and %s.\n\n"+
				"One proto package comes from one input. Merging them would pick a winner "+
				"per file with nothing saying which, and that silent winner is the drift a "+
				"manifest exists to remove — so declare it once and delete the other",
				pkg, prev.Entry, in.Entry)
		}
		declared[pkg] = in
		seen[in][pkg] = true
	}

	for _, in := range inputs {
		in.Packages = nil
		for pkg := range seen[in] {
			in.Packages = append(in.Packages, pkg)
		}
		sort.Strings(in.Packages)

		if !in.Entry.IsModule() {
			if len(in.Packages) == 0 {
				return fmt.Errorf("%s contributes no proto package", in.Entry)
			}
			continue
		}
		// A module entry is a promise about what it contributes, so what it
		// actually contributed has to match it in both directions: a named
		// package missing is the §2 refusal, and an unnamed package appearing
		// means a directory holds a file whose package is not the one its path
		// spells — adopted silently, which is the consent problem again.
		for _, pkg := range in.Entry.Packages {
			if !seen[in][pkg] {
				return fmt.Errorf("module %s@%s does not declare %s: the files at %s declare %s.\n\n"+
					"A package's name and its directory have to agree for an import of it to "+
					"resolve, so this module's tree and the manifest's name for it disagree "+
					"about one of the two", in.Identity, in.Version, pkg, PackageDir(pkg),
					strings.Join(in.Packages, ", "))
			}
		}
		for _, pkg := range in.Packages {
			if !containsString(in.Entry.Packages, pkg) {
				return fmt.Errorf("module %s@%s contributed %s, which the manifest does not "+
					"name.\n\nA module entry adopts the packages it lists and no others: a "+
					"package arriving because it shares a directory with one that was named "+
					"is a tool the deployment never said yes to", in.Identity, in.Version, pkg)
			}
		}
	}
	return nil
}

// Provenance is the inputs as the artifact records them.
//
// Unsorted: the canonical order is the contract's and catalogue.Build applies
// it, so there is one implementation of it and it sits with the code that
// stamps it.
func Provenance(inputs []*Input) []*cataloguev1.Input {
	out := make([]*cataloguev1.Input, 0, len(inputs))
	for _, in := range inputs {
		rec := &cataloguev1.Input{ProtoPackages: sortedCopy(in.Packages)}
		if in.Kind == KindModule {
			rec.Of = &cataloguev1.Input_Module{Module: &cataloguev1.ModuleInput{
				Path:    in.Identity,
				Version: in.Version,
			}}
		} else {
			rec.Of = &cataloguev1.Input_Local{Local: &cataloguev1.LocalInput{
				Path:   in.Identity,
				Source: in.Source,
			}}
		}
		out = append(out, rec)
	}
	return out
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
