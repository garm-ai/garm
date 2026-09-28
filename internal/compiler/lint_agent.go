package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/garm/policy"
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
	var out []Diag
	// One CEL environment cache per lint run: several agents may guard the
	// same tool, and building an environment means walking a file's whole
	// type graph.
	envs := map[protoreflect.FullName]*cel.Env{}
	for _, a := range agents {
		out = append(out, lintAgentShape(a)...)
		out = append(out, lintAgentPrompts(a, opts)...)
		out = append(out, lintAgentTools(a, tools)...)
		out = append(out, lintAgentGuards(a, tools, envs)...)
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
// further, to a target outside it. So the resolved path is checked again with
// filepath.EvalSymlinks, against a similarly-resolved root, before the read —
// the same refusal ValidatePromptPath gives a path that escapes lexically,
// because from the caller's side there is no difference.
func lintAgentPrompts(a Agent, opts Options) []Diag {
	var out []Diag
	svc := string(a.Service.FullName())
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
					"prompts root, which is the directory containing the proto tree",
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
					"`garm lint` and `garm catalogue build` resolve one from --proto "+
					"and do check it", k)})
			continue
		}
		full := filepath.Join(opts.PromptsRoot, filepath.FromSlash(p.GetPath()))

		// EvalSymlinks both requires the target to exist and resolves every
		// symlink on the way to it, so a path that is missing and a path that
		// is a dangling symlink are reported the same way here: not there.
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q]: %s does not exist under the prompts root %s: %v",
				k, p.GetPath(), opts.PromptsRoot, err)})
			continue
		}

		// The root itself may sit behind a symlink (a temp dir on macOS
		// commonly does), so it is resolved the same way before the
		// containment check — otherwise every prompt in that tree would look
		// like it escapes.
		resolvedRoot, err := filepath.EvalSymlinks(opts.PromptsRoot)
		if err != nil {
			resolvedRoot = opts.PromptsRoot
		}
		if rel, err := filepath.Rel(resolvedRoot, resolved); err != nil ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q].path %q is not usable: it resolves (following symlinks) to "+
					"%s, which is outside the prompts root %s. Paths are relative to the "+
					"prompts root, which is the directory containing the proto tree",
				k, p.GetPath(), resolved, opts.PromptsRoot)})
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
func lintAgentGuards(a Agent, tools map[string]Tool, envs map[protoreflect.FullName]*cel.Env) []Diag {
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
			env, err = guardEnv(msg)
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

// guardEnv declares `args` as the tool's request message type.
//
// The descriptors come from the tree being linted, not from anything this
// binary links, so the type provider is built from the file descriptor rather
// than from a Go message. RegisterDescriptor registers one file, so imports
// are walked too — a request message with a google.protobuf.Timestamp field is
// otherwise an unknown type the moment a guard touches it.
func guardEnv(md protoreflect.MessageDescriptor) (*cel.Env, error) {
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, err
	}
	if err := registerFileAndImports(reg, md.ParentFile(), map[string]bool{}); err != nil {
		return nil, err
	}
	return cel.NewEnv(
		cel.CustomTypeAdapter(reg),
		cel.CustomTypeProvider(reg),
		cel.Variable("args", cel.ObjectType(string(md.FullName()))),
	)
}

func registerFileAndImports(reg *types.Registry, fd protoreflect.FileDescriptor, seen map[string]bool) error {
	if seen[fd.Path()] {
		return nil
	}
	seen[fd.Path()] = true
	imports := fd.Imports()
	for i := 0; i < imports.Len(); i++ {
		if err := registerFileAndImports(reg, imports.Get(i).FileDescriptor, seen); err != nil {
			return err
		}
	}
	return reg.RegisterDescriptor(fd)
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
