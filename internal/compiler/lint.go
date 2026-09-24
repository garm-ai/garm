package compiler

import (
	"fmt"
	"regexp"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
)

// Diag is one lint finding. Warn=false fails the build.
type Diag struct {
	Rule string
	Path string
	Msg  string
	Warn bool
}

func (d Diag) String() string {
	level := "error"
	if d.Warn {
		level = "warning"
	}
	return fmt.Sprintf("%s: %s: %s: %s", level, d.Rule, d.Path, d.Msg)
}

// toolNameRE is the shape every MCP tool name must match, whether it comes
// from an explicit ToolPolicy.Name or is derived by DefaultToolName.
var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Lint runs every rule this package owns (L1-L11, L19, L20 — L8 is vacant,
// superseded — plus L12-L18, L21-L24 and L26-L29) over the input set.
//
// L28 is the declared-name-format rule that landed on main in PR #36; it is
// thirty lines below, having arrived here on rebase. L29 was chosen over the
// then-locally-free L28 precisely so the two would not collide when that
// happened, and the reason stands: check MAIN, not just this file, before
// taking the next rule number. What it did not save us from is the two rules
// interacting once both were present — see lintFieldAndShapeRules.
//
// L25 is reserved for Plan D's registration-time streaming check, which is a
// Go-level check rather than a descriptor one; it is deliberately not
// implemented here and its number is not reused.
//
// Lint is a thin dispatcher on purpose: Task 10 owns L12-L18 and L21-L24 and
// appends its own diagnostics here without needing to touch
// lintFieldAndShapeRules' internals. The one exception is L22: it needs
// lintMessage's existing per-field walk of the *output* message (to find a
// RESTRICTED read clearance) cross-referenced with the tool's
// audit.record_response, which lives on ToolPolicy rather than on any field.
// Rather than write a second walker over the same message just to duplicate
// that traversal, lintMessage takes a recordResponse flag and reports L22
// itself when walking a tool's output.
func Lint(fds []protoreflect.FileDescriptor) []Diag {
	out := lintFieldAndShapeRules(fds)
	out = append(out, lintServiceCoverage(fds)...)
	tools, _ := Tools(fds)
	out = append(out, LintEffects(tools)...)
	return out
}

// lintFieldAndShapeRules implements L1-L11, L19, L20 and L29.
func lintFieldAndShapeRules(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag

	// L29 must run, and be appended to `out`, before the compartments
	// registry check below: it looks directly at the descriptor set for a
	// same-name/different-declaration conflict that DeclaredCompartments/
	// DeclaredSets (internal/toolgen/loader.go's declared()) would
	// otherwise silently resolve by keeping whichever file happened to
	// come first. That silent-collapse policy is exactly the one
	// aggregateDecls (cmd/garmdev/toolindex.go) exists to refuse one scope
	// up, across packages; L29 is the same refusal at this scope, across
	// the files ONE lint invocation actually sees. Running it first, and
	// appending rather than returning, means a conflict is still reported
	// even when an UNRELATED problem short-circuits the rest of this
	// function.
	//
	// There are TWO such early returns below — L28's malformed-name check
	// and NewRegistry's failure (too many compartments, say) — and BOTH
	// must return append(out, ...) rather than their own findings alone,
	// or they swallow everything collected here. L28's returned bare until
	// the final fix pass, so one malformed name anywhere hid every
	// declaration conflict in the run. It was missed because it is a
	// rebase interaction: L28 arrived from main after L29 was written, so
	// neither side's suite ever ran the two together.
	out = append(out, lintDeclaredConflicts(fds)...)

	compartments := DeclaredCompartments(fds)
	sets := map[string]bool{}
	for _, d := range DeclaredSets(fds) {
		sets[d.GetName()] = true
	}

	// L28 — a declared name becomes a Go constant, a JWT claim value, a
	// ledger field and a sorted bit position. Catch a malformed one here,
	// where the diagnostic can name it, rather than downstream as generated
	// code that will not parse or as a compartment silently dropped from a
	// caller's token.
	// Ordered before NewRegistry deliberately: NewRegistry validates the
	// same names and would fail first, reporting a correct message under
	// the wrong rule number and stopping before tool sets were checked at
	// all. L28 owns name format; L7 owns undeclared references.
	if bad := append(lintDeclNames(compartments, "compartment"),
		lintDeclNames(DeclaredSets(fds), "tool set")...); len(bad) > 0 {
		// append(out, ...), not `return bad`. This early return discards
		// everything already in `out` if it returns bare, and L29 is in
		// there — so ONE malformed name anywhere hid EVERY declaration
		// conflict in the run, and an author who fixed the case met the
		// conflict only on the next run. The two rules are independent
		// and both are build gates; report both.
		return append(out, bad...)
	}

	reg, err := policy.NewRegistry(compartments)
	if err != nil {
		return append(out, Diag{Rule: "L7", Path: "<file options>", Msg: err.Error()})
	}

	tools, _ := Tools(fds)
	seenName := map[string]string{}

	for _, tl := range tools {
		path := string(tl.Method.FullName())

		out = append(out, lintToolShape(tl, path, seenName, sets)...)
		out = append(out, lintMessage(tl.Method.Input(), reg, path+"(input)", false)...)
		out = append(out, lintMessage(tl.Method.Output(), reg, path+"(output)",
			tl.Policy.GetAudit().GetRecordResponse())...)
		out = append(out, lintExamples(tl, path)...)
		out = append(out, lintVisibility(tl, reg, path)...)
	}
	return out
}

// lintDeclaredConflicts covers L29: within the linted set, two files
// declaring the same garm.v1.compartments or garm.v1.tool_sets name must be
// proto.Equal declarations. DeclaredCompartments/DeclaredSets
// (internal/toolgen/loader.go's declared()) dedupe by name and silently
// keep whichever file happened to come first, with no diagnostic — a team
// splitting a growing taxonomy.proto into two files in the SAME package and
// accidentally redefining a name would see nothing wrong. This is the
// check that makes that loud, for whatever descriptor set the caller
// lints.
//
// That set is only ever what ONE lint invocation can see. buf invokes
// protoc-gen-garm-tools once per proto package, so a conflict between two
// packages that do not depend on each other never reaches any single call
// to Lint; catching that is aggregateDecls' job, one scope up in
// cmd/garmdev/toolindex.go, which is the only pass that ever holds the
// whole build's descriptor set at once (garmdev gen toolindex also runs
// Lint itself over that same whole-build set, so this rule ends up
// covering the cross-package case too when invoked from there — see
// runToolIndex).
func lintDeclaredConflicts(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	out = append(out, declConflicts(fds, toolv1.E_Compartments, "compartment")...)
	out = append(out, declConflicts(fds, toolv1.E_ToolSets, "tool set")...)
	return out
}

// declConflicts finds same-name/different-declaration conflicts for one
// extension kind (garm.tool.v1.compartments or garm.v1.tool_sets) across fds,
// comparing the whole Decl with proto.Equal rather than the name alone —
// a name-only comparison would let a changed description through
// unnoticed.
func declConflicts(fds []protoreflect.FileDescriptor, xt protoreflect.ExtensionType, kind string) []Diag {
	var out []Diag
	byName := map[string]*toolv1.Decl{}
	fileOf := map[string]string{}
	for _, fd := range fds {
		opts, ok := fd.Options().(*descriptorpb.FileOptions)
		if !ok || !proto.HasExtension(opts, xt) {
			continue
		}
		ds, _ := proto.GetExtension(opts, xt).(*toolv1.DeclSet)
		for _, d := range ds.GetDeclared() {
			name := d.GetName()
			existing, seen := byName[name]
			if !seen {
				byName[name] = d
				fileOf[name] = fd.Path()
				continue
			}
			if proto.Equal(existing, d) {
				continue
			}
			out = append(out, Diag{Rule: "L29", Path: fd.Path(), Msg: fmt.Sprintf(
				"%s %q is declared differently in %s and %s: description %q vs %q — "+
					"a name declared twice must mean the same thing both times",
				kind, name, fileOf[name], fd.Path(),
				existing.GetDescription(), d.GetDescription())})
		}
	}
	return out
}

// lintToolShape covers L2, L3, L4 and L20.
func lintToolShape(t Tool, path string, seen map[string]string, sets map[string]bool) []Diag {
	var out []Diag
	p := t.Policy

	if p.GetDescription() == "" {
		out = append(out, Diag{Rule: "L2", Path: path,
			Msg: "tool requires a description; it is prompt surface and must be " +
				"reviewed, not inherited from a comment"})
	}

	// t.Name is already resolved (explicit ToolPolicy.Name, or the
	// SnakeCase-derived default) by Tools(), so this one check covers both
	// paths — SnakeCase itself has no length or charset bound.
	if !toolNameRE.MatchString(t.Name) {
		out = append(out, Diag{Rule: "L3", Path: path, Msg: fmt.Sprintf(
			"tool name %q must match %s", t.Name, toolNameRE)})
	} else if prev, dup := seen[t.Name]; dup {
		// Across proto packages too, not only within one.
		//
		// The FQN stays unique either way — it is package plus name — so this
		// is not about identity. It is about MCP, which lists and dispatches
		// on the SHORT name: two tools sharing one means tools/call resolves
		// to whichever the catalogue lists first. A governed call reaching the
		// wrong tool is the worst failure in the system, and it would look
		// like a working call.
		//
		// The plugin cannot catch this: it runs once per proto package and
		// never compares two packages against each other. Linting a whole
		// catalogue can, which is the one check that only exists here.
		out = append(out, Diag{Rule: "L3", Path: path, Msg: fmt.Sprintf(
			"tool name %q already used by %s; MCP dispatches on the short name, "+
				"so two tools sharing one means a call resolves to whichever is "+
				"listed first", t.Name, prev)})
	} else {
		seen[t.Name] = path
	}

	if t.Method.IsStreamingServer() || t.Method.IsStreamingClient() {
		out = append(out, Diag{Rule: "L4", Path: path,
			Msg: "streaming RPCs cannot be MCP tools; set exclude: true"})
	}

	for _, name := range p.GetSets() {
		if !sets[name] {
			out = append(out, Diag{Rule: "L20", Path: path, Msg: fmt.Sprintf(
				"undeclared tool set %q; a typo here makes the tool invisible to "+
					"sessions scoped to that set, with no error", name)})
		}
	}
	return out
}

// lintMessage covers L1, L5, L6, L7, L9, L10 and L26 over every field
// reachable from a tool's request or response message, plus L22 when called
// for a tool's output with recordResponse set (see Lint's doc comment for
// why L22 lives here instead of in lint_effects.go).
//
// A type reached by more than one distinct path is linted once per path, so
// a shared type can produce duplicate diagnostics. That is deliberate: the
// alternative (a global seen) is what made this walk blind to cycles. Lint
// already emits duplicates when a tool's request and response are the same
// type; deduplication belongs at the printing layer, not here, where
// dropping a repeat would mean dropping the path that names it.
func lintMessage(md protoreflect.MessageDescriptor, reg *policy.Registry, path string, recordResponse bool) []Diag {
	var out []Diag
	// seen is PATH-scoped (note the defer delete), exactly like
	// policy.compileInto's. A globally-scoped seen — what this walk used
	// to have — visits each type once per message and never unwinds, so it
	// cannot tell a cycle apart from a diamond: it silently skips the second
	// route to a shared type (leaving its fields unlinted along that path)
	// and never notices a cycle at all. Compile rejects cycles at boot, so a
	// lint that cannot see them turns a build diagnostic into a server that
	// refuses to start.
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor, string)

	walk = func(md protoreflect.MessageDescriptor, prefix string) {
		// L26 — a cycle in the message graph reachable from a tool.
		//
		// policy.Compile cannot flatten a cycle into its finite Plan and
		// rejects one outright; before it did, it stopped at the repeat and
		// emitted no policy for any nested instance, so every classified
		// field below the first occurrence was returned in the clear. This
		// rule moves that failure to build time, where a schema author can
		// see it, instead of to Mount, where an operator sees a server that
		// will not start.
		if seen[md.FullName()] {
			out = append(out, Diag{Rule: "L26", Path: prefix, Msg: fmt.Sprintf(
				"message type %s is reachable from itself; a recursive message graph "+
					"cannot be compiled into a field policy plan, because a flat plan "+
					"can only describe a finite set of paths. Break the cycle, or keep "+
					"this RPC off the tool surface with exclude: true", md.FullName())})
			return
		}
		seen[md.FullName()] = true
		defer delete(seen, md.FullName())

		def := policy.MessageDefaultPolicy(md)
		for i := 0; i < md.Fields().Len(); i++ {
			fd := md.Fields().Get(i)
			name := prefix + "." + string(fd.Name())
			fp := policy.FieldPolicyOf(fd, def)

			// L1 — unlabeled, or labeled with an unspecified requirement. The
			// second half matters: read=0 would otherwise pass Allows for
			// nobody and read like a deliberate lockout instead of an omission.
			if fp == nil {
				out = append(out, Diag{Rule: "L1", Path: name,
					Msg: "no field policy and no message default"})
				continue
			}
			if fp.GetRead() == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
				out = append(out, Diag{Rule: "L1", Path: name,
					Msg: "read clearance is unspecified"})
			}

			// L7 — compartments must be declared.
			if _, err := reg.Set(fp.GetCompartments()); err != nil {
				out = append(out, Diag{Rule: "L7", Path: name, Msg: err.Error()})
			}

			// L5 — the redaction must accept this field's kind.
			out = append(out, lintRedactionKind(fd, fp, name)...)

			// L6 — omit on a singular scalar needs explicit presence.
			if isOmitRedaction(fp.GetOnDeny()) &&
				fd.Kind() != protoreflect.MessageKind &&
				!fd.IsList() && !fd.IsMap() && !fd.HasPresence() {
				out = append(out, Diag{Rule: "L6", Path: name,
					Msg: "omit on a singular scalar requires `optional`, " +
						"otherwise redacted and empty are indistinguishable"})
			}

			// L9 — partial reveals must carry a reason.
			switch k := fp.GetOnDeny().GetKind().(type) {
			case *toolv1.Redaction_KeepLast:
				if k.KeepLast.GetReason() == "" {
					out = append(out, Diag{Rule: "L9", Path: name,
						Msg: "keep_last requires a reason"})
				}
			case *toolv1.Redaction_Truncate:
				if k.Truncate.GetReason() == "" {
					out = append(out, Diag{Rule: "L9", Path: name,
						Msg: "truncate requires a reason"})
				}
			}

			// L10 — write below read is legal but usually a mistake.
			if w := fp.GetWrite(); w != toolv1.Clearance_CLEARANCE_UNSPECIFIED &&
				int32(w) < int32(fp.GetRead()) {
				out = append(out, Diag{Rule: "L10", Path: name, Warn: true,
					Msg: "write clearance is below read clearance"})
			}

			// L22 — audit.record_response persists the post-sanitization
			// response into a long-retention audit stream. A RESTRICTED field
			// surviving redaction for a high-clearance caller then lands in
			// that stream too. That can be intentional, but it should be a
			// deliberate call weighed against retain_days, not an accident of
			// composing two independently-reasonable settings.
			if recordResponse && fp.GetRead() == toolv1.Clearance_CLEARANCE_RESTRICTED {
				out = append(out, Diag{Rule: "L22", Path: name, Warn: true,
					Msg: "audit.record_response persists this RESTRICTED field into " +
						"long-term audit storage; confirm audit.retain_days accounts " +
						"for its classification"})
			}

			// Descend wherever policy.compileInto descends, and nowhere
			// else — the two walks must agree on what is reachable, in BOTH
			// directions. Walking further than Compile reports errors on
			// fields Compile never enforces (a well-known type's seconds and
			// nanos); walking less far leaves a whole class of type unlinted
			// (L1, L5, L6, L7, L9, L10 and L22 all went dead for anything
			// reachable only through a message-valued map, back when this
			// walk refused to enter maps at all) and, worse, lets the build
			// pass and Mount then fail.
			//
			// Rather than restate the rule and hope the two copies stay in
			// step, both walks call policy.SubtreeOf. It owns the map
			// case and the opaque-leaf (google.protobuf.*) boundary; there is
			// no second copy here to drift.
			if child, ok := policy.SubtreeOf(fd); ok {
				walk(child, name)
			}
		}
	}
	walk(md, path)
	return out
}

