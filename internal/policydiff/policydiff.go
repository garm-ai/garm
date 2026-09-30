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

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/policy"
	"github.com/garm-ai/garm/internal/compiler"
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
				What: fmt.Sprintf("new tool, %s, min_clearance %s, %s",
					verb(n.Policy), clearance(n.Policy), sets(n.Policy)),
				Why: "a tool that did not exist is now reachable. New surface is " +
					"widening even when it is correct. The sets bound who gets it: " +
					"a tool in no set reaches only an unscoped caller, and one in a " +
					"set reaches every session scoped to it",
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

	out = append(out, compareApproval(fqn, o.GetApproval(), n.GetApproval())...)

	if auditRank(n.GetAudit().GetLevel()) < auditRank(o.GetAudit().GetLevel()) {
		add(Widening, fmt.Sprintf("audit.level %s → %s",
			o.GetAudit().GetLevel(), n.GetAudit().GetLevel()),
			"less is recorded about a call than was recorded before")
	}
	if auditRank(n.GetAudit().GetLevel()) > auditRank(o.GetAudit().GetLevel()) {
		add(Narrowing, fmt.Sprintf("audit.level %s → %s",
			o.GetAudit().GetLevel(), n.GetAudit().GetLevel()),
			"more is recorded, and LEVEL_AUDIT is what makes garmd write the "+
				"stream at all. A tool that moves up to it will not mount without "+
				"a Sink configured behind it")
	}
	if o.GetAudit().GetFailClosed() && !n.GetAudit().GetFailClosed() {
		add(Widening, "audit.fail_closed removed",
			"the call may now proceed when its record cannot be written")
	}
	if !o.GetAudit().GetFailClosed() && n.GetAudit().GetFailClosed() {
		add(Narrowing, "audit.fail_closed added",
			"the call is refused before it runs when its record cannot be "+
				"written. Correct, and an availability change: the tool now fails "+
				"when the ledger does")
	}
	if n.GetAudit().GetRetainDays() < o.GetAudit().GetRetainDays() {
		add(Widening, fmt.Sprintf("audit.retain_days %d → %d",
			o.GetAudit().GetRetainDays(), n.GetAudit().GetRetainDays()),
			"the record is kept for less time than it was")
	}
	if n.GetAudit().GetRetainDays() > o.GetAudit().GetRetainDays() {
		add(Narrowing, fmt.Sprintf("audit.retain_days %d → %d",
			o.GetAudit().GetRetainDays(), n.GetAudit().GetRetainDays()),
			"the record is kept for longer. garmd compares this against the "+
				"retention the Sink actually has, so a tool asking for more than "+
				"the lake keeps will not mount")
	}

	// Only the block's PRESENCE, because only its presence is read: garmd
	// projects the annotation down to one `HasAuthorization` bool and refuses
	// to mount the tool without a checker behind it. The relation, the object
	// type and the field selectors inside it reach no engine at all, so a diff
	// reporting a change to them would be describing a decision nothing makes.
	if o.GetAuthorization() != nil && n.GetAuthorization() == nil {
		add(Widening, "authorization block removed",
			"per-instance authorization no longer runs; clearance and "+
				"compartments are all that stands in front of the object")
	}
	if o.GetAuthorization() == nil && n.GetAuthorization() != nil {
		add(Narrowing, "authorization block added",
			"a per-instance check now stands in front of the object, so holding "+
				"the clearance and the compartments is no longer sufficient. The "+
				"tool will not mount until a checker is configured")
	}

	out = append(out, compareSets(fqn, o.GetSets(), n.GetSets())...)
	out = append(out, compareAudience(fqn, o.GetAudience(), n.GetAudience())...)
	return out
}

