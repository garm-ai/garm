package redact_test

import (
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy/redact"
)

func TestFormatTransformers(t *testing.T) {
	keepLast4 := &toolv1.Redaction{Kind: &toolv1.Redaction_KeepLast{
		KeepLast: &toolv1.KeepLast{N: 4, Runes: true, Reason: "verification"}}}

	cases := []struct {
		name string
		r    *toolv1.Redaction
		in   string
		want string
		omit bool
	}{
		{"email keeps domain",
			&toolv1.Redaction{Kind: &toolv1.Redaction_EmailDomain{
				EmailDomain: &toolv1.EmailDomain{}}},
			"ada@corp.com", "***@corp.com", false},
		{"email without @ omits",
			&toolv1.Redaction{Kind: &toolv1.Redaction_EmailDomain{
				EmailDomain: &toolv1.EmailDomain{}}},
			"not-an-email", "", true},
		{"keep last four", keepLast4, "+442071234567", "*********4567", false},
		// Shorter than N must not reveal the whole value.
		{"keep last four on short input", keepLast4, "12", "**", false},
		{"keep last four on empty", keepLast4, "", "", false},
		// Runes, not bytes: slicing bytes would split a character. Ten runes in,
		// keep the last four, so six stars then 語テスト.
		{"keep last four counts runes", keepLast4, "aaaa日本語テスト", "******語テスト", false},
		{"ip to /24",
			&toolv1.Redaction{Kind: &toolv1.Redaction_IpPrefix{
				IpPrefix: &toolv1.IpPrefix{Bits: 24}}},
			"10.1.2.77", "10.1.2.0", false},
		{"ip malformed omits",
			&toolv1.Redaction{Kind: &toolv1.Redaction_IpPrefix{
				IpPrefix: &toolv1.IpPrefix{Bits: 24}}},
			"not-an-ip", "", true},
		{"url keeps origin",
			&toolv1.Redaction{Kind: &toolv1.Redaction_UrlOrigin{
				UrlOrigin: &toolv1.UrlOrigin{}}},
			"https://x.com/a/b?token=secret", "https://x.com", false},
		{"card keeps bin and last four",
			&toolv1.Redaction{Kind: &toolv1.Redaction_CardBinLast4{
				CardBinLast4: &toolv1.CardBinLast4{}}},
			"4111111111111111", "411111******1111", false},
		{"card too short omits",
			&toolv1.Redaction{Kind: &toolv1.Redaction_CardBinLast4{
				CardBinLast4: &toolv1.CardBinLast4{}}},
			"4111", "", true},
	}

	for _, c := range cases {
		got, omit := redact.Apply(redact.Ctx{}, c.r, protoreflect.StringKind, str(c.in))
		if omit != c.omit {
			t.Fatalf("%s: omit = %v, want %v", c.name, omit, c.omit)
		}
		if !omit && got.String() != c.want {
			t.Fatalf("%s: = %q, want %q", c.name, got.String(), c.want)
		}
	}
}

func TestTruncateTransformer(t *testing.T) {
	// 'a' (1 byte) + three 3-byte CJK runes = 10 bytes total.
	multibyte := "a日本語"
	runesInput := "aaaa日本語テスト"

	byteMode := func(n uint32) *toolv1.Redaction {
		return &toolv1.Redaction{Kind: &toolv1.Redaction_Truncate{
			Truncate: &toolv1.Truncate{N: n}}}
	}
	runeMode := func(n uint32) *toolv1.Redaction {
		return &toolv1.Redaction{Kind: &toolv1.Redaction_Truncate{
			Truncate: &toolv1.Truncate{N: n, Runes: true}}}
	}

	t.Run("byte mode backs a mid-rune cut off to a valid boundary", func(t *testing.T) {
		// n=2 lands inside the first CJK rune's 3-byte encoding, forcing the
		// backoff loop to run at least once.
		got, omit := redact.Apply(redact.Ctx{}, byteMode(2), protoreflect.StringKind, str(multibyte))
		if omit {
			t.Fatalf("expected a value, got omit")
		}
		if !utf8.ValidString(got.String()) {
			t.Fatalf("truncate produced invalid UTF-8: %q", got.String())
		}
		if len(got.String()) >= len(multibyte) {
			t.Fatalf("truncate did not shorten input: %q", got.String())
		}
	})

	t.Run("rune mode truncates by rune count", func(t *testing.T) {
		got, omit := redact.Apply(redact.Ctx{}, runeMode(4), protoreflect.StringKind, str(runesInput))
		want := "aaaa…"
		if omit || got.String() != want {
			t.Fatalf("= %q omit=%v, want %q", got.String(), omit, want)
		}
	})

	t.Run("input no longer than n is returned unchanged", func(t *testing.T) {
		got, omit := redact.Apply(redact.Ctx{}, runeMode(5), protoreflect.StringKind, str("ab"))
		if omit || got.String() != "ab" {
			t.Fatalf("= %q omit=%v, want unchanged %q", got.String(), omit, "ab")
		}
	})

	t.Run("n zero omits", func(t *testing.T) {
		_, omit := redact.Apply(redact.Ctx{}, byteMode(0), protoreflect.StringKind, str("anything"))
		if !omit {
			t.Fatal("expected omit for n=0")
		}
	})
}

func TestDateGrainTransformer(t *testing.T) {
	ts := time.Date(2024, time.March, 17, 13, 45, 30, 0, time.UTC)
	tsVal := protoreflect.ValueOfMessage(timestamppb.New(ts).ProtoReflect())

	grainRedaction := func(g toolv1.Grain) *toolv1.Redaction {
		return &toolv1.Redaction{Kind: &toolv1.Redaction_DateGrain{
			DateGrain: &toolv1.DateGrain{Grain: g}}}
	}

	cases := []struct {
		name  string
		grain toolv1.Grain
		want  time.Time
	}{
		{"year zeroes month, day, and time", toolv1.Grain_GRAIN_YEAR,
			time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"month zeroes day and time", toolv1.Grain_GRAIN_MONTH,
			time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)},
		{"day zeroes time", toolv1.Grain_GRAIN_DAY,
			time.Date(2024, time.March, 17, 0, 0, 0, 0, time.UTC)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, omit := redact.Apply(redact.Ctx{}, grainRedaction(c.grain), protoreflect.MessageKind, tsVal)
			if omit {
				t.Fatalf("expected a value, got omit")
			}
			outMsg, ok := got.Message().Interface().(*timestamppb.Timestamp)
			if !ok {
				t.Fatalf("result is not a Timestamp: %v", got)
			}
			if !outMsg.AsTime().Equal(c.want) {
				t.Fatalf("= %v, want %v", outMsg.AsTime(), c.want)
			}
		})
	}

	t.Run("unspecified grain omits", func(t *testing.T) {
		_, omit := redact.Apply(redact.Ctx{}, grainRedaction(toolv1.Grain_GRAIN_UNSPECIFIED), protoreflect.MessageKind, tsVal)
		if !omit {
			t.Fatal("expected omit for unspecified grain")
		}
	})

	t.Run("non-timestamp message omits", func(t *testing.T) {
		notATimestamp := protoreflect.ValueOfMessage((&toolv1.EmailDomain{}).ProtoReflect())
		_, omit := redact.Apply(redact.Ctx{}, grainRedaction(toolv1.Grain_GRAIN_DAY), protoreflect.MessageKind, notATimestamp)
		if !omit {
			t.Fatal("expected omit for non-Timestamp message")
		}
	})
}
