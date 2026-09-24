package compiler_test

import (
	"strings"
	"testing"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
)

// effTool starts from validTool (legal name, description, READ verb, public
// min_clearance — no violation of any rule under test) and lets each case
// override exactly the fields its rule cares about, mirroring the pattern
// TestLintRules uses for field-level rules.
func effTool(name string) *toolv1.ToolPolicy {
	return validTool(name)
}

func TestLintEffectRules(t *testing.T) {
	cases := []struct {
		name        string
		rule        string
		warn        bool
		tool        func() *toolv1.ToolPolicy
		methods     []methodSpec        // extra methods beyond the tool under test
		fieldPolicy *toolv1.FieldPolicy // defaults to validField() when nil
	}{
		{
			// L12 — COMPENSABLE with no compensating_tool named at all.
			name: "compensable with no compensating tool named",
			rule: "L12", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("get")
				tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_COMPENSABLE}
				return tp
			},
		},
		{
			// L12 — COMPENSABLE naming a tool that isn't registered.
			name: "compensable naming an unregistered tool",
			rule: "L12", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("get")
				tp.Effects = &toolv1.Effects{
					Reversibility:    toolv1.Reversibility_REVERSIBILITY_COMPENSABLE,
					CompensatingTool: "ghost_tool",
				}
				return tp
			},
		},
		{
			// L13 — notify on an irreversible action, no notes to record intent.
			// verb WRITE (not DESTRUCTIVE) and external=false keep L14/L16 quiet.
			name: "notify on irreversible with no notes",
			rule: "L13", warn: true,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("notify_irreversible")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_NONE}
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_NOTIFY}
				return tp
			},
		},
		{
			// L13 — notify on an irreversible action where reversibility was
			// never set at all (UNSPECIFIED, the zero value), not explicitly
			// NONE. Pins the fail-closed disjunct in `irreversible`: deleting
			// `|| Reversibility == REVERSIBILITY_UNSPECIFIED` must not leave
			// this suite green. Effects left nil: External defaults to false,
			// which keeps L16 quiet.
			name: "notify on unspecified reversibility with no notes",
			rule: "L13", warn: true,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("notify_unspecified")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_NOTIFY}
				return tp
			},
		},
		{
			// L14 — destructive without a grant. Reversibility FULL (not
			// irreversible) keeps L13/L16 quiet; audit is fully compliant so
			// L21/L23 don't also fire on this fixture.
			name: "destructive without a grant",
			rule: "L14", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("destroy")
				tp.Verb = toolv1.Verb_VERB_DESTRUCTIVE
				tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_FULL}
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_NOTIFY}
				tp.Audit = &toolv1.Audit{
					Level: toolv1.Audit_LEVEL_AUDIT, FailClosed: true, RetainDays: 30,
				}
				return tp
			},
		},
		{
			// L15 — external and not idempotent. Reversibility FULL keeps L16 quiet.
			name: "external and not idempotent",
			rule: "L15", warn: true,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("charge")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Effects = &toolv1.Effects{
					External: true, Idempotent: false,
					Reversibility: toolv1.Reversibility_REVERSIBILITY_FULL,
				}
				return tp
			},
		},
		{
			// L16 — irreversible and external, nobody notified. idempotent=true
			// keeps L15 quiet; verb WRITE keeps L14/L21 quiet.
			name: "irreversible and external, silent",
			rule: "L16", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("purge")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Effects = &toolv1.Effects{
					Reversibility: toolv1.Reversibility_REVERSIBILITY_NONE,
					External:      true, Idempotent: true,
				}
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_NONE}
				return tp
			},
		},
		{
			// L16 — irreversible via unspecified reversibility (never set,
			// not explicitly NONE) and external, nobody notified. Pins the
			// same fail-closed disjunct as the L13 row above, for L16's own
			// `irreversible` check. idempotent=true keeps L15 quiet; verb
			// WRITE keeps L14/L21 quiet; mode NONE (not NOTIFY) keeps L13 quiet.
			name: "unspecified reversibility and external, silent",
			rule: "L16", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("purge_unspecified")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Effects = &toolv1.Effects{External: true, Idempotent: true}
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_NONE}
				return tp
			},
		},
		{
			// L17 — response-based authorization on a non-READ verb.
			name: "post-check authorization on a write",
			rule: "L17", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("update")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Authorization = &toolv1.Authorization{
					Backend: &toolv1.Authorization_Fga{Fga: &toolv1.Fga{
						ObjectType: "doc",
						Object:     &toolv1.Fga_FromResponseField{FromResponseField: "visible"},
					}},
				}
				return tp
			},
		},
		{
			// L18 — from_request_field names a field the request doesn't have.
			// verb READ keeps L17 quiet.
			name: "fga from_request_field names a nonexistent field",
			rule: "L18", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("get_doc")
				tp.Authorization = &toolv1.Authorization{
					Backend: &toolv1.Authorization_Fga{Fga: &toolv1.Fga{
						ObjectType: "doc",
						Object:     &toolv1.Fga_FromRequestField{FromRequestField: "ghost_field"},
					}},
				}
				return tp
			},
		},
		{
			// L18 — filter_response.repeated_field names a field the output
			// doesn't have. This is lintFgaFields' *other* hasField branch
			// (the from_request_field row above only covers the first);
			// verb READ keeps L17 quiet.
			name: "fga filter_response names a nonexistent repeated field",
			rule: "L18", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("list_docs")
				tp.Authorization = &toolv1.Authorization{
					Backend: &toolv1.Authorization_Fga{Fga: &toolv1.Fga{
						ObjectType: "doc",
						Object: &toolv1.Fga_FilterResponse{FilterResponse: &toolv1.ListFilter{
							RepeatedField: "ghost_repeated_field",
						}},
					}},
				}
				return tp
			},
		},
		{
			// L17 — post-check authorization via filter_response (not
			// from_response_field) on a non-READ verb. This is L17's other
			// "post" disjunct; the row above only covers from_response_field.
			// repeated_field names an existing field ("visible") so L18 stays
			// quiet on this fixture.
			name: "post-check via filter_response on a write",
			rule: "L17", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("update_list")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Authorization = &toolv1.Authorization{
					Backend: &toolv1.Authorization_Fga{Fga: &toolv1.Fga{
						ObjectType: "doc",
						Object: &toolv1.Fga_FilterResponse{FilterResponse: &toolv1.ListFilter{
							RepeatedField: "visible",
						}},
					}},
				}
				return tp
			},
		},
		{
			// L21 — destructive without LEVEL_AUDIT + fail_closed. mode GRANT
			// keeps L14 quiet; reversibility FULL keeps L12/L13/L16 quiet.
			name: "destructive without fail-closed audit",
			rule: "L21", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("destroy2")
				tp.Verb = toolv1.Verb_VERB_DESTRUCTIVE
				tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_FULL}
				tp.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_GRANT}
				tp.Audit = &toolv1.Audit{
					Level: toolv1.Audit_LEVEL_LEDGER, FailClosed: false, RetainDays: 30,
				}
				return tp
			},
		},
		{
			// L23 — LEVEL_AUDIT with no retention window. verb WRITE keeps
			// L14/L21 quiet.
			name: "audit level with no retention",
			rule: "L23", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("record")
				tp.Verb = toolv1.Verb_VERB_WRITE
				tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_FULL}
				tp.Audit = &toolv1.Audit{Level: toolv1.Audit_LEVEL_AUDIT, FailClosed: true, RetainDays: 0}
				return tp
			},
		},
		{
			// L24 — a streaming RPC declares WRITE. Effects left zero: external
			// stays false so L16 stays quiet despite reversibility defaulting
			// to UNSPECIFIED (irreversible).
			name: "streaming rpc declares write",
			rule: "L24", warn: false,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("stream_write")
				tp.Verb = toolv1.Verb_VERB_WRITE
				return tp
			},
			methods: nil,
		},
		{
			// L22 — record_response captures a RESTRICTED output field into
			// the audit stream. verb READ and a zero Effects/Approval keep
			// every other rule in this set quiet (external defaults false,
			// so irreversible-by-default doesn't trip L16; mode defaults to
			// UNSPECIFIED, not NOTIFY, so L13 stays quiet too).
			name: "record_response captures a restricted field",
			rule: "L22", warn: true,
			tool: func() *toolv1.ToolPolicy {
				tp := effTool("read_secret")
				tp.Audit = &toolv1.Audit{RecordResponse: true}
				return tp
			},
			fieldPolicy: &toolv1.FieldPolicy{
				Read:   toolv1.Clearance_CLEARANCE_RESTRICTED,
				OnDeny: maskRedaction(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			streaming := c.rule == "L24"
			methods := []methodSpec{{name: "Get", tool: c.tool(), streaming: streaming}}
			methods = append(methods, c.methods...)
			fixtureName := "eff_" + c.rule + "_" + strings.ReplaceAll(c.name, " ", "_")
			fp := c.fieldPolicy
			if fp == nil {
				fp = validField()
			}
			fds := lintFixture(t, fixtureName, fp, nil, methods...)
			diags := compiler.Lint(fds)
			if !hasRule(diags, c.rule, c.warn) {
				t.Fatalf("%s: expected %s (warn=%v), got %v", c.name, c.rule, c.warn, diags)
			}
		})
	}
}

