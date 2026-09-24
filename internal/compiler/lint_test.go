package compiler_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// longMethodName is an RPC method name whose SnakeCase-derived tool name
// exceeds the 64-character limit toolNameRE enforces. It is built from
// repeated "Aaa" words (each an ASCII, otherwise-legal identifier segment) so
// the only thing wrong with the derived name is its length, isolating L3's
// length check from any charset concern.
const longMethodName = "AaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaaAaa"

func TestLintRules(t *testing.T) {
	cases := []struct {
		name  string
		rule  string
		warn  bool
		build func(t *testing.T) []protoreflect.FileDescriptor
	}{
		{
			name: "unlabeled field has no policy and no message default",
			rule: "L1", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				return lintFixture(t, "unlabeled_field", nil, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "field policy present but read is unspecified",
			rule: "L1", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{OnDeny: maskRedaction()} // Read left zero
				return lintFixture(t, "unspecified_read", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "tool has no description",
			rule: "L2", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				tool := &toolv1.ToolPolicy{
					Name: "get", Verb: toolv1.Verb_VERB_READ,
					MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
				}
				return lintFixture(t, "no_description", validField(), nil,
					methodSpec{name: "Get", tool: tool})
			},
		},
		{
			name: "explicit tool name fails the charset/case rule",
			rule: "L3", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				tool := &toolv1.ToolPolicy{
					Name: "Bad-Name!", Description: "d", Verb: toolv1.Verb_VERB_READ,
					MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
				}
				return lintFixture(t, "bad_tool_name", validField(), nil,
					methodSpec{name: "Get", tool: tool})
			},
		},
		{
			// Amendment 2: L3 must also validate names that come from
			// DefaultToolName (no explicit ToolPolicy.Name), not only
			// explicit ones. SnakeCase has no length bound of its own, so an
			// overlong method name must be caught here.
			name: "derived tool name (no explicit Name) exceeds the length limit",
			rule: "L3", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				tool := &toolv1.ToolPolicy{
					Description: "d", Verb: toolv1.Verb_VERB_READ,
					MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
				}
				return lintFixture(t, "bad_tool_name_derived", validField(), nil,
					methodSpec{name: longMethodName, tool: tool})
			},
		},
		{
			name: "two tools resolve to the same name",
			rule: "L3", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				return lintFixture(t, "duplicate_tool_name", validField(), nil,
					methodSpec{name: "GetOne", tool: validTool("dup")},
					methodSpec{name: "GetTwo", tool: validTool("dup")},
				)
			},
		},
		{
			name: "streaming RPC is annotated as a tool",
			rule: "L4", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				return lintFixture(t, "streaming_tool", validField(), nil,
					methodSpec{name: "Get", tool: validTool("get"), streaming: true})
			},
		},
		{
			name: "redaction kind does not accept the field's type",
			rule: "L5", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{
					Read: toolv1.Clearance_CLEARANCE_PUBLIC,
					OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_DateGrain{
						DateGrain: &toolv1.DateGrain{Grain: toolv1.Grain_GRAIN_DAY},
					}},
				}
				return lintFixture(t, "redaction_kind_mismatch", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "omit redaction on a singular scalar with no explicit optional",
			rule: "L6", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{Read: toolv1.Clearance_CLEARANCE_RESTRICTED}
				return lintFixture(t, "omit_without_optional", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "field requires an undeclared compartment",
			rule: "L7", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{
					Read:         toolv1.Clearance_CLEARANCE_PUBLIC,
					Compartments: []string{"ghost-compartment"},
					OnDeny:       maskRedaction(),
				}
				return lintFixture(t, "undeclared_compartment", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "keep_last redaction has no reason",
			rule: "L9", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{
					Read: toolv1.Clearance_CLEARANCE_RESTRICTED,
					OnDeny: &toolv1.Redaction{Kind: &toolv1.Redaction_KeepLast{
						KeepLast: &toolv1.KeepLast{N: 4},
					}},
				}
				return lintFixture(t, "partial_no_reason", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "write clearance is below read clearance",
			rule: "L10", warn: true,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{
					Read:   toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
					Write:  toolv1.Clearance_CLEARANCE_PUBLIC,
					OnDeny: maskRedaction(),
				}
				return lintFixture(t, "write_below_read", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")})
			},
		},
		{
			name: "every field of the output is invisible at the tool's min_clearance",
			rule: "L11", warn: true,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				fp := &toolv1.FieldPolicy{
					Read:   toolv1.Clearance_CLEARANCE_RESTRICTED,
					OnDeny: maskRedaction(),
				}
				return lintFixtureNoCanary(t, "all_invisible", fp, nil,
					methodSpec{name: "Get", tool: validTool("get")}) // min_clearance PUBLIC
			},
		},
		{
			name: "example request_json does not parse into the request message",
			rule: "L19", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				tool := validTool("get")
				tool.Guidance = &toolv1.Guidance{Examples: []*toolv1.Example{
					{Description: "ex", RequestJson: "not-json"},
				}}
				return lintFixture(t, "example_invalid", validField(), nil,
					methodSpec{name: "Get", tool: tool})
			},
		},
		{
			name: "tool references an undeclared tool set",
			rule: "L20", warn: false,
			build: func(t *testing.T) []protoreflect.FileDescriptor {
				tool := validTool("get")
				tool.Sets = []string{"ghost-set"}
				return lintFixture(t, "undeclared_set", validField(), nil,
					methodSpec{name: "Get", tool: tool})
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diags := compiler.Lint(c.build(t))
			if !hasRule(diags, c.rule, c.warn) {
				t.Fatalf("expected %s (warn=%v), got %v", c.rule, c.warn, diags)
			}
		})
	}
}

