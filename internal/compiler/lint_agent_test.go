package compiler_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/garm/internal/compiler"
)

// hasDiag is hasError plus a path assertion. Rule and severity alone do not
// say WHICH service or method a diagnostic is about, and a catalogue built
// from many trees needs that answered from the diagnostic itself — so an A1
// test pins the exact path, not just the rule id and message substring.
func hasDiag(ds []compiler.Diag, rule, path, contains string) bool {
	for _, d := range ds {
		if d.Rule == rule && !d.Warn && d.Path == path && strings.Contains(d.Msg, contains) {
			return true
		}
	}
	return false
}

// A1 — the governed door is exactly two RPCs. A third method on an agent
// service is a tool the runner does not know how to serve and garmd would
// mount anyway: a governed call that reaches nothing.
func TestA1RefusesAThirdMethod(t *testing.T) {
	files := agentSrc(fullPolicy)
	// The anchor is the service's closing brace, which is the only place two
	// closing braces sit on consecutive lines in this fixture.
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		"  }\n}\n", `  }
  rpc Cancel(garm.agent.v1.RunRef) returns (Ask) {
    option (garm.tool.v1.tool) = {
      name: "support_cancel" title: "Cancel" description: "Cancel a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
  }
}
`, 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant.Cancel", "is a third method") {
		t.Errorf("a third method was accepted:\n%s", render(diags))
	}
}

func TestA1RefusesAnAgentWithNoInvoke(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		"rpc Invoke(Ask)", "rpc Start(Ask)", 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant", "must declare exactly one rpc named Invoke") {
		t.Errorf("an agent with no Invoke was accepted:\n%s", render(diags))
	}
	// Exactly two A1 diagnostics, not three: the missing-Invoke error on the
	// service, and one saying Start is neither Invoke nor GetRun — never "a
	// third method", which would be factually wrong here. There are only two
	// RPCs on this service (Start, GetRun); Start is not a third anything.
	var got []compiler.Diag
	for _, d := range diags {
		if d.Rule == "A1" {
			got = append(got, d)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d A1 diagnostics, want 2:\n%s", len(got), render(got))
	}
	if !hasDiag(diags, "A1", "bank.v1.SupportAssistant.Start", "is neither Invoke nor GetRun") {
		t.Errorf("Start should be reported as neither Invoke nor GetRun, not as a third method:\n%s", render(diags))
	}
	if hasDiag(diags, "A1", "bank.v1.SupportAssistant.Start", "is a third method") {
		t.Errorf("Start was reported as a third method, but the service only has two RPCs:\n%s", render(diags))
	}
}

func TestA1RefusesTheWrongMessagesOnEitherDoor(t *testing.T) {
	for _, tc := range []struct{ name, from, to, path, want string }{
		{"Invoke returning something else",
			"rpc Invoke(Ask) returns (garm.agent.v1.RunRef)",
			"rpc Invoke(Ask) returns (Ask)",
			"bank.v1.SupportAssistant.Invoke",
			"Invoke must return garm.agent.v1.RunRef"},
		{"GetRun taking something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(Ask) returns (garm.agent.v1.RunStatus)",
			"bank.v1.SupportAssistant.GetRun",
			"GetRun must take garm.agent.v1.RunRef"},
		{"GetRun returning something else",
			"rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus)",
			"rpc GetRun(garm.agent.v1.RunRef) returns (Ask)",
			"bank.v1.SupportAssistant.GetRun",
			"GetRun must return garm.agent.v1.RunStatus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := agentSrc(fullPolicy)
			files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"], tc.from, tc.to, 1)
			diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
			if !hasDiag(diags, "A1", tc.path, tc.want) {
				t.Errorf("accepted:\n%s", render(diags))
			}
		})
	}
}

// An agent that declares no GetRun is legal: design §2.2 says "at most one".
func TestA1AcceptsAnAgentWithNoGetRun(t *testing.T) {
	files := agentSrc(fullPolicy)
	src := files["bank/v1/agent.proto"]
	files["bank/v1/agent.proto"] = src[:strings.Index(src, "  rpc GetRun")] + "}\n"
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A1" && !d.Warn {
			t.Errorf("an agent with no GetRun was refused: %s", d.String())
		}
	}
}