// lintRedactionKind maps each oneof case to the kinds its transformer
// accepts. omit, custom and an unset redaction are structural or resolved at
// boot, so they place no constraint on the field's kind here.
//
// A map field's own Kind() is always MessageKind (protobuf represents a map
// as a synthetic repeated MapEntry message) regardless of what its values
// hold, so the kind actually being redacted is fd.MapValue().Kind() for a
// map field and fd.Kind() for everything else. Checking fd.Kind() directly
// on a map field would reject a mask redaction on a map<string,string> —
// exactly the shape the "attributes" fixture field exists to exercise end
// to end through Sanitize's map branch.
func lintRedactionKind(fd protoreflect.FieldDescriptor, fp *toolv1.FieldPolicy, name string) []Diag {
	var want []protoreflect.Kind
	switch fp.GetOnDeny().GetKind().(type) {
	case nil, *toolv1.Redaction_Omit, *toolv1.Redaction_Custom:
		return nil
	case *toolv1.Redaction_DateGrain:
		want = []protoreflect.Kind{protoreflect.MessageKind}
	default:
		want = []protoreflect.Kind{protoreflect.StringKind}
	}
	kind := fd.Kind()
	if fd.IsMap() {
		kind = fd.MapValue().Kind()
	}
	for _, k := range want {
		if kind == k {
			return nil
		}
	}
	return []Diag{{Rule: "L5", Path: name, Msg: fmt.Sprintf(
		"redaction %T does not accept %v fields", fp.GetOnDeny().GetKind(), kind)}}
}

