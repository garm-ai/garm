package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

const defaultMask = "[REDACTED]"

var stringOnly = []protoreflect.Kind{protoreflect.StringKind}

type maskWith struct{ with string }

func (maskWith) Name() string                 { return "mask" }
func (maskWith) Accepts() []protoreflect.Kind { return stringOnly }
func (m maskWith) Transform(_ Ctx, _ protoreflect.Value) (protoreflect.Value, error) {
	if m.with == "" {
		return protoreflect.ValueOfString(defaultMask), nil
	}
	return protoreflect.ValueOfString(m.with), nil
}

type hasher struct{ perTenant bool }

func (hasher) Name() string                 { return "hash" }
func (hasher) Accepts() []protoreflect.Kind { return stringOnly }

// Transform produces a stable pseudonym. Determinism is the point: redacted
// values must still join across ledger rows, which is the only reason to choose
// hash over mask.
func (h hasher) Transform(ctx Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	if len(ctx.Key) == 0 {
		return protoreflect.Value{}, fmt.Errorf("hash: no key material")
	}
	key := ctx.Key
	if h.perTenant {
		if ctx.Tenant == "" {
			return protoreflect.Value{}, fmt.Errorf("hash: per-tenant salt with no tenant")
		}
		d := hmac.New(sha256.New, ctx.Key)
		d.Write([]byte(ctx.Tenant))
		key = d.Sum(nil)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(v.String()))
	return protoreflect.ValueOfString(hex.EncodeToString(m.Sum(nil)[:16])), nil
}
