package compiler

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
)

// Rule P1 — a required platform package (composition design §6).
//
// A declaration can depend on a capability that another package PROVIDES. When
// it does, the catalogue has to declare that package, and this is the rule that
// refuses one that does not.
//
// The general form is the part worth keeping: a capability dependency is
// declared by the thing that needs it and satisfied by a package in the same
// catalogue. New capabilities add rows to requirementsOf, not rules here.
//
// This is the failure the plane actually had on 2026-09-29, and it is the
// reason the rule is an error rather than a warning. `tasksd` ran all day
// serving eight tools while `payments.v1.initiate_payment` declared MODE_GRANT
// in a catalogue that did not declare `garm.tasks.v1`. Nothing was wrong with
// either half: the daemon mounted the catalogue, the service answered its
// subject, and there was no route between them. So the queue stayed empty,
// Studio had nothing to render, and the only symptom was an absence. Build time
// is the one place that is cheap to catch, because at run time it presents as a
// tool waiting and then timing out, which looks like every other unapproved
// call. It cost a day to diagnose.
//
// ONE ROW OF THE DESIGN'S TABLE IS DELIBERATELY ABSENT. "Produces an artefact
// reference" requires `garm.artefacts.v1`, and nothing in `garm.tool.v1` says a
// tool produces one — the trigger needs a declaration to hang off, most likely
// a response field typed as an artefact reference, which the artefact store's
// own contract will introduce. Until it does, that row is specified and
// unenforceable, and inferring a trigger (a field named `artefact_id`, say)
// would be this linter guessing at governance. See KNOWN-GAPS.md.
//
// A SECOND ROW IS ABSENT FOR THE SAME REASON, AND THE DESIGN DOES NOT SAY SO.
// §6's table requires `garm.tasks.v1` of a declaration that "declares a task of
// Kind.ASK", and §6.1 asserts that `MODE_GRANT` and `Kind.ASK` are both already
// annotations. Only the first is. `garm.tasks.v1.Kind.ASK` is the value of
// `CreateTaskRequest.kind`, set by the runner at run time; the declaration that
// was to carry it is the agent manifest's `asks` list, which the cards-and-tasks
// design specifies (§2.4 there, lint A10 in its numbering) and which
// `garm.agent.v1.AgentPolicy` does not have — fields 1 to 7 are mode,
// principal, model, bounds, prompts, tools and output_rules. So the ASK row
// lands when the contract grows `asks`, as one more entry in the table below.
type platformRequirement struct {
	// pkg is the proto package the catalogue must declare.
	pkg string

	// trigger is what the declaration said, quoted back the way the author
	// wrote it so the diagnostic names the line to look at.
	trigger string

	// because is the run-time failure this refusal replaces. It is the whole
	// value of the rule: somebody who trips it has to be able to tell, from
	// the message alone, what would have happened instead.
	because string

	// module is where the package comes from, for the include entry the
	// message suggests. It is advice and not a resolution — which version the
	// tree gets is go.mod's answer, and internal/manifest's.
	module string
}

// requirementsOf reads the triggers a tool declares.
//
// Per-declaration, which is why it is here and not in internal/manifest: the
// trigger is a fact about an annotation. Whether it is SATISFIED is a fact
// about the whole assembled set, which is why the check below is not.
func requirementsOf(t Tool) []platformRequirement {
	var out []platformRequirement
	if t.Policy.GetApproval().GetMode() == toolv1.Approval_MODE_GRANT {
		out = append(out, platformRequirement{
			pkg:     tasksPackage,
			trigger: "approval { mode: MODE_GRANT }",
			module:  "github.com/garm-ai/contracts",
			because: "A grant-mode approval opens a task, and a catalogue with no task " +
				"queue in it has nothing to open one with. The call parks on a task nobody " +
				"can open, decide or see; the runner waits out the approval window and " +
				"reports that no approval arrived, which is indistinguishable from a person " +
				"declining to act. Nothing in that symptom points at this declaration",
		})
	}
	return out
}