// compareApproval reads the sub-fields that decide WHO may approve and WHAT a
// grant is good for. Every one of them is enforced — by garmd at spend time,
// by tasksd on the decision and by agentd as the predicate deciding who even
// sees the ask — so a change to any of them changes who can authorise a call.
func compareApproval(fqn string, o, n *toolv1.Approval) []Change {
	var out []Change
	add := func(d Direction, what, why string) {
		out = append(out, Change{Direction: d, Subject: fqn, What: what, Why: why})
	}

	if o.GetApproverMinClearance() != n.GetApproverMinClearance() {
		d, why := Narrowing, "a smaller group of people may approve it"
		if rank(n.GetApproverMinClearance()) < rank(o.GetApproverMinClearance()) {
			d, why = Widening, "everyone cleared below the old bar may now approve "+
				"the call. The bar on CALLING the tool has not moved; the bar on "+
				"authorising it has, which is the same disclosure reached one step later"
		}
		add(d, fmt.Sprintf("approval.approver_min_clearance %s → %s",
			o.GetApproverMinClearance(), n.GetApproverMinClearance()), why)
	}
	for _, c := range removed(o.GetApproverCompartments(), n.GetApproverCompartments()) {
		add(Widening, fmt.Sprintf("approval.approver_compartments %q no longer required", c),
			"a wider group of people may approve the call. Need-to-know applied "+
				"to the approver, not to the caller")
	}
	for _, c := range removed(n.GetApproverCompartments(), o.GetApproverCompartments()) {
		add(Narrowing, fmt.Sprintf("approval.approver_compartments %q now required", c),
			"only people holding it may approve; everyone else's decision is refused")
	}

	switch ao, an := o.GetMaxGrantAgeSeconds(), n.GetMaxGrantAgeSeconds(); {
	case ao == an:
	case ao != 0 && an == 0:
		add(Unclear, fmt.Sprintf("approval.max_grant_age_seconds %d → unset", ao),
			"the ceiling on how old a grant may be is gone. Not a widening, "+
				"because garmd refuses to start at all when a MODE_GRANT tool "+
				"declares no ceiling — so this is either a tool that no longer "+
				"needs a grant, or a deployment that will not boot")
	case ao == 0 && an != 0:
		add(Narrowing, fmt.Sprintf("approval.max_grant_age_seconds unset → %d", an),
			"a grant older than this is now refused, and the issuer cannot raise it")
	case an > ao:
		add(Widening, fmt.Sprintf("approval.max_grant_age_seconds %d → %d", ao, an),
			"one approval now authorises calls for longer. A grant is reusable "+
				"inside its window, so this multiplies what a single human decision buys")
	default:
		add(Narrowing, fmt.Sprintf("approval.max_grant_age_seconds %d → %d", ao, an),
			"a grant goes stale sooner, so an approver is asked more often")
	}

	// material_fields is the sub-field most worth a sentence, because losing it
	// is silent at every other layer: the tool still requires approval, the
	// approver still needs their clearance, and the grant simply stops being
	// about the request it was given for.
	if len(o.GetMaterialFields()) > 0 && len(n.GetMaterialFields()) == 0 {
		add(Widening, fmt.Sprintf("approval.material_fields %s → none", list(o.GetMaterialFields())),
			"a grant now binds only the tool, the subject and the time, so one "+
				"approval authorises ANY call to this tool inside its window — "+
				"approve ten pounds, send ten thousand. Approval is still required "+
				"and no longer says what was approved")
	} else if len(o.GetMaterialFields()) == 0 && len(n.GetMaterialFields()) > 0 {
		add(Narrowing, fmt.Sprintf("approval.material_fields none → %s", list(n.GetMaterialFields())),
			"the grant now carries a digest over these values and garmd refuses "+
				"a request that does not match: the human approved THIS call rather "+
				"than a call to this tool")
	} else {
		for _, f := range removed(o.GetMaterialFields(), n.GetMaterialFields()) {
			add(Widening, fmt.Sprintf("approval.material_fields %q dropped", f),
				"the grant no longer binds this value, so a request may change it "+
					"after the approval and still be accepted")
		}
		for _, f := range removed(n.GetMaterialFields(), o.GetMaterialFields()) {
			add(Narrowing, fmt.Sprintf("approval.material_fields %q added", f),
				"the grant now binds this value too, so an outstanding grant that "+
					"did not digest it is refused")
		}
	}
	return out
}

