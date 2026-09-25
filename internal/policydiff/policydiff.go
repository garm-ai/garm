// Package policydiff reports what changed about POLICY between two
// catalogues, and which direction it moved.
//
// It exists because the annotations are the policy, so lowering a
// min_clearance is a security decision that arrives as an ordinary proto diff.
// CODEOWNERS routes that to a domain owner, who is not a security reviewer,
// and at any real scale nobody reads every diff. The linter catches policy
// that is MALFORMED and cannot catch policy that is WRONG.
//
// The output is deliberately not "these fields changed". A reviewer needs to
// know the consequence — whether strictly more callers can now reach a tool or
// read a field — so every change carries a direction and a sentence about what
// it means.
//
// Direction is decided by lattice movement where there is a lattice, and
// admitted to be Unclear where there is not. A verb changing from READ to
// WRITE takes it away from one set of callers and gives it to another; a
// redaction changing from omit to mask discloses more of a value to the same
// callers. Neither is a widening in the sense that matters, and reporting them
// as one would train people to skim the widening list, which is the only
// outcome worse than not having it.
package policydiff

import (
	"fmt"
	"sort"
	"strings"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/internal/compiler"
	"github.com/garm-ai/garm/policy"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Direction is which way a change moved the boundary.
type Direction int

const (
	// Widening: strictly more callers can reach something, or strictly less
	// is recorded about it. The bucket a gate fails on.
	Widening Direction = iota
	// Narrowing: strictly fewer callers, or more recorded. Safe by
	// construction, and still reported — a narrowing nobody intended is an
	// outage waiting for the caller who relied on it.
	Narrowing
	// Unclear: a real change whose direction is not a lattice move. Needs a
	// human, and says so rather than guessing.
	Unclear
)

func (d Direction) String() string {
	switch d {
	case Widening:
		return "widening"
	case Narrowing:
		return "narrowing"
	default:
		return "unclear"
	}
}

// Change is one policy movement, named by its subject and its consequence.
type Change struct {
	Direction Direction
	Subject   string // a tool FQN, or a fully-qualified message field
	What      string // "min_clearance CONFIDENTIAL → INTERNAL"
	Why       string // what that means for a caller
}

// Diff reports every policy change between two catalogues' descriptors.
//
// Both sides are the full file set, so a tool present in one and absent in the
// other is reported as added or removed rather than silently skipped.
func Diff(before, after []protoreflect.FileDescriptor) ([]Change, error) {
	oldTools, err := toolsByFQN(before)
	if err != nil {
		return nil, err
	}
	newTools, err := toolsByFQN(after)
	if err != nil {
		return nil, err
	}

	var out []Change
	for _, fqn := range sortedKeys(oldTools, newTools) {
		o, inOld := oldTools[fqn]
		n, inNew := newTools[fqn]
		switch {
		case !inOld:
			out = append(out, Change{
				Direction: Widening,
				Subject:   fqn,
				What:      fmt.Sprintf("new tool, %s, min_clearance %s", verb(n.Policy), clearance(n.Policy)),
				Why: "a tool that did not exist is now reachable. New surface is " +
					"widening even when it is correct",
			})
		case !inNew:
			out = append(out, Change{
				Direction: Narrowing, Subject: fqn, What: "tool removed",
				Why: "anything calling it now gets not-found. Safe for disclosure, " +
					"breaking for a caller that relied on it",
			})
		default:
			out = append(out, compareTool(fqn, o.Policy, n.Policy)...)
		}
	}

	out = append(out, compareFields(before, after)...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction < out[j].Direction
		}
		return out[i].Subject < out[j].Subject
	})
	return out, nil
}

