package policy

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Action is one field's policy, flattened with its path from the root message.
type Action struct {
	Path   []protoreflect.FieldNumber
	Name   string // dotted path, for diagnostics and the ledger
	Read   toolv1.Clearance
	Write  toolv1.Clearance
	Need   CompartmentSet
	OnDeny *toolv1.Redaction
	// IsSubtree means "this Plan holds Actions BELOW this one", so denying it
	// lets the resolver prune them (see resolve's `pruned`). It is not "the
	// field is message-typed": an opaque leaf — a google.protobuf.* value —
	// is message-typed and has no Actions beneath it, so it is false there.
	// Marking it true would be a lie the resolver happens not to act on
	// today, and a trap for anything that later reads it as a structural
	// claim. Denial still CLEARS such a field when its on_deny is omit; that
	// happens in Sanitize's applyToField, which never consults IsSubtree.
	IsSubtree   bool
	AuditOnRead bool
}

// Plan is every field of a message type, compiled once at boot.
type Plan struct {
	Desc    protoreflect.MessageDescriptor
	Actions []Action
}

// Compile walks a message descriptor into a flat plan.
//
// A Plan is a FLAT list of paths, so it can only describe a finite message
// graph. A self- or mutually-referential message type has no finite flattening
// and is therefore REJECTED, not truncated: see compileInto for why silently
// stopping at the repeat is a leak rather than an optimisation.
func Compile(md protoreflect.MessageDescriptor, reg *Registry) (*Plan, error) {
	p := &Plan{Desc: md}
	seen := map[protoreflect.FullName]bool{}
	if err := compileInto(p, md, reg, nil, "", seen); err != nil {
		return nil, err
	}
	return p, nil
}

