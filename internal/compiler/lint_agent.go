package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/policy"
)

// Rules A1-A5 — the agent manifest (design §2.2, program plan §3.2).
//
// All five are errors. An agent declaration that does not hold is not a
// degraded agent: it is one the runner will refuse by name at load time, in
// production, where the author is not present. That is the same argument
// `catalogue build` makes for linting before it writes an artifact, one level
// up.
//
// Every rule here reports against the SERVICE or the METHOD it is about, never
// against the file, because a catalogue is built from many trees and "which
// team wrote this" has to be answerable from the diagnostic alone.
func lintAgents(fds []protoreflect.FileDescriptor, opts Options) []Diag {
	agents := Agents(fds)
	if len(agents) == 0 {
		return nil
	}
	tools := toolIndex(fds)
	// agentReplies is what an agent step's `response` resolves to when the
	// step's tool is some OTHER agent's Invoke — see agentStepReplies and the
	// substitution in lintWorkflowExpressionsWith. Built once, over every
	// agent in the linted set, and shared across every agent's own A7 run,
	// the same way tools is: it is a fact about the composed set, not about
	// any one agent being checked against it.
	agentReplies := agentStepReplies(agents, tools)
	var out []Diag
	// One CEL environment cache per lint run: several agents may guard the
	// same tool, and building an environment means walking a file's whole
	// type graph.
	envs := map[protoreflect.FullName]*cel.Env{}

	// files is this run's whole generation, for celenv to resolve a type by
	// name across it rather than just one variable's own file and its
	// transitive imports — see lint_cel.go. opts.Files lets a caller that
	// already built one hand it in directly; every caller today leaves it
	// nil and this rebuilds it from fds, which this function already has.
	files := opts.Files
	if files == nil {
		var ferr error
		files, ferr = filesRegistry(fds)
		if ferr != nil {
			// Surfaced once, against the whole run rather than once per
			// agent: every guard, consent caveat and workflow expression in
			// this run would otherwise be silently unchecked with nothing
			// saying so.
			out = append(out, Diag{Rule: "A4", Path: "<descriptor set>", Msg: fmt.Sprintf(
				"building the type registry guards, consent caveats and workflow "+
					"expressions resolve against failed: %v", ferr)})
		}
	}
	optsWithFiles := opts
	optsWithFiles.Files = files

	for _, a := range agents {
		out = append(out, lintAgentShape(a)...)
		out = append(out, lintAgentPrompts(a, opts)...)
		out = append(out, lintAgentMode(a)...)
		out = append(out, lintWorkflowGraph(a)...)
		// A7 is whole-catalogue, and takes opts rather than being skipped: the
		// half that needs a tool's descriptors warns off under PartialSet and
		// the half that needs only this agent's own declaration — the
		// write-dominator rule, `initial`, `set` keys, edge predicates — still
		// errors. See lintWorkflowExpressionsWith for why the split is there
		// rather than at this call site.
		out = append(out, lintWorkflowExpressionsWith(a, tools, agentReplies, optsWithFiles)...)
		// A11 reads the same tool index A7 does, to compare a `set` source's
		// field policy against the state field it lands on, and — like A4,
		// A6, A7 and A13 — resolves `response`'s type over `files`, this
		// run's whole generation, rather than only the import closure of
		// `state` and the one step's tool. It takes no Options, unlike A7:
		// a step whose tool is not in `tools` (PartialSet) is simply not
		// checked here, the same silence A7's own per-step handling would
		// have if it did not warn — see KNOWN-GAPS if this ever needs a
		// PartialSet warning of its own.
		out = append(out, lintStatePropagationFiles(a, tools, files)...)
		// A12, A14 and A15 judge only the agent's own declaration — the
		// scope against its own allowlist, the description, the triggers —
		// so, like A1, A6 and A8, they run everywhere, PartialSet included.
		out = append(out, lintConsentScope(a)...)
		out = append(out, lintConsentDescription(a)...)
		out = append(out, lintConsentTriggers(a)...)
		if opts.PartialSet {
			// Not silently: a rule that is skipped wherever nobody is
			// looking is not a rule. Same treatment A2 gives a missing
			// prompts root.
			out = append(out, Diag{Rule: "A3", Path: string(a.Service.FullName()), Warn: true,
				Msg: "the allowlist, its guards, the audience of every tool it names, " +
					"and whether a consent scope's writable tools are bounded (A13) " +
					"are not checked here: this run sees one directory, not the " +
					"catalogue. `garm lint` and `garm catalogue build` resolve them " +
					"against the whole tree and do check them"})
		} else {
			out = append(out, lintAgentTools(a, tools)...)
			out = append(out, lintManifestToolAudience(a, tools)...)
			out = append(out, lintAgentGuards(a, tools, envs, files)...)
			// A13 needs the SCOPED tools' verb and request descriptors, same
			// as A3's resolution and A4's guard compilation, so it lives
			// beside them rather than with A12/A14/A15 above.
			out = append(out, lintConsentLimits(a, tools, files)...)
		}
		out = append(out, lintAgentDoorParity(a)...)
	}
	return out
}

