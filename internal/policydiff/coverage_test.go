package policydiff

import (
	"strings"
	"testing"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// This file is the check that this package keeps up with the contract.
//
// The comparisons in policydiff.go are an explicit list of `if` blocks, not a
// walk over the descriptor, and that is deliberate: what a change MEANS for a
// caller is a judgement per field, and a generic walk could only ever say "this
// field differs". The cost of the decision is that nothing fails when
// `garm.tool.v1.ToolPolicy` grows a field nobody wrote an `if` for — which is
// how `sets` went uncompared from the day the field existed until v0.21.1, and
// how `catalogue diff` came to report "no policy changes" across a contract bump
// that granted two named people access to two tools.
//
// That failure was silent in the worst available way. A missing rule in a linter
// fails open and somebody eventually notices the thing it did not refuse; a
// missing comparison here produces a REASSURANCE. "No policy changes" is an
// answer a reviewer acts on, so a diff that cannot see a field is worse than no
// diff at all.
//
// So the descriptor is walked HERE, in a test, where the failure is a build
// failure. Every field of ToolPolicy and of FieldPolicy must be either compared
// or on an ignore list WITH A REASON — the reason is the point of the list,
// because "deliberately not policy" and "nobody noticed" look identical without
// one.
//
// The shape is copied rather than invented. `garm-ai/contracts` uses it for its
// tool-set rule (garm/tasks/v1/scoping_test.go) and `garm-ai/sink` for its lake
// columns (internal/row/contract_test.go); both caught a real omission this week.
//
// Two things make the walk a real check rather than a second list to keep in
// step:
//
//   - "compared" is proved BEHAVIOURALLY. Each entry is a pair of values
//     differing in that field alone, and the claim is that comparing them
//     yields a Change. Deleting an `if` from policydiff.go fails this file.
//   - Every comparison must report in BOTH directions. Narrowings are reported
//     on purpose — a narrowing nobody intended is an outage waiting for the
//     caller who relied on it — and three of them were missing (audit.level
//     raised, audit.retain_days raised, audit.fail_closed added) because each
//     was written as a single one-sided `if`. That class is closed here.

// ---------------------------------------------------------------- the walk

// toolMutation is a pair of ToolPolicy values differing in exactly one field,
// and the claim that compareTool reports it.
type toolMutation func() (before, after *toolv1.ToolPolicy)

// fieldMutation is the same for FieldPolicy and compareField.
type fieldMutation func() (before, after *toolv1.FieldPolicy)

func tp(mutate func(*toolv1.ToolPolicy)) *toolv1.ToolPolicy {
	p := &toolv1.ToolPolicy{
		Name: "t", Title: "T", Description: "d.",
		Verb: toolv1.Verb_VERB_READ, MinClearance: toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Compartments: []string{"pii"}, Sets: []string{"triage"},
		Approval: &toolv1.Approval{
			Mode:                 toolv1.Approval_MODE_GRANT,
			ApproverMinClearance: toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
			ApproverCompartments: []string{"fin"},
			MaxGrantAgeSeconds:   900,
			MaterialFields:       []string{"amount.units"},
		},
		Audit: &toolv1.Audit{
			Level: toolv1.Audit_LEVEL_AUDIT, RetainDays: 400, FailClosed: true,
		},
		Audience: []toolv1.Audience{toolv1.Audience_AUDIENCE_AGENT},
	}
	mutate(p)
	return p
}

func fp(mutate func(*toolv1.FieldPolicy)) *toolv1.FieldPolicy {
	p := &toolv1.FieldPolicy{
		Read:         toolv1.Clearance_CLEARANCE_CONFIDENTIAL,
		Write:        toolv1.Clearance_CLEARANCE_RESTRICTED,
		Compartments: []string{"pii"},
		OnDeny:       &toolv1.Redaction{Kind: &toolv1.Redaction_Omit{Omit: &toolv1.Omit{}}},
		AuditOnRead:  true,
	}
	mutate(p)
	return p
}

// toolPolicyCompared is one entry per ToolPolicy field this package compares.
// The value moves that field and nothing else.
var toolPolicyCompared = map[string]toolMutation{
	"verb": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Verb = toolv1.Verb_VERB_WRITE })
	},
	"min_clearance": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.MinClearance = toolv1.Clearance_CLEARANCE_INTERNAL })
	},
	"compartments": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Compartments = nil })
	},
	// The field this file exists for.
	"sets": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) { p.Sets = nil }),
			tp(func(p *toolv1.ToolPolicy) {})
	},
	"audience": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Audience = []toolv1.Audience{toolv1.Audience_AUDIENCE_PERSON}
			})
	},
	"approval": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.Mode = toolv1.Approval_MODE_NONE })
	},
	"audit": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Audit.Level = toolv1.Audit_LEVEL_LEDGER })
	},
	"authorization": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Authorization = &toolv1.Authorization{}
			})
	},
}

