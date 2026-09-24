package compiler

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// LintEffects covers the rules that relate a tool's declared effects to the
// supervision and audit it requires. These are the rules that encode "you may
// not ship a tool that erases data but might not record that it did".
func LintEffects(tools []Tool) []Diag {
	known := map[string]bool{}
	for _, t := range tools {
		known[t.Name] = true
	}

	var out []Diag
	for _, t := range tools {
		path := string(t.Method.FullName())
		p := t.Policy
		eff := p.GetEffects()
		streaming := t.Method.IsStreamingServer() || t.Method.IsStreamingClient()

		// L12 — a compensating tool must exist.
		if eff.GetReversibility() == toolv1.Reversibility_REVERSIBILITY_COMPENSABLE {
			switch {
			case eff.GetCompensatingTool() == "":
				out = append(out, Diag{Rule: "L12", Path: path,
					Msg: "COMPENSABLE requires compensating_tool"})
			case !known[eff.GetCompensatingTool()]:
				out = append(out, Diag{Rule: "L12", Path: path, Msg: fmt.Sprintf(
					"compensating_tool %q is not a registered tool",
					eff.GetCompensatingTool())})
			}
		}

		// REVERSIBILITY_UNSPECIFIED is zero and fails closed to NONE: an
		// effect nobody bothered to classify is treated as irreversible,
		// never as safe-by-omission.
		irreversible := eff.GetReversibility() == toolv1.Reversibility_REVERSIBILITY_NONE ||
			eff.GetReversibility() == toolv1.Reversibility_REVERSIBILITY_UNSPECIFIED
		mode := p.GetApproval().GetMode()

		// L13 — notify on something irreversible: the supervisor can respond
		// but not undo. Deliberate is fine; `notes` records the decision.
		if mode == toolv1.Approval_MODE_NOTIFY && irreversible &&
			p.GetApproval().GetNotes() == "" {
			out = append(out, Diag{Rule: "L13", Path: path, Warn: true,
				Msg: "notify on an irreversible action: the supervisor cannot undo " +
					"it. Set approval.notes to record that this is intended"})
		}

		// L14 — destructive tools require a grant.
		if p.GetVerb() == toolv1.Verb_VERB_DESTRUCTIVE && mode != toolv1.Approval_MODE_GRANT {
			out = append(out, Diag{Rule: "L14", Path: path,
				Msg: "VERB_DESTRUCTIVE requires approval.mode MODE_GRANT"})
		}

		// L15 — a non-idempotent external effect duplicates on agent retry.
		if eff.GetExternal() && !eff.GetIdempotent() {
			out = append(out, Diag{Rule: "L15", Path: path, Warn: true,
				Msg: "external and not idempotent: an agent retry duplicates the " +
					"effect. Consider a deduplication key in the request"})
		}

		// L16 — irreversible and external may not happen silently.
		if irreversible && eff.GetExternal() && mode != toolv1.Approval_MODE_NOTIFY &&
			mode != toolv1.Approval_MODE_GRANT {
			out = append(out, Diag{Rule: "L16", Path: path,
				Msg: "irreversible and external requires at least MODE_NOTIFY"})
		}

		// L17 — a post-check denies something that already happened.
		if fga := p.GetAuthorization().GetFga(); fga != nil {
			post := fga.GetFromResponseField() != "" || fga.GetFilterResponse() != nil
			if post && p.GetVerb() != toolv1.Verb_VERB_READ {
				out = append(out, Diag{Rule: "L17", Path: path,
					Msg: "response-based authorization is only valid on VERB_READ"})
			}
			out = append(out, lintFgaFields(t, fga, path)...)
		}

		// L21 / L23 — audit grade.
		audit := p.GetAudit()
		if p.GetVerb() == toolv1.Verb_VERB_DESTRUCTIVE &&
			(audit.GetLevel() != toolv1.Audit_LEVEL_AUDIT || !audit.GetFailClosed()) {
			out = append(out, Diag{Rule: "L21", Path: path,
				Msg: "VERB_DESTRUCTIVE requires audit.level LEVEL_AUDIT with fail_closed"})
		}
		if audit.GetLevel() == toolv1.Audit_LEVEL_AUDIT && audit.GetRetainDays() == 0 {
			out = append(out, Diag{Rule: "L23", Path: path,
				Msg: "LEVEL_AUDIT requires retain_days > 0"})
		}

		// L24 — streaming tools are read-only.
		if streaming && p.GetVerb() != toolv1.Verb_VERB_READ {
			out = append(out, Diag{Rule: "L24", Path: path,
				Msg: "a streaming RPC may not declare WRITE or DESTRUCTIVE"})
		}
	}
	return out
}

// lintFgaFields covers L18: the named field must exist.
func lintFgaFields(t Tool, fga *toolv1.Fga, path string) []Diag {
	var out []Diag
	if f := fga.GetFromRequestField(); f != "" {
		if !hasField(t.Method.Input(), f) {
			out = append(out, Diag{Rule: "L18", Path: path, Msg: fmt.Sprintf(
				"from_request_field %q is not a field of %s", f, t.Method.Input().FullName())})
		}
	}
	if lf := fga.GetFilterResponse(); lf != nil {
		if !hasField(t.Method.Output(), lf.GetRepeatedField()) {
			out = append(out, Diag{Rule: "L18", Path: path, Msg: fmt.Sprintf(
				"filter_response.repeated_field %q is not a field of %s",
				lf.GetRepeatedField(), t.Method.Output().FullName())})
		}
	}
	return out
}

func hasField(md protoreflect.MessageDescriptor, name string) bool {
	return md.Fields().ByName(protoreflect.Name(name)) != nil
}