// toolIndex keys every tool in the linted set by the FQN an agent's allowlist
// names it with.
//
// Built the same way `catalogue build` builds it — proto package, a dot, the
// RESOLVED tool name (the explicit ToolPolicy.Name or the SnakeCase default) —
// because the two have to agree exactly or a manifest that lints will name a
// tool the catalogue does not have.
func toolIndex(fds []protoreflect.FileDescriptor) map[string]Tool {
	// Tools cannot fail over an already-compiled descriptor set today; if it
	// ever can, the index is empty and every allowlist entry looks unknown.
	tools, _ := Tools(fds)
	out := make(map[string]Tool, len(tools))
	for _, t := range tools {
		out[string(t.Method.ParentFile().Package())+"."+t.Name] = t
	}
	return out
}

// runRef and runStatus are the two message types the governed door is fixed
// to. Written out rather than taken off the linked descriptors so that a
// diagnostic quotes the name a schema author typed.
const (
	runRefName    protoreflect.FullName = "garm.agent.v1.RunRef"
	runStatusName protoreflect.FullName = "garm.agent.v1.RunStatus"
)

// lintAgentShape covers A1.
//
// Two RPCs and no others. The reason is not tidiness: garmd mounts every
// annotated method on a service as a tool and knows nothing about agents, so a
// third method is a governed tool that resolves to an agentd endpoint with no
// handler behind it — a call that passes every policy check and then fails as
// `internal`.
func lintAgentShape(a Agent) []Diag {
	var out []Diag
	svc := string(a.Service.FullName())

	if a.Invoke == nil {
		out = append(out, Diag{Rule: "A1", Path: svc,
			Msg: "a service carrying (garm.agent.v1.agent) must declare exactly one " +
				"rpc named Invoke; this one declares none"})
	} else if got := a.Invoke.Output().FullName(); got != runRefName {
		out = append(out, Diag{Rule: "A1", Path: string(a.Invoke.FullName()), Msg: fmt.Sprintf(
			"Invoke must return %s, not %s; the governed door accepts and returns a "+
				"run reference before any work has happened", runRefName, got)})
	}

	if a.GetRun != nil {
		if got := a.GetRun.Input().FullName(); got != runRefName {
			out = append(out, Diag{Rule: "A1", Path: string(a.GetRun.FullName()), Msg: fmt.Sprintf(
				"GetRun must take %s, not %s", runRefName, got)})
		}
		if got := a.GetRun.Output().FullName(); got != runStatusName {
			out = append(out, Diag{Rule: "A1", Path: string(a.GetRun.FullName()), Msg: fmt.Sprintf(
				"GetRun must return %s, not %s", runStatusName, got)})
		}
	}

	for i := 0; i < a.Service.Methods().Len(); i++ {
		md := a.Service.Methods().Get(i)
		switch string(md.Name()) {
		case "Invoke", "GetRun":
			continue
		case "GetState":
			// GetState's OUTPUT is the state type (spec §3.1). Recognised here
			// rather than refused as a third method; A8 decides whether it is
			// allowed in this mode, because that is a mode question and this is
			// a shape one. (The descriptor itself is resolved onto
			// Agent.StateMessage in Agents, the one walk that already visits
			// every method.)
			if md.Input().FullName() == runRefName {
				continue
			}
		case "SetState":
			out = append(out, Diag{Rule: "A1", Path: string(md.FullName()),
				Msg: "an agent service may not declare SetState: the graph is " +
					"the only writer of a run's state, and a method that mutated " +
					"it would be reachable by a caller or by a model through " +
					"another agent"})
			continue
		}
		// "Third method" is the right word only when Invoke is present: a
		// service that is missing Invoke and has one extra method does not
		// have three RPCs, it has two, and the extra one is not "the third"
		// — it is just not named right. Reported separately from the
		// missing-Invoke diagnostic above (rather than suppressed) because
		// the two are independent facts: a service can be missing Invoke
		// *and* carry a genuine third method, and each needs to be named so
		// both get fixed in one pass.
		if a.Invoke == nil {
			out = append(out, Diag{Rule: "A1", Path: string(md.FullName()), Msg: fmt.Sprintf(
				"an agent service declares only Invoke and GetRun; %q is neither Invoke "+
					"nor GetRun. The governed door is two RPCs: one that starts a run and "+
					"one that reads it", md.Name())})
			continue
		}
		out = append(out, Diag{Rule: "A1", Path: string(md.FullName()), Msg: fmt.Sprintf(
			"an agent service declares only Invoke and GetRun; %q is a third method. "+
				"The governed door is two RPCs: one that starts a run and one that reads it",
			md.Name())})
	}
	return out
}