func compileInto(
	p *Plan,
	md protoreflect.MessageDescriptor,
	reg *Registry,
	path []protoreflect.FieldNumber,
	prefix string,
	seen map[protoreflect.FullName]bool,
) error {
	// A cycle is a hard error, never a stopping condition.
	//
	// The obvious-looking alternative — return nil here, on the theory that
	// "the outer occurrence already carries the policy" — is false and was a
	// live leak. An Action's Path is a field-number path from the ROOT, so
	// the outer occurrence's actions describe the outer INSTANCE only. With
	// `Node { Node child = 1; optional string secret = 2 [RESTRICTED, omit] }`
	// the plan then holds exactly two actions (child, secret) and none for
	// child.secret, child.child.secret, … — so a PUBLIC caller gets the
	// top-level secret removed and every nested one returned in the clear.
	// checkInput iterates the same actions, so it is equally a privilege
	// escalation on the input side.
	//
	// Depth-bounded unrolling is not a fix either: it makes the leak
	// depth-dependent rather than removing it. A flat plan cannot represent
	// an unbounded graph, so the only fail-closed answer is to refuse to
	// compile one. A schema that needs recursion must either break the cycle
	// (e.g. flatten to a repeated node list with parent ids) or keep the RPC
	// off the tool surface with exclude: true.
	if seen[md.FullName()] {
		at := "(root)"
		if prefix != "" {
			at = prefix
		}
		return fmt.Errorf("%s: recursive message type reached again at %q: a cycle cannot be "+
			"flattened into a finite plan, so every nested instance would carry no policy and "+
			"every classified field below the first occurrence would be returned unredacted; "+
			"break the cycle or exclude the tool", md.FullName(), at)
	}
	seen[md.FullName()] = true
	defer delete(seen, md.FullName())

	def := MessageDefaultPolicy(md)

	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		fp := FieldPolicyOf(fd, def)
		if fp == nil {
			return fmt.Errorf("%s: field %q has no policy and no message default",
				md.FullName(), fd.Name())
		}
		need, err := reg.Set(fp.GetCompartments())
		if err != nil {
			return fmt.Errorf("%s.%s: %w", md.FullName(), fd.Name(), err)
		}

		name := string(fd.Name())
		if prefix != "" {
			name = prefix + "." + name
		}
		// SubtreeOf is the single definition of "the walk descends here",
		// shared with toolgen's lintMessage. See its doc comment.
		//
		// IsSubtree is exactly "there are Actions beneath this one", which is
		// why it is `ok` and not "the field is message-typed": an opaque leaf
		// (a well-known type) is message-typed and still has nothing below it.
		childMD, ok := SubtreeOf(fd)

		write := fp.GetWrite()
		if write == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
			write = fp.GetRead()
		}

		p.Actions = append(p.Actions, Action{
			Path:        append(append([]protoreflect.FieldNumber{}, path...), fd.Number()),
			Name:        name,
			Read:        fp.GetRead(),
			Write:       write,
			Need:        need,
			OnDeny:      fp.GetOnDeny(),
			IsSubtree:   ok,
			AuditOnRead: fp.GetAuditOnRead(),
		})

		if ok {
			child := append(append([]protoreflect.FieldNumber{}, path...), fd.Number())
			if err := compileInto(p, childMD, reg, child, name, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// wellKnownPrefix is the package every protobuf well-known type lives in.
const wellKnownPrefix = "google.protobuf."

// IsOpaqueLeafMessage reports whether md is a message the policy walks treat
// as an opaque VALUE rather than as a structure to classify field by field.
//
// Today that means the protobuf well-known types. Their internal fields
// (Timestamp's seconds/nanos, Duration's, a wrapper's `value`, Struct's
// recursive Value graph) carry no (garm.v1.field_policy) and never can — the
// descriptors ship with protobuf and this repo does not own them. Descending
// into one therefore reports every field unlabeled and fails the build, which
// is how `google.protobuf.Timestamp created_at = 4;` — the spec's own worked
// example — was unrepresentable, and how date_grain, whose transformer
// accepts nothing but a Timestamp, ended up dead code.
//
// Treating them as leaves is not a concession, it is the right model: a
// Timestamp is a VALUE. Classifying `seconds` at one clearance and `nanos` at
// another is meaningless, and the field's own policy already governs the
// whole value — including for redaction, since date_grain, omit and a custom
// transformer all operate on the Timestamp message as a unit.
//
// The dynamic types (Any, Struct, Value, ListValue, FieldMask) are leaves by
// the same rule. That is deliberate: nothing here forbids them, and nothing
// here should — they are opaque by construction, so a schema that carries one
// is saying "this field's policy governs whatever is inside", which is
// precisely what a leaf means.
//
// NOTE: this is a PACKAGE-PREFIX HEURISTIC, not a general "a message type we
// do not own, and therefore cannot annotate" test. It happens to be exact for
// the well-known types, because `google.protobuf.` is reserved for them, but
// the general question — what to do about an imported third-party message
// with no garm annotations, which today fails the build with no recourse
// short of vendoring and editing its .proto — is OPEN and deliberately not
// answered here. Widening this predicate is a policy decision (an unannotated
// foreign message would become an opaque value governed by one clearance,
// instead of a build error), not a refactor.
func IsOpaqueLeafMessage(md protoreflect.MessageDescriptor) bool {
	return md != nil && strings.HasPrefix(string(md.FullName()), wellKnownPrefix)
}

// SubtreeOf reports whether the policy walk descends into fd, and if so, into
// which message descriptor.
//
// This is the SINGLE definition of what is reachable from a message, shared
// by policy.Compile (which builds the enforced Plan) and toolgen's
// lintMessage (which is the build gate over the same graph). They must agree:
// a lint that walks further than Compile reports errors on fields Compile
// never enforces, and — the direction that actually bit this repo, when the
// two walks disagreed about message-valued maps — a lint that walks LESS far
// than Compile passes the build and then hands the operator a server that
// refuses to start at Mount. One function, called from both, makes that class
// of divergence unrepresentable rather than merely tested for.
//
// The three cases:
//
//   - A non-message field is a leaf. Nothing to descend into.
//   - A map field is a subtree only when its VALUES are messages. A map's own
//     Kind() is always MessageKind (protobuf models a map as a repeated
//     synthetic MapEntry), so the element type lives on MapValue(), not on
//     Message(); a map<string,string> has nothing below it and redaction
//     there applies elementwise.
//   - A well-known type is a leaf: see IsOpaqueLeafMessage.
func SubtreeOf(fd protoreflect.FieldDescriptor) (protoreflect.MessageDescriptor, bool) {
	if fd.Kind() != protoreflect.MessageKind {
		return nil, false
	}
	md := fd.Message()
	if fd.IsMap() {
		if fd.MapValue().Kind() != protoreflect.MessageKind {
			return nil, false
		}
		md = fd.MapValue().Message()
	}
	if IsOpaqueLeafMessage(md) {
		return nil, false
	}
	return md, true
}

// FieldPolicyOf returns the field's own policy, or the message default.
func FieldPolicyOf(fd protoreflect.FieldDescriptor, def *toolv1.FieldPolicy) *toolv1.FieldPolicy {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if ok && proto.HasExtension(opts, toolv1.E_FieldPolicy) {
		if fp, ok := proto.GetExtension(opts, toolv1.E_FieldPolicy).(*toolv1.FieldPolicy); ok && fp != nil {
			return fp
		}
	}
	return def
}

// MessageDefaultPolicy returns the message's default field policy, if declared.
func MessageDefaultPolicy(md protoreflect.MessageDescriptor) *toolv1.FieldPolicy {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || !proto.HasExtension(opts, toolv1.E_DefaultFieldPolicy) {
		return nil
	}
	fp, _ := proto.GetExtension(opts, toolv1.E_DefaultFieldPolicy).(*toolv1.FieldPolicy)
	return fp
}

// String renders a plan for diagnostics. It prints paths and redaction kinds
// only — never values, which do not exist at this layer anyway.
func (p *Plan) String() string {
	var b strings.Builder
	for _, a := range p.Actions {
		fmt.Fprintf(&b, "%s read=%v need=%d subtree=%v\n", a.Name, a.Read, a.Need, a.IsSubtree)
	}
	return b.String()
}