func compareTool(fqn string, o, n *toolv1.ToolPolicy) []Change {
	var out []Change
	add := func(d Direction, what, why string) {
		out = append(out, Change{Direction: d, Subject: fqn, What: what, Why: why})
	}

	if o.GetMinClearance() != n.GetMinClearance() {
		d, why := Narrowing, "fewer callers can reach it"
		if rank(n.GetMinClearance()) < rank(o.GetMinClearance()) {
			d, why = Widening, "every caller cleared below the old bar can now reach it"
		}
		add(d, fmt.Sprintf("min_clearance %s → %s", o.GetMinClearance(), n.GetMinClearance()), why)
	}

	for _, c := range removed(o.GetCompartments(), n.GetCompartments()) {
		add(Widening, fmt.Sprintf("compartment %q no longer required", c),
			"callers without that compartment can now reach it. Compartments are "+
				"need-to-know, so this is a wider audience and not a lower bar")
	}
	for _, c := range removed(n.GetCompartments(), o.GetCompartments()) {
		add(Narrowing, fmt.Sprintf("compartment %q now required", c),
			"callers holding it keep access; everyone else loses it")
	}

	if o.GetVerb() != n.GetVerb() {
		add(Unclear, fmt.Sprintf("verb %s → %s", o.GetVerb(), n.GetVerb()),
			"a verb change moves a tool between caller sets rather than up or "+
				"down. Check who holds the new verb, and whether the tool's "+
				"effects still match it")
	}

	if approvalRank(n.GetApproval().GetMode()) < approvalRank(o.GetApproval().GetMode()) {
		add(Widening, fmt.Sprintf("approval.mode %s → %s",
			o.GetApproval().GetMode(), n.GetApproval().GetMode()),
			"the tool runs with less supervision than it used to")
	} else if approvalRank(n.GetApproval().GetMode()) > approvalRank(o.GetApproval().GetMode()) {
		add(Narrowing, fmt.Sprintf("approval.mode %s → %s",
			o.GetApproval().GetMode(), n.GetApproval().GetMode()),
			"the tool now requires supervision it did not")
	}

	if auditRank(n.GetAudit().GetLevel()) < auditRank(o.GetAudit().GetLevel()) {
		add(Widening, fmt.Sprintf("audit.level %s → %s",
			o.GetAudit().GetLevel(), n.GetAudit().GetLevel()),
			"less is recorded about a call than was recorded before")
	}
	if o.GetAudit().GetFailClosed() && !n.GetAudit().GetFailClosed() {
		add(Widening, "audit.fail_closed removed",
			"the call may now proceed when its record cannot be written")
	}
	if n.GetAudit().GetRetainDays() < o.GetAudit().GetRetainDays() {
		add(Widening, fmt.Sprintf("audit.retain_days %d → %d",
			o.GetAudit().GetRetainDays(), n.GetAudit().GetRetainDays()),
			"the record is kept for less time than it was")
	}
	if o.GetAuthorization() != nil && n.GetAuthorization() == nil {
		add(Widening, "authorization block removed",
			"per-instance authorization no longer runs; clearance and "+
				"compartments are all that stands in front of the object")
	}
	return out
}

// compareFields walks the messages a tool actually exposes.
//
// Scoped to tool inputs and outputs rather than every message in the tree: a
// message no tool names is not a disclosure surface, and reporting it would
// bury the ones that are.
func compareFields(before, after []protoreflect.FileDescriptor) []Change {
	o := fieldPolicies(before)
	n := fieldPolicies(after)

	var out []Change
	for _, path := range sortedKeys(o, n) {
		op, inOld := o[path]
		np, inNew := n[path]
		switch {
		case !inOld:
			out = append(out, Change{
				Direction: Unclear, Subject: path,
				What: fmt.Sprintf("new field, readable at %s", np.GetRead()),
				Why: "new data in a governed message. Whether that widens anything " +
					"depends on what it holds, which this cannot know",
			})
		case !inNew:
			out = append(out, Change{
				Direction: Narrowing, Subject: path, What: "field removed",
				Why: "nobody can read it. Breaking for a caller that used it",
			})
		default:
			out = append(out, compareField(path, op, np)...)
		}
	}
	return out
}

func compareField(path string, o, n *toolv1.FieldPolicy) []Change {
	var out []Change
	add := func(d Direction, what, why string) {
		out = append(out, Change{Direction: d, Subject: path, What: what, Why: why})
	}

	if o.GetRead() != n.GetRead() {
		d, why := Narrowing, "fewer callers see the value"
		if rank(n.GetRead()) < rank(o.GetRead()) {
			d, why = Widening, "every caller cleared below the old bar now reads the value in full"
		}
		add(d, fmt.Sprintf("read %s → %s", o.GetRead(), n.GetRead()), why)
	}
	for _, c := range removed(o.GetCompartments(), n.GetCompartments()) {
		add(Widening, fmt.Sprintf("compartment %q no longer required to read", c),
			"a wider audience reads the value in full")
	}
	for _, c := range removed(n.GetCompartments(), o.GetCompartments()) {
		add(Narrowing, fmt.Sprintf("compartment %q now required to read", c),
			"callers without it get the redaction instead")
	}
	if o.GetAuditOnRead() && !n.GetAuditOnRead() {
		add(Widening, "audit_on_read removed",
			"reading it is no longer recorded. The disclosure is unchanged; the "+
				"evidence that it happened is gone")
	}
	if !o.GetAuditOnRead() && n.GetAuditOnRead() {
		add(Narrowing, "audit_on_read added", "reads are now recorded")
	}
	if redactionKind(o.GetOnDeny()) != redactionKind(n.GetOnDeny()) {
		add(Unclear, fmt.Sprintf("on_deny %s → %s",
			redactionKind(o.GetOnDeny()), redactionKind(n.GetOnDeny())),
			"what a DENIED caller receives changed. Redactions are not ordered — "+
				"whether a domain discloses more than a last-four is a judgement "+
				"about the value, not about the shape")
	}
	return out
}

