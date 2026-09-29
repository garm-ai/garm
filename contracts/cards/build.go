package cards

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	cardv1 "github.com/garm-ai/garm/contracts/garm/card/v1"
	metav1 "github.com/garm-ai/garm/contracts/garm/meta/v1"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// The generated defaults.
//
// A card built from a descriptor is a boring card, and that is the ambition:
// a tool author should get a usable form, a readable answer and a correct
// approval without writing any of the three, and should reach for an override
// only to add something the contract does not already say.
//
// These are RUNTIME functions rather than generated code on purpose. The
// generator emits a three-line wrapper per card that hands one of these the
// method descriptor out of the .pb.go file's own descriptor; the building
// itself happens here, once, in a package both a generated binding and a
// hand-written service can call. Generating the card CONSTRUCTION would put
// hundreds of lines of literal-building in every tool module and make a fix
// to the layout a regeneration of every repository that has one.
//
// They are here rather than in tool-go because tool-go v0.5.0 has no card
// support and a release of it is not on this release's path. Nothing here
// needs a runtime: it reads descriptors and returns a message.

// ErrResultUnavailable is what a generated result card answers when the
// call's stored response cannot be found.
//
// The generated default cannot read a response it was never handed: the
// runtime writes a call's answer to a sealed store keyed by call id, and that
// store does not exist yet. Until it does, the default answers this and a
// tool that keeps its own record (a payments service with a payments table,
// a runner with a run row) overrides ResultCard and reads its own.
//
// A named error rather than a blank card, because a blank card is a card a
// person will read as "nothing happened".
var ErrResultUnavailable = errors.New("result_unavailable: the stored response for this call is not available")

// BuildInputCard renders a tool's request message as a form.
//
// One Input per field the caller may set, in declaration order, with the kind
// taken from the field's type and the constraints from its protovalidate
// rules. Each input is labelled at the field's WRITE policy, so the daemon
// drops an input the viewer may not fill — a masked field in a form is a box
// a person will try to type in.
//
// Fields the RUNNER supplies (`source: SOURCE_RUNNER`) are absent. They are
// not the caller's to fill, and showing an idempotency key to a person is
// showing them a decision the platform already made.
func BuildInputCard(md protoreflect.MethodDescriptor) (*cardv1.Card, error) {
	pol := policyOf(md)
	if pol == nil {
		return nil, fmt.Errorf("%s carries no (garm.tool.v1.tool), so it has no card", md.FullName())
	}
	endpoint := EndpointLabel(pol)

	in := md.Input()
	def := messageDefaultPolicy(in)
	var elems []*cardv1.Element
	for i := 0; i < in.Fields().Len(); i++ {
		fd := in.Fields().Get(i)
		fp := fieldPolicyOf(fd, def)
		if fp.GetSource() == toolv1.FieldPolicy_SOURCE_RUNNER {
			continue
		}
		input := inputFor(fd)
		if input == nil {
			// A repeated field, a map, or a nested message: a form has no
			// control for one, and inventing a JSON textarea would be a box
			// nobody can fill correctly. An author who needs it overrides.
			continue
		}
		elems = append(elems, &cardv1.Element{
			Of:     &cardv1.Element_Input{Input: input},
			Access: Join(endpoint, writeLabel(fp)),
		})
	}

	return &cardv1.Card{
		Kind:      cardv1.Kind_START,
		SubjectId: toolFQN(md, pol),
		Title:     titleOf(md, pol),
		State:     cardv1.State_OPEN,
		Body:      elems,
		Actions: []*cardv1.Action{{
			Id:    "start",
			Label: "Start",
			Style: cardv1.Style_STYLE_DEFAULT,
			Kind:  &cardv1.Action_Submit{Submit: &cardv1.Submit{}},
		}},
		Access: endpoint,
	}, nil
}