// TestLintL22AcceptsRecordResponseWithoutRestrictedFields is the positive
// half of L22: record_response on a tool whose output has no RESTRICTED
// field must not fire L22.
func TestLintL22AcceptsRecordResponseWithoutRestrictedFields(t *testing.T) {
	tp := effTool("read_public")
	tp.Audit = &toolv1.Audit{RecordResponse: true}

	fds := lintFixture(t, "l22_good", validField(), nil, // validField() is CLEARANCE_PUBLIC
		methodSpec{name: "Get", tool: tp})
	diags := compiler.Lint(fds)
	if hasRule(diags, "L22", true) {
		t.Fatalf("record_response over a PUBLIC-only output should not trigger L22, got %v", diags)
	}
}

// TestLintL22RequiresRecordResponse pins the `recordResponse &&` conjunct in
// L22: a RESTRICTED output field alone, with audit.record_response left
// unset (Audit nil, so it defaults to false), must not trigger L22. Without
// this test, dropping that conjunct from the L22 check would leave the
// entire suite green.
func TestLintL22RequiresRecordResponse(t *testing.T) {
	tp := effTool("read_secret_no_record")
	// tp.Audit intentionally left nil: record_response defaults to false.

	fds := lintFixture(t, "l22_no_record_response", &toolv1.FieldPolicy{
		Read:   toolv1.Clearance_CLEARANCE_RESTRICTED,
		OnDeny: maskRedaction(),
	}, nil, methodSpec{name: "Get", tool: tp})
	diags := compiler.Lint(fds)
	if hasRule(diags, "L22", true) {
		t.Fatalf("RESTRICTED field without audit.record_response should not trigger L22, got %v", diags)
	}
}