// lintAgentPrompts covers A2.
//
// The prompt is the agent's instructions, and the hash in the manifest is what
// makes the catalogue self-describing: the runner fetches prompts/<sha256>.md
// from the object store and refuses an agent whose bytes do not match. Every
// way that can go wrong is cheaper to find here — a path that resolves to
// nothing, a file edited without the hash being updated, a digest written by
// hand in the wrong case.
//
// Note what A2 does NOT do: it never reads a file outside the prompts root.
// ValidatePromptPath runs before os.ReadFile, not after, because a linter that
// hashes ../../../etc/passwd and then complains about the digest has already
// read the file. That check is lexical, though, and a symlink is not: a
// declared path that stays inside the root can still resolve, one level
// further, to a target outside it. So ContainedPromptPath re-checks the
// resolved path, against a similarly-resolved root, before the read — the
// same refusal ValidatePromptPath gives a path that escapes lexically,
// because from the caller's side there is no difference. `catalogue publish`
// calls the same function for the same reason: there is exactly one
// implementation of "does this resolve inside the tree."
func lintAgentPrompts(a Agent, opts Options) []Diag {
	// A workflow agent makes no generation call of its own, so it has no
	// prompts to hash — A8 refuses them outright. Without this guard A2 and A8
	// contradict each other and no valid workflow agent can lint.
	if a.Policy.GetMode() == agentv1.Mode_MODE_WORKFLOW {
		return nil
	}
	var out []Diag
	svc := string(a.FQN)
	prompts := a.Policy.GetPrompts()

	if _, ok := prompts["system"]; !ok {
		out = append(out, Diag{Rule: "A2", Path: svc,
			Msg: `prompts has no "system" entry; an agent with no system prompt has ` +
				`no instructions, and the runner would hand the model an empty one`})
	}

	keys := make([]string, 0, len(prompts))
	for k := range prompts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		p := prompts[k]
		if err := ValidatePromptPath(p.GetPath()); err != nil {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q].path %q is not usable: %v. Paths are relative to the "+
					"prompts root, which the manifest's `prompts:` names",
				k, p.GetPath(), err)})
			continue
		}
		if err := ValidatePromptSHA256(p.GetSha256()); err != nil {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q].sha256 is malformed: %v", k, err)})
			continue
		}
		if opts.PromptsRoot == "" {
			out = append(out, Diag{Rule: "A2", Path: svc, Warn: true, Msg: fmt.Sprintf(
				"prompts[%q] is not checked here: this run has no prompts root. "+
					"`garm lint` and `garm catalogue build` resolve one from the "+
					"manifest and do check it", k)})
			continue
		}
		// The lexical check above cannot see a symlink: a declared path can
		// stay inside the root as TEXT and still resolve, one level further,
		// to a target outside it. ContainedPromptPath re-resolves both sides
		// with filepath.EvalSymlinks before this reads anything — the one
		// implementation of that check, shared with `catalogue publish`.
		resolved, err := ContainedPromptPath(opts.PromptsRoot, p.GetPath())
		if err != nil {
			if errors.Is(err, ErrPromptEscapesRoot) {
				out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
					"prompts[%q].path %q is not usable: %v. Paths are relative to the "+
						"prompts root, which the manifest's `prompts:` names",
					k, p.GetPath(), err)})
			} else {
				out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
					"prompts[%q]: %v", k, err)})
			}
			continue
		}

		body, err := os.ReadFile(resolved)
		if err != nil {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q]: %s does not exist under the prompts root %s: %v",
				k, p.GetPath(), opts.PromptsRoot, err)})
			continue
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != p.GetSha256() {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q]: %s hashes to %s, but the declaration says %s. "+
					"The hash is what the runner fetches by and verifies against; "+
					"update it in the same commit as the prompt",
				k, p.GetPath(), got, p.GetSha256())})
		}
	}
	return out
}

