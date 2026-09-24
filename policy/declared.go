package policy

import (
	"fmt"
	"sort"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Registry maps declared compartment names to bits.
type Registry struct {
	bits  map[string]CompartmentSet
	names []string // index = bit position
}

// NewRegistry assigns one bit per declared name. Names are sorted first so the
// assignment is deterministic for a given declaration set.
func NewRegistry(decls []*toolv1.Decl) (*Registry, error) {
	seen := map[string]bool{}
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		if err := ValidateDeclName(d.GetName()); err != nil {
			return nil, fmt.Errorf("compartment: %w", err)
		}
		if seen[d.GetName()] {
			continue
		}
		seen[d.GetName()] = true
		names = append(names, d.GetName())
	}
	if len(names) > maxCompartments {
		return nil, fmt.Errorf(
			"%d compartments declared, maximum is %d: widen CompartmentSet or "+
				"reduce the taxonomy", len(names), maxCompartments)
	}
	sort.Strings(names)

	r := &Registry{bits: make(map[string]CompartmentSet, len(names)), names: names}
	for i, n := range names {
		r.bits[n] = CompartmentSet(1) << uint(i)
	}
	return r, nil
}

// Set converts names to a bitset. An undeclared name is an error, because a
// typo would otherwise make a field invisible to everyone with no diagnostic.
func (r *Registry) Set(names []string) (CompartmentSet, error) {
	var out CompartmentSet
	for _, n := range names {
		b, ok := r.bits[n]
		if !ok {
			return 0, fmt.Errorf("undeclared compartment %q", n)
		}
		out |= b
	}
	return out, nil
}

// SetLenient converts names to a bitset, dropping undeclared ones and returning
// them. Used for caller tokens, where rejecting an unknown compartment would
// turn an IdP typo into an outage while dropping it merely denies access.
func (r *Registry) SetLenient(names []string) (CompartmentSet, []string) {
	var out CompartmentSet
	var dropped []string
	for _, n := range names {
		b, ok := r.bits[n]
		if !ok {
			dropped = append(dropped, n)
			continue
		}
		out |= b
	}
	return out, dropped
}

// Names expands a bitset back to declared names, for the ledger.
func (r *Registry) Names(s CompartmentSet) []string {
	var out []string
	for i, n := range r.names {
		if s&(CompartmentSet(1)<<uint(i)) != 0 {
			out = append(out, n)
		}
	}
	return out
}