// TestLintL12AcceptsARegisteredCompensatingTool is the positive half of L12:
// naming a compensating_tool that IS registered must not fire L12.
func TestLintL12AcceptsARegisteredCompensatingTool(t *testing.T) {
	main := effTool("main_op")
	main.Effects = &toolv1.Effects{
		Reversibility:    toolv1.Reversibility_REVERSIBILITY_COMPENSABLE,
		CompensatingTool: "undo_op",
	}
	undo := effTool("undo_op")

	fds := lintFixture(t, "l12_good", validField(), nil,
		methodSpec{name: "MainOp", tool: main},
		methodSpec{name: "UndoOp", tool: undo},
	)
	diags := compiler.Lint(fds)
	if hasRule(diags, "L12", false) {
		t.Fatalf("registered compensating_tool should not trigger L12, got %v", diags)
	}
}

// TestLintL13SilencedByNotes covers the amendment case explicitly: the same
// irreversible+notify shape as the L13 warning case above, but with
// approval.notes set, must NOT produce an L13 diagnostic.
func TestLintL13SilencedByNotes(t *testing.T) {
	tp := effTool("notify_irreversible_documented")
	tp.Verb = toolv1.Verb_VERB_WRITE
	tp.Effects = &toolv1.Effects{Reversibility: toolv1.Reversibility_REVERSIBILITY_NONE}
	tp.Approval = &toolv1.Approval{
		Mode:  toolv1.Approval_MODE_NOTIFY,
		Notes: "intended: irreversible archive purge, notify only",
	}

	fds := lintFixture(t, "l13_silenced", validField(), nil,
		methodSpec{name: "Get", tool: tp})
	diags := compiler.Lint(fds)
	if hasRule(diags, "L13", true) {
		t.Fatalf("approval.notes should silence L13, got %v", diags)
	}
}

// TestLintEffectsAcceptsAWellFormedDestructiveTool builds a destructive tool
// that satisfies every rule in this set at once (grant, fail-closed audit
// with retention, a registered compensating tool, notified/external effects
// that are idempotent) and asserts it produces no error-level diagnostic.
// This guards against a rule that is accidentally unconditional.
func TestLintEffectsAcceptsAWellFormedDestructiveTool(t *testing.T) {
	main := effTool("archive_delete")
	main.Verb = toolv1.Verb_VERB_DESTRUCTIVE
	main.Effects = &toolv1.Effects{
		Reversibility:    toolv1.Reversibility_REVERSIBILITY_COMPENSABLE,
		CompensatingTool: "archive_restore",
		External:         true,
		Idempotent:       true,
	}
	main.Approval = &toolv1.Approval{Mode: toolv1.Approval_MODE_GRANT}
	main.Audit = &toolv1.Audit{
		Level: toolv1.Audit_LEVEL_AUDIT, FailClosed: true, RetainDays: 30,
	}
	undo := effTool("archive_restore")

	fds := lintFixture(t, "well_formed_destructive", validField(), nil,
		methodSpec{name: "ArchiveDelete", tool: main},
		methodSpec{name: "ArchiveRestore", tool: undo},
	)
	for _, d := range compiler.Lint(fds) {
		if !d.Warn {
			t.Fatalf("well-formed destructive fixture produced an error: %+v", d)
		}
	}
}