// isOmitRedaction reports whether r resolves to an omit, including the
// unset case — an unset Redaction means "omit" throughout this codebase
// (see policy.Sanitize's isOmit, which this mirrors for lint purposes).
func isOmitRedaction(r *toolv1.Redaction) bool {
	if r == nil || r.GetKind() == nil {
		return true
	}
	_, ok := r.GetKind().(*toolv1.Redaction_Omit)
	return ok
}

// lintExamples covers L19: an example must parse into the request message.
func lintExamples(t Tool, path string) []Diag {
	var out []Diag
	for i, ex := range t.Policy.GetGuidance().GetExamples() {
		msg := dynamicpb.NewMessage(t.Method.Input())
		if err := protojson.Unmarshal([]byte(ex.GetRequestJson()), msg); err != nil {
			out = append(out, Diag{Rule: "L19",
				Path: fmt.Sprintf("%s example[%d]", path, i),
				Msg:  err.Error()})
		}
	}
	return out
}

// lintVisibility covers L11: a tool whose entire output is invisible at its
// own minimum clearance will always return an empty response.
func lintVisibility(t Tool, reg *policy.Registry, path string) []Diag {
	plan, err := policy.Compile(t.Method.Output(), reg)
	if err != nil {
		return nil // L1 or L7 already reported the underlying problem
	}
	need, err := reg.Set(t.Policy.GetCompartments())
	if err != nil {
		return nil
	}
	resolved := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance:    t.Policy.GetMinClearance(),
		Compartments: need,
	})
	if len(plan.Actions) > 0 && len(resolved.Deny) == len(plan.Actions) {
		return []Diag{{Rule: "L11", Path: path, Warn: true, Msg: fmt.Sprintf(
			"every field of %s is invisible at min_clearance %v; this tool always "+
				"returns an empty response", t.Method.Output().FullName(),
			t.Policy.GetMinClearance())}}
	}
	return nil
}