// lintAgentTools covers A3.
//
// An allowlist entry naming a tool the agent could never call is not a
// harmless extra: the runner projects the model's tool list from the
// catalogue and filters it by this list, so the entry either disappears
// silently (and the manifest lies about what the agent can do) or the model
// is offered a call that is refused at step 2 every time, burning a step of
// the bounds on each attempt.
//
// The verb is deliberately NOT checked. The manifest carries no verbs — a
// principal is a clearance and a set of compartments — and inventing one here
// would be a fourth vocabulary that can refuse a build.
func lintAgentTools(a Agent, tools map[string]Tool) []Diag {
	var out []Diag
	svc := string(a.Service.FullName())
	have := a.Policy.GetPrincipal().GetClearance()

	held := map[string]bool{}
	for _, c := range a.Policy.GetPrincipal().GetCompartments() {
		held[c] = true
	}

	for i, ref := range a.Policy.GetTools() {
		fqn := ref.GetFqn()
		if strings.Contains(fqn, "/") {
			out = append(out, Diag{Rule: "A3", Path: svc, Msg: fmt.Sprintf(
				"tools[%d].fqn %q looks like a method path; a tool key is "+
					`"<proto package>.<tool name>", which is what the catalogue `+
					"and the ledger both use", i, fqn)})
			continue
		}
		t, ok := tools[fqn]
		if !ok {
			out = append(out, Diag{Rule: "A3", Path: svc, Msg: fmt.Sprintf(
				"tools[%d].fqn %q names no tool in this catalogue; the allowlist is "+
					"resolved against the catalogue the agent is published in",
				i, fqn)})
			continue
		}
		if need := t.Policy.GetMinClearance(); !policy.Allows(have, need) {
			out = append(out, Diag{Rule: "A3", Path: svc, Msg: fmt.Sprintf(
				"tools[%d].fqn %q requires %v and the agent's principal has %v, so "+
					"the agent could never call it", i, fqn, need, have)})
		}
		for _, c := range t.Policy.GetCompartments() {
			if !held[c] {
				out = append(out, Diag{Rule: "A3", Path: svc, Msg: fmt.Sprintf(
					"tools[%d].fqn %q requires compartment %q, which the agent's "+
						"principal does not hold; compartments are a set and holding "+
						"one does not admit you to another", i, fqn, c)})
			}
		}
	}
	return out
}

