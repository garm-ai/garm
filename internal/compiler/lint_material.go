package compiler

import (
	"fmt"
	"strings"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/grant"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// L31, L32, L33 — material_fields. Dispatched from Lint alongside LintEffects.
//
// These names are what a human sees on an approval screen and what a grant
// binds to, so a path that resolves to nothing is not a typo to discover in
// production: it is a field silently absent from the digest, which means the
// approval covers less than its author believed and nothing says so.
func lintMaterialFields(tools []Tool) []Diag {
	var out []Diag
	for _, t := range tools {
		p := t.Policy
		path := string(t.Method.FullName())
		fields := p.GetApproval().GetMaterialFields()

		// L31 — material fields only mean something under MODE_GRANT.
		//
		// Nothing reads them otherwise, so an author who set them under
		// MODE_NOTIFY has written a specification of what is being approved
		// for a tool where nobody approves anything.
		if len(fields) > 0 && p.GetApproval().GetMode() != toolv1.Approval_MODE_GRANT {
			out = append(out, Diag{Rule: "L31", Path: path,
				Msg: "approval.material_fields is set without MODE_GRANT; nothing " +
					"reads them, so this describes an approval that never happens"})
			continue
		}

		seen := map[string]bool{}
		for _, f := range fields {
			if err := grant.ValidPath(f); err != nil {
				out = append(out, Diag{Rule: "L32", Path: path,
					Msg: fmt.Sprintf("approval.material_fields %q is malformed: %v", f, err)})
				continue
			}
			if seen[f] {
				// Harmless to the digest, which is keyed by path, but it
				// means an author believes they named two things.
				out = append(out, Diag{Rule: "L32", Path: path, Warn: true,
					Msg: fmt.Sprintf("approval.material_fields lists %q twice", f)})
				continue
			}
			seen[f] = true

			fd, err := resolveScalarPath(t.Method.Input(), f)
			if err != nil {
				out = append(out, Diag{Rule: "L32", Path: path,
					Msg: fmt.Sprintf("approval.material_fields %q: %v", f, err)})
				continue
			}

			// L33 — a material field that may be absent.
			//
			// A warning, not an error, because absence is a legitimate thing
			// to approve: an unset schedule date meaning "now" is material
			// and correctly optional. What it flags is the other case — a
			// human approving a request with the amount blank, and a handler
			// supplying a default they never saw.
			if !isRequired(fd) {
				out = append(out, Diag{Rule: "L33", Path: path, Warn: true,
					Msg: fmt.Sprintf("approval.material_fields %q is not required, so a "+
						"human can approve a request with it unset. That is correct "+
						"when absence is itself what they are approving, and a hole "+
						"when the handler fills in a default they never saw", f)})
			}
		}
	}
	return out
}

// resolveScalarPath walks a dotted path and insists it ends at a scalar.
//
// Messages, repeated fields and maps are refused because digesting one needs
// a canonical protobuf encoding agreed across every language that computes or
// checks a grant — and because a field a human cannot read in a sentence is
// one they cannot meaningfully approve.
func resolveScalarPath(md protoreflect.MessageDescriptor, path string) (protoreflect.FieldDescriptor, error) {
	segs := strings.Split(path, ".")
	cur := md
	for i, seg := range segs {
		if cur == nil {
			return nil, fmt.Errorf("%q is not a message, so %q cannot be reached through it",
				strings.Join(segs[:i], "."), path)
		}
		fd := cur.Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return nil, fmt.Errorf("%s has no field %q", cur.FullName(), seg)
		}
		last := i == len(segs)-1
		switch {
		case fd.IsMap():
			return nil, fmt.Errorf("%q is a map; a digest over one needs a canonical "+
				"encoding this deliberately avoids", seg)
		case fd.IsList():
			return nil, fmt.Errorf("%q is repeated; a digest over one needs a canonical "+
				"encoding this deliberately avoids", seg)
		case fd.Kind() == protoreflect.MessageKind, fd.Kind() == protoreflect.GroupKind:
			if last {
				return nil, fmt.Errorf("%q is a message; name a scalar field inside it", seg)
			}
			cur = fd.Message()
		default:
			if !last {
				return nil, fmt.Errorf("%q is a scalar and %q continues past it", seg, path)
			}
			return fd, nil
		}
	}
	return nil, fmt.Errorf("%q names no field", path)
}

// isRequired reports whether this field can be absent at all.
//
// Two ways it cannot. A proto3 scalar without `optional` has no presence — it
// is always on the wire, defaulting to zero, which is its own kind of unsafe
// but not the one L33 is about. Or buf.validate marks it required, which is
// how the interesting fields are written: `optional int64` for presence, plus
// a required constraint so absence is refused before the chain resolves
// anything.
func isRequired(fd protoreflect.FieldDescriptor) bool {
	if !fd.HasPresence() {
		return true
	}
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || !proto.HasExtension(opts, validate.E_Field) {
		return false
	}
	rules, ok := proto.GetExtension(opts, validate.E_Field).(*validate.FieldRules)
	return ok && rules.GetRequired()
}