// BuildResultCard renders one recorded call's answer.
//
// `ref` names the call and `resp` is what that call returned — the caller's
// to supply, because the descriptor cannot know it. A tool that keeps its own
// record passes its own row; the generated wrapper has nothing to pass until
// the sealed result store exists, and answers ErrResultUnavailable.
//
// One Fact per scalar leaf reachable without crossing a repeated field, in
// declaration order, each labelled at the field's READ policy and carrying
// the dotted path it came from. A repeated field is left out of the default
// (see KNOWN-GAPS): a list of anything is a layout decision, and the wrong
// layout is worse than an override.
func BuildResultCard(md protoreflect.MethodDescriptor, ref *cardv1.CallRef, resp proto.Message) (*cardv1.Card, error) {
	pol := policyOf(md)
	if pol == nil {
		return nil, fmt.Errorf("%s carries no (garm.tool.v1.tool), so it has no card", md.FullName())
	}
	// A typed nil pointer is a non-nil proto.Message, and the generated
	// wrapper passes exactly that when it has no stored response to hand
	// over — so both shapes of "there is no answer" have to be caught here,
	// or the card comes back empty instead of unavailable.
	if resp == nil || !resp.ProtoReflect().IsValid() {
		return nil, ErrResultUnavailable
	}
	endpoint := EndpointLabel(pol)

	facts := factsOf(resp.ProtoReflect(), "", endpoint, nil, 0)
	var body []*cardv1.Element
	if len(facts) > 0 {
		body = append(body, &cardv1.Element{
			Of:     &cardv1.Element_Facts{Facts: &cardv1.FactSet{Facts: facts}},
			Access: endpoint,
		})
	}

	return &cardv1.Card{
		Kind:      cardv1.Kind_RUN,
		SubjectId: ref.GetCallId(),
		Title:     titleOf(md, pol),
		State:     cardv1.State_COMPLETED,
		Body:      body,
		Access:    endpoint,
	}, nil
}

// BuildApprovalCard renders the open approval of a MODE_GRANT tool.
//
// The values come from the ref rather than from the request, because the
// daemon refuses a grant-mode call before it resolves the tool: the tool
// never saw what was parked. Every material field gets a Fact with its dotted
// path set, so the client can rebuild the map it will hand the token service,
// and every Fact is labelled at its own read policy JOINED with the tool's
// approver predicate — the higher clearance and the union of the
// compartments, because who may approve is a stricter question than who may
// read.
//
// The owner facts come from (garm.meta.v1.owner) on the service: an approver
// deciding something irreversible should be able to see who is answerable for
// the tool without leaving the page.
func BuildApprovalCard(md protoreflect.MethodDescriptor, ref *cardv1.TaskRef) (*cardv1.Card, error) {
	pol := policyOf(md)
	if pol == nil {
		return nil, fmt.Errorf("%s carries no (garm.tool.v1.tool), so it has no card", md.FullName())
	}
	if pol.GetApproval().GetMode() != toolv1.Approval_MODE_GRANT {
		return nil, fmt.Errorf("%s is not MODE_GRANT, so nothing opens an approval for it", md.FullName())
	}
	endpoint := EndpointLabel(pol)
	approver := Join(endpoint, ApproverLabel(pol))

	in := md.Input()
	def := messageDefaultPolicy(in)
	material := make([]*cardv1.Fact, 0, len(pol.GetApproval().GetMaterialFields()))
	for _, path := range pol.GetApproval().GetMaterialFields() {
		fd := fieldAt(in, path)
		label := path
		access := approver
		if fd != nil {
			label = captionOf(fd)
			access = Join(approver, readLabel(fieldPolicyOf(fd, def)))
		}
		material = append(material, &cardv1.Fact{
			Label:  label,
			Value:  ref.GetMaterial()[path],
			Field:  path,
			Access: access,
		})
	}

	body := []*cardv1.Element{{
		Of: &cardv1.Element_Section{Section: &cardv1.Section{
			Title: "Material",
			Elements: []*cardv1.Element{{
				Of:     &cardv1.Element_Facts{Facts: &cardv1.FactSet{Facts: material}},
				Access: approver,
			}},
		}},
		Access: approver,
	}}

	if owner := ownerFacts(md, approver); owner != nil {
		body = append(body, owner)
	}
	body = append(body, templateElements(md, ref.GetMaterial(), approver)...)

	return &cardv1.Card{
		Kind:      cardv1.Kind_TASK,
		SubjectId: ref.GetTaskId(),
		Title:     titleOf(md, pol),
		State:     cardv1.State_OPEN,
		Body:      body,
		Access:    approver,
	}, nil
}

// ---------------------------------------------------------------- inputs

