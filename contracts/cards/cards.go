// Package cards synthesises the three card endpoints every tool serves.
//
// A tool has a form, an answer and — if somebody has to approve it — an open
// approval. Those are three more things a person needs to see about that tool,
// and the only place they are governed the same way as the call itself is AS
// calls to that tool. So every tool serves them, as ordinary RPCs, at its own
// clearance, through the same ten steps:
//
//	InputCard    (google.protobuf.Empty) returns (garm.card.v1.Card)
//	ResultCard   (garm.card.v1.CallRef)  returns (garm.card.v1.Card)
//	ApprovalCard (garm.card.v1.TaskRef)  returns (garm.card.v1.Card)
//
// **An author writes no proto for any of them.** They are synthesised from
// the tool's own declaration, here, by one function that BOTH
// `garm catalogue build` and the protoc plugin call. That sharing is the
// point of the package and not an optimisation: the catalogue is what the
// daemon routes by and the generated binding is what the service registers,
// so if the two computed these names separately, the first mismatch would be
// a tool whose card the daemon dispatches and the service does not serve —
// at run time, in production, over a method neither side's author ever wrote.
// It is the same reason `wire.Subject` is shared rather than derived twice.
//
// # Where this package may live
//
// In contracts, beside the generated types, because both callers are outside
// each other: the CLI's catalogue builder is in cmd, the generator is in
// internal/compiler, and a tool runtime may one day want the names too. It
// therefore imports nothing but the generated contract packages — no linter,
// no policy compiler, no CLI.
package cards

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	agentv1 "github.com/garm-ai/garm/contracts/garm/agent/v1"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Kind is which of the three cards a synthesised endpoint serves.
type Kind int

const (
	// Input is the tool's form: what a person fills in to call it.
	Input Kind = iota
	// Result is the tool's answer, for one recorded call.
	Result
	// Approval is the open approval of a MODE_GRANT tool. Only a MODE_GRANT
	// tool has one, because a tool nobody approves never opens a task.
	Approval
)

func (k Kind) String() string {
	switch k {
	case Input:
		return "InputCard"
	case Result:
		return "ResultCard"
	case Approval:
		return "ApprovalCard"
	}
	return "UnknownCard"
}

// suffix is the snake_case tail of the synthesised tool's name.
func (k Kind) suffix() string {
	switch k {
	case Input:
		return "_input_card"
	case Result:
		return "_result_card"
	case Approval:
		return "_approval_card"
	}
	return "_card"
}

// The three request types, and the one response type. Written out rather than
// taken off linked descriptors so a diagnostic quotes the name an author
// would recognise, and so this package does not depend on the order packages
// register themselves in.
const (
	EmptyType   protoreflect.FullName = "google.protobuf.Empty"
	CallRefType protoreflect.FullName = "garm.card.v1.CallRef"
	TaskRefType protoreflect.FullName = "garm.card.v1.TaskRef"
	CardType    protoreflect.FullName = "garm.card.v1.Card"
)

// Files a synthesised method needs its host file to import.
const (
	CardProto  = "garm/card/v1/card.proto"
	EmptyProto = "google/protobuf/empty.proto"
)

// Synth is one synthesised card endpoint.
type Synth struct {
	// Kind is which card this is.
	Kind Kind

	// Parent is the tool this card is about. Everything else here is derived
	// from it, so a reader chasing "why is this card RESTRICTED" has one
	// place to look.
	Parent protoreflect.MethodDescriptor

	// Name is the RPC's name on the parent's own service — the name the
	// generator emits a Go method for and the daemon routes to.
	Name protoreflect.Name

	// InputType is one of EmptyType, CallRefType or TaskRefType.
	InputType protoreflect.FullName

	// Policy is the synthesised (garm.tool.v1.tool) option.
	Policy *toolv1.ToolPolicy
}

// ToolName is the name this card carries in the catalogue, unqualified.
func (s Synth) ToolName() string { return s.Policy.GetName() }

// FQN is the name this card carries in the catalogue, qualified by its proto
// package — the value a CardRef pointing at this card must carry.
func (s Synth) FQN() string {
	return string(s.Parent.ParentFile().Package()) + "." + s.ToolName()
}