// And the well-formed one produces no A1 at all.
func TestA1AcceptsTheWellFormedAgent(t *testing.T) {
	for _, d := range compiler.LintWith(compileSource(t, agentSrc(fullPolicy)), compiler.Options{}) {
		if d.Rule == "A1" {
			t.Errorf("the well-formed agent produced an A1: %s", d.String())
		}
	}
}

// a2Tree writes a fixture whose prompts root is the temp tree itself, so the
// declared path "prompts/system.md" resolves the way it does in a real
// checkout: proto/ and prompts/ as siblings.
func a2Tree(t *testing.T, promptPath, sha, body string) (fds []protoreflect.FileDescriptor, root string) {
	t.Helper()
	root = t.TempDir()
	if body != "" {
		p := filepath.Join(root, "prompts", "system.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	policy := strings.Replace(fullPolicy,
		`prompts: { key: "system" value: { path: "prompts/support.md" sha256: "`+emptySHA+`" } }`,
		`prompts: { key: "system" value: { path: "`+promptPath+`" sha256: "`+sha+`" } }`, 1)
	return compileSource(t, agentSrc(policy)), root
}

const helloPrompt = "You are a support assistant. Answer from the tools you are given.\n"
const helloSHA = "d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a"

func TestA2AcceptsAPromptThatExistsAndHashes(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", helloSHA, helloPrompt)
	for _, d := range compiler.LintWith(fds, compiler.Options{PromptsRoot: root}) {
		if d.Rule == "A2" {
			t.Errorf("a correct prompt produced an A2: %s", d.String())
		}
	}
}

func TestA2RefusesAPromptThatHashesDifferently(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", emptySHA, helloPrompt)
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
	if !hasError(diags, "A2", "but the declaration says") {
		t.Errorf("a drifted prompt was accepted:\n%s", render(diags))
	}
}

func TestA2RefusesAPromptThatIsNotThere(t *testing.T) {
	fds, root := a2Tree(t, "prompts/system.md", helloSHA, "")
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
	if !hasError(diags, "A2", "does not exist under the prompts root") {
		t.Errorf("a missing prompt was accepted:\n%s", render(diags))
	}
}

// Review Focus 3 — a declared path that leaves the tree. The linter must
// refuse the declaration rather than read, hash and bless whatever is there.
//
// Pinned with hasDiag's "is not usable" rather than hasError's looser
// "prompts root": that substring also appears in the missing-file message
// ("does not exist under the prompts root"), and none of these three targets
// actually exists where the escape would resolve to — so with the
// ValidatePromptPath guard deleted, the linter falls through to "the file is
// not there" (a genuine substring match on "prompts root") without ever
// exercising the escape refusal. Pinning "is not usable" fails closed against
// that: it is the wording only ValidatePromptPath's error produces.
func TestA2RefusesAPromptPathThatEscapesTheRoot(t *testing.T) {
	for _, p := range []string{"../secrets.md", "prompts/../../secrets.md", "/etc/passwd"} {
		fds, root := a2Tree(t, p, helloSHA, helloPrompt)
		diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
		if !hasDiag(diags, "A2", "bank.v1.SupportAssistant", "is not usable") {
			t.Errorf("the path %q was accepted:\n%s", p, render(diags))
		}
	}
}

// The mutation the substring above cannot catch on its own: an escape target
// that really exists, with a hash that really matches. If ValidatePromptPath
// were ever skipped, this is the case that would otherwise sail through as a
// correct prompt — the read would succeed and the digest would agree —
// rather than fail for the unrelated reason "the file is not there".
func TestA2RefusesAPromptPathThatEscapesTheRootEvenWhenTheTargetExistsAndMatches(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling of root, not under it: exactly what "../secrets.md" resolves
	// to once joined against root.
	if err := os.WriteFile(filepath.Join(parent, "secrets.md"), []byte(helloPrompt), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := strings.Replace(fullPolicy,
		`prompts: { key: "system" value: { path: "prompts/support.md" sha256: "`+emptySHA+`" } }`,
		`prompts: { key: "system" value: { path: "../secrets.md" sha256: "`+helloSHA+`" } }`, 1)
	diags := compiler.LintWith(compileSource(t, agentSrc(policy)), compiler.Options{PromptsRoot: root})
	if !hasDiag(diags, "A2", "bank.v1.SupportAssistant", "is not usable") {
		t.Errorf("a path escaping the root was accepted because the file it "+
			"escapes to happens to exist and hash correctly:\n%s", render(diags))
	}
}

// Item 3 (ruled in) — the escape check up to here is lexical, so it never
// sees a symlink: the declared path "prompts/system.md" never leaves the
// root as TEXT, and only escapes one level further, when that name on disk
// is a symlink resolving outside the root. Without resolving symlinks before
// the read, this would be followed, read and hashed like any other prompt.
func TestA2RefusesASymlinkThatEscapesTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs elevated privileges on windows")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "secrets.md")
	if err := os.WriteFile(outside, []byte(helloPrompt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "prompts", "system.md")); err != nil {
		t.Fatal(err)
	}
	policy := strings.Replace(fullPolicy,
		`prompts: { key: "system" value: { path: "prompts/support.md" sha256: "`+emptySHA+`" } }`,
		`prompts: { key: "system" value: { path: "prompts/system.md" sha256: "`+helloSHA+`" } }`, 1)
	diags := compiler.LintWith(compileSource(t, agentSrc(policy)), compiler.Options{PromptsRoot: root})
	if !hasDiag(diags, "A2", "bank.v1.SupportAssistant", "is not usable") {
		t.Errorf("a symlink escaping the root was accepted, and its target read and "+
			"hashed:\n%s", render(diags))
	}
}

// Review Focus 4 — the digest is lowercase hex with no prefix (program plan
// §3.1). An uppercase one never matches a computed digest and would also be a
// different object key on S3, so it is refused as malformed rather than
// reported as a mismatch.
func TestA2RefusesASha256ThatIsNotLowercaseHex(t *testing.T) {
	for _, tc := range []struct{ sha, want string }{
		{strings.ToUpper(helloSHA), "lowercase"},
		{"sha256:" + helloSHA, "no prefix"},
		{helloSHA[:32], "must be 64"},
	} {
		fds, root := a2Tree(t, "prompts/system.md", tc.sha, helloPrompt)
		diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: root})
		if !hasError(diags, "A2", tc.want) {
			t.Errorf("the sha256 %q was accepted:\n%s", tc.sha, render(diags))
		}
	}
}

