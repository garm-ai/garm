package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
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
	var out []Diag
	for _, a := range agents {
		out = append(out, lintAgentShape(a)...)
		out = append(out, lintAgentPrompts(a, opts)...)
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
// read the file.
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
		body, err := os.ReadFile(full)
		if err != nil {
			out = append(out, Diag{Rule: "A2", Path: svc, Msg: fmt.Sprintf(
				"prompts[%q]: %s does not exist under the prompts root %s",
				k, p.GetPath(), opts.PromptsRoot)})
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