// tasksPackage is the task queue's proto package. Written out rather than taken
// off the linked descriptor so the diagnostic quotes the name an author types
// into a manifest.
const tasksPackage = "garm.tasks.v1"

// lintRequiredPackages covers P1 over the whole linted set.
//
// It is a WHOLE-SET rule and it behaves like one (§6.2). The trigger is per
// declaration but the question — is `garm.tasks.v1` in this catalogue — is
// about the assembled catalogue, so it belongs with A3 and A9 in the group
// PartialSet turns into warnings: buf invokes the protoc plugin once per
// directory, the provider almost never lives in the same directory as the
// declaration that needs it, and a rule that quietly passed there would be
// enforced only where nobody is looking.
//
// The one inference that IS sound under PartialSet is the positive one, and it
// is worth taking because presence is monotone: a package present in a subset
// of the catalogue's files is present in the catalogue, since composing more
// inputs only adds packages. So a partial run that can SEE the provider says
// nothing rather than warning about a question it has just answered. The
// negative direction is the one that does not hold, and that is the one that
// warns.
func lintRequiredPackages(fds []protoreflect.FileDescriptor, tools []Tool, opts Options) []Diag {
	// Which packages this set actually serves tools from — not which files it
	// contains. The requirement is that garmd have something to dispatch: a
	// package whose tools are in the catalogue satisfies it, and that is what
	// Catalogue.descriptor_hashes ends up keyed by.
	//
	// Taken off the resolved tools rather than off the file list because a file
	// reached only as an import still contributes its tools to the catalogue
	// (compiler.Tools walks the whole set), so "is the queue callable" and "is
	// the file present" are the same answer here and the tool list is the one
	// that stays right if that ever changes.
	serves := map[string]bool{}
	for _, t := range tools {
		serves[string(t.Method.ParentFile().Package())] = true
	}

	var out []Diag
	for _, t := range tools {
		path := string(t.Method.FullName())
		for _, r := range requirementsOf(t) {
			if serves[r.pkg] {
				continue
			}
			if opts.PartialSet {
				out = append(out, Diag{Rule: "P1", Path: path, Warn: true, Msg: fmt.Sprintf(
					"declares %s, which requires %s, and whether this catalogue declares it "+
						"was NOT checked here: this run sees one directory, not the "+
						"catalogue. `garm lint` and `garm catalogue build` see the whole "+
						"assembled set and do check it", r.trigger, r.pkg)})
				continue
			}
			out = append(out, Diag{Rule: "P1", Path: path, Msg: fmt.Sprintf(
				"declares %s, and this catalogue does not declare %s.\n\n"+
					"%s.\n\n"+
					"Declare the package that provides it, in catalogue.yaml:\n\n"+
					"    include:\n"+
					"      - module: %s\n"+
					"        packages: [%s]\n\n"+
					"The catalogue serves %s. A capability a declaration depends on has to be "+
					"satisfied by a package in the same catalogue, so the other way out is to "+
					"stop depending on it — %s.",
				r.trigger, r.pkg, r.because, r.module, r.pkg,
				servedList(serves), retreat(r))})
		}
	}
	return out
}

// servedList renders what the catalogue does have, so a reader can tell a
// missing input apart from a misspelled package name — "the catalogue serves
// garm.tasks.v1" beside "does not declare garm.task.v1" is the whole diagnosis.
func servedList(serves map[string]bool) string {
	names := make([]string, 0, len(serves))
	for p := range serves {
		names = append(names, p)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// retreat names the edit that removes the dependency rather than satisfying it.
//
// Both ways out are legitimate and the second is the right one surprisingly
// often: a tool that was never meant to be supervised acquires MODE_GRANT by
// being copied from one that was.
func retreat(r platformRequirement) string {
	if r.pkg == tasksPackage {
		return "`mode: MODE_NOTIFY` records the call and asks nobody, and `MODE_NONE` " +
			"is an unsupervised tool. Neither needs a queue"
	}
	return "drop the declaration that requires it"
}
