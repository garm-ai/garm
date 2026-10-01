package compiler

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/contracts/cards"
)

// Rule C9 — a hand-written method may not take a synthesised card's name.
//
// Every tool gets three card endpoints it never declared (contracts/cards),
// and they are added to the catalogue after lint. So a collision is not
// caught by anything that looks at the author's file: the build would
// succeed, the catalogue would carry two methods with one name, and which of
// them a viewer reached would depend on which the daemon indexed last.
//
// The rule reports against the HAND-WRITTEN method, because that is the one
// its author can move. The fix is either to rename it or — if the author
// wanted to customise the card — to override it in Go on the generated
// `<Service>Cards` interface, which is what an override is for and does not
// need a method in the proto at all.
//
// C9 also refuses a synthesised name that could not BE a tool name. A tool
// name is at most 64 characters, and `_approval_card` is fourteen of them:
// a parent close to the limit produces a card the catalogue cannot carry,
// and the author has to learn that from the declaration they can shorten
// rather than from a catalogue build three commits later.
func lintSynthesisedNames(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			synths := cards.ForService(svc)
			if len(synths) == 0 {
				continue
			}
			declared := map[protoreflect.Name]protoreflect.MethodDescriptor{}
			for j := 0; j < svc.Methods().Len(); j++ {
				md := svc.Methods().Get(j)
				declared[md.Name()] = md
			}
			for _, s := range synths {
				if md, clash := declared[s.Name]; clash {
					out = append(out, Diag{Rule: "C9", Path: string(md.FullName()), Msg: fmt.Sprintf(
						"this method's name is the one synthesised for %s's %s, so the "+
							"catalogue would carry two methods called %s and which one a "+
							"viewer reached would be an accident of indexing order. Rename "+
							"it, or — if you meant to customise the card — delete it and "+
							"implement %s on the generated %sCards interface, which needs "+
							"no proto at all",
						s.Parent.Name(), s.Kind, s.Name, s.Name, svc.Name())})
				}
				if !cards.ValidToolName(s.ToolName()) {
					out = append(out, Diag{Rule: "C9", Path: string(s.Parent.FullName()), Msg: fmt.Sprintf(
						"this tool's %s would be called %q, which is not a usable tool "+
							"name (at most 64 characters, lower case, starting with a "+
							"letter). Shorten the tool's own name",
						s.Kind, s.ToolName())})
				}
			}
		}
	}
	return out
}