// toolPolicyIgnored is a ToolPolicy field this package deliberately does not
// compare, and why. Each reason is a claim that a change to that field is not a
// policy change, or that it is already reported by another route — and it was
// checked against what an engine actually reads, not against what the field
// looks like.
var toolPolicyIgnored = map[string]string{
	"name": "the tool's IDENTITY rather than its policy. It is half the FQN " +
		"(`<proto package>.<name>`) that Diff keys tools by, so a rename is " +
		"already reported — as the old tool removed and a new one added, which " +
		"is exactly what it is to a grant naming the old FQN and to a manifest " +
		"pinning it. Pinned by TestRenamingAToolIsReportedAsRemovedAndAdded.",

	"title": "prose. Carried to a caller in ListTools and used as a card " +
		"heading; nothing reads it to decide anything.",

	"description": "prose, published to callers. It is model-facing text and " +
		"therefore worth reviewing, but it is not the boundary: no clearance, " +
		"compartment, set or audit decision reads it. `git diff` shows it, and " +
		"putting it here would bury the handful of lines that need a human.",

	"exclude": "the tool stops existing. compiler.Tools drops an excluded " +
		"method, so flipping this reports as a tool added or removed rather " +
		"than as a change to a tool — which is what garmd does with it too: an " +
		"excluded method is never projected into a definition, so there is no " +
		"route and no listing. Pinned by TestExcludingAToolIsReportedAsRemoved.",

	"effects": "advisory at run time. idempotent, reversibility, external and " +
		"compensating_tool are projected into garmd's definition and compared " +
		"for declaration-identity, and nothing in the invoke chain branches on " +
		"them. What acts on them is `garm lint` at build time (L-rules push an " +
		"irreversible external tool toward an approval mode), and a linter " +
		"refusing the tree is a better report than a diff line.",

	"guidance": "prose for a model: when_to_use, when_not_to_use, on_error and " +
		"examples. The first three are published to callers and the examples " +
		"are dropped before they reach anyone. Nothing decides on any of them.",
}

// toolPolicyNested names the message-typed fields whose own fields get their own
// walk, because the entry above moves ONE sub-field and would be satisfied by a
// comparison that read only that one. `approval.material_fields` is the reason
// this matters: the `approval` entry moves `mode`, and a diff comparing mode
// alone would pass here while missing the sub-field that decides whether a
// grant binds the request it was given for.
//
// The walk ASSERTS this set covers every message-typed field it did not ignore,
// so adding a message to ToolPolicy fails until it is either walked or ignored
// with a reason.
var toolPolicyNested = map[string]bool{
	"approval":      true,
	"audit":         true,
	"authorization": true,
}