// TestLintAcceptsTheGoodFixture asserts ZERO diagnostics on valid input —
// warnings included, not just errors.
//
// The old assertion tolerated any warning ("if !d.Warn { fail }"), which is
// the wrong shape for this test: a warning on a schema that breaks no rule
// is a false positive, and a false positive in a build-gating linter is the
// failure mode that gets the whole gate disabled. Combined with the
// enriched fixture (message, repeated message, message-valued map, scalar
// map and an `optional` scalar — see canaryFields), this is what would have
// caught the L5 map false positive Task 11 had to fix by hand.
func TestLintAcceptsTheGoodFixture(t *testing.T) {
	fds := lintFixture(t, "good", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")})

	if diags := compiler.Lint(fds); len(diags) != 0 {
		t.Fatalf("good fixture produced %d diagnostic(s), want 0: %+v", len(diags), diags)
	}
}

// TestLintMessageDefaultSatisfiesL1 covers the path every other fixture in
// this file leaves untested: a field with no field-level policy of its own,
// resolved instead through the message's default_field_policy. L1 only
// fires when a field has neither, so this must NOT produce an L1 diagnostic.
// Without this test, `msgDefault` is a parameter every call site passes nil
// for a rule the whole design rests on — coverage that reads as real but
// isn't.
func TestLintMessageDefaultSatisfiesL1(t *testing.T) {
	def := validField() // a fully valid policy, inherited rather than owned
	fds := lintFixture(t, "message_default_satisfies_l1", nil, def,
		methodSpec{name: "Get", tool: validTool("get")})

	for _, d := range compiler.Lint(fds) {
		if d.Rule == "L1" {
			t.Fatalf("message default should have satisfied L1, got: %+v", d)
		}
	}
}

// TestLintDerivedToolNameDiagnosticNamesTheDerivedName pins the L3 message
// content for the derived-name fixture specifically. Checking only Rule+Warn
// (as the table above does for every other row) would also pass if the check
// were narrowed to validate solely the explicit ToolPolicy.Name: an empty
// explicit name fails the same regex, so that narrowing produces an L3 error
// too, just for the wrong reason. Asserting the derived name appears in Msg
// forces the diagnostic to have actually evaluated the resolved (derived)
// name, not the empty explicit one.
func TestLintDerivedToolNameDiagnosticNamesTheDerivedName(t *testing.T) {
	tool := &toolv1.ToolPolicy{
		Description: "d", Verb: toolv1.Verb_VERB_READ,
		MinClearance: toolv1.Clearance_CLEARANCE_PUBLIC,
	}
	fds := lintFixture(t, "bad_tool_name_derived_msg", validField(), nil,
		methodSpec{name: longMethodName, tool: tool})

	derived := compiler.SnakeCase(longMethodName)
	diags := compiler.Lint(fds)
	if !hasRuleWithMsg(diags, "L3", false, derived) {
		t.Fatalf("expected an L3 diagnostic naming the derived tool name %q, got %v", derived, diags)
	}
}

// TestLintFixtureHasEveryShapeItClaims is a self-check on the fixture
// builder, not on any rule.
//
// TestLintAcceptsTheGoodFixture asserts zero diagnostics; that assertion is
// only worth anything if the message it runs over actually contains the
// shapes the rules treat differently. A fixture that silently lost its map
// field (or whose `optional` scalar quietly degraded to implicit presence,
// which protodesc would accept without complaint) would still pass with
// zero diagnostics — vacuously. This pins the shapes themselves.
func TestLintFixtureHasEveryShapeItClaims(t *testing.T) {
	fds := lintFixture(t, "shape_check", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")})
	md := fds[0].Messages().ByName("M")
	if md == nil {
		t.Fatal("fixture message M is missing")
	}

	field := func(name string) protoreflect.FieldDescriptor {
		t.Helper()
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			t.Fatalf("fixture lost field %q", name)
		}
		return fd
	}

	if k := field("nested").Kind(); k != protoreflect.MessageKind {
		t.Fatalf("nested is %v, want a message-typed field", k)
	}
	many := field("many")
	if !many.IsList() || many.Kind() != protoreflect.MessageKind {
		t.Fatalf("many is not a repeated message field")
	}
	msgMap := field("msg_map")
	if !msgMap.IsMap() || msgMap.MapValue().Kind() != protoreflect.MessageKind {
		t.Fatalf("msg_map is not a message-valued map")
	}
	strMap := field("str_map")
	if !strMap.IsMap() || strMap.MapValue().Kind() != protoreflect.StringKind {
		t.Fatalf("str_map is not a scalar-valued map")
	}
	if !field("opt_scalar").HasPresence() {
		t.Fatal("opt_scalar has no explicit presence; L6's presence branch is untested")
	}
	// secret is deliberately presence-LESS: it is what the L6 fixture trips.
	if field("secret").HasPresence() {
		t.Fatal("secret gained presence; the L6 fixture can no longer fire")
	}
}

// TestLintWalksEveryRouteIntoANestedMessage proves the lint walk reaches N
// by each of the three routes the fixture offers — a singular message
// field, a repeated message field, and a MESSAGE-VALUED MAP.
//
// The map route is the one that was broken: lintMessage refused to descend
// into maps at all (`fd.Kind() == MessageKind && !fd.IsMap()`) while
// policy.compileInto descends via fd.MapValue().Message(), a deliberate
// Task 7 fix for a leak class. A type reachable only through a
// message-valued map was therefore never linted — L1, L5, L6, L7, L9, L10
// and L22 were all dead for it — and the failure surfaced as a server that
// refused to start rather than as a build diagnostic.
//
// N.leaf is left unlabeled, so each route that is genuinely walked yields
// one L1 whose Path names it.
func TestLintWalksEveryRouteIntoANestedMessage(t *testing.T) {
	fds := lintFixtureUnlabeledLeaf(t, "nested_routes",
		methodSpec{name: "Get", tool: validTool("get")})
	diags := compiler.Lint(fds)

	for _, route := range []string{".nested.leaf", ".many.leaf", ".msg_map.leaf"} {
		found := false
		for _, d := range diags {
			if d.Rule == "L1" && strings.HasSuffix(d.Path, route) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the walk never reached %s; got %+v", route, diags)
		}
	}
}

// TestLintRejectsARecursiveMessageGraph covers L26.
//
// policy.Compile rejects a cycle outright (it cannot be flattened into
// a finite plan, and the previous behaviour — stop at the repeat — returned
// every classified field below depth 1 in the clear). Without L26 that
// rejection lands at Mount, as a server that will not start, instead of at
// build time where the schema author can act on it. The old walk could not
// see cycles at all: its `seen` was global and never unwound.
func TestLintRejectsARecursiveMessageGraph(t *testing.T) {
	fds := lintFixtureRecursive(t, "recursive_graph",
		methodSpec{name: "Get", tool: validTool("get")})
	diags := compiler.Lint(fds)

	if !hasRule(diags, "L26", false) {
		t.Fatalf("expected L26 (error) for a self-referencing message, got %+v", diags)
	}
	if !hasRuleWithMsg(diags, "L26", false, "compiler.testdata.lint.M") {
		t.Fatalf("L26 does not name the recursive message type: %+v", diags)
	}
}

// TestLintDoesNotReportACycleForAMerelySharedType is L26's negative case.
// N is reached three times in the good fixture (nested, many, msg_map) by
// three DISTINCT paths. That is a diamond, not a cycle, and it compiles
// fine — so L26 must stay silent. Without this, the lazy implementation
// (report on any repeat of a type anywhere in the walk) would look correct.
func TestLintDoesNotReportACycleForAMerelySharedType(t *testing.T) {
	fds := lintFixture(t, "shared_type", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")})

	for _, d := range compiler.Lint(fds) {
		if d.Rule == "L26" {
			t.Fatalf("L26 fired on a shared (non-recursive) type: %+v", d)
		}
	}
}

// TestLintRequiresEveryMethodOfAToolServiceToBeAnnotated covers L27.
//
// MountService mounts a whole connect service; only annotated,
// non-excluded methods become tools. An unannotated sibling is still
// served, still returns the same classified messages, and — before the
// interceptor was fixed — bypassed the entire policy chain. L27 makes that
// omission a build error rather than a runtime discovery.
func TestLintRequiresEveryMethodOfAToolServiceToBeAnnotated(t *testing.T) {
	fds := lintFixture(t, "unannotated_sibling", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")},
		methodSpec{name: "Sibling", tool: nil})
	diags := compiler.Lint(fds)

	if !hasRule(diags, "L27", false) {
		t.Fatalf("expected L27 (error) for an unannotated sibling RPC, got %+v", diags)
	}
	if !hasRuleWithMsg(diags, "L27", false, "exclude: true") {
		t.Fatalf("L27 does not tell the author what to write instead: %+v", diags)
	}
	for _, d := range diags {
		if d.Rule == "L27" && !strings.HasSuffix(d.Path, ".Sibling") {
			t.Fatalf("L27 fired on the wrong method: %+v", d)
		}
	}
}

// TestLintAcceptsAnExplicitlyExcludedSibling is L27's negative case, and
// the one that separates "must be annotated" from "must be a tool".
// exclude: true is a decision and is reviewable in a diff; absence is not.
// Spec §5.6's ToolCatalogService is exactly this shape.
func TestLintAcceptsAnExplicitlyExcludedSibling(t *testing.T) {
	excluded := &toolv1.ToolPolicy{Exclude: true}
	fds := lintFixture(t, "excluded_sibling", validField(), nil,
		methodSpec{name: "Get", tool: validTool("get")},
		methodSpec{name: "Sibling", tool: excluded})

	for _, d := range compiler.Lint(fds) {
		if d.Rule == "L27" {
			t.Fatalf("L27 fired on an explicitly excluded method: %+v", d)
		}
	}
}

// TestLintIgnoresAServiceWithNoToolsAtAll keeps L27 scoped: a service that
// declares no tools is not a garm surface, is never mounted on a
// toolplane.Server through generated code, and must not be dragged into
// this vocabulary by the mere presence of another annotated service in the
// same file.
//
// Both cases matter, and the second is what makes the `!tp.GetExclude()`
// half of serviceHasTool observable at all. A service whose only
// annotation is `exclude: true` has opted OUT; requiring its remaining
// methods to be annotated would be L27 punishing an author for using the
// escape hatch the rule itself recommends.
func TestLintIgnoresAServiceWithNoToolsAtAll(t *testing.T) {
	cases := []struct {
		name    string
		methods []methodSpec
	}{
		{
			name:    "no annotations anywhere",
			methods: []methodSpec{{name: "Sibling", tool: nil}},
		},
		{
			name: "every annotation is exclude: true",
			methods: []methodSpec{
				{name: "Sibling", tool: &toolv1.ToolPolicy{Exclude: true}},
				{name: "Other", tool: nil},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fds := lintFixture(t, "no_tools_at_all", validField(), nil, c.methods...)
			for _, d := range compiler.Lint(fds) {
				if d.Rule == "L27" {
					t.Fatalf("L27 fired on a service that declares no tools: %+v", d)
				}
			}
		})
	}
}

// TestLintReportsConflictingCompartmentDeclarations pins L29: this is the
// round-1 fix for the review finding that DeclaredCompartments/
// DeclaredSets (internal/toolgen/loader.go's declared()) dedupe by name
// alone and silently keep whichever file came first — reproduced by the
// reviewer with taxonomy.proto and a sibling file in the SAME proto
// package, both declaring "shared" with different descriptions, where
// `make gen` exited 0 and kept the wrong one. Lint is what makes that loud:
// cmd/protoc-gen-garm-tools/main.go's generate() already fails the build on
// any non-Warn diag (dedupeDiags(compiler.Lint(fds))), so this rule alone
// closes the gap without touching declared()'s signature.
func TestLintReportsConflictingCompartmentDeclarations(t *testing.T) {
	fdA := declFile(t, "compiler/testdata/lint/shared_a.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "shared", Description: "Alpha's meaning"})
	fdB := declFile(t, "compiler/testdata/lint/shared_b.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "shared", Description: "A COMPLETELY DIFFERENT meaning"})

	diags := compiler.Lint([]protoreflect.FileDescriptor{fdA, fdB})

	var found *compiler.Diag
	for i := range diags {
		if diags[i].Rule == "L29" {
			found = &diags[i]
		}
	}
	if found == nil {
		t.Fatalf("Lint did not report an L29 diagnostic for two files declaring "+
			"\"shared\" with different descriptions; got: %+v", diags)
	}
	if found.Warn {
		t.Fatalf("L29 must be a hard error, not a warning — this is a build gate "+
			"(cmd/protoc-gen-garm-tools/main.go only fails on Warn=false): %+v", found)
	}
	for _, want := range []string{
		"shared", "shared_a.proto", "shared_b.proto",
		"Alpha's meaning", "A COMPLETELY DIFFERENT meaning",
	} {
		if !strings.Contains(found.Msg, want) {
			t.Fatalf("L29 message does not mention %q: %v", want, found.Msg)
		}
	}
}

// TestLintAllowsIdenticalCompartmentDeclarations is L29's other half: the
// same name declared identically in two files is a governance term the
// files agree on, not a conflict, and must not fire.
func TestLintAllowsIdenticalCompartmentDeclarations(t *testing.T) {
	fdA := declFile(t, "compiler/testdata/lint/agree_a.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "shared", Description: "Same meaning everywhere"})
	fdB := declFile(t, "compiler/testdata/lint/agree_b.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "shared", Description: "Same meaning everywhere"})

	for _, d := range compiler.Lint([]protoreflect.FileDescriptor{fdA, fdB}) {
		if d.Rule == "L29" {
			t.Fatalf("L29 fired on two files declaring \"shared\" identically: %+v", d)
		}
	}
}

// TestLintReportsBothAConflictAndAMalformedName is the rebase-interaction
// case: one malformed declared name used to hide EVERY declaration conflict
// in the run.
//
// lintFieldAndShapeRules appends L29's findings to `out` and then, three
// lines later, `return bad` for L28 — discarding `out` entirely. Two files
// declaring "Shared" with different descriptions therefore reported the
// uppercase letter and said nothing about the conflict, so an author who
// fixed the case would meet the conflict only on the next run, if at all.
//
// The two rules are independent and both are build gates, so both must be
// reported in one pass.
func TestLintReportsBothAConflictAndAMalformedName(t *testing.T) {
	// "Shared" is malformed (L28 requires ^[a-z][a-z0-9_]{0,63}$) AND
	// declared twice with different meanings (L29).
	fdA := declFile(t, "compiler/testdata/lint/both_a.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "Shared", Description: "Alpha's meaning"})
	fdB := declFile(t, "compiler/testdata/lint/both_b.proto", toolv1.E_Compartments,
		&toolv1.Decl{Name: "Shared", Description: "A COMPLETELY DIFFERENT meaning"})

	diags := compiler.Lint([]protoreflect.FileDescriptor{fdA, fdB})

	seen := map[string]bool{}
	for _, d := range diags {
		seen[d.Rule] = true
	}
	for _, rule := range []string{"L28", "L29"} {
		if !seen[rule] {
			t.Errorf("Lint did not report %s for two files declaring \"Shared\" "+
				"(malformed) with different descriptions (a conflict); one rule "+
				"is swallowing the other. Got: %+v", rule, diags)
		}
	}
}
