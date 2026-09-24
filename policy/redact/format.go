package redact

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

func init() {
	Register(emailDomain{})
	Register(cardBinLast4{})
	Register(urlOrigin{})
}

type emailDomain struct{}

func (emailDomain) Name() string                 { return "email_domain" }
func (emailDomain) Accepts() []protoreflect.Kind { return stringOnly }
func (emailDomain) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	s := v.String()
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return protoreflect.Value{}, fmt.Errorf("email_domain: no local@domain split")
	}
	return protoreflect.ValueOfString("***" + s[at:]), nil
}

type keepLast struct {
	n     int
	runes bool
}

func (keepLast) Name() string                 { return "keep_last" }
func (keepLast) Accepts() []protoreflect.Kind { return stringOnly }

// Transform keeps the final n characters and stars the rest. When the input is
// no longer than n the entire value is starred: revealing a short value whole
// would defeat the redaction exactly when it matters most.
func (k keepLast) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	if k.n == 0 {
		return protoreflect.Value{}, fmt.Errorf("keep_last: n is zero")
	}
	s := v.String()
	if k.runes {
		rs := []rune(s)
		if len(rs) <= k.n {
			return protoreflect.ValueOfString(strings.Repeat("*", len(rs))), nil
		}
		return protoreflect.ValueOfString(
			strings.Repeat("*", len(rs)-k.n) + string(rs[len(rs)-k.n:])), nil
	}
	if len(s) <= k.n {
		return protoreflect.ValueOfString(strings.Repeat("*", len(s))), nil
	}
	return protoreflect.ValueOfString(
		strings.Repeat("*", len(s)-k.n) + s[len(s)-k.n:]), nil
}

type truncate struct {
	n     int
	runes bool
}

func (truncate) Name() string                 { return "truncate" }
func (truncate) Accepts() []protoreflect.Kind { return stringOnly }
func (t truncate) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	if t.n == 0 {
		return protoreflect.Value{}, fmt.Errorf("truncate: n is zero")
	}
	s := v.String()
	if t.runes {
		rs := []rune(s)
		if len(rs) <= t.n {
			return protoreflect.ValueOfString(s), nil
		}
		return protoreflect.ValueOfString(string(rs[:t.n]) + "…"), nil
	}
	if len(s) <= t.n {
		return protoreflect.ValueOfString(s), nil
	}
	// Back off to a rune boundary so truncation never emits invalid UTF-8.
	cut := t.n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return protoreflect.ValueOfString(s[:cut] + "…"), nil
}

type cardBinLast4 struct{}

func (cardBinLast4) Name() string                 { return "card_bin_last4" }
func (cardBinLast4) Accepts() []protoreflect.Kind { return stringOnly }
func (cardBinLast4) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	s := v.String()
	if len(s) < 12 {
		return protoreflect.Value{}, fmt.Errorf("card_bin_last4: too short")
	}
	return protoreflect.ValueOfString(
		s[:6] + strings.Repeat("*", len(s)-10) + s[len(s)-4:]), nil
}

type ipPrefix struct{ bits int }

func (ipPrefix) Name() string                 { return "ip_prefix" }
func (ipPrefix) Accepts() []protoreflect.Kind { return stringOnly }
func (p ipPrefix) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	addr, err := netip.ParseAddr(v.String())
	if err != nil {
		return protoreflect.Value{}, fmt.Errorf("ip_prefix: %w", err)
	}
	pfx, err := addr.Prefix(p.bits)
	if err != nil {
		return protoreflect.Value{}, fmt.Errorf("ip_prefix: %w", err)
	}
	return protoreflect.ValueOfString(pfx.Addr().String()), nil
}

type urlOrigin struct{}

func (urlOrigin) Name() string                 { return "url_origin" }
func (urlOrigin) Accepts() []protoreflect.Kind { return stringOnly }
func (urlOrigin) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	u, err := url.Parse(v.String())
	if err != nil || u.Scheme == "" || u.Host == "" {
		return protoreflect.Value{}, fmt.Errorf("url_origin: not an absolute URL")
	}
	return protoreflect.ValueOfString(u.Scheme + "://" + u.Host), nil
}

type dateGrain struct{ grain toolv1.Grain }

func (dateGrain) Name() string { return "date_grain" }
func (dateGrain) Accepts() []protoreflect.Kind {
	return []protoreflect.Kind{protoreflect.MessageKind}
}

// Transform coarsens a google.protobuf.Timestamp. It operates on the message
// value rather than a string because that is how dates appear in these schemas.
func (d dateGrain) Transform(_ Ctx, v protoreflect.Value) (protoreflect.Value, error) {
	msg, ok := v.Message().Interface().(*timestamppb.Timestamp)
	if !ok {
		return protoreflect.Value{}, fmt.Errorf("date_grain: not a Timestamp")
	}
	t := msg.AsTime()
	var out time.Time
	switch d.grain {
	case toolv1.Grain_GRAIN_YEAR:
		out = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	case toolv1.Grain_GRAIN_MONTH:
		out = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	case toolv1.Grain_GRAIN_DAY:
		out = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	default:
		return protoreflect.Value{}, fmt.Errorf("date_grain: unspecified grain")
	}
	return protoreflect.ValueOfMessage(timestamppb.New(out).ProtoReflect()), nil
}