var approvalCompared = map[string]toolMutation{
	"mode": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.Mode = toolv1.Approval_MODE_NOTIFY })
	},
	"approver_min_clearance": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Approval.ApproverMinClearance = toolv1.Clearance_CLEARANCE_INTERNAL
			})
	},
	"approver_compartments": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.ApproverCompartments = nil })
	},
	"max_grant_age_seconds": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaxGrantAgeSeconds = 3600 })
	},
	"material_fields": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaterialFields = nil })
	},
}

var approvalIgnored = map[string]string{
	"notes": "prose, and the only ToolPolicy sub-field that reaches nobody at " +
		"all: garmd does not even project it into its definition, so it is not " +
		"published and not compared for mount identity.",
}

var auditCompared = map[string]toolMutation{
	"level": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Audit.Level = toolv1.Audit_LEVEL_LEDGER })
	},
	"retain_days": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Audit.RetainDays = 30 })
	},
	"fail_closed": func() (*toolv1.ToolPolicy, *toolv1.ToolPolicy) {
		return tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Audit.FailClosed = false })
	},
}

var auditIgnored = map[string]string{
	"record_request": "a tool declaring it does not mount. garmd refuses the " +
		"tool by name — \"a ledger Event carries no payload to put them in\" — " +
		"so a change here is not a change to what is recorded about a call; it " +
		"is a tool that will not start. That belongs to the daemon's refusal " +
		"and to lint rule L22, not to a diff line about disclosure.",
	"record_response": "the same refusal, the same line of garmd, and L22 also " +
		"refuses it over a RESTRICTED output field at build time.",
}

var authorizationIgnored = map[string]string{
	"fga": "no engine reads inside the block. garmd projects the whole " +
		"annotation down to one `HasAuthorization` bool and refuses to mount " +
		"the tool without a checker configured behind it, and the relation, " +
		"object type and field selectors reach no daemon at all — there is no " +
		"production FGAChecker in the estate. So the diff compares the block's " +
		"PRESENCE, which is the only thing that changes a decision, and " +
		"reporting a change to `relation` would be describing a judgement " +
		"nothing makes. When a checker ships, this entry comes off the list.",
}

var fieldPolicyCompared = map[string]fieldMutation{
	"read": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.Read = toolv1.Clearance_CLEARANCE_INTERNAL })
	},
	"write": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.Write = toolv1.Clearance_CLEARANCE_INTERNAL })
	},
	"compartments": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.Compartments = nil })
	},
	"on_deny": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) {
				p.OnDeny = &toolv1.Redaction{Kind: &toolv1.Redaction_EmailDomain{
					EmailDomain: &toolv1.EmailDomain{}}}
			})
	},
	"audit_on_read": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.AuditOnRead = false })
	},
	"source": func() (*toolv1.FieldPolicy, *toolv1.FieldPolicy) {
		return fp(func(p *toolv1.FieldPolicy) { p.Source = toolv1.FieldPolicy_SOURCE_RUNNER }),
			fp(func(p *toolv1.FieldPolicy) {})
	},
}

// Empty, and that is a statement rather than an oversight: every field of
// FieldPolicy decides something a caller can feel, and every one is compared.
var fieldPolicyIgnored = map[string]string{}

// ---------------------------------------------------------------- assertions

func descriptorOf(m interface{ ProtoReflect() protoreflect.Message }) protoreflect.MessageDescriptor {
	return m.ProtoReflect().Descriptor()
}

