package compiler_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
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
		"func DefaultAccountsServiceGetAccountSummaryInputCard(",
		"func DefaultAccountsServiceGetAccountSummaryResultCard(",
		"func AccountsServiceGetAccountSummaryResultCardFrom(",
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
	if !strings.Contains(src, "return DefaultAccountsServiceGetAccountSummaryInputCard(ctx, in)") {
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

// The names a card contributes to the Go package are qualified by the
// service; the RPC name is not.
//
// contracts/cards.methodName gives a card a BARE RPC name on an agent and on
// a service with exactly one tool — InputCard, ResultCard, ApprovalCard — and
// that is right: an RPC name is scoped by the service it hangs off, and the
// catalogue's name for the same card is qualified already
// (card_guardian_input_card). A Go identifier has no such scope. These two
// tests are the two ways a proto package reaches the same bare name twice,
// and both shipped broken in v0.18.1: bank.agents.v1 declares five agent
// services and the generated package therefore declared DefaultInputCard
// five times, which does not compile.

// Two single-tool services in one proto package. No agent involved: the bare
// name comes from toolCount(svc) == 1, which is true of both of them.
func TestTwoSingleToolServicesInOnePackageDoNotCollide(t *testing.T) {
	src := renderMicroFromSource(t, map[string]string{"twin/v1/twin.proto": `syntax = "proto3";
package twin.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/twin/v1;twinv1";
message Req {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
message Resp {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string value = 1;
}
service AlphaService {
  rpc Get(Req) returns (Resp) {
    option (garm.tool.v1.tool) = {
      name: "alpha_get" title: "Alpha" description: "Read an alpha."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
service BetaService {
  rpc Get(Req) returns (Resp) {
    option (garm.tool.v1.tool) = {
      name: "beta_get" title: "Beta" description: "Read a beta."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
}
`}, "twin/v1/twin.proto")

	assertDeclaredOnce(t, src)

	// The RPC name is still bare on both — this is an emitter fix, and
	// moving the RPC name would move every descriptor and every catalogue
	// digest. Compared with interior whitespace collapsed, as the other
	// endpoint assertions are: gofmt aligns the struct literal's columns.
	assertRPCNames(t, src,
		`Method: "InputCard"`,
		`Subject: "twin.v1.AlphaService.InputCard"`,
		`Subject: "twin.v1.BetaService.InputCard"`,
	)

	for _, want := range []string{
		"func DefaultAlphaServiceInputCard(",
		"func DefaultAlphaServiceResultCard(",
		"func AlphaServiceResultCardFrom(",
		"func DefaultBetaServiceInputCard(",
		"func DefaultBetaServiceResultCard(",
		"func BetaServiceResultCardFrom(",
		"return DefaultAlphaServiceInputCard(ctx, in)",
		"return DefaultBetaServiceInputCard(ctx, in)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source is missing:\n  %s", want)
		}
	}
}

// Two agent services in one proto package: bank.agents.v1's shape, reduced.
// The bare name here comes from isAgentService(svc), so it holds however many
// tools the service has.
func TestTwoAgentServicesInOnePackageDoNotCollide(t *testing.T) {
	agent := func(name, tool string) string {
		return `
service ` + name + ` {
  option (garm.agent.v1.agent) = { mode: MODE_REACT model: { alias: "fast" } };
  rpc Invoke(Ask) returns (garm.agent.v1.RunRef) {
    option (garm.tool.v1.tool) = {
      name: "` + tool + `" title: "` + tool + `" description: "Start a run."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
  rpc GetRun(garm.agent.v1.RunRef) returns (garm.agent.v1.RunStatus) {
    option (garm.tool.v1.tool) = {
      name: "` + tool + `_run" title: "` + tool + ` run" description: "Read a run."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
	}
	src := renderMicroFromSource(t, map[string]string{"pair/agents/v1/agents.proto": `syntax = "proto3";
package pair.agents.v1;
import "garm/agent/v1/agent.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/pair/agents/v1;pairagentsv1";
message Ask {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string question = 1;
}
` + agent("Concierge", "concierge") + agent("SupportAssistant", "support_assistant"),
	}, "pair/agents/v1/agents.proto")

	assertDeclaredOnce(t, src)

	assertRPCNames(t, src,
		`Subject: "pair.agents.v1.Concierge.InputCard"`,
		`Subject: "pair.agents.v1.SupportAssistant.InputCard"`,
	)

	for _, want := range []string{
		"func DefaultConciergeInputCard(",
		"func DefaultConciergeResultCard(",
		"func ConciergeResultCardFrom(",
		"func DefaultSupportAssistantInputCard(",
		"func DefaultSupportAssistantResultCard(",
		"func SupportAssistantResultCardFrom(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source is missing:\n  %s", want)
		}
	}
}

// assertRPCNames pins that a card's RPC-level name is untouched by the Go
// renaming. Whitespace is stripped because gofmt aligns the struct literal.
func assertRPCNames(t *testing.T, src string, want ...string) {
	t.Helper()
	flat := strings.ReplaceAll(src, " ", "")
	for _, w := range want {
		if !strings.Contains(flat, strings.ReplaceAll(w, " ", "")) {
			t.Errorf("the RPC name moved; generated source is missing:\n  %s", w)
		}
	}
}

// assertDeclaredOnce is the compile check this defect needed.
//
// go/parser accepts a file that declares DefaultInputCard five times — a
// duplicate declaration is a type-checker error, not a syntax error — so
// parsing the output, which the emit tests already do, could never have
// caught this. The package-level names are collected off the AST instead and
// every repeat is reported.
func assertDeclaredOnce(t *testing.T, src string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "generated.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("generated source does not parse as Go: %v\n%s", err, src)
	}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		var names []string
		switch d := d.(type) {
		case *ast.FuncDecl:
			// A method is scoped by its receiver, and nothing here emits
			// one; only package-level functions can redeclare.
			if d.Recv == nil {
				names = append(names, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					names = append(names, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
		for _, n := range names {
			if n == "_" {
				continue
			}
			if seen[n] {
				t.Errorf("%s is declared more than once: the generated package does not compile", n)
			}
			seen[n] = true
		}
	}
}

// renderMicroFromSource compiles proto SOURCE and runs EmitMicro over one
// file of it.
//
// From source rather than a hand-built descriptor because both fixtures here
// turn on a SERVICE-level fact — two services in one file, and the agent
// option on a service — and assembling that by hand would be assembling the
// thing under test. compile.Tree is the same path `garm catalogue build`
// takes, and it hands back a topologically ordered set with the options
// re-parsed against the linked extension types, which is exactly what
// protogen wants.
func renderMicroFromSource(t *testing.T, files map[string]string, target string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, _, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	gen, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{target},
		ProtoFile:      set.GetFile(),
	})
	if err != nil {
		t.Fatalf("protogen.Options{}.New: %v", err)
	}
	f := gen.FilesByPath[target]
	if f == nil {
		t.Fatalf("%s is not in the compiled set", target)
	}
	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	outPkg := f.GoPackageName + protogen.GoPackageName("micro")
	outImportPath := protogen.GoImportPath(path.Join(string(f.GoImportPath), string(outPkg)))
	filename := path.Join(path.Dir(f.GeneratedFilenamePrefix), string(outPkg), string(outPkg)) + "_micro.pb.go"

	g := gen.NewGeneratedFile(filename, outImportPath)
	if err := compiler.EmitMicro(g, []*protogen.File{f}, outPkg, tools, "dev", compiler.MicroOptions{}); err != nil {
		t.Fatalf("EmitMicro: %v", err)
	}
	return gen.Response().GetFile()[0].GetContent()
}