// lintServiceCoverage covers L27: once a service carries one tool, EVERY
// method on it must declare `(garm.tool.v1.tool)` — `exclude: true` is a valid
// answer, absence is not.
//
// This is the build-time half of the interceptor's refusal to pass an
// unknown procedure through. MountService mounts a WHOLE connect service,
// but only annotated, non-excluded methods become tools. A sibling RPC that
// nobody annotated returns the same messages as its tool siblings, and
// field classification lives on the MESSAGE — so "not a tool, nothing to
// enforce" was never true. Spec §5.6's own ToolCatalogService is the model:
// both its methods are explicitly `exclude: true`, not merely unannotated.
//
// Requiring the annotation rather than inferring intent is the whole point.
// An author who omits it has said nothing; an author who writes
// `exclude: true` has decided, and the decision is reviewable in the diff.
func lintServiceCoverage(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			if !serviceHasTool(svc) {
				continue
			}
			for j := 0; j < svc.Methods().Len(); j++ {
				md := svc.Methods().Get(j)
				if methodPolicy(md) != nil {
					continue
				}
				out = append(out, Diag{Rule: "L27", Path: string(md.FullName()), Msg: fmt.Sprintf(
					"%s declares tools, so every method on it must declare (garm.tool.v1.tool); "+
						"this one declares none. An unannotated sibling is still mounted and "+
						"still returns classified messages — say `exclude: true` if that is "+
						"intended", svc.FullName())})
			}
		}
	}
	return out
}

func serviceHasTool(svc protoreflect.ServiceDescriptor) bool {
	for j := 0; j < svc.Methods().Len(); j++ {
		if tp := methodPolicy(svc.Methods().Get(j)); tp != nil && !tp.GetExclude() {
			return true
		}
	}
	return false
}

// lintDeclNames implements L28 for both declaration kinds. It fails rather
// than warns: every failure mode behind ValidateDeclName is either a
// compile error far from its cause or, worse, a compartment that a caller's
// token silently loses.
func lintDeclNames(decls []*toolv1.Decl, kind string) []Diag {
	var out []Diag
	for _, d := range decls {
		if err := policy.ValidateDeclName(d.GetName()); err != nil {
			out = append(out, Diag{
				Rule: "L28",
				Path: "<file options>",
				Msg:  kind + ": " + err.Error(),
			})
		}
	}
	return out
}