// walkTool asserts every field of d is compared, ignored with a reason, or both
// — never neither and never both.
func walkTool(t *testing.T, d protoreflect.MessageDescriptor,
	compared map[string]toolMutation, ignored map[string]string, nested map[string]bool) {
	t.Helper()

	fields := d.Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		mut, isCompared := compared[name]
		reason, isIgnored := ignored[name]

		switch {
		case isCompared && isIgnored:
			t.Errorf("%s.%s is both compared and on the ignore list (%q); it is one or the other",
				d.FullName(), name, reason)
		case isIgnored:
			if reason == "" {
				t.Errorf("%s.%s is on the ignore list with no reason beside it; an entry "+
					"without a reason cannot be told from an oversight", d.FullName(), name)
			}
		case !isCompared:
			t.Errorf("%s.%s reaches no comparison in policydiff.go, so `catalogue diff` "+
				"will report \"no policy changes\" across a change to it. Compare it, or "+
				"add it to the ignore list in this file WITH THE REASON a change to it is "+
				"not a policy change.", d.FullName(), name)
		default:
			assertToolReported(t, d, name, mut)
		}

		// A message-typed field is a subtree, and the entry above moves one
		// field of it. Either the subtree has a walk of its own, or the ignore
		// reason covers it — there is no third state here either.
		if f := fields.Get(i); f.Kind() == protoreflect.MessageKind && !f.IsMap() {
			switch {
			case isIgnored && nested[name]:
				t.Errorf("%s.%s is on the ignore list and also claims a nested walk",
					d.FullName(), name)
			case !isIgnored && !nested[name]:
				t.Errorf("%s.%s is a message and nothing walks its fields, so a policy "+
					"sub-field added to %s would be compared by nobody and reported by "+
					"nobody. Add it to toolPolicyNested with a walk, or ignore %s with a "+
					"reason that covers the whole subtree.",
					d.FullName(), name, f.Message().FullName(), name)
			}
		}
	}

	for name := range compared {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Errorf("this file claims to compare %s.%s and the contract has no such field; "+
				"a renamed field leaves the old name here and the new one unchecked",
				d.FullName(), name)
		}
	}
	for name := range ignored {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Errorf("this file ignores %s.%s and the contract has no such field",
				d.FullName(), name)
		}
	}
}

// assertToolReported is what makes "compared" a fact rather than a second list.
// It runs the real comparison, in both directions.
func assertToolReported(t *testing.T, d protoreflect.MessageDescriptor, name string, mut toolMutation) {
	t.Helper()
	before, after := mut()

	forward := compareTool("x.y.tool", before, after)
	if len(forward) == 0 {
		t.Errorf("%s.%s is claimed compared and compareTool reports nothing when it moves. "+
			"Either the comparison was removed, or the mutation in this file no longer "+
			"moves the field.", d.FullName(), name)
		return
	}
	// A narrowing nobody intended is an outage waiting for the caller who
	// relied on it, so every comparison has to speak in both directions. Three
	// did not until v0.21.1, each written as a single one-sided `if`.
	if reverse := compareTool("x.y.tool", after, before); len(reverse) == 0 {
		t.Errorf("%s.%s is reported when it moves one way and silent when it moves back. "+
			"A one-sided comparison hides every narrowing of that field, and a narrowing "+
			"nobody intended is an outage.", d.FullName(), name)
	}
	for _, c := range forward {
		if c.What == "" || c.Why == "" {
			t.Errorf("%s.%s reports a change with no %s: the direction alone does not tell "+
				"a reviewer what it means", d.FullName(), name,
				map[bool]string{true: "subject line", false: "consequence"}[c.What == ""])
		}
	}
}

// ToolPolicy is the annotation `catalogue diff` exists to compare. Every field
// of it is compared, or it is on the ignore list with a reason, and there is no
// third state.
func TestEveryToolPolicyFieldIsComparedOrDeliberatelyNot(t *testing.T) {
	walkTool(t, descriptorOf(&toolv1.ToolPolicy{}),
		toolPolicyCompared, toolPolicyIgnored, toolPolicyNested)
}

// Approval gets its own walk because the ToolPolicy entry moves `mode` alone,
// and a comparison reading only mode would satisfy that entry while missing
// material_fields — the sub-field that decides whether a human approved THIS
// call or any call to the tool inside the window.
func TestEveryApprovalFieldIsComparedOrDeliberatelyNot(t *testing.T) {
	walkTool(t, descriptorOf(&toolv1.Approval{}), approvalCompared, approvalIgnored, nil)
}

