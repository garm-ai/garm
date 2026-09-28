package compiler

import (
	"fmt"

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
		out = append(out, Diag{Rule: "A1", Path: string(md.FullName()), Msg: fmt.Sprintf(
			"an agent service declares only Invoke and GetRun; %q is a third method. "+
				"The governed door is two RPCs: one that starts a run and one that reads it",
			md.Name())})
	}
	return out
}
