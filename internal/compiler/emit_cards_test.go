package compiler_test

import (
	"regexp"
	"strings"
	"testing"
)

// The Go package ALIAS a generated file gives garm.card.v1 is protogen's to
// choose, and the test harness picks a different one from a real build. So
// every assertion here matches the signature with the alias left open: what
// is under test is which method is emitted with which argument, not how the
// import happened to be named.
func cardSig(method, req string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(method) +
		`\(context\.Context, \*` + req + `\) \(\*\w+\.Card, error\)`)
}

// Every tool in the demo package gets its cards emitted, under the names
// contracts/cards derives — the same function the catalogue builder calls,
// which is the only reason the daemon and the service agree about what a card
// is called.
func TestMicroEmitsACardPerToolAndTheOverrideInterface(t *testing.T) {
	src := renderMicro(t)

	for _, want := range []*regexp.Regexp{
		cardSig("GetAccountSummaryInputCard", `emptypb\.Empty`),
		cardSig("GetAccountSummaryResultCard", `\w+\.CallRef`),
		cardSig("SearchTransactionsInputCard", `emptypb\.Empty`),
	} {
		if !want.MatchString(src) {
			t.Errorf("generated binding is missing a method matching:\n  %s", want)
		}
	}
	for _, want := range []string{
		"type AccountsServiceCards interface {",
		"func DefaultGetAccountSummaryInputCard(",
		"func DefaultGetAccountSummaryResultCard(",
		"func GetAccountSummaryResultCardFrom(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated binding is missing:\n  %s", want)
		}
	}
}

// The override wins where the handler has one, and the default is registered
// where it does not — per method, so overriding one card leaves the rest
// generated.
func TestServeRegistersAnOverridePerCardAndTheDefaultForTheRest(t *testing.T) {
	src := renderMicro(t)

	// The assertion is on the card's OWN signature, not on the aggregate
	// interface: asserting AccountsServiceCards would make overriding one
	// card mean implementing all of them.
	want := regexp.MustCompile(`h\.\(interface \{\s*` +
		`GetAccountSummaryInputCard\(context\.Context, \*emptypb\.Empty\) \(\*\w+\.Card, error\)\s*` +
		`\}\); ok \{`)
	if !want.MatchString(src) {
		t.Errorf("Serve does not assert one card's own signature; generated:\n%s",
			excerpt(src, "if o, ok := h."))
	}
	if !strings.Contains(src, "return DefaultGetAccountSummaryInputCard(ctx, in)") {
		t.Error("Serve does not fall back to the generated default")
	}
}

// A card is registered exactly as a tool is, because it IS one — the daemon
// routes to it through the same steps at the same clearance. A card that
// registered some other way would be a card outside the chain.
func TestACardIsRegisteredLikeAnyTool(t *testing.T) {
	src := strings.ReplaceAll(renderMicro(t), " ", "")
	for _, want := range []string{
		`FQN:"garm.demo.v1beta1.get_account_summary_input_card"`,
		`Subject:"garm.demo.v1beta1.AccountsService.GetAccountSummaryInputCard"`,
		`Method:"GetAccountSummaryInputCard"`,
	} {
		if !strings.Contains(src, strings.ReplaceAll(want, " ", "")) {
			t.Errorf("a card's endpoint is not registered with:\n  %s", want)
		}
	}
}

// The result card answers result_unavailable rather than an empty card. An
// empty card is one a person reads as "nothing happened".
func TestTheGeneratedResultCardSaysItHasNoAnswerYet(t *testing.T) {
	src := renderMicro(t)
	if !strings.Contains(src, "return nil, cards.ErrResultUnavailable") {
		t.Error("the generated result card does not answer result_unavailable")
	}
}

func excerpt(src, around string) string {
	i := strings.Index(src, around)
	if i < 0 {
		return "(not found)"
	}
	end := i + 400
	if end > len(src) {
		end = len(src)
	}
	return src[i:end]
}