// compareAudience reports a change to who a tool is OFFERED to.
//
// Always Unclear, and the reason is worth stating rather than inferring: an
// audience is a listing rule and not a gate. garmd consults it when it builds
// a caller's list and never in the predicate that admits an invoke, so
// widening an audience grants nobody anything they could not already call, and
// narrowing one hides a tool from a client whose caller may still invoke it by
// name. Neither is a movement of the boundary, and putting either in the
// widening bucket would teach people to skim that bucket.
//
// It is reported at all because mistaking this field for a gate is exactly how
// `decide_task` came to be unreachable: it declared `audience: [PERSON]` and no
// set, written as though the audience were the scoping.
func compareAudience(fqn string, o, n []toolv1.Audience) []Change {
	if audienceKey(o) == audienceKey(n) {
		return nil
	}
	return []Change{{
		Direction: Unclear, Subject: fqn,
		What: fmt.Sprintf("audience %s → %s", audienceNames(o), audienceNames(n)),
		Why: "who the tool is OFFERED to changed. An audience is a listing rule " +
			"and not a gate — nothing in the invoke chain reads it — so this " +
			"neither grants nor revokes reach. What it changes is which clients " +
			"show the tool, and an empty list means AUDIENCE_AGENT rather than " +
			"everyone. If the intent was to scope who may CALL it, that is `sets`",
	}}
}

// audienceKey normalises so that reordering is not a change and so that an
// empty list compares equal to an explicit [AUDIENCE_AGENT] — which is what the
// contract says it means.
func audienceKey(as []toolv1.Audience) string {
	if len(as) == 0 {
		as = []toolv1.Audience{toolv1.Audience_AUDIENCE_AGENT}
	}
	ns := make([]string, 0, len(as))
	for _, a := range as {
		ns = append(ns, a.String())
	}
	sort.Strings(ns)
	return strings.Join(ns, ",")
}

func audienceNames(as []toolv1.Audience) string {
	if len(as) == 0 {
		return "[AUDIENCE_AGENT] (declared empty)"
	}
	ns := make([]string, 0, len(as))
	for _, a := range as {
		ns = append(ns, a.String())
	}
	sort.Strings(ns)
	return "[" + strings.Join(ns, ", ") + "]"
}

// compareSets reports what a change of tool-set membership does to reach.
//
// Sets are not a bar and not a lattice. garmd's visibility predicate ends in
// inScope(the session's sets, the tool's sets), and that predicate is
// asymmetric in the way that makes every casual reading of this field wrong:
//
//	a session naming NO sets is UNSCOPED and reaches every tool it is
//	otherwise entitled to, whatever sets that tool declares;
//	a session naming sets reaches only tools declaring one of them — and a
//	tool declaring NO sets shares none with it, so a tool in no set is out
//	of scope for every scoped session there is.
//
// So membership grants and revokes reach on its own, without a clearance or a
// compartment moving, and the two transitions across empty are the ones that
// mislead. A tool gaining its FIRST set is a grant and never a revocation,
// because the unscoped caller held it throughout. A tool losing its LAST set
// reads as a restriction lifted and is a revocation from every scoped session
// at once.
//
// This is reported per set rather than as one line of before-and-after because
// the audience that gains `triage` is a different audience from the one that
// loses `payments`, and a reviewer has to agree to them separately.
func compareSets(fqn string, o, n []string) []Change {
	var out []Change
	add := func(d Direction, what, why string) {
		out = append(out, Change{Direction: d, Subject: fqn, What: what, Why: why})
	}

	switch {
	case len(o) == 0 && len(n) == 0:
		return nil

	case len(o) == 0:
		add(Widening, fmt.Sprintf("sets none → %s — the tool is now scoped", list(n)),
			"it was in no set, so every session scoped to any set was refused it "+
				"and only an unscoped caller could reach it at all. Every session "+
				"scoped to one of these now reaches it too. Nobody loses it — an "+
				"unscoped caller is not narrowed by sets — so this is a grant of "+
				"access with no clearance and no compartment moved")

	case len(n) == 0:
		add(Narrowing, fmt.Sprintf("sets %s → none — the tool is now in no set", list(o)),
			"this reads as a restriction lifted and is the opposite. A tool in no "+
				"set shares none with a session that names one, so every scoped "+
				"session loses it at once and gets not-found rather than "+
				"permission-denied. What is left is the unscoped caller, which in a "+
				"deployment where every staff role is scoped is the least privileged "+
				"role and nothing else")

	default:
		for _, s := range removed(n, o) {
			add(Widening, fmt.Sprintf("set %q added", s),
				"every session scoped to it now reaches the tool. Membership is "+
					"reach rather than a bar, so this grants access without "+
					"lowering a clearance or dropping a compartment")
		}
		for _, s := range removed(o, n) {
			add(Narrowing, fmt.Sprintf("set %q removed", s),
				"a session scoped to it and to nothing else the tool still "+
					"declares can no longer reach the tool, and is told not-found "+
					"rather than permission-denied")
		}
	}
	return out
}