// The "system" key is required (program plan §3.1). An agent with no system
// prompt has no instructions, and the runner would hand the model an empty
// system message rather than refusing.
func TestA2RequiresASystemPrompt(t *testing.T) {
	policy := strings.Replace(fullPolicy, `key: "system"`, `key: "critic"`, 1)
	fds := compileSource(t, agentSrc(policy))
	diags := compiler.LintWith(fds, compiler.Options{PromptsRoot: t.TempDir()})
	if !hasError(diags, "A2", `no "system" entry`) {
		t.Errorf("an agent with no system prompt was accepted:\n%s", render(diags))
	}
}

// With no prompts root there is nothing to resolve against. A2 says so, as a
// warning, rather than silently passing — a rule enforced only where nobody
// is looking is worse than no rule.
func TestA2WarnsWhenThereIsNoPromptsRoot(t *testing.T) {
	fds, _ := a2Tree(t, "prompts/system.md", helloSHA, helloPrompt)
	var warned bool
	for _, d := range compiler.LintWith(fds, compiler.Options{}) {
		if d.Rule == "A2" {
			if !d.Warn {
				t.Errorf("A2 is an error with no prompts root: %s", d.String())
			}
			warned = true
		}
	}
	if !warned {
		t.Error("A2 said nothing at all with no prompts root")
	}
}

// a3Src is the agent fixture plus a real tool in a second package, so the
// allowlist points at something that exists. minClearance and compartments
// are what the tests vary.
func a3Src(agentPolicy, toolMinClearance, toolCompartments string) map[string]string {
	files := agentSrc(agentPolicy)
	files["payments/v1/payments.proto"] = `syntax = "proto3";
package payments.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/payments/v1;paymentsv1";
message Pay {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 amount_minor_units = 1;
  optional string beneficiary_iban = 2;
}
message Paid {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string payment_id = 1;
}
service Payments {
  rpc InitiatePayment(Pay) returns (Paid) {
    option (garm.tool.v1.tool) = {
      name: "initiate_payment" title: "Pay" description: "Move money."
      verb: VERB_WRITE min_clearance: ` + toolMinClearance + `
      compartments: [` + toolCompartments + `]
    };
  }
}
`
	return files
}

func a3Policy(tools string) string {
	return strings.Replace(fullPolicy, `tools: [{ fqn: "bank.v1.support_assistant" }]`, tools, 1)
}

