package compiler_test

import (
	"fmt"
	"strings"
	"testing"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"

	"github.com/garm-ai/garm/internal/compiler"
)

// Task 3 (standing-grants plan A): A12-A15, the consent template a bank's
// agent may publish. A template is the thing a principal is asked to agree
// to — a scope of tools, a sentence they can read, the limits a grant minted
// from it will carry — and an unbounded one cannot be meaningfully agreed
// to, an unreadable one cannot be agreed to at all. Both fail here, at
// publish, rather than at 3am when a run tries to exercise a grant nobody
// could really have consented to.
//
// consentSpec and its option functions build the smallest fixture each test
// needs: a bank.v1.SupportAssistant agent (react mode, the same shape
// agents_test.go's fullPolicy fixture uses) carrying one consent block, plus
// one small proto package per tool its scope or allowlist names — generated
// on demand, by name, rather than kept as static fixtures, because the
// verb (read vs. write) each test needs is a property of the test, not of
// the tool.

// consentSpec is what a consent-template test varies. The zero value plus
// agentWithConsent's defaults describes a template that lints clean: a
// read-only scope, a real description, and no external-event trigger — so
// each test need only set the one thing it is testing.
type consentSpec struct {
	scope        []string
	allowlist    []string
	allowlistSet bool
	writable     bool
	limits       *agentv1.Limits
	description  string
	triggers     []agentv1.TriggerKind
}

type consentOpt func(*consentSpec)

// agentWithConsent assembles a consentSpec from the given options, over
// defaults that lint clean on their own: every test below changes exactly
// the one thing its name says and nothing else.
func agentWithConsent(opts ...consentOpt) consentSpec {
	spec := consentSpec{
		description: "Read the account balance on the customer's behalf.",
		triggers:    []agentv1.TriggerKind{agentv1.TriggerKind_TRIGGER_KIND_PRINCIPAL},
	}
	for _, o := range opts {
		o(&spec)
	}
	return spec
}

func scope(fqns ...string) consentOpt { return func(s *consentSpec) { s.scope = fqns } }

func allowlist(fqns ...string) consentOpt {
	return func(s *consentSpec) { s.allowlist = fqns; s.allowlistSet = true }
}

func writable(w bool) consentOpt { return func(s *consentSpec) { s.writable = w } }

func limits(l *agentv1.Limits) consentOpt { return func(s *consentSpec) { s.limits = l } }

func description(d string) consentOpt { return func(s *consentSpec) { s.description = d } }

func triggers(t ...agentv1.TriggerKind) consentOpt {
	return func(s *consentSpec) { s.triggers = t }
}

// lintErr is lintAgent's non-nil return: every error-level diagnostic the
// fixture produced, kept structured rather than flattened to a string so
// wantLintError can reuse hasError's exact rule+substring match instead of a
// second, looser one.
type lintErr struct{ diags []compiler.Diag }

func (e *lintErr) Error() string { return render(e.diags) }

