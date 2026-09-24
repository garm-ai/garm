// Package toolpolicy resolves what a principal may see, from annotations on the schema.
package policy

import (
	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Allows reports whether `have` satisfies `need`.
//
// CLEARANCE_UNSPECIFIED is zero, so a naive `have >= need` would make an
// unlabeled requirement satisfiable by anyone. Both sides are guarded: an
// unspecified requirement denies everyone, and an unspecified caller is denied
// everything. The lint (L1) rejects unspecified requirements at build time; this
// is the runtime backstop.
func Allows(have, need toolv1.Clearance) bool {
	if have == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
		return false
	}
	if need == toolv1.Clearance_CLEARANCE_UNSPECIFIED {
		return false
	}
	return int32(have) >= int32(need)
}

// CompartmentSet is a bitset over the declared compartments of one build.
//
// Bit assignment is stable only within a build. Never persist a CompartmentSet
// as a number: adding a compartment can shift bits and would silently
// reinterpret older records. Persist names via Registry.Names.
type CompartmentSet uint64

// Covers reports whether the held set is a superset of the needed set.
func (held CompartmentSet) Covers(need CompartmentSet) bool {
	return need&^held == 0
}

const maxCompartments = 64
