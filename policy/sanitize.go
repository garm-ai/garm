package policy

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy/redact"
)

// Sanitize mutates m in place, applying every denial in r. It returns the field
// paths that were actually changed, for the ledger — paths only, never values.
//
// It PANICS if m's descriptor is not the one r was resolved for. See the
// guard below: that pairing is impossible after boot-time validation, and
// silently doing nothing would be indistinguishable from a clean call.
//
// r is treated as strictly read-only: *Resolved is shared, by pointer, across
// every caller with the same shape (see Cache), so Sanitize must never sort,
// reassign, or append into r.Deny or r.Disclosed. Doing so would corrupt
// policy for every other concurrent caller of that shape.
func Sanitize(m proto.Message, r *Resolved, ctx redact.Ctx) []string {
	if m == nil || r == nil {
		return nil
	}
	root := m.ProtoReflect()
	// r was resolved for a specific message type; pairing it with a message
	// of a different type would redact whatever field happens to sit at the
	// same field numbers (or panic on a mismatched kind).
	//
	// This PANICS rather than returning nil. Returning nil looks like the
	// fail-closed choice and is the opposite: Sanitize's only signal is the
	// list of paths it changed, so "mismatched descriptor, did nothing" and
	// "matched fine, nothing needed redacting" are the same value. A caller
	// that trusted it would hand the response straight to the wire,
	// unredacted, while every log line said the call was clean. The
	// interceptor used to wrap this in a guardedSanitize helper that caught
	// the mismatch itself, which fixed the one call site and left the
	// PRIMITIVE fail-open for the next one — Plan B's schema projection and
	// Plan D's descriptor loading both call Sanitize directly.
	//
	// A mismatch is impossible after boot-time validation: plans and cache
	// entries are keyed on descriptor IDENTITY (see Cache.key and
	// Server.plans), so a Resolved can only be paired with the descriptor it
	// was resolved for. Panicking on an impossible condition is the honest
	// encoding of "this cannot happen, and if it does, nothing downstream is
	// trustworthy" — and it keeps the signature free of an error nobody
	// would otherwise have to handle.
	//
	// The check deliberately precedes the len(r.Deny) == 0 shortcut. A
	// mispaired Resolved that happens to deny nothing is still mispaired,
	// and letting it through would make the guard's coverage depend on the
	// caller's clearance — the higher the clearance, the emptier Deny, the
	// likelier the mismatch goes unnoticed.
	if root.Descriptor() != r.Desc {
		panic("toolpolicy: Sanitize called with a Resolved for " +
			descName(r.Desc) + " and a message of type " +
			descName(root.Descriptor()) +
			"; refusing to return data this policy was not resolved for")
	}
	if len(r.Deny) == 0 {
		return nil
	}
	var touched []string
	for _, a := range r.Deny {
		if len(a.Path) == 0 {
			continue // Compile never emits this; a hand-built Action might.
		}
		if applyAction(root, a, ctx) {
			touched = append(touched, a.Name)
		}
	}
	return touched
}

// applyAction navigates the path and applies the action to every instance it
// reaches. A path crossing a repeated or map field fans out to all elements,
// which is why this returns "did anything change" rather than a count.
func applyAction(root protoreflect.Message, a Action, ctx redact.Ctx) bool {
	targets := []protoreflect.Message{root}

	// Walk every path element but the last, expanding across collections.
	for _, num := range a.Path[:len(a.Path)-1] {
		var next []protoreflect.Message
		for _, t := range targets {
			fd := t.Descriptor().Fields().ByNumber(num)
			if fd == nil || !t.Has(fd) {
				continue
			}
			v := t.Get(fd)
			switch {
			case fd.IsList():
				l := v.List()
				for i := 0; i < l.Len(); i++ {
					next = append(next, l.Get(i).Message())
				}
			case fd.IsMap():
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					if fd.MapValue().Kind() == protoreflect.MessageKind {
						next = append(next, mv.Message())
					}
					return true
				})
			default:
				next = append(next, v.Message())
			}
		}
		targets = next
		if len(targets) == 0 {
			return false
		}
	}

	last := a.Path[len(a.Path)-1]
	changed := false
	for _, t := range targets {
		fd := t.Descriptor().Fields().ByNumber(last)
		if fd == nil || !t.Has(fd) {
			continue
		}
		if applyToField(t, fd, a, ctx) {
			changed = true
		}
	}
	return changed
}

func applyToField(
	t protoreflect.Message,
	fd protoreflect.FieldDescriptor,
	a Action,
	ctx redact.Ctx,
) bool {
	// Omit is structural: clear the field, whatever its cardinality. A denied
	// message-typed gate also lands here, which is what prunes the subtree.
	if isOmit(a.OnDeny) {
		t.Clear(fd)
		return true
	}

	ctx.Path = a.Name

	switch {
	case fd.IsList():
		l := t.Mutable(fd).List()
		for i := 0; i < l.Len(); i++ {
			out, omit := redact.Apply(ctx, a.OnDeny, fd.Kind(), l.Get(i))
			if omit {
				t.Clear(fd) // cannot partially clear a list safely; drop it
				return true
			}
			l.Set(i, out)
		}
		return l.Len() > 0

	// A non-omit redaction on a map redacts VALUES ONLY; keys are always left
	// in the clear. That's harmless for a map like tags (string values,
	// non-sensitive keys), but a map keyed by PII would disclose every key
	// even though every value was redacted. Schema guidance is that a
	// key-bearing map uses on_deny: omit instead, which clears the whole map
	// via the branch above; this branch never sees that case.
	case fd.IsMap():
		mp := t.Mutable(fd).Map()
		var keys []protoreflect.MapKey
		mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
			keys = append(keys, k)
			return true
		})
		for _, k := range keys {
			out, omit := redact.Apply(ctx, a.OnDeny, fd.MapValue().Kind(), mp.Get(k))
			if omit {
				mp.Clear(k)
				continue
			}
			mp.Set(k, out)
		}
		return len(keys) > 0

	default:
		out, omit := redact.Apply(ctx, a.OnDeny, fd.Kind(), t.Get(fd))
		if omit {
			t.Clear(fd)
			return true
		}
		t.Set(fd, out)
		return true
	}
}

// descName names a descriptor for the panic message, tolerating a nil one —
// a Resolved with no Desc is not resolved for anything, and the panic must
// say so rather than becoming a nil dereference on its way out.
func descName(md protoreflect.MessageDescriptor) string {
	if md == nil {
		return "<no descriptor>"
	}
	return string(md.FullName())
}

func isOmit(r *toolv1.Redaction) bool {
	if r == nil || r.GetKind() == nil {
		return true // unset means omit
	}
	_, ok := r.GetKind().(*toolv1.Redaction_Omit)
	return ok
}
