package compiler

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	agentv1 "github.com/garm-ai/garm/contracts/garm/agent/v1"
)

// Agent is one proto service carrying (garm.agent.v1.agent).
//
// The two method descriptors are resolved here rather than by each rule,
// because "which method is Invoke" is a fact about the declaration and not a
// judgement about it: A1 decides whether the shape is acceptable, and it can
// only do that if it can see what is there — including nothing.
type Agent struct {
	Service protoreflect.ServiceDescriptor
	Policy  *agentv1.AgentPolicy

	// Invoke is nil when the service declares no method called Invoke. That
	// is an A1 error, reported by the rule, not by this reader.
	Invoke protoreflect.MethodDescriptor

	// GetRun is nil when the service declares none, which is legitimate:
	// design §2.2 says at most one, so an agent with no result-reading door
	// is a valid declaration.
	GetRun protoreflect.MethodDescriptor
}

// Agents collects every service carrying the agent option, in file-path order
// and then declaration order within a file.
//
// The order is total and deterministic on purpose: diagnostics are compared
// against golden files, and a rule that reports in map order reports in a
// different order on every run.
func Agents(fds []protoreflect.FileDescriptor) []Agent {
	var out []Agent
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			ap := ServiceAgentPolicy(svc)
			if ap == nil {
				continue
			}
			a := Agent{Service: svc, Policy: ap}
			for j := 0; j < svc.Methods().Len(); j++ {
				switch md := svc.Methods().Get(j); string(md.Name()) {
				case "Invoke":
					if a.Invoke == nil {
						a.Invoke = md
					}
				case "GetRun":
					if a.GetRun == nil {
						a.GetRun = md
					}
				}
			}
			out = append(out, a)
		}
	}
	return out
}

// ServiceAgentPolicy returns the agent option on svc, or nil when it carries
// none.
//
// The type assertion mirrors methodPolicy in loader.go: protocompile hands
// back a dynamic message unless the option was re-parsed against the linked
// extension types, which internal/compile.Tree does for the whole set. A
// failed assertion therefore means the caller skipped that step, and nil is
// the honest answer — the alternative is a panic inside a linter.
func ServiceAgentPolicy(svc protoreflect.ServiceDescriptor) *agentv1.AgentPolicy {
	opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
	if !ok || !proto.HasExtension(opts, agentv1.E_Agent) {
		return nil
	}
	ap, _ := proto.GetExtension(opts, agentv1.E_Agent).(*agentv1.AgentPolicy)
	return ap
}

// PromptRef is one entry of one agent's prompts map, flattened so that callers
// which do not care which agent declared it — `catalogue publish`, for one —
// need not walk the structure again.
type PromptRef struct {
	Agent  protoreflect.FullName
	Key    string
	Path   string
	SHA256 string
}

// PromptRefs flattens every agent's prompts map, in agent order and then
// prompt-key order.
//
// Sorted by key because a Go map iterates in a random order and both a golden
// diagnostic file and an upload order have to be stable. Two agents declaring
// the same file are two refs; deduplication is the uploader's business,
// because the diagnostic that names one of them must name which agent.
func PromptRefs(fds []protoreflect.FileDescriptor) []PromptRef {
	var out []PromptRef
	for _, a := range Agents(fds) {
		prompts := a.Policy.GetPrompts()
		keys := make([]string, 0, len(prompts))
		for k := range prompts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, PromptRef{
				Agent:  a.Service.FullName(),
				Key:    k,
				Path:   prompts[k].GetPath(),
				SHA256: prompts[k].GetSha256(),
			})
		}
	}
	return out
}

// ValidatePromptPath insists a declared prompt path stays inside the tree it
// is resolved against.
//
// A path is joined to a prompts root by the linter, by `catalogue build` and
// by `catalogue publish`, and all three would otherwise happily read
// ../../../etc/passwd, hash it, and publish it to an object store under a name
// that says it is a system prompt. The check is on the DECLARED path rather
// than on the result of joining, because a rejection has to be able to quote
// what the author wrote.
//
// Forward slashes only: the declaration is portable text compiled on one
// machine and resolved on another, and a backslash is a legal filename
// character on the machines that are not Windows.
func ValidatePromptPath(p string) error {
	switch {
	case p == "":
		return errors.New("the path is empty")
	case strings.ContainsRune(p, '\\'):
		return errors.New("the path must use forward slashes")
	case path.IsAbs(p):
		return errors.New("the path must be relative to the prompts root")
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("the path escapes the prompts root (it resolves to %q)", clean)
	}
	return nil
}

// ValidatePromptSHA256 insists on exactly what the contract says: 64 lowercase
// hex characters, no algorithm prefix.
//
// Case matters twice over. The hash is compared against one computed at load
// time, which is lowercase, so an uppercase declaration never matches
// anything; and the object key an uploaded prompt gets is
// prompts/<sha256>.md, so publisher and reader would disagree on the key by
// case alone.
func ValidatePromptSHA256(s string) error {
	if s == "" {
		return errors.New("the sha256 is empty")
	}
	if strings.Contains(s, ":") {
		return errors.New(`the sha256 is lowercase hex with no prefix; drop the "sha256:"`)
	}
	if len(s) != 64 {
		return fmt.Errorf("the sha256 is %d characters; it must be 64", len(s))
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
			return fmt.Errorf("the sha256 %q is not lowercase; it is compared "+
				"against a lowercase digest and is also an object key", s)
		default:
			return fmt.Errorf("the sha256 %q is not hex", s)
		}
	}
	return nil
}