// Synthesise returns the card endpoints for one tool.
//
// The design writes this as `Synthesise(method)`, and it is a method rather
// than a service because the policy of every card is the PARENT TOOL's: a
// card is as tight as the thing it is about, never tighter and never looser.
//
// Two methods get no cards of their own, and both absences are deliberate:
//
//   - A method whose response IS a card. A card endpoint serving its own form
//     and its own answer is an infinite regress with nothing at the bottom;
//     what a person needs about a card is the card.
//   - A method that is not a tool at all, or excludes itself.
//
// An AGENT service is the one place the parent is chosen rather than taken:
// only `Invoke` gets cards. `GetRun` is the same run read a second way, and
// giving it its own pair would put two `InputCard`s on one service.
func Synthesise(md protoreflect.MethodDescriptor) []Synth {
	pol := policyOf(md)
	if pol == nil || pol.GetExclude() {
		return nil
	}
	if md.Output().FullName() == CardType {
		return nil
	}
	svc, ok := md.Parent().(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	if isAgentService(svc) && md.Name() != "Invoke" {
		return nil
	}

	toolName := pol.GetName()
	if toolName == "" {
		toolName = DefaultToolName(md)
	}

	kinds := []Kind{Input, Result}
	if pol.GetApproval().GetMode() == toolv1.Approval_MODE_GRANT {
		kinds = append(kinds, Approval)
	}

	out := make([]Synth, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, Synth{
			Kind:      k,
			Parent:    md,
			Name:      methodName(svc, md, k),
			InputType: inputType(k),
			Policy:    cardPolicy(pol, toolName, k, md),
		})
	}
	return out
}

// ForService is Synthesise over every method of a service, in declaration
// order.
//
// Declaration order, and not sorted: the catalogue's method list is written
// in this order and a build must be byte-reproducible.
func ForService(svc protoreflect.ServiceDescriptor) []Synth {
	var out []Synth
	for i := 0; i < svc.Methods().Len(); i++ {
		out = append(out, Synthesise(svc.Methods().Get(i))...)
	}
	return out
}

// methodName is the RPC name a card takes on its parent's service.
//
// Bare on a service with ONE tool and on an agent — there is nothing to
// distinguish the card from, and `PaymentsService.InputCard` reads better
// than `PaymentsService.PayInputCard` when `Pay` is the only tool there.
// Prefixed with the parent's method name everywhere else, because three
// tools on a service would otherwise want the same three names.
func methodName(svc protoreflect.ServiceDescriptor, md protoreflect.MethodDescriptor, k Kind) protoreflect.Name {
	if isAgentService(svc) || toolCount(svc) == 1 {
		return protoreflect.Name(k.String())
	}
	return protoreflect.Name(string(md.Name()) + k.String())
}

func inputType(k Kind) protoreflect.FullName {
	switch k {
	case Result:
		return CallRefType
	case Approval:
		return TaskRefType
	default:
		return EmptyType
	}
}

// cardPolicy derives a card's (garm.tool.v1.tool) from its parent's.
//
// Five things are copied, three are fixed, and one is added. What is COPIED
// is the parent's reach: min_clearance and compartments, so a card is exactly
// as visible as the tool it describes — that is what makes "a card is
// governed the same way as the call" true rather than aspirational, and it is
// the floor an element's own label may not go below.
//
// What is FIXED: VERB_READ, because fetching a card changes nothing;
// MODE_NONE, because needing an approval to look at an approval does not
// terminate; and record_request/record_response false, because a card is
// never recorded by value — it is built for one viewer at one moment and
// writing it down would put another viewer's withheld facts in the ledger.
// The audit LEVEL is the parent's: a card about an audited tool is worth the
// same row.
//
// What is ADDED is `audience: [PERSON]`, whatever the parent's audience is.
// A card exists to be read by a person. That is also the whole reason a model
// is never offered one: not a set it might be granted, but an audience it is
// not.
//
// The parent's SETS are dropped rather than copied. A set says who holds a
// tool, and holding `payments` is about calling payments tools, not about
// reading their forms.
func cardPolicy(parent *toolv1.ToolPolicy, toolName string, k Kind, md protoreflect.MethodDescriptor) *toolv1.ToolPolicy {
	title := parent.GetTitle()
	if title == "" {
		title = string(md.Name())
	}
	return &toolv1.ToolPolicy{
		Name:         toolName + k.suffix(),
		Title:        cardTitle(title, k),
		Description:  cardDescription(title, k),
		Verb:         toolv1.Verb_VERB_READ,
		MinClearance: parent.GetMinClearance(),
		Compartments: append([]string(nil), parent.GetCompartments()...),
		Audience:     []toolv1.Audience{toolv1.Audience_AUDIENCE_PERSON},
		Approval:     &toolv1.Approval{Mode: toolv1.Approval_MODE_NONE},
		Effects: &toolv1.Effects{
			Idempotent:    true,
			Reversibility: toolv1.Reversibility_REVERSIBILITY_FULL,
		},
		Audit: &toolv1.Audit{
			Level:          parent.GetAudit().GetLevel(),
			RetainDays:     parent.GetAudit().GetRetainDays(),
			FailClosed:     parent.GetAudit().GetFailClosed(),
			RecordRequest:  false,
			RecordResponse: false,
		},
	}
}