// Review Focus 1 (program plan §4 line 1) — the tool exists and sits above the
// agent's requested clearance. The agent could never call it, so listing it is
// a manifest that describes an agent nobody built.
func TestA3RefusesAToolAboveTheAgentsClearance(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.initiate_payment" }]`),
		"CLEARANCE_RESTRICTED", "")
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A3", "bank.v1.SupportAssistant", "the agent could never call it") {
		t.Errorf("a tool above the agent's clearance was accepted:\n%s", render(diags))
	}
}

// Same shape, one level down: CONFIDENTIAL principal, CONFIDENTIAL tool — the
// boundary, so a rule written with `>` rather than `>=` fails here rather than
// passing on a strictly-lower fixture.
//
// The tool also sits in `financial`, which the principal holds: without that,
// the loop over the tool's compartments never runs in any passing case and a
// rule that reported EVERY compartment as unheld would go unnoticed.
func TestA3AcceptsAToolAtOrBelowTheAgentsClearance(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.initiate_payment" }]`),
		"CLEARANCE_CONFIDENTIAL", `"financial"`)
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A3" {
			t.Errorf("a reachable tool produced an A3: %s", d.String())
		}
	}
}

// Compartments are a set, not a level: holding `financial` does not get you
// into `pii-contact`.
func TestA3RefusesAToolInACompartmentTheAgentDoesNotHold(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.initiate_payment" }]`),
		"CLEARANCE_INTERNAL", `"pii-contact"`)
	files["bank/v1/taxonomy.proto"] = strings.Replace(files["bank/v1/taxonomy.proto"],
		`{ name: "financial" description: "Money." }`,
		`{ name: "financial" description: "Money." }, { name: "pii-contact" description: "Contact details." }`, 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A3", "bank.v1.SupportAssistant", `compartment "pii-contact"`) {
		t.Errorf("a tool in an unheld compartment was accepted:\n%s", render(diags))
	}
}

func TestA3RefusesAToolThatIsNotInTheCatalogue(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.refund_payment" }]`),
		"CLEARANCE_INTERNAL", "")
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A3", "bank.v1.SupportAssistant", "names no tool in this catalogue") {
		t.Errorf("an unknown tool was accepted:\n%s", render(diags))
	}
}

// Design §2.1: the key is the FQN `pkg.name`, and a method path is a lint
// error. The two look similar enough that an author reaching for the thing
// they see in a URL will write the wrong one.
func TestA3RefusesAMethodPathAsAToolKey(t *testing.T) {
	for _, fqn := range []string{
		"/payments.v1.Payments/InitiatePayment",
		"payments.v1.Payments/InitiatePayment",
	} {
		files := a3Src(a3Policy(`tools: [{ fqn: "`+fqn+`" }]`), "CLEARANCE_INTERNAL", "")
		diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
		if !hasDiag(diags, "A3", "bank.v1.SupportAssistant", "looks like a method path") {
			t.Errorf("the method path %q was accepted:\n%s", fqn, render(diags))
		}
	}
}

// a4Files is the A3 fixture with one guarded tool: a real tool in a second
// package, so the guard has an actual request message to type against.
func a4Files(guard string) map[string]string {
	return a3Src(a3Policy(`tools: [{ fqn: "payments.v1.initiate_payment" guard: "`+guard+`" }]`),
		"CLEARANCE_INTERNAL", "")
}

// Review Focus 2 — a guard naming a field the request message does not have.
// CEL resolves fields at check time, so this is catchable at build time; left
// to run time it is a guard that errors on every call, which the runner treats
// as a refusal, so the tool silently becomes uncallable.
func TestA4RefusesAGuardOnAFieldThatDoesNotExist(t *testing.T) {
	diags := compiler.LintWith(compileSource(t, a4Files("args.amount <= 500000")), compiler.Options{})
	if !hasDiag(diags, "A4", "bank.v1.SupportAssistant", "undefined field 'amount'") {
		t.Errorf("a guard on a field that does not exist was accepted:\n%s", render(diags))
	}
}

func TestA4AcceptsAGuardOnARealField(t *testing.T) {
	files := a4Files("args.amount_minor_units <= 500000")
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A4" {
			t.Errorf("a correct guard produced an A4: %s", d.String())
		}
	}
}