// inputFor maps a request field to a form control, or nil when there is no
// honest one.
//
// Constraints come from protovalidate and nowhere else: `required`, a
// string's max_len, and a number's gt/gte/lt/lte. A control that advertised a
// limit the server does not enforce, or omitted one it does, is worse than a
// plain box — the person fills it in and is refused after the fact.
func inputFor(fd protoreflect.FieldDescriptor) *cardv1.Input {
	if fd.IsList() || fd.IsMap() {
		return nil
	}
	rules := validateRules(fd)
	in := &cardv1.Input{
		Id:       string(fd.Name()),
		Label:    captionOf(fd),
		Required: rules.GetRequired(),
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		t := &cardv1.TextInput{}
		if s := rules.GetString(); s != nil {
			if s.MaxLen != nil {
				t.MaxLen = uint32(s.GetMaxLen())
			}
			if s.MinLen != nil && s.GetMinLen() > 0 {
				in.Required = true
			}
			t.Multiline = t.MaxLen == 0 || t.MaxLen > 200
		}
		in.Kind = &cardv1.Input_Text{Text: t}
	case protoreflect.BoolKind:
		in.Kind = &cardv1.Input_Toggle{Toggle: &cardv1.ToggleInput{}}
	case protoreflect.EnumKind:
		in.Kind = &cardv1.Input_Choice{Choice: &cardv1.ChoiceInput{Choices: enumChoices(fd)}}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		in.Kind = &cardv1.Input_Number{Number: numberInput(rules)}
	case protoreflect.MessageKind:
		if fd.Message().FullName() == "google.protobuf.Timestamp" {
			in.Kind = &cardv1.Input_Date{Date: &cardv1.DateInput{}}
			break
		}
		return nil
	default:
		return nil
	}
	return in
}

// numberInput reads the numeric bounds protovalidate declares.
//
// Exclusive bounds (gt, lt) are rendered as the inclusive ones a form can
// express, because a card has no way to say "greater than" — and a control
// that admitted the boundary the server refuses is a control that produces
// one rejected submission per person who tries it. Narrowing by one is only
// correct for integers; a float's exclusive bound is left unset rather than
// wrong.
func numberInput(rules *validate.FieldRules) *cardv1.NumberInput {
	n := &cardv1.NumberInput{}
	switch t := rules.GetType().(type) {
	case *validate.FieldRules_Uint32:
		if v, ok := t.Uint32.GetGreaterThan().(*validate.UInt32Rules_Gte); ok {
			n.Min = float64(v.Gte)
		}
		if v, ok := t.Uint32.GetGreaterThan().(*validate.UInt32Rules_Gt); ok {
			n.Min = float64(v.Gt) + 1
		}
		if v, ok := t.Uint32.GetLessThan().(*validate.UInt32Rules_Lte); ok {
			n.Max = float64(v.Lte)
		}
		if v, ok := t.Uint32.GetLessThan().(*validate.UInt32Rules_Lt); ok {
			n.Max = float64(v.Lt) - 1
		}
	case *validate.FieldRules_Uint64:
		if v, ok := t.Uint64.GetGreaterThan().(*validate.UInt64Rules_Gte); ok {
			n.Min = float64(v.Gte)
		}
		if v, ok := t.Uint64.GetGreaterThan().(*validate.UInt64Rules_Gt); ok {
			n.Min = float64(v.Gt) + 1
		}
		if v, ok := t.Uint64.GetLessThan().(*validate.UInt64Rules_Lte); ok {
			n.Max = float64(v.Lte)
		}
		if v, ok := t.Uint64.GetLessThan().(*validate.UInt64Rules_Lt); ok {
			n.Max = float64(v.Lt) - 1
		}
	case *validate.FieldRules_Int32:
		if v, ok := t.Int32.GetGreaterThan().(*validate.Int32Rules_Gte); ok {
			n.Min = float64(v.Gte)
		}
		if v, ok := t.Int32.GetGreaterThan().(*validate.Int32Rules_Gt); ok {
			n.Min = float64(v.Gt) + 1
		}
		if v, ok := t.Int32.GetLessThan().(*validate.Int32Rules_Lte); ok {
			n.Max = float64(v.Lte)
		}
		if v, ok := t.Int32.GetLessThan().(*validate.Int32Rules_Lt); ok {
			n.Max = float64(v.Lt) - 1
		}
	case *validate.FieldRules_Int64:
		if v, ok := t.Int64.GetGreaterThan().(*validate.Int64Rules_Gte); ok {
			n.Min = float64(v.Gte)
		}
		if v, ok := t.Int64.GetGreaterThan().(*validate.Int64Rules_Gt); ok {
			n.Min = float64(v.Gt) + 1
		}
		if v, ok := t.Int64.GetLessThan().(*validate.Int64Rules_Lte); ok {
			n.Max = float64(v.Lte)
		}
		if v, ok := t.Int64.GetLessThan().(*validate.Int64Rules_Lt); ok {
			n.Max = float64(v.Lt) - 1
		}
	}
	return n
}

