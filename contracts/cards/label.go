package cards

import (
	"sort"

	cardv1 "github.com/garm-ai/garm/contracts/garm/card/v1"
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Label builds a card label.
func Label(clearance toolv1.Clearance, compartments []string) *cardv1.Label {
	return &cardv1.Label{
		Clearance:    clearance,
		Compartments: append([]string(nil), compartments...),
	}
}

// Join is the TIGHTER of two labels: the higher clearance and the union of
// the compartments.
//
// Tighter, not looser, and the asymmetry is the whole safety property. A
// generated default labels a fact from the field policy that produced it and
// joins that with the endpoint's own policy, so the result can never be below
// the endpoint — which is the floor the daemon refuses a card for crossing.
// An override adding a fact from a RESTRICTED field to a card whose endpoint
// is INTERNAL gets RESTRICTED, and an override that forgot to label gets the
// endpoint's, which is as tight as the thing that let them in.
//
// Compartments union rather than intersect for the same reason they do
// everywhere else in this system: a compartment is a requirement placed on
// the viewer, so needing two is stricter than needing one.
//
// A nil label is the identity: absent means "no requirement of its own", not
// "public".
func Join(a, b *cardv1.Label) *cardv1.Label {
	if a == nil {
		return cloneLabel(b)
	}
	if b == nil {
		return cloneLabel(a)
	}
	clearance := a.GetClearance()
	if b.GetClearance() > clearance {
		clearance = b.GetClearance()
	}
	seen := map[string]bool{}
	var comps []string
	for _, c := range append(append([]string{}, a.GetCompartments()...), b.GetCompartments()...) {
		if seen[c] {
			continue
		}
		seen[c] = true
		comps = append(comps, c)
	}
	// Sorted, because a label is compared and a card is a build artefact: two
	// runs that differ only in map iteration order would produce two cards.
	sort.Strings(comps)
	return &cardv1.Label{Clearance: clearance, Compartments: comps}
}

func cloneLabel(l *cardv1.Label) *cardv1.Label {
	if l == nil {
		return nil
	}
	return Label(l.GetClearance(), l.GetCompartments())
}

// EndpointLabel is the label of the card endpoint itself — the floor every
// element on the card must be at or above.
//
// It is the PARENT tool's policy, because that is what cardPolicy copied on
// to the endpoint. Stated as its own function so a builder and a checker
// cannot disagree about what "the endpoint's own policy" means.
func EndpointLabel(parent *toolv1.ToolPolicy) *cardv1.Label {
	return Label(parent.GetMinClearance(), parent.GetCompartments())
}

// ApproverLabel is the predicate an approval card's facts are labelled at,
// joined with whatever the field's own policy said.
//
// A material fact on an approval card is readable by the people who may
// APPROVE, which is a stricter question than who may read the field: a tool
// may expose an amount at INTERNAL and still require a RESTRICTED approver
// with the `financial` compartment.
func ApproverLabel(parent *toolv1.ToolPolicy) *cardv1.Label {
	a := parent.GetApproval()
	return Label(a.GetApproverMinClearance(), a.GetApproverCompartments())
}