func TestEveryAuditFieldIsComparedOrDeliberatelyNot(t *testing.T) {
	walkTool(t, descriptorOf(&toolv1.Audit{}), auditCompared, auditIgnored, nil)
}

func TestEveryAuthorizationFieldIsComparedOrDeliberatelyNot(t *testing.T) {
	walkTool(t, descriptorOf(&toolv1.Authorization{}), nil, authorizationIgnored, nil)
}

// FieldPolicy is the other half of the annotation: who reads a value, who may
// set one, and what a denied caller receives instead.
func TestEveryFieldPolicyFieldIsComparedOrDeliberatelyNot(t *testing.T) {
	d := descriptorOf(&toolv1.FieldPolicy{})
	fields := d.Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		mut, isCompared := fieldPolicyCompared[name]
		reason, isIgnored := fieldPolicyIgnored[name]

		switch {
		case isCompared && isIgnored:
			t.Errorf("%s.%s is both compared and ignored (%q)", d.FullName(), name, reason)
		case isIgnored:
			if reason == "" {
				t.Errorf("%s.%s is on the ignore list with no reason beside it", d.FullName(), name)
			}
		case !isCompared:
			t.Errorf("%s.%s reaches no comparison in policydiff.go. Compare it, or add it "+
				"to fieldPolicyIgnored WITH THE REASON.", d.FullName(), name)
		default:
			before, after := mut()
			if len(compareField("m.f", before, after)) == 0 {
				t.Errorf("%s.%s is claimed compared and compareField reports nothing", d.FullName(), name)
			}
			if len(compareField("m.f", after, before)) == 0 {
				t.Errorf("%s.%s is reported one way and silent on the way back", d.FullName(), name)
			}
		}
	}
	for name := range fieldPolicyCompared {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Errorf("this file claims to compare %s.%s and no such field exists", d.FullName(), name)
		}
	}
}

// ---------------------------------------------------------------- the two indirect ones

// `name` is on the ignore list because a rename is reported through the FQN
// Diff keys by, not through compareTool. That is a claim about a different
// function, so it is pinned here: the ignore entry would otherwise be a
// statement nothing checks.
func TestRenamingAToolIsReportedAsRemovedAndAdded(t *testing.T) {
	// compareTool is the wrong level — the key is built in toolsByFQN — so this
	// asserts the property the ignore reason depends on: the FQN carries the
	// declared name.
	before := tp(func(p *toolv1.ToolPolicy) {})
	after := tp(func(p *toolv1.ToolPolicy) { p.Name = "renamed" })
	if before.GetName() == after.GetName() {
		t.Fatal("the fixture did not change")
	}
	if got := compareTool("x.y.tool", before, after); len(got) != 0 {
		t.Errorf("compareTool reports a rename (%v), which means two changes are reported "+
			"for one rename: this and the removed/added pair", got)
	}
	// And the property itself, at the level that owns it.
	if k := toolKey("x.y", before.GetName()); k != "x.y.t" {
		t.Errorf("a tool's key is %q, want it to carry the declared name: a rename has to "+
			"present as the old FQN removed and a new one added, because that is what it is "+
			"to a grant naming the old FQN", k)
	}
}

// `exclude` is on the ignore list because compiler.Tools drops an excluded
// method, so Diff never sees the policy at all and reports the tool as removed.
// Pinned for the same reason as the rename: the entry is a claim about another
// package.
func TestExcludingAToolIsReportedAsRemoved(t *testing.T) {
	if got := compareTool("x.y.tool",
		tp(func(p *toolv1.ToolPolicy) {}),
		tp(func(p *toolv1.ToolPolicy) { p.Exclude = true })); len(got) != 0 {
		t.Errorf("compareTool reports exclude (%v). An excluded method is not a tool with "+
			"different policy, it is a tool that does not exist — compiler.Tools drops it "+
			"and Diff reports it removed. Reporting it twice trains people to skim.", got)
	}
}