func cardTitle(parentTitle string, k Kind) string {
	switch k {
	case Input:
		return parentTitle + ": the form"
	case Result:
		return parentTitle + ": the answer"
	default:
		return parentTitle + ": the approval"
	}
}

func cardDescription(parentTitle string, k Kind) string {
	switch k {
	case Input:
		return "The form for " + parentTitle + ", rendered for this viewer."
	case Result:
		return "The answer of one recorded call to " + parentTitle + ", rendered for this viewer."
	default:
		return "The open approval of " + parentTitle + ", rendered for this viewer."
	}
}

// Descriptor renders the synthesised method as a descriptor proto, ready to
// append to its parent service in a catalogue's file set.
func (s Synth) Descriptor() (*descriptorpb.MethodDescriptorProto, error) {
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, toolv1.E_Tool, s.Policy)
	return &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(string(s.Name)),
		InputType:  proto.String("." + string(s.InputType)),
		OutputType: proto.String("." + string(CardType)),
		Options:    opts,
	}, nil
}

// Imports names the files a synthesised method's host file must import for
// its types to resolve.
//
// A tool author's proto imports neither, because the author wrote no card. A
// catalogue that appended the methods and not the imports would be a file set
// that does not rebuild — and it would fail at the daemon's boot, not at the
// build, which is the wrong end.
func Imports(synths []Synth) []string {
	need := map[string]bool{}
	for _, s := range synths {
		need[CardProto] = true
		if s.InputType == EmptyType {
			need[EmptyProto] = true
		}
	}
	var out []string
	for _, p := range []string{EmptyProto, CardProto} {
		if need[p] {
			out = append(out, p)
		}
	}
	return out
}

// toolNameRE is the shape a catalogue tool name must have. Duplicated from
// the linter's own copy deliberately: this package may not import the
// linter, and the check here exists so a synthesised name that could not be
// a tool name is caught by a rule rather than written into a catalogue.
var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidToolName reports whether a synthesised name is a usable tool name.
func ValidToolName(name string) bool { return toolNameRE.MatchString(name) }

func policyOf(md protoreflect.MethodDescriptor) *toolv1.ToolPolicy {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(opts, toolv1.E_Tool) {
		return nil
	}
	tp, _ := proto.GetExtension(opts, toolv1.E_Tool).(*toolv1.ToolPolicy)
	return tp
}

func isAgentService(svc protoreflect.ServiceDescriptor) bool {
	opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
	return ok && proto.HasExtension(opts, agentv1.E_Agent)
}

// toolCount counts the methods on svc that are tools and are not themselves
// card endpoints — the methods this package would synthesise for.
func toolCount(svc protoreflect.ServiceDescriptor) int {
	n := 0
	for i := 0; i < svc.Methods().Len(); i++ {
		md := svc.Methods().Get(i)
		if md.Output().FullName() == CardType {
			continue
		}
		if pol := policyOf(md); pol != nil && !pol.GetExclude() {
			n++
		}
	}
	return n
}

// DefaultToolName is the snake_case name a method takes when its policy
// names none.
//
// The same derivation the linter and the catalogue builder use; kept here as
// well because this package may not import them, and pinned against theirs by
// a test, because a card whose name derived differently from its parent's
// would be a tool nobody could find.
func DefaultToolName(md protoreflect.MethodDescriptor) string {
	return SnakeCase(string(md.Name()))
}

// SnakeCase converts an RPC name to the default tool name. Runs of capitals
// are one word, so GetHTTPStatus becomes get_http_status.
func SnakeCase(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(rs[i-1])
			nextLower := i+1 < len(rs) && !unicode.IsUpper(rs[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// String renders a synth for a diagnostic.
func (s Synth) String() string {
	return fmt.Sprintf("%s.%s (%s)", s.Parent.Parent().FullName(), s.Name, s.ToolName())
}
