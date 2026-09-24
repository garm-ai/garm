// Package tool is the compiled form of a tool declaration.
//
// A Def is what a .proto says about a tool: its identity, the clearance and
// compartments it requires, what it does to the world, and the prose a model
// reads. It is data, and it is deliberately inert — nothing here decides
// whether a call may proceed.
//
// It lives beside the annotations rather than with the runtime that enforces
// them, because a declaration is not an enforcement. The daemon builds these
// from a catalogue at boot and the generator emits them at build time; both
// need the same type, and neither should have to depend on the other to get
// it.
package tool

import (
	"maps"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Def is one tool, emitted by protoc-gen-garm-tools.
type Def struct {
	FullMethod string
	Name       string

	// FQN is the globally-unique tool identity: proto package + tool name,
	// e.g. "garm.examples.v1alpha1.lookup_customer". Name alone is unique only
	// within a package (L3), which is one plugin invocation's view.
	//
	// buf runs protoc-gen-garm-tools once per proto package, so L3's `seen`
	// map — the only duplicate-name check there is — never compares two
	// packages' names against each other. Prefixing with the proto package
	// makes the identity unique by construction: proto package names are
	// globally unique, and L3 makes the name unique within one. There is
	// deliberately NO lint rule for FQN uniqueness; L3 plus proto package
	// uniqueness already provides the guarantee, and a second rule would be
	// redundant (and could not see across packages anyway).
	//
	// Parse by splitting at the LAST dot: L3's charset forbids dots in tool
	// names, so the final segment is always the name and everything before
	// it is the proto package.
	//
	// The proto package carries domain AND version, so moving a service from
	// v1alpha1 to v1 changes every FQN it declares. That is intentional: a
	// version move is a new identity that consumers (manifests, pins,
	// allowlists) must acknowledge, not inherit silently.
	FQN string

	Title        string
	Description  string
	Verb         toolv1.Verb
	MinClearance toolv1.Clearance
	Compartments []string
	Sets         []string
	Input        protoreflect.MessageDescriptor
	Output       protoreflect.MessageDescriptor

	// ApprovalMode, AuditLevel and HasAuthorization carry the governance a
	// tool DECLARES. Plan A implements none of them, and until this branch
	// codegen dropped them on the floor — so L14 and L21 forced a
	// destructive tool's author to write `approval: { mode: MODE_GRANT }`
	// and `audit: { level: LEVEL_AUDIT, fail_closed: true }`, the build
	// passed, the server started, and the tool executed ungated with no
	// audit record and no signal that anything was missing. A declaration
	// the runtime silently ignores is worse than no declaration: it reads
	// as protection in review.
	//
	// Carrying them makes mount able to REFUSE, which is what turns the
	// gap into a startup error. See mount's governance check.
	ApprovalMode toolv1.Approval_Mode
	AuditLevel   toolv1.Audit_Level
	// HasAuthorization records that an `authorization` block was declared
	// at all; its contents are Plan C's business, and reproducing them here
	// would suggest this package knows what to do with them.
	HasAuthorization bool

	// Effects, from the tool's declared `effects`. They exist on Def
	// because two surfaces need them and neither can reach the descriptor:
	// MCP's annotations (readOnlyHint, destructiveHint, idempotentHint,
	// openWorldHint) are derived from exactly these, and the catalogue's
	// GetTool reports them so an agent can decide whether a retry is safe.
	//
	// Reversibility fails closed: an unspecified declaration is treated as
	// NONE by the policy that reads it, because "we did not say" and "cannot
	// be undone" must not be distinguishable to something deciding whether
	// to retry.
	Idempotent    bool
	Reversibility toolv1.Reversibility
	External      bool

	// Guidance, from the tool's declared `guidance`. Prose for the model,
	// not for a human reading the proto.
	//
	// WhenNotToUse is consistently worth more than a longer description:
	// wrong-TOOL selection beats wrong-argument construction as a source of
	// agent error, and no schema can express "never to retry a payment whose
	// status is unknown". MCP's annotation set is closed and has nowhere to
	// put either of these, which is why the MCP surface composes them into
	// the description text rather than annotating them.
	WhenNotToUse string
	OnError      string

	// FieldDocs is the prose for this tool's request and response fields,
	// keyed by each field's FULL proto name (e.g.
	// "garm.demo.v1beta1.AccountSummary.iban"), which is what a schema
	// projector already holds when it walks a descriptor.
	//
	// It exists because protoc-gen-go strips SourceCodeInfo from the runtime
	// descriptor, so a proto comment is unreachable at run time. The plugin
	// captures it at generation time instead. Without it a projected
	// inputSchema is typed fields with no prose — a model sees
	// `postcode_area: string` with no indication of what it means or why it
	// is the only address field it may read, which is a large accuracy loss
	// for the one reader the schema exists for.
	//
	// The accepted cost: a comment edited without regenerating drifts from
	// the schema it documents. CI fails on a dirty tree after `make gen`, so
	// the drift surfaces as a failed build rather than as stale prose.
	FieldDocs map[string]string
}

func (t Def) sameDeclarationAs(o Def) bool {
	md := func(d protoreflect.MessageDescriptor) protoreflect.FullName {
		if d == nil {
			return ""
		}
		return d.FullName()
	}
	return t.FullMethod == o.FullMethod &&
		t.Name == o.Name &&
		t.FQN == o.FQN &&
		t.Title == o.Title &&
		t.Description == o.Description &&
		t.Verb == o.Verb &&
		t.MinClearance == o.MinClearance &&
		slices.Equal(t.Compartments, o.Compartments) &&
		slices.Equal(t.Sets, o.Sets) &&
		md(t.Input) == md(o.Input) &&
		md(t.Output) == md(o.Output) &&
		t.ApprovalMode == o.ApprovalMode &&
		t.AuditLevel == o.AuditLevel &&
		t.HasAuthorization == o.HasAuthorization &&
		t.Idempotent == o.Idempotent &&
		t.Reversibility == o.Reversibility &&
		t.External == o.External &&
		t.WhenNotToUse == o.WhenNotToUse &&
		t.OnError == o.OnError &&
		maps.Equal(t.FieldDocs, o.FieldDocs)
}

// Service returns the proto service name, for the generated per-service filter.
func (t Def) Service() string {
	// "/pkg.v1.SvcName/Method" -> "SvcName"
	for i := len(t.FullMethod) - 1; i >= 0; i-- {
		if t.FullMethod[i] == '/' {
			svc := t.FullMethod[:i]
			for j := len(svc) - 1; j >= 0; j-- {
				if svc[j] == '.' {
					return svc[j+1:]
				}
			}
			return svc
		}
	}
	return ""
}

// Config configures a Server.
//
// PrincipalFunc is the seam where Plan B will later plug in JWT verification:
// Plan A supplies a static or trivially-derived Principal, Plan B derives one
// from a verified token. Nothing about token handling belongs here.