func enumChoices(fd protoreflect.FieldDescriptor) []*cardv1.Choice {
	vals := fd.Enum().Values()
	out := make([]*cardv1.Choice, 0, vals.Len())
	for i := 0; i < vals.Len(); i++ {
		v := vals.Get(i)
		if v.Number() == 0 {
			// The zero value is "unspecified" by convention, and offering it
			// as a choice is offering a person the option of saying nothing
			// while appearing to answer.
			continue
		}
		out = append(out, &cardv1.Choice{Title: humanise(string(v.Name())), Value: string(v.Name())})
	}
	return out
}

// ---------------------------------------------------------------- facts

// maxFactDepth bounds how far the default descends into a response.
//
// Not a performance guard: a message graph reachable from a tool cannot be
// recursive (the policy compiler refuses one), so this is a LAYOUT bound. Two
// levels of dotted path is about as much as a fact list can carry before it
// stops being readable, and an author with a deeper answer is better served
// by an override than by a card with `a.b.c.d.e` in it.
const maxFactDepth = 2

func factsOf(
	msg protoreflect.Message,
	prefix string,
	endpoint *cardv1.Label,
	def *toolv1.FieldPolicy,
	depth int,
) []*cardv1.Fact {
	if msg == nil || !msg.IsValid() || depth > maxFactDepth {
		return nil
	}
	md := msg.Descriptor()
	if def == nil {
		def = messageDefaultPolicy(md)
	}
	var out []*cardv1.Fact
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}
		path := string(fd.Name())
		if prefix != "" {
			path = prefix + "." + path
		}
		fp := fieldPolicyOf(fd, def)
		if fd.Kind() == protoreflect.MessageKind && !isOpaqueValue(fd.Message()) {
			if msg.Has(fd) {
				out = append(out, factsOf(msg.Get(fd).Message(), path, endpoint, nil, depth+1)...)
			}
			continue
		}
		text, ok := factText(fd, msg)
		if !ok {
			continue
		}
		out = append(out, &cardv1.Fact{
			Label:  captionOf(fd),
			Value:  text,
			Field:  path,
			Access: Join(endpoint, readLabel(fp)),
		})
	}
	return out
}

// factText renders one value the way a person reads it: an enum by name, a
// number in decimal, a bool as yes/no. An absent field is left out rather
// than shown empty — a fact with no value is a row a reader has to decide
// about.
func factText(fd protoreflect.FieldDescriptor, msg protoreflect.Message) (string, bool) {
	if fd.HasPresence() && !msg.Has(fd) {
		return "", false
	}
	v := msg.Get(fd)
	switch fd.Kind() {
	case protoreflect.StringKind:
		s := v.String()
		return s, s != ""
	case protoreflect.BoolKind:
		if v.Bool() {
			return "yes", true
		}
		return "no", true
	case protoreflect.EnumKind:
		ed := fd.Enum().Values().ByNumber(v.Enum())
		if ed == nil {
			return strconv.FormatInt(int64(v.Enum()), 10), true
		}
		return humanise(string(ed.Name())), true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10), true
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10), true
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), true
	case protoreflect.MessageKind:
		// A well-known value: its protojson text is the one rendering every
		// language agrees on.
		return v.Message().Interface().(interface{ String() string }).String(), true
	}
	return "", false
}

// isOpaqueValue mirrors the policy walk's leaf boundary: a well-known type is
// a VALUE, not a structure to flatten into dotted facts.
func isOpaqueValue(md protoreflect.MessageDescriptor) bool {
	return strings.HasPrefix(string(md.FullName()), "google.protobuf.")
}

// ---------------------------------------------------------------- owner and template

func ownerFacts(md protoreflect.MethodDescriptor, access *cardv1.Label) *cardv1.Element {
	owner := methodOwner(md)
	if owner == nil {
		owner = serviceOwner(md)
	}
	if owner == nil || owner.GetTeam() == "" {
		return nil
	}
	facts := []*cardv1.Fact{{Label: "Owner", Value: owner.GetTeam(), Access: access}}
	if c := owner.GetContact(); c != "" {
		facts = append(facts, &cardv1.Fact{Label: "Contact", Value: c, Access: access})
	}
	if c := owner.GetOnCall(); c != "" {
		facts = append(facts, &cardv1.Fact{Label: "On call", Value: c, Access: access})
	}
	return &cardv1.Element{
		Of:     &cardv1.Element_Facts{Facts: &cardv1.FactSet{Facts: facts}},
		Access: access,
	}
}