// A guard is a predicate. An expression that yields a string is not one, and
// the runner would have nothing to compare against — so the tool call would
// either always pass or always fail depending on how the coercion was
// written, which is the worst kind of governance bug: it looks like it works.
func TestA4RefusesAGuardThatIsNotBoolean(t *testing.T) {
	diags := compiler.LintWith(compileSource(t, a4Files("args.beneficiary_iban")), compiler.Options{})
	if !hasDiag(diags, "A4", "bank.v1.SupportAssistant", "must be a bool predicate") {
		t.Errorf("a non-boolean guard was accepted:\n%s", render(diags))
	}
}

func TestA4RefusesAGuardThatIsNotCel(t *testing.T) {
	diags := compiler.LintWith(compileSource(t, a4Files("args.amount_minor_units <=")), compiler.Options{})
	if !hasDiag(diags, "A4", "bank.v1.SupportAssistant", "does not compile") {
		t.Errorf("a syntactically broken guard was accepted:\n%s", render(diags))
	}
}

// An empty guard is "no guard", not "a guard that is empty". Most tools have
// none.
func TestA4IgnoresAnAbsentGuard(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.initiate_payment" }]`),
		"CLEARANCE_INTERNAL", "")
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A4" {
			t.Errorf("an absent guard produced an A4: %s", d.String())
		}
	}
}

// A guard on a tool that is not in the catalogue has no request message to
// type against. A3 has already said the tool does not exist, so A4 says
// nothing — and must not reach through the zero Tool for a method descriptor
// that is not there.
func TestA4SaysNothingAboutAGuardOnAToolThatDoesNotExist(t *testing.T) {
	files := a3Src(a3Policy(`tools: [{ fqn: "payments.v1.refund_payment" guard: "args.amount <= 5" }]`),
		"CLEARANCE_INTERNAL", "")
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A3", "bank.v1.SupportAssistant", "names no tool in this catalogue") {
		t.Fatalf("the fixture no longer exercises an unknown tool:\n%s", render(diags))
	}
	for _, d := range diags {
		if d.Rule == "A4" {
			t.Errorf("A4 spoke about a tool A3 already refused: %s", d.String())
		}
	}
}

// Each guard is typed against ITS OWN tool's request message, not against
// whichever tool happened to be looked up first. Both entries carry the same
// guard text here: it names a field of payments.v1.Pay, which bank.v1.Ask
// does not have. So entry 0 must fail and entry 1 must pass — an
// implementation that built one environment for the whole allowlist, or that
// indexed the tools off by one, would report both or neither.
func TestA4ChecksEachGuardAgainstItsOwnToolsRequestMessage(t *testing.T) {
	files := a3Src(a3Policy(`tools: [
      { fqn: "bank.v1.support_assistant" guard: "args.amount_minor_units <= 500000" },
      { fqn: "payments.v1.initiate_payment" guard: "args.amount_minor_units <= 500000" }
    ]`), "CLEARANCE_INTERNAL", "")
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A4", "bank.v1.SupportAssistant",
		`tools[0].guard "args.amount_minor_units <= 500000" does not compile against bank.v1.Ask`) {
		t.Errorf("the guard on tools[0] was not checked against bank.v1.Ask:\n%s", render(diags))
	}
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A4" && strings.Contains(d.Msg, "tools[1]") {
			t.Errorf("the guard on tools[1] is correct for payments.v1.Pay but was "+
				"reported: %s", d.String())
		}
	}
}