// lintAgentGuards covers A4.
//
// A guard is the last thing between a model's chosen arguments and a governed
// call, and it runs in the runner's process rather than in the chain — so a
// guard that errors is a refusal and a guard that is not a predicate is
// whatever the coercion happens to do. Both look like a working agent right up
// until the call that mattered.
//
// CEL is checked, not merely parsed: `args` is declared with the tool's actual
// request message type, so `args.amount` against a field named
// `amount_minor_units` fails here instead of failing every call.
//
// Errors are reported without their source locations. A guard is one short
// expression on one line, the column CEL reports is of little use in a
// diagnostic that does not print the source, and a location in the text would
// make a conformance golden move on a cel-go upgrade for no gain.
func lintAgentGuards(a Agent, tools map[string]Tool, envs map[protoreflect.FullName]*cel.Env,
	files *protoregistry.Files) []Diag {
	var out []Diag
	svc := string(a.Service.FullName())

	for i, ref := range a.Policy.GetTools() {
		if ref.GetGuard() == "" {
			continue
		}
		t, ok := tools[ref.GetFqn()]
		if !ok {
			continue // A3 already reported that the tool does not exist.
		}
		msg := t.Method.Input()
		env, ok := envs[msg.FullName()]
		if !ok {
			var err error
			env, err = guardEnv(msg, files)
			if err != nil {
				out = append(out, Diag{Rule: "A4", Path: svc, Msg: fmt.Sprintf(
					"tools[%d].guard cannot be checked: building a CEL environment for "+
						"%s failed: %v", i, msg.FullName(), err)})
				continue
			}
			envs[msg.FullName()] = env
		}
		ast, iss := env.Compile(ref.GetGuard())
		if iss.Err() != nil {
			out = append(out, Diag{Rule: "A4", Path: svc, Msg: fmt.Sprintf(
				"tools[%d].guard %q does not compile against %s: %s",
				i, ref.GetGuard(), msg.FullName(), celMessages(iss))})
			continue
		}
		if !ast.OutputType().IsExactType(cel.BoolType) {
			out = append(out, Diag{Rule: "A4", Path: svc, Msg: fmt.Sprintf(
				"tools[%d].guard %q has type %s; a guard must be a bool predicate, "+
					"because the runner refuses the call when it is false and has "+
					"nothing to compare when it is anything else",
				i, ref.GetGuard(), ast.OutputType())})
		}
	}

	// output_rules are parsed and otherwise ignored in this MVP (design §2.2).
	// Syntax only: nothing yet fixes what variables an output rule is
	// evaluated against, and type-checking one would invent that contract
	// here rather than in the design.
	if len(a.Policy.GetOutputRules()) == 0 {
		return out
	}
	parseEnv, err := cel.NewEnv()
	if err != nil {
		return append(out, Diag{Rule: "A4", Path: svc,
			Msg: "output_rules cannot be parsed: " + err.Error()})
	}
	for i, r := range a.Policy.GetOutputRules() {
		if _, iss := parseEnv.Parse(r.GetExpr()); iss.Err() != nil {
			out = append(out, Diag{Rule: "A4", Path: svc, Msg: fmt.Sprintf(
				"output_rules[%d].expr %q does not parse as CEL: %s",
				i, r.GetExpr(), celMessages(iss))})
		}
	}
	return out
}

// guardEnv declares `args` as the tool's request message type, over files —
// this run's whole generation when the caller has it, or md's own import
// closure when it does not (envFiles, lint_cel.go). Both sides of a guard's
// contract, celenv.EnvVars and cel.TypeDescs, are exactly what agentd's own
// catalogue loader and internal/guard's call-time check build their
// environment from too (celenv's own doc comment): one dialect, so a guard
// that lints here and a guard the runner loads cannot disagree about what it
// means.
func guardEnv(md protoreflect.MessageDescriptor, files *protoregistry.Files) (*cel.Env, error) {
	return envFiles(files, map[string]protoreflect.MessageDescriptor{"args": md})
}