// templateElements renders a declared task_card.
//
// The template is the DECLARATIVE customisation: an author who wants a
// sentence above the material writes it in the proto and does not touch Go.
// A `{field}` reference resolves against the material map and nothing else,
// which is what lint C1 already holds it to — so a reference that survives
// lint has a value here.
func templateElements(md protoreflect.MethodDescriptor, material map[string]string, access *cardv1.Label) []*cardv1.Element {
	tpl := taskCardTemplate(md)
	if tpl == nil {
		return nil
	}
	var out []*cardv1.Element
	if t := tpl.GetTitle(); t != "" {
		out = append(out, &cardv1.Element{
			Of: &cardv1.Element_Text{Text: &cardv1.Text{
				Text:     interpolate(t, material),
				Emphasis: cardv1.Emphasis_HEADING,
			}},
			Access: access,
		})
	}
	for _, el := range tpl.GetBody() {
		if e := templateElement(el, material, access); e != nil {
			out = append(out, e)
		}
	}
	return out
}

func templateElement(el *cardv1.TemplateElement, material map[string]string, access *cardv1.Label) *cardv1.Element {
	switch of := el.GetOf().(type) {
	case *cardv1.TemplateElement_Text:
		return &cardv1.Element{
			Of:     &cardv1.Element_Text{Text: &cardv1.Text{Text: interpolate(of.Text, material)}},
			Access: access,
		}
	case *cardv1.TemplateElement_Divider:
		return &cardv1.Element{Of: &cardv1.Element_Divider{Divider: &cardv1.Divider{}}, Access: access}
	case *cardv1.TemplateElement_Facts:
		facts := make([]*cardv1.Fact, 0, len(of.Facts.GetFacts()))
		for _, f := range of.Facts.GetFacts() {
			facts = append(facts, &cardv1.Fact{
				Label:  f.GetLabel(),
				Value:  material[f.GetField()],
				Field:  f.GetField(),
				Access: access,
			})
		}
		return &cardv1.Element{
			Of:     &cardv1.Element_Facts{Facts: &cardv1.FactSet{Facts: facts}},
			Access: access,
		}
	case *cardv1.TemplateElement_Section:
		var inner []*cardv1.Element
		for _, e := range of.Section.GetElements() {
			if built := templateElement(e, material, access); built != nil {
				inner = append(inner, built)
			}
		}
		return &cardv1.Element{
			Of: &cardv1.Element_Section{Section: &cardv1.Section{
				Title: interpolate(of.Section.GetTitle(), material), Elements: inner,
			}},
			Access: access,
		}
	}
	return nil
}

// interpolate replaces {field} with the material value, and an unresolved
// reference with nothing.
//
// Nothing, rather than the literal braces: lint C1 has already refused a
// reference that names something other than a material field, so a reference
// that reaches here and has no value names a field the request did not set —
// and showing a person "{amount}" teaches them to distrust the card.
func interpolate(s string, material map[string]string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(material[s[i+1:i+j]])
		s = s[i+j+1:]
	}
}

// ---------------------------------------------------------------- descriptor reading

func titleOf(md protoreflect.MethodDescriptor, pol *toolv1.ToolPolicy) string {
	if t := pol.GetTitle(); t != "" {
		return t
	}
	return humanise(string(md.Name()))
}

// ToolFQN is the catalogue name of the tool a method declares: the proto
// package, a dot, and the resolved tool name.
//
// Exported because it is what a CardRef has to carry. A ref names a card by
// kind and id, and a client that has only those cannot fetch it — it does not
// know whose ResultCard or ApprovalCard to call. This is that answer, derived
// from the contract rather than kept in a table on the client that would go
// stale the moment a tool moved service.
func ToolFQN(md protoreflect.MethodDescriptor) string {
	return toolFQN(md, policyOf(md))
}

// Ref builds a pointer from one card to another.
//
// It takes the tool FQN as a parameter rather than letting a caller leave it
// out, because "a ref with no tool" is exactly the hole this constructor
// exists to close: it is not detectable at build time, it renders fine, and
// it fails when somebody reaches that card by a route the author did not
// picture. A ref with an empty FQN is refused here rather than served.
//
// An agent's run card refs its tasks with the TASKS tool's FQN; a task card
// refs its run with the AGENT's. In both cases the FQN is the one whose
// service serves the card being pointed AT, never the one doing the pointing.
func Ref(kind cardv1.Kind, subjectID, title string, state cardv1.State, toolFQN string) (*cardv1.CardRef, error) {
	if toolFQN == "" {
		return nil, errors.New("a CardRef needs the fully-qualified name of the tool whose " +
			"service serves the card it names: a kind and an id say WHICH card, not whose " +
			"endpoint to fetch it from, and a client that reached this card by an " +
			"unexpected route has nothing else to go on")
	}
	return &cardv1.CardRef{
		Kind:      kind,
		SubjectId: subjectID,
		Title:     title,
		State:     state,
		ToolFqn:   toolFQN,
	}, nil
}