// ---------------------------------------------------------------- directions

// The walk above proves each field is COMPARED and speaks both ways. It does
// not prove the direction is right, and the direction is the product: a
// widening bucket with a narrowing in it is a bucket people learn to skim.
//
// These assert the judgement on the comparisons the field audit added in
// v0.21.1. They read the policy values directly rather than through a proto
// text fixture because each one is a sub-field of a sub-message, and threading
// them through the fixture would add a parameter per sub-message while reaching
// the same function by the same call. The end-to-end path is covered by the
// `sets` tests in policydiff_test.go, which compile real protos.
func TestTheDirectionOfEachComparisonTheFieldAuditAdded(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after *toolv1.ToolPolicy
		want          string
		dir           Direction
	}{
		{"an approver's clearance lowered",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Approval.ApproverMinClearance = toolv1.Clearance_CLEARANCE_INTERNAL
			}),
			"approver_min_clearance", Widening},

		{"an approver's clearance raised",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Approval.ApproverMinClearance = toolv1.Clearance_CLEARANCE_RESTRICTED
			}),
			"approver_min_clearance", Narrowing},

		{"an approver's compartment dropped",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.ApproverCompartments = nil }),
			"approver_compartments", Widening},

		// One approval buys more calls.
		{"a grant's window lengthened",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaxGrantAgeSeconds = 7200 }),
			"max_grant_age_seconds", Widening},

		{"a grant's window shortened",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaxGrantAgeSeconds = 60 }),
			"max_grant_age_seconds", Narrowing},

		// Not a widening: garmd refuses to start when a MODE_GRANT tool
		// declares no ceiling, so the change is either a tool that no longer
		// needs a grant or a deployment that will not boot.
		{"a grant's ceiling removed altogether",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaxGrantAgeSeconds = 0 }),
			"max_grant_age_seconds", Unclear},

		// The worst of the holes the audit found. Approval is still required
		// and stops saying what was approved.
		{"material_fields dropped entirely",
			tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaterialFields = nil }),
			"material_fields", Widening},

		{"material_fields declared for the first time",
			tp(func(p *toolv1.ToolPolicy) { p.Approval.MaterialFields = nil }),
			tp(func(p *toolv1.ToolPolicy) {}),
			"material_fields", Narrowing},

		{"audit.level raised", tp(func(p *toolv1.ToolPolicy) { p.Audit.Level = toolv1.Audit_LEVEL_LEDGER }),
			tp(func(p *toolv1.ToolPolicy) {}), "audit.level", Narrowing},

		{"audit.retain_days raised", tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Audit.RetainDays = 3650 }), "audit.retain_days", Narrowing},

		{"audit.fail_closed added",
			tp(func(p *toolv1.ToolPolicy) { p.Audit.FailClosed = false }),
			tp(func(p *toolv1.ToolPolicy) {}), "audit.fail_closed added", Narrowing},

		{"an authorization block added", tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) { p.Authorization = &toolv1.Authorization{} }),
			"authorization block added", Narrowing},

		// An audience is a listing rule and not a gate, in either direction.
		// Mistaking it for a gate is how decide_task came to be unreachable.
		{"an audience widened to a person", tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Audience = []toolv1.Audience{
					toolv1.Audience_AUDIENCE_AGENT, toolv1.Audience_AUDIENCE_PERSON}
			}),
			"audience", Unclear},

		{"an audience narrowed to a person only", tp(func(p *toolv1.ToolPolicy) {}),
			tp(func(p *toolv1.ToolPolicy) {
				p.Audience = []toolv1.Audience{toolv1.Audience_AUDIENCE_PERSON}
			}),
			"audience", Unclear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := only(t, compareTool("x.y.tool", tc.before, tc.after), tc.want)
			if c.Direction != tc.dir {
				t.Errorf("%s: direction = %s, want %s\n  %s", tc.want, c.Direction, tc.dir, c.Why)
			}
			if c.Why == "" {
				t.Error("no consequence given")
			}
		})
	}
}