// celMessages flattens CEL's issues to one line, dropping the source
// locations. See lintAgentGuards for why.
func celMessages(iss *cel.Issues) string {
	msgs := make([]string, 0, len(iss.Errors()))
	for _, e := range iss.Errors() {
		msgs = append(msgs, e.Message)
	}
	return strings.Join(msgs, "; ")
}

// lintAgentDoorParity covers A5.
//
// Design §5.2 frame 12: GetRun has no visibility rule of its own. garmd runs
// step 2 against the labels on the method and nothing else, and agentd's only
// additional check is that the caller started the run. So if GetRun is
// labelled more loosely than Invoke, the set of people who can read a run's
// result is larger than the set who could have started one — and the result is
// the agent's output, derived from every tool the run touched.
//
// Compartments and sets are compared as SETS. Order is not meaning, and a
// diagnostic about ordering is noise that teaches an author to skim past A5.
func lintAgentDoorParity(a Agent) []Diag {
	if a.Invoke == nil || a.GetRun == nil {
		return nil // A1 reports a missing Invoke; a missing GetRun is legal.
	}
	var out []Diag
	path := string(a.Service.FullName())

	// A fixed-order slice, not a map: the order Invoke-then-GetRun is
	// deterministic by construction, and each door's diagnostic is reported
	// against ITS OWN method FQN, the way A1 does — a catalogue built from
	// many trees needs "which method" answerable from the diagnostic alone.
	doors := [2]struct {
		name string
		md   protoreflect.MethodDescriptor
		pol  *toolv1.ToolPolicy
	}{
		{"Invoke", a.Invoke, methodPolicy(a.Invoke)},
		{"GetRun", a.GetRun, methodPolicy(a.GetRun)},
	}
	for _, d := range doors {
		if d.pol == nil || d.pol.GetExclude() {
			out = append(out, Diag{Rule: "A5", Path: string(d.md.FullName()), Msg: fmt.Sprintf(
				"%s carries no (garm.tool.v1.tool), or excludes itself; both doors of "+
					"an agent are governed tools and an unmounted one cannot be called "+
					"at all", d.name)})
		}
	}
	if len(out) > 0 {
		return out
	}

	inv, get := doors[0].pol, doors[1].pol
	if inv.GetMinClearance() != get.GetMinClearance() {
		out = append(out, Diag{Rule: "A5", Path: path, Msg: fmt.Sprintf(
			"Invoke declares min_clearance %v and GetRun declares %v; a caller who "+
				"can start a run must be able to read it, and no one else",
			inv.GetMinClearance(), get.GetMinClearance())})
	}
	if d := sameSet(inv.GetCompartments(), get.GetCompartments()); d != "" {
		out = append(out, Diag{Rule: "A5", Path: path, Msg: fmt.Sprintf(
			"Invoke and GetRun declare different compartments: %s. A caller who can "+
				"start a run must be able to read it, and no one else", d)})
	}
	if d := sameSet(inv.GetSets(), get.GetSets()); d != "" {
		out = append(out, Diag{Rule: "A5", Path: path, Msg: fmt.Sprintf(
			"Invoke and GetRun declare different sets: %s. A session scoped to one "+
				"door and not the other can start runs it cannot read, or read runs it "+
				"could not have started", d)})
	}
	return out
}

// sameSet returns "" when two label lists describe the same set, and a
// rendered difference otherwise. Sorted so the message is stable.
func sameSet(a, b []string) string {
	in := func(xs []string) map[string]bool {
		m := make(map[string]bool, len(xs))
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	am, bm := in(a), in(b)
	var onlyA, onlyB []string
	for x := range am {
		if !bm[x] {
			onlyA = append(onlyA, x)
		}
	}
	for x := range bm {
		if !am[x] {
			onlyB = append(onlyB, x)
		}
	}
	if len(onlyA) == 0 && len(onlyB) == 0 {
		return ""
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return fmt.Sprintf("only on Invoke %v, only on GetRun %v", onlyA, onlyB)
}