// fieldPolicies is every field of every message a tool names, with the policy
// actually in force — the field's own, or the message default it inherits.
//
// Resolved rather than raw: a field inheriting a default that moved has moved,
// and a diff reading only explicit annotations would miss it entirely. That is
// the change most likely to be made carelessly, because it is made in one
// place and lands on every field of the message.
func fieldPolicies(fds []protoreflect.FileDescriptor) map[string]*toolv1.FieldPolicy {
	out := map[string]*toolv1.FieldPolicy{}
	tools, err := compiler.Tools(fds)
	if err != nil {
		return out
	}
	seen := map[protoreflect.FullName]bool{}
	for _, t := range tools {
		for _, md := range []protoreflect.MessageDescriptor{t.Method.Input(), t.Method.Output()} {
			collectFields(md, out, seen, 0)
		}
	}
	return out
}

// maxDepth bounds a walk that a self-referential message would otherwise take
// forever. Eight is deeper than any governed message has a right to be.
const maxDepth = 8

func collectFields(md protoreflect.MessageDescriptor, out map[string]*toolv1.FieldPolicy,
	seen map[protoreflect.FullName]bool, depth int) {
	if md == nil || depth > maxDepth || seen[md.FullName()] {
		return
	}
	seen[md.FullName()] = true
	def := policy.MessageDefaultPolicy(md)
	for i := 0; i < md.Fields().Len(); i++ {
		f := md.Fields().Get(i)
		out[string(md.FullName())+"."+string(f.Name())] = policy.FieldPolicyOf(f, def)
		if f.Kind() == protoreflect.MessageKind && !f.IsMap() {
			collectFields(f.Message(), out, seen, depth+1)
		}
	}
}

func toolsByFQN(fds []protoreflect.FileDescriptor) (map[string]compiler.Tool, error) {
	ts, err := compiler.Tools(fds)
	if err != nil {
		return nil, err
	}
	out := make(map[string]compiler.Tool, len(ts))
	for _, t := range ts {
		out[string(t.Method.ParentFile().Package())+"."+t.Name] = t
	}
	return out, nil
}

// rank orders clearance so a comparison can say which way it moved. Zero for
// anything unrecognised, which sorts below PUBLIC — an unknown clearance is
// not a high one.
func rank(c toolv1.Clearance) int { return int(c.Number()) }

func approvalRank(m toolv1.Approval_Mode) int {
	switch m {
	case toolv1.Approval_MODE_GRANT:
		return 3
	case toolv1.Approval_MODE_NOTIFY:
		return 2
	case toolv1.Approval_MODE_NONE:
		return 1
	default:
		return 0
	}
}

func auditRank(l toolv1.Audit_Level) int {
	switch l {
	case toolv1.Audit_LEVEL_AUDIT:
		return 2
	case toolv1.Audit_LEVEL_LEDGER:
		return 1
	default:
		return 0
	}
}

func redactionKind(r *toolv1.Redaction) string {
	if r == nil {
		return "omit"
	}
	if r.GetKind() == nil {
		return "omit"
	}
	name := fmt.Sprintf("%T", r.GetKind())
	if i := strings.LastIndex(name, "_"); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return name
}

func verb(p *toolv1.ToolPolicy) string      { return p.GetVerb().String() }
func clearance(p *toolv1.ToolPolicy) string { return p.GetMinClearance().String() }

// removed returns the members of a not present in b.
func removed(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, s := range b {
		have[s] = true
	}
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](a, b map[string]V) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
