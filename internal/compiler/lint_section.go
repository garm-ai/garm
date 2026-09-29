package compiler

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	cardv1 "github.com/garm-ai/contracts/garm/card/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
)

// Rule C8 — a child may not be labelled below its Section.
//
// A Section is how a card says "this block is compliance's", and its label is
// a FLOOR rather than a value. A child may be labelled higher — one fact
// inside a compliance section that only the head of compliance sees — and
// never lower, for a reason that is about reading rather than about secrecy:
// the daemon removes what a viewer does not reach, so a section whose heading
// is withheld takes its children with it. A child labelled below its section
// is therefore a fact the author believes a PUBLIC reader will see and which
// no PUBLIC reader can ever reach, because the heading above it was removed
// first. It is the same argument C1 makes about a `{field}` that resolves to
// nothing, one level up: a card that promises something it cannot show is
// worse than one that does not promise it.
//
// What this rule can see is a TEMPLATE. Labels mostly travel on the value —
// a card built at run time by a generated default or an override — and no
// linter reaches those; the daemon enforces the same shape there, per call,
// as it enforces the endpoint floor. A template is the one place the
// structure is DECLARED, so it is the one place a build can refuse it.
//
// Reported against the method (task_card) or the service (result_card), like
// every other rule here.
func lintSectionFloors(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			if tpl := serviceResultCard(svc); tpl != nil {
				out = append(out, sectionFloors(string(svc.FullName()), "result_card", tpl)...)
			}
			for j := 0; j < svc.Methods().Len(); j++ {
				md := svc.Methods().Get(j)
				if tpl := methodTaskCard(md); tpl != nil {
					out = append(out, sectionFloors(string(md.FullName()), "task_card", tpl)...)
				}
			}
		}
	}
	return out
}

func sectionFloors(path, which string, tpl *cardv1.Template) []Diag {
	var out []Diag
	for i, el := range tpl.GetBody() {
		out = append(out, sectionFloorsIn(path, which, fmt.Sprintf("body[%d]", i), el)...)
	}
	return out
}

func sectionFloorsIn(path, which, at string, el *cardv1.TemplateElement) []Diag {
	sec, ok := el.GetOf().(*cardv1.TemplateElement_Section)
	if !ok {
		return nil
	}
	floor := sec.Section.GetAccess()
	var out []Diag
	for i, child := range sec.Section.GetElements() {
		childAt := fmt.Sprintf("%s.section.elements[%d]", at, i)
		out = append(out, belowFloor(path, which, childAt, floor, childAccess(child))...)
		if facts, ok := child.GetOf().(*cardv1.TemplateElement_Facts); ok {
			for j, f := range facts.Facts.GetFacts() {
				out = append(out, belowFloor(path, which,
					fmt.Sprintf("%s.facts[%d]", childAt, j), floor, f.GetAccess())...)
			}
		}
		// Sections are one level deep by the vocabulary's own rule, so a
		// nested one is somebody else's error to report; recursing anyway
		// costs nothing and means this rule does not go quiet if that ever
		// changes.
		out = append(out, sectionFloorsIn(path, which, childAt, child)...)
	}
	return out
}

func childAccess(el *cardv1.TemplateElement) *cardv1.Label {
	if sec, ok := el.GetOf().(*cardv1.TemplateElement_Section); ok {
		return sec.Section.GetAccess()
	}
	return nil
}

// belowFloor reports the two ways a child can be beneath its section: a lower
// clearance, or a compartment the section requires and the child drops.
//
// An ABSENT child label is not a violation. Absent means "the enclosing
// policy", which is the section's floor here and the endpoint's above that —
// so the one thing an author can do by omission is be exactly as tight as
// what encloses them, which is never wrong.
func belowFloor(path, which, at string, floor, child *cardv1.Label) []Diag {
	if floor == nil || child == nil {
		return nil
	}
	var out []Diag
	if child.GetClearance() != toolv1.Clearance_CLEARANCE_UNSPECIFIED &&
		child.GetClearance() < floor.GetClearance() {
		out = append(out, Diag{Rule: "C8", Path: path, Msg: fmt.Sprintf(
			"%s %s is labelled %v inside a Section labelled %v. A Section is a FLOOR: "+
				"a viewer who does not reach the heading never sees what is under it, so "+
				"a child labelled lower is a fact nobody at that clearance can actually "+
				"read. Raise the child, or lower the Section",
			which, at, child.GetClearance(), floor.GetClearance())})
	}
	held := map[string]bool{}
	for _, c := range child.GetCompartments() {
		held[c] = true
	}
	for _, need := range floor.GetCompartments() {
		if !held[need] {
			out = append(out, Diag{Rule: "C8", Path: path, Msg: fmt.Sprintf(
				"%s %s does not carry the %q compartment its Section requires. A Section "+
					"is a FLOOR, and compartments are a set: dropping one does not widen "+
					"who sees the child, it only makes the card claim something the "+
					"projection will not honour",
				which, at, need)})
		}
	}
	return out
}