// The descriptors come from the tree being linted, so the type provider has to
// be built from them — and from their imports too. The request message's
// `amount` field is a message declared in ANOTHER file in another package, so
// registering only the request message's own file leaves that field an unknown
// type the moment the guard selects through it. google.protobuf.Timestamp is
// in the same expression because CEL maps it to its own `timestamp` type
// rather than to the message, and a guard author will reach for it.
func TestA4ResolvesNestedAndImportedMessageFields(t *testing.T) {
	files := a3Src(a3Policy(
		`tools: [{ fqn: "payments.v1.initiate_payment" `+
			`guard: "args.amount.minor_units <= 500000 && `+
			`args.requested_at > timestamp(\'2020-01-01T00:00:00Z\')" }]`),
		"CLEARANCE_INTERNAL", "")
	files["money/v1/money.proto"] = `syntax = "proto3";
package money.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/money/v1;moneyv1";
message Money {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional int64 minor_units = 1;
}
`
	files["payments/v1/payments.proto"] = `syntax = "proto3";
package payments.v1;
import "garm/tool/v1/tool.proto";
import "google/protobuf/timestamp.proto";
import "money/v1/money.proto";
option go_package = "example.com/payments/v1;paymentsv1";
message Pay {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional money.v1.Money amount = 1;
  optional google.protobuf.Timestamp requested_at = 2;
}
message Paid {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string payment_id = 1;
}
service Payments {
  rpc InitiatePayment(Pay) returns (Paid) {
    option (garm.tool.v1.tool) = {
      name: "initiate_payment" title: "Pay" description: "Move money."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A4" {
			t.Errorf("a guard on an imported message type produced an A4: %s", d.String())
		}
	}
}

// output_rules are parsed for CEL validity and otherwise ignored in this MVP
// (design §2.2). Only syntax: the variable environment an output rule is
// evaluated in is not fixed anywhere yet, so type-checking one would be
// inventing a contract.
func TestA4ParsesOutputRulesForSyntaxOnly(t *testing.T) {
	policy := strings.Replace(fullPolicy, `tools: [{ fqn: "bank.v1.support_assistant" }]`,
		`tools: []
    output_rules: [{ expr: "result.answer !=" message: "no answer" }]`, 1)
	diags := compiler.LintWith(compileSource(t, agentSrc(policy)), compiler.Options{})
	if !hasDiag(diags, "A4", "bank.v1.SupportAssistant",
		`output_rules[0].expr "result.answer !=" does not parse as CEL`) {
		t.Errorf("an unparseable output rule was accepted:\n%s", render(diags))
	}
	// And a rule naming something nothing declares is NOT an error: it is
	// only parsed, because nothing fixes what an output rule sees.
	ok := strings.Replace(fullPolicy, `tools: [{ fqn: "bank.v1.support_assistant" }]`,
		`tools: []
    output_rules: [{ expr: "result.answer != ''" message: "no answer" }]`, 1)
	for _, d := range compiler.LintWith(compileSource(t, agentSrc(ok)), compiler.Options{}) {
		if d.Rule == "A4" {
			t.Errorf("a parseable output rule produced an A4: %s", d.String())
		}
	}
}

// a5Files varies only GetRun's labels; Invoke keeps
// `verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]`.
func a5Files(getRunLabels string) map[string]string {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]`,
		getRunLabels, 1)
	return files
}

func TestA5RefusesADifferentMinClearanceOnGetRun(t *testing.T) {
	diags := compiler.LintWith(compileSource(t,
		a5Files(`verb: VERB_READ min_clearance: CLEARANCE_PUBLIC sets: ["support"]`)),
		compiler.Options{})
	if !hasError(diags, "A5", "min_clearance") {
		t.Errorf("GetRun at a different clearance was accepted:\n%s", render(diags))
	}
}

func TestA5RefusesDifferentSetsOnGetRun(t *testing.T) {
	files := a5Files(`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ops"]`)
	files["bank/v1/taxonomy.proto"] = strings.Replace(files["bank/v1/taxonomy.proto"],
		`{ name: "support" description: "Support desk." }`,
		`{ name: "support" description: "Support desk." }, { name: "ops" description: "Operations." }`, 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasError(diags, "A5", "sets") {
		t.Errorf("GetRun in a different tool set was accepted:\n%s", render(diags))
	}
}

func TestA5RefusesDifferentCompartmentsOnGetRun(t *testing.T) {
	diags := compiler.LintWith(compileSource(t,
		a5Files(`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"] compartments: ["financial"]`)),
		compiler.Options{})
	if !hasError(diags, "A5", "compartments") {
		t.Errorf("GetRun in a different compartment was accepted:\n%s", render(diags))
	}
}

// Order is not meaning. Two declarations listing the same names differently
// describe the same policy, and a diagnostic about the order would be noise
// an author learns to ignore — which is how they come to ignore the one that
// matters. Both compartments and sets are exercised: each door lists both
// labels the taxonomy declares, in a different order per door, and the
// principal is granted both compartments so A3 has nothing to say about it.
func TestA5IgnoresTheOrderOfSetsAndCompartments(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/taxonomy.proto"] = strings.Replace(files["bank/v1/taxonomy.proto"],
		`{ name: "support" description: "Support desk." }`,
		`{ name: "support" description: "Support desk." }, { name: "ops" description: "Operations." }`, 1)
	files["bank/v1/taxonomy.proto"] = strings.Replace(files["bank/v1/taxonomy.proto"],
		`{ name: "financial" description: "Money." }`,
		`{ name: "financial" description: "Money." }, { name: "legal" description: "Legal." }`, 1)
	src := files["bank/v1/agent.proto"]
	src = strings.Replace(src, `principal: { clearance: CLEARANCE_CONFIDENTIAL compartments: ["financial"] }`,
		`principal: { clearance: CLEARANCE_CONFIDENTIAL compartments: ["financial", "legal"] }`, 1)
	src = strings.Replace(src, `verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support"]`,
		`verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL sets: ["support", "ops"] compartments: ["financial", "legal"]`, 1)
	src = strings.Replace(src, `verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]`,
		`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["ops", "support"] compartments: ["legal", "financial"]`, 1)
	files["bank/v1/agent.proto"] = src
	for _, d := range compiler.LintWith(compileSource(t, files), compiler.Options{}) {
		if d.Rule == "A5" {
			t.Errorf("a reordered but identical label set produced an A5: %s", d.String())
		}
	}
}