// lintAgent builds the fixture agentWithConsent describes, compiles it, and
// lints the whole tree (never PartialSet — A13's caveat check is
// catalogue-scoped, like A3 and A4, and this package's own tests lint the
// whole tree the way `garm lint` does). nil means the template lints clean;
// otherwise the error carries every ERROR-level diagnostic (warnings are not
// failures here, same as everywhere else lint is asserted against in this
// package).
func lintAgent(t *testing.T, spec consentSpec) error {
	t.Helper()
	files := consentFixture(spec)
	fds := compileSource(t, files)
	var errs []compiler.Diag
	for _, d := range compiler.LintWith(fds, compiler.Options{}) {
		if !d.Warn {
			errs = append(errs, d)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &lintErr{diags: errs}
}

// wantLintError asserts err is a lintAgent failure carrying a diagnostic for
// rule naming contains in its message — hasError's own rule+substring match,
// reused rather than duplicated (see lint_material_test.go).
func wantLintError(t *testing.T, err error, rule, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("lint accepted a template that should have failed %s (%q)", rule, contains)
	}
	le, ok := err.(*lintErr)
	if !ok {
		t.Fatalf("unexpected error type %T: %v", err, err)
	}
	if !hasError(le.diags, rule, contains) {
		t.Errorf("diagnostics do not contain %s %q:\n%s", rule, contains, render(le.diags))
	}
}

// consentFixture builds the proto tree agentWithConsent's spec describes:
// the agent file (bank/v1/agent.proto, reusing agentSrc's taxonomy.proto
// too) plus one small file per proto package consent.scope or the
// allowlist names, each a single-method service whose verb is spec.writable
// and whose request field matches what that tool's short name suggests a
// caveat would actually read.
func consentFixture(spec consentSpec) map[string]string {
	allow := spec.allowlist
	if !spec.allowlistSet {
		allow = spec.scope
	}
	files := agentSrc(consentPolicyBody(spec, allow))
	for rel, body := range consentToolProtos(append(append([]string{}, spec.scope...), allow...), spec.writable) {
		files[rel] = body
	}
	return files
}

// consentPolicyBody is the agent option body: the same shape fullPolicy
// (agents_test.go) uses, with `tools` set to the effective allowlist and a
// `consent` block built from spec.
func consentPolicyBody(spec consentSpec, allow []string) string {
	var toolRefs []string
	for _, fqn := range allow {
		toolRefs = append(toolRefs, fmt.Sprintf("{ fqn: %q }", fqn))
	}
	var scopeQ []string
	for _, fqn := range spec.scope {
		scopeQ = append(scopeQ, fmt.Sprintf("%q", fqn))
	}
	var trig []string
	for _, tr := range spec.triggers {
		trig = append(trig, tr.String())
	}
	// Omitted entirely (not `default_limits: {}`) when spec.limits is nil, so
	// consent.GetDefaultLimits() comes back nil — "no limits at all" — rather
	// than a declared-but-empty Limits, which would read the same to A13
	// either way but should arrive here the way the test asked for it.
	limitsText := ""
	if spec.limits != nil {
		limitsText = "default_limits: { " + limitsBody(spec.limits) + " }"
	}
	return fmt.Sprintf(`
    mode: MODE_REACT
    principal: { clearance: CLEARANCE_CONFIDENTIAL compartments: ["financial"] }
    model: { alias: "fast" }
    bounds: { max_steps: 12 max_tokens: 200000 max_tool_calls: 30 timeout: { seconds: 600 } }
    prompts: { key: "system" value: { path: "prompts/support.md" sha256: "`+emptySHA+`" } }
    tools: [%s]
    consent: {
      scope: [%s]
      description: %q
      default_period_days: 30
      %s
      triggers: [%s]
    }
`, strings.Join(toolRefs, ", "), strings.Join(scopeQ, ", "), spec.description,
		limitsText, strings.Join(trig, ", "))
}

func limitsBody(l *agentv1.Limits) string {
	var parts []string
	if l.GetMaxRuns() > 0 {
		parts = append(parts, fmt.Sprintf("max_runs: %d", l.GetMaxRuns()))
	}
	for _, c := range l.GetCaveats() {
		parts = append(parts, fmt.Sprintf("caveats: %q", c))
	}
	for k, v := range l.GetCumulative() {
		parts = append(parts, fmt.Sprintf("cumulative: { key: %q value: %d }", k, v))
	}
	return strings.Join(parts, " ")
}

// consentToolProtos builds one proto file per distinct package among fqns,
// each a single-method service: one rpc per tool short name in that
// package, verb VERB_WRITE when writable else VERB_READ, min_clearance
// INTERNAL (below the fixture agent's own CONFIDENTIAL principal, so a
// scoped-but-unlisted-elsewhere check never trips on clearance by accident).
func consentToolProtos(fqns []string, writable bool) map[string]string {
	verb := "VERB_READ"
	if writable {
		verb = "VERB_WRITE"
	}
	byPkg := map[string][]string{}
	var pkgOrder []string
	seen := map[string]bool{}
	for _, fqn := range fqns {
		if fqn == "" || seen[fqn] {
			continue
		}
		seen[fqn] = true
		pkg, name := splitFQN(fqn)
		if _, ok := byPkg[pkg]; !ok {
			pkgOrder = append(pkgOrder, pkg)
		}
		byPkg[pkg] = append(byPkg[pkg], name)
	}
	files := map[string]string{}
	for _, pkg := range pkgOrder {
		files[strings.ReplaceAll(pkg, ".", "/")+"/fixture.proto"] = consentPkgProto(pkg, byPkg[pkg], verb)
	}
	return files
}

func splitFQN(fqn string) (pkg, name string) {
	i := strings.LastIndex(fqn, ".")
	return fqn[:i], fqn[i+1:]
}

func consentPkgProto(pkg string, names []string, verb string) string {
	var b strings.Builder
	alias := strings.ReplaceAll(pkg, ".", "") + "pb"
	fmt.Fprintf(&b, "syntax = \"proto3\";\npackage %s;\nimport \"garm/tool/v1/tool.proto\";\n"+
		"option go_package = \"example.com/%s;%s\";\n",
		pkg, strings.ReplaceAll(pkg, ".", "/"), alias)
	for _, name := range names {
		p := toPascal(name)
		fmt.Fprintf(&b, `message %sReq {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  %s
}
message %sResp {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string status = 1;
}
`, p, requestFieldFor(name), p)
	}
	b.WriteString("service Svc {\n")
	for _, name := range names {
		p := toPascal(name)
		fmt.Fprintf(&b, `  rpc %s(%sReq) returns (%sResp) {
    option (garm.tool.v1.tool) = {
      name: %q title: %q description: %q
      verb: %s min_clearance: CLEARANCE_INTERNAL
    };
  }
`, p, p, p, name, p, "Fixture tool "+name+".", verb)
	}
	b.WriteString("}\n")
	return b.String()
}

// requestFieldFor gives each fixture tool the one field its own test needs:
// "to" (a string a caveat can call endsWith on) for mail.v1.send,
// "amount_minor_units" (an int, so the review-focus caveat tests have a real
// non-boolean field to misuse) for payments.v1.initiate_payment, and a
// generic "account_id" otherwise.
func requestFieldFor(name string) string {
	switch name {
	case "send":
		return "optional string to = 1;"
	case "initiate_payment":
		return "optional int64 amount_minor_units = 1;"
	default:
		return "optional string account_id = 1;"
	}
}

func toPascal(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

func TestA12RefusesAConsentScopeOutsideTheAllowlist(t *testing.T) {
	// A template may not ask for a tool the agent cannot call. Without this
	// a principal could consent to something that would never work, which
	// reads to them as the platform losing their permission.
	err := lintAgent(t, agentWithConsent(scope("mail.v1.send"), allowlist("accounts.v1.get_balance")))
	wantLintError(t, err, "A12", "consent scope")
}

func TestA13RefusesAWritableScopeWithNoLimits(t *testing.T) {
	// An unbounded standing authority over a WRITE tool is the thing F5 said
	// nothing bounded. At least one of max_runs, a caveat, or a cumulative
	// ceiling.
	err := lintAgent(t, agentWithConsent(scope("mail.v1.send"), writable(true), limits(nil)))
	wantLintError(t, err, "A13", "max_runs")
}

func TestA13AcceptsAWritableScopeBoundedByACaveatAlone(t *testing.T) {
	// Any ONE of the three bounds is enough. A caveat is a real bound and
	// must not be refused for not being a count.
	err := lintAgent(t, agentWithConsent(scope("mail.v1.send"), writable(true),
		limits(&agentv1.Limits{Caveats: []string{"args.to.endsWith('@example.com')"}})))
	if err != nil {
		t.Errorf("a caveat bounds a scope; lint refused it: %v", err)
	}
}

func TestA14RefusesAnEmptyDescription(t *testing.T) {
	// A consent nobody can read is not consent.
	err := lintAgent(t, agentWithConsent(scope("accounts.v1.get_balance"), description("")))
	wantLintError(t, err, "A14", "description")
}

func TestA15RefusesExternalEventTriggers(t *testing.T) {
	// An external event is attacker-controlled; a schedule is not. Refused
	// until the deployment declares A3's rate-budget machinery, so that
	// "e.g. mail received" cannot arrive by accident.
	err := lintAgent(t, agentWithConsent(scope("accounts.v1.get_balance"),
		triggers(agentv1.TriggerKind_TRIGGER_KIND_EXTERNAL_EVENT)))
	wantLintError(t, err, "A15", "EXTERNAL_EVENT")
}

func TestEveryConsentCaveatIsTypeCheckedAgainstItsToolsRequest(t *testing.T) {
	// REVIEW FOCUS 2. `args.amount_minor_units` type-checks as an int and
	// would be truthy in a looser language. celenv.Check refuses a
	// non-boolean, and the refusal must happen here rather than at 3am.
	err := lintAgent(t, agentWithConsent(scope("payments.v1.initiate_payment"), writable(true),
		limits(&agentv1.Limits{Caveats: []string{"args.amount_minor_units"}})))
	wantLintError(t, err, "A13", "not a predicate")
}

func TestAConsentCaveatNamingAFieldTheToolDoesNotHaveIsRefused(t *testing.T) {
	// The same descriptor-aware lint that makes an allowlist guard refuse a
	// misspelled field. This is the half Cedar could not give us.
	err := lintAgent(t, agentWithConsent(scope("payments.v1.initiate_payment"), writable(true),
		limits(&agentv1.Limits{Caveats: []string{"args.amount_minor_unit <= 1"}})))
	wantLintError(t, err, "A13", "amount_minor_unit")
}