// list renders a set of names for a message, sorted so the sentence does not
// depend on declaration order.
func list(ss []string) string {
	cp := append([]string(nil), ss...)
	sort.Strings(cp)
	for i, s := range cp {
		cp[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(cp, ", ") + "]"
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

	// write, resolved rather than as written: an unset `write` means the same
	// as `read`, so a field whose read bar moved has had its write bar moved
	// with it, and comparing the raw values would report nothing.
	//
	// It is a separate line from read because it is a separate consequence.
	// read decides who SEES a response value; write decides who may SET a
	// request value, and garmd refuses the whole call — it does not quietly
	// drop the field — when a caller sets one it may not.
	if ow, nw := writeOf(o), writeOf(n); ow != nw {
		d, why := Narrowing, "fewer callers may set the value; the rest are refused the call"
		if rank(nw) < rank(ow) {
			d, why = Widening, "every caller cleared below the old bar may now set "+
				"this request field. A write bar is not a redaction: the value the "+
				"caller supplies is the one the tool acts on"
		}
		add(d, fmt.Sprintf("write %s → %s", ow, nw), why)
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
	// source says the value is the RUNNER's rather than the caller's. Dropping
	// it does not move a clearance and hands the field to whoever is calling,
	// which for the one rule that exists — idempotency_key — is the difference
	// between a retry that is recognised and a second payment.
	if o.GetSource() == toolv1.FieldPolicy_SOURCE_RUNNER &&
		n.GetSource() != toolv1.FieldPolicy_SOURCE_RUNNER {
		add(Widening, "source SOURCE_RUNNER → the caller's",
			"the field was filled by the dispatching runner and is now the "+
				"caller's to supply. A model picking its own idempotency key after "+
				"a timeout it never saw the answer to pays twice")
	}
	if o.GetSource() != toolv1.FieldPolicy_SOURCE_RUNNER &&
		n.GetSource() == toolv1.FieldPolicy_SOURCE_RUNNER {
		add(Narrowing, "source the caller's → SOURCE_RUNNER",
			"the field leaves the schema the caller is offered and is filled by "+
				"the runner. Breaking for anything that was setting it")
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
		out[toolKey(string(t.Method.ParentFile().Package()), t.Name)] = t
	}
	return out, nil
}

// toolKey is the FQN a tool is reported under, and the reason `name` is not
// compared as a field: it is half of this key, so renaming a tool presents as
// the old FQN removed and a new one added. That is what a rename IS to a grant
// naming the old FQN and to a manifest pinning it.
func toolKey(pkg, name string) string { return pkg + "." + name }

// writeOf is the write clearance actually in force. The contract says an unset
// `write` is the same as `read`, and garmd's plan compiler resolves it that
// way, so a comparison reading the raw field would miss every field that
// inherits.
func writeOf(p *toolv1.FieldPolicy) toolv1.Clearance {
	if p.GetWrite() == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
		return p.GetRead()
	}
	return p.GetWrite()
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

func sets(p *toolv1.ToolPolicy) string {
	if len(p.GetSets()) == 0 {
		return "in no set"
	}
	return "sets " + list(p.GetSets())
}

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
