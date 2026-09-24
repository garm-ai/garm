// Package redact turns a denied field value into what the caller may see.
//
// It is deliberately importable without the rest of the policy machinery, so
// leak tests can exercise every transformer in isolation.
package redact

import (
	"sync"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Ctx carries what a transformer may need beyond the value itself.
type Ctx struct {
	Key    []byte // HMAC key material for hash
	Tenant string // for per-tenant salting
	Path   string // field path, for diagnostics only — never emitted with a value
}

// Transformer converts a value the caller may not see into one they may.
//
// Transform must be pure and total. Returning an error means the field is
// omitted; it must never result in the original value reaching the caller.
type Transformer interface {
	Name() string
	Accepts() []protoreflect.Kind
	Transform(Ctx, protoreflect.Value) (protoreflect.Value, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Transformer{}
)

// Register adds a transformer. Safe to call from init.
func Register(t Transformer) {
	mu.Lock()
	defer mu.Unlock()
	registry[t.Name()] = t
}

// Lookup finds a transformer by name.
func Lookup(name string) (Transformer, bool) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := registry[name]
	return t, ok
}

// For maps a Redaction to its transformer. Omit has none — it is structural.
func For(r *toolv1.Redaction) (Transformer, bool) {
	switch k := r.GetKind().(type) {
	case *toolv1.Redaction_Mask:
		return maskWith{with: k.Mask.GetWith()}, true
	case *toolv1.Redaction_Hash:
		return hasher{perTenant: k.Hash.GetPerTenantSalt()}, true
	case *toolv1.Redaction_EmailDomain:
		return Lookup("email_domain")
	case *toolv1.Redaction_CardBinLast4:
		return Lookup("card_bin_last4")
	case *toolv1.Redaction_UrlOrigin:
		return Lookup("url_origin")
	case *toolv1.Redaction_KeepLast:
		return keepLast{n: int(k.KeepLast.GetN()), runes: k.KeepLast.GetRunes()}, true
	case *toolv1.Redaction_Truncate:
		return truncate{n: int(k.Truncate.GetN()), runes: k.Truncate.GetRunes()}, true
	case *toolv1.Redaction_IpPrefix:
		return ipPrefix{bits: int(k.IpPrefix.GetBits())}, true
	case *toolv1.Redaction_DateGrain:
		return dateGrain{grain: k.DateGrain.GetGrain()}, true
	case *toolv1.Redaction_Custom:
		return Lookup(k.Custom.GetName())
	}
	return nil, false // nil, empty oneof, or explicit Omit
}

// Apply redacts one value of the given field kind. The bool reports "omit
// this field entirely".
//
// kind is the field's own protoreflect.Kind (for a map, the map's VALUE
// kind). It is checked against the transformer's Accepts() before Transform
// ever runs: a schema that pairs a transformer with a field type it was
// never built for (e.g. keep_last on an int64) must not reach Transform and
// panic on the resulting type mismatch when the caller does t.Set(fd, out) —
// it must omit instead. The build-time lint that would catch this mismatch
// earlier does not exist yet, so this check is defence in depth in the
// serving path itself.
//
// Every failure path omits: no transformer, unknown name, a kind mismatch,
// or a transformer that errors. Returning the original value on failure
// would be the single worst bug this package could have.
func Apply(ctx Ctx, r *toolv1.Redaction, kind protoreflect.Kind, v protoreflect.Value) (protoreflect.Value, bool) {
	t, ok := For(r)
	if !ok {
		return protoreflect.Value{}, true
	}
	if !acceptsKind(t, kind) {
		return protoreflect.Value{}, true
	}
	out, err := t.Transform(ctx, v)
	if err != nil {
		return protoreflect.Value{}, true
	}
	return out, false
}

func acceptsKind(t Transformer, kind protoreflect.Kind) bool {
	for _, k := range t.Accepts() {
		if k == kind {
			return true
		}
	}
	return false
}
