package compiler

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/policy"
)

// runnerRuleField is the one SOURCE_RUNNER field a runner has a rule for in
// this release: agentd fills it with `<run_id>-<dispatch seq>`. A second rule
// is a second entry here, a second case in agentd's dispatch, and a line in
// docs/catalogue.md — in that order, because the lint gate is what keeps a
// catalogue from declaring a field nobody can fill.
const runnerRuleField = "idempotency_key"

// L34 — a SOURCE_RUNNER field must be one a runner has a rule for.
//
// `source: SOURCE_RUNNER` on a field policy says the caller may not set the
// field and the model never sees it; whoever dispatches the call fills it. So
// a runner field the runner has no rule for is a request nobody can send: not
// the model, not the caller, and not the runner either. Refusing it at build
// time is the difference between a lint error with the author present and a
// run that fails with reason `catalogue` at 3am.
//
// Three shapes are refused, each with its own sentence, because each is a
// different mistake:
//
//   - a runner field on the REQUEST with a name other than idempotency_key,
//     or with that name but not a singular string — the rule fills a string;
//   - a runner field NESTED below the request root — the rule names a
//     top-level field, and a runner fills by path from the root;
//   - a runner field on the RESPONSE, or `source` on a message's
//     default_field_policy — the first means nothing (a runner supplies
//     requests, not responses) and the second applies to every field, which
//     is never what anyone meant.
//
// SOURCE_UNSPECIFIED is the caller and is never L34's business, whatever the
// field is called: a request field named idempotency_key with no source is a
// caller field, and agentd's name-convention fallback for catalogues older
// than garm v0.16.0 is agentd's to retire, not this rule's to enforce.
func lintRunnerFields(tools []Tool) []Diag {
	var out []Diag
	for _, t := range tools {
		path := string(t.Method.FullName())
		out = append(out, lintRunnerMessage(t.Method.Input(), path+"(input)", true)...)
		out = append(out, lintRunnerMessage(t.Method.Output(), path+"(output)", false)...)
	}
	return out
}

// lintRunnerMessage walks md the way lintMessage does — through
// policy.SubtreeOf, so the two agree on what is reachable — and reports
// every field whose resolved policy says SOURCE_RUNNER.
func lintRunnerMessage(root protoreflect.MessageDescriptor, path string, request bool) []Diag {
	var out []Diag
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor, string, bool)

	walk = func(md protoreflect.MessageDescriptor, prefix string, top bool) {
		if seen[md.FullName()] {
			return // L26 reports the cycle; nothing more to say here
		}
		seen[md.FullName()] = true
		defer delete(seen, md.FullName())

		def := policy.MessageDefaultPolicy(md)
		if def.GetSource() != toolv1.FieldPolicy_SOURCE_UNSPECIFIED {
			out = append(out, Diag{Rule: "L34", Path: prefix, Msg: fmt.Sprintf(
				"%s sets source on its default_field_policy; a default applies to every "+
					"field, and a message whose every field is the runner's is a request "+
					"nobody can send. Mark the one runner field itself", md.FullName())})
		}

		for i := 0; i < md.Fields().Len(); i++ {
			fd := md.Fields().Get(i)
			name := prefix + "." + string(fd.Name())
			// Only a field's OWN policy is judged, never one inherited from
			// a default the walk already refused above; without this every
			// field of such a message would repeat the same finding.
			if own := policy.FieldPolicyOf(fd, nil); own.GetSource() == toolv1.FieldPolicy_SOURCE_RUNNER {
				out = append(out, lintRunnerField(fd, name, request, top)...)
			}
			if child, ok := policy.SubtreeOf(fd); ok {
				walk(child, name, false)
			}
		}
	}
	walk(root, path, true)
	return out
}

func lintRunnerField(fd protoreflect.FieldDescriptor, name string, request, top bool) []Diag {
	switch {
	case !request:
		return []Diag{{Rule: "L34", Path: name, Msg: "source: SOURCE_RUNNER on a response " +
			"field means nothing; a runner supplies request fields, and a response is " +
			"the tool's to fill"}}
	case !top:
		return []Diag{{Rule: "L34", Path: name, Msg: fmt.Sprintf(
			"a SOURCE_RUNNER field must be a top-level field of the request message; "+
				"the runner fills %q by path from the request root, and that rule does "+
				"not reach a nested message", runnerRuleField)}}
	case string(fd.Name()) != runnerRuleField:
		return []Diag{{Rule: "L34", Path: name, Msg: fmt.Sprintf(
			"a SOURCE_RUNNER field must be named %q — the only runner rule this release "+
				"knows (agentd fills it with <run_id>-<dispatch seq>). A runner field "+
				"with no rule is one the model cannot see, the caller may not set and "+
				"the runner has nothing to put in", runnerRuleField)}}
	case fd.Kind() != protoreflect.StringKind || fd.IsList() || fd.IsMap():
		return []Diag{{Rule: "L34", Path: name, Msg: fmt.Sprintf(
			"%q must be a singular string; the runner's rule fills it with "+
				"<run_id>-<dispatch seq>, which is one string", runnerRuleField)}}
	}
	return nil
}