// An empty audience means AUDIENCE_AGENT, so declaring that explicitly is not a
// change. Reporting it as one would put a line in front of a reviewer for a
// no-op, and a diff that cries wolf is one nobody reads.
func TestDeclaringTheDefaultAudienceIsNotAChange(t *testing.T) {
	got := compareTool("x.y.tool",
		tp(func(p *toolv1.ToolPolicy) { p.Audience = nil }),
		tp(func(p *toolv1.ToolPolicy) {}))
	if len(got) != 0 {
		t.Errorf("declaring the default audience produced %d change(s): %v", len(got), got)
	}
}

// The FieldPolicy half of the audit: `write` resolved through its fallback, and
// `source` handing a runner's field back to the caller.
func TestTheDirectionOfTheFieldPolicyComparisonsTheAuditAdded(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after *toolv1.FieldPolicy
		want          string
		dir           Direction
	}{
		{"a write bar lowered", fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.Write = toolv1.Clearance_CLEARANCE_INTERNAL }),
			"write ", Widening},
		{"a write bar raised",
			fp(func(p *toolv1.FieldPolicy) { p.Write = toolv1.Clearance_CLEARANCE_INTERNAL }),
			fp(func(p *toolv1.FieldPolicy) {}), "write ", Narrowing},
		{"a runner's field handed to the caller",
			fp(func(p *toolv1.FieldPolicy) { p.Source = toolv1.FieldPolicy_SOURCE_RUNNER }),
			fp(func(p *toolv1.FieldPolicy) {}), "source SOURCE_RUNNER", Widening},
		{"a caller's field taken by the runner", fp(func(p *toolv1.FieldPolicy) {}),
			fp(func(p *toolv1.FieldPolicy) { p.Source = toolv1.FieldPolicy_SOURCE_RUNNER }),
			"→ SOURCE_RUNNER", Narrowing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := only(t, compareField("m.f", tc.before, tc.after), tc.want)
			if c.Direction != tc.dir {
				t.Errorf("%s: direction = %s, want %s\n  %s", tc.want, c.Direction, tc.dir, c.Why)
			}
		})
	}
}

// An unset `write` means the same as `read`, so a field whose read bar moves has
// had its write bar moved with it. A comparison reading the raw field would say
// nothing about who may now SET the value, which is a different consequence from
// who may see it.
func TestAMovedReadBarMovesTheInheritedWriteBarWithIt(t *testing.T) {
	got := compareField("m.f",
		&toolv1.FieldPolicy{Read: toolv1.Clearance_CLEARANCE_RESTRICTED},
		&toolv1.FieldPolicy{Read: toolv1.Clearance_CLEARANCE_PUBLIC})
	var sawWrite bool
	for _, c := range got {
		if strings.HasPrefix(c.What, "write") {
			sawWrite = true
			if c.Direction != Widening {
				t.Errorf("the inherited write bar moved %s, want widening", c.Direction)
			}
		}
	}
	if !sawWrite {
		t.Errorf("a read bar moved from RESTRICTED to PUBLIC and no write change was "+
			"reported. An unset write is the read value, so the set of callers who may "+
			"SET this field moved too, and that is not the same consequence as who may "+
			"see it. Got: %v", got)
	}
}

// only is find's twin for the internal tests: exactly one change matching a
// substring, so a comparison that fires twice for one movement fails rather
// than passing on its first hit.
func only(t *testing.T, cs []Change, want string) Change {
	t.Helper()
	var hits []Change
	for _, c := range cs {
		if strings.Contains(c.What, want) {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one change containing %q, got %d:\n%v", want, len(hits), cs)
	}
	return hits[0]
}
