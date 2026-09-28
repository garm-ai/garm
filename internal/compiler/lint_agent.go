package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	for _, a := range agents {
		out = append(out, lintAgentShape(a)...)
		out = append(out, lintAgentPrompts(a, opts)...)
		out = append(out, lintAgentTools(a, tools)...)
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