// Both doors are governed tools. A method with no (garm.tool.v1.tool) is not
// mounted at all, so an agent whose GetRun is unannotated has no governed
// door and A5 has nothing to compare — say so, rather than passing.
func TestA5RefusesADoorThatIsNotAGovernedTool(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		`    option (garm.tool.v1.tool) = {
      name: "support_assistant_run" title: "Support run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]
    };
`, "", 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasError(diags, "A5", "carries no (garm.tool.v1.tool)") {
		t.Errorf("an unannotated door was accepted:\n%s", render(diags))
	}
}

// A door can carry (garm.tool.v1.tool) and still not be governed: `exclude:
// true` unmounts it exactly the way no annotation at all does (Tools, in
// loader.go, skips excluded methods the same as unannotated ones). The nil
// case above is already caught, independently, by L27 (every method on a
// service that declares tools must carry an annotation) — an
// annotated-but-excluded door is the one case where A5 is the sole defence,
// so it needs its own test rather than relying on the nil case to cover it.
func TestA5RefusesADoorThatExcludesItself(t *testing.T) {
	files := agentSrc(fullPolicy)
	files["bank/v1/agent.proto"] = strings.Replace(files["bank/v1/agent.proto"],
		`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"]`,
		`verb: VERB_READ min_clearance: CLEARANCE_INTERNAL sets: ["support"] exclude: true`, 1)
	diags := compiler.LintWith(compileSource(t, files), compiler.Options{})
	if !hasDiag(diags, "A5", "bank.v1.SupportAssistant.GetRun", "carries no (garm.tool.v1.tool)") {
		t.Errorf("a door that excludes itself was accepted:\n%s", render(diags))
	}
}

func TestA5AcceptsIdenticalLabels(t *testing.T) {
	for _, d := range compiler.LintWith(compileSource(t, agentSrc(fullPolicy)), compiler.Options{}) {
		if d.Rule == "A5" {
			t.Errorf("identical labels produced an A5: %s", d.String())
		}
	}
}

// The protoc plugin sees one directory at a time. An agent whose allowlist
// names a tool from another directory must not be refused there — that is
// what `garm lint` over the whole tree is for — but it must not be waved
// through silently either.
func TestAPartialSetWarnsInsteadOfResolvingTheAllowlist(t *testing.T) {
	// The allowlist names a tool this set does not contain.
	files := agentSrc(strings.Replace(fullPolicy,
		`tools: [{ fqn: "bank.v1.support_assistant" }]`,
		`tools: [{ fqn: "payments.v1.initiate_payment" guard: "args.amount <= 1" }]`, 1))
	fds := compileSource(t, files)

	full := compiler.LintWith(fds, compiler.Options{})
	if !hasError(full, "A3", "names no tool in this catalogue") {
		t.Fatalf("a full lint did not refuse the unknown tool: %v", full)
	}

	partial := compiler.LintWith(fds, compiler.Options{PartialSet: true})
	for _, d := range partial {
		if (d.Rule == "A3" || d.Rule == "A4") && !d.Warn {
			t.Fatalf("a partial set refused with %s: %s", d.Rule, d.Msg)
		}
	}
	warned := false
	for _, d := range partial {
		if d.Rule == "A3" && d.Warn && d.Path == "bank.v1.SupportAssistant" &&
			strings.Contains(d.Msg, "not checked here") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("a partial set skipped the allowlist without saying so: %v", partial)
	}
}