func toolFQN(md protoreflect.MethodDescriptor, pol *toolv1.ToolPolicy) string {
	name := pol.GetName()
	if name == "" {
		name = DefaultToolName(md)
	}
	return string(md.ParentFile().Package()) + "." + name
}

// captionOf is the caption a person reads beside a value.
//
// The field's leading comment when it has one, because that is what the
// author already wrote for the reader; otherwise the field name made
// readable. A first sentence only: a card is a layout, and a paragraph in a
// label is a paragraph nobody reads.
func captionOf(fd protoreflect.FieldDescriptor) string {
	if c := strings.TrimSpace(fd.ParentFile().SourceLocations().ByDescriptor(fd).LeadingComments); c != "" {
		line := strings.TrimSpace(strings.SplitN(c, "\n", 2)[0])
		if line != "" {
			return strings.TrimSuffix(line, ".")
		}
	}
	return humanise(string(fd.Name()))
}

// humanise turns snake_case or SCREAMING_SNAKE into a sentence-cased phrase.
func humanise(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "_", " "))
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func fieldAt(md protoreflect.MessageDescriptor, path string) protoreflect.FieldDescriptor {
	var fd protoreflect.FieldDescriptor
	for _, seg := range strings.Split(path, ".") {
		if md == nil {
			return nil
		}
		fd = md.Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return nil
		}
		if fd.Kind() == protoreflect.MessageKind {
			md = fd.Message()
		} else {
			md = nil
		}
	}
	return fd
}

func readLabel(fp *toolv1.FieldPolicy) *cardv1.Label {
	return Label(fp.GetRead(), fp.GetCompartments())
}

// writeLabel is the WRITE policy, falling back to read — the same fallback
// the policy compiler makes, because an unset write clearance means "as read"
// and not "anyone".
func writeLabel(fp *toolv1.FieldPolicy) *cardv1.Label {
	w := fp.GetWrite()
	if w == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
		w = fp.GetRead()
	}
	return Label(w, fp.GetCompartments())
}

func fieldPolicyOf(fd protoreflect.FieldDescriptor, def *toolv1.FieldPolicy) *toolv1.FieldPolicy {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if ok && proto.HasExtension(opts, toolv1.E_FieldPolicy) {
		if fp, ok := proto.GetExtension(opts, toolv1.E_FieldPolicy).(*toolv1.FieldPolicy); ok && fp != nil {
			return fp
		}
	}
	return def
}

func messageDefaultPolicy(md protoreflect.MessageDescriptor) *toolv1.FieldPolicy {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || !proto.HasExtension(opts, toolv1.E_DefaultFieldPolicy) {
		return nil
	}
	fp, _ := proto.GetExtension(opts, toolv1.E_DefaultFieldPolicy).(*toolv1.FieldPolicy)
	return fp
}

func validateRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || !proto.HasExtension(opts, validate.E_Field) {
		return nil
	}
	r, _ := proto.GetExtension(opts, validate.E_Field).(*validate.FieldRules)
	return r
}

func taskCardTemplate(md protoreflect.MethodDescriptor) *cardv1.Template {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(opts, cardv1.E_TaskCard) {
		return nil
	}
	tpl, _ := proto.GetExtension(opts, cardv1.E_TaskCard).(*cardv1.Template)
	return tpl
}

func serviceOwner(md protoreflect.MethodDescriptor) *metav1.Owner {
	svc, ok := md.Parent().(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
	if !ok || !proto.HasExtension(opts, metav1.E_Owner) {
		return nil
	}
	o, _ := proto.GetExtension(opts, metav1.E_Owner).(*metav1.Owner)
	return o
}

func methodOwner(md protoreflect.MethodDescriptor) *metav1.Owner {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(opts, metav1.E_MethodOwner) {
		return nil
	}
	o, _ := proto.GetExtension(opts, metav1.E_MethodOwner).(*metav1.Owner)
	return o
}
