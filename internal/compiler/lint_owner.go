package compiler

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	metav1 "github.com/garm-ai/garm/contracts/garm/meta/v1"
)

// Rule O1 — every tool service and every agent names its owner (studio cards
// design §3.5).
//
// A card names who is answerable for the thing it shows: the team that owns
// the tool an approver is deciding about, the team that owns the agent whose
// run is being read. A service with no owner renders a card that says nobody
// is, which is the fact an approver most needs at the moment it is missing.
//
// A WARNING in v0.15.0, not the error the design asks for. Every catalogue
// built before this release has no owners at all, and turning O1 on as an
// error would refuse all of them at once — a build gate nobody can pass on the
// day it appears is a gate people disable. It becomes an error in a later
// release, once the catalogues this one ships with carry owners; docs/catalogue.md
// says so where an author will read it.
//
// O2 (team format) and O3 (method_owner in use) are not in this release.
func lintOwners(fds []protoreflect.FileDescriptor) []Diag {
	var out []Diag
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			if !serviceIsGoverned(svc) {
				continue
			}
			path := string(svc.FullName())
			owner := serviceOwner(svc)
			switch {
			case owner == nil:
				out = append(out, Diag{Rule: "O1", Path: path, Warn: true,
					Msg: "carries no (garm.meta.v1.owner); every card built from this service " +
						"names who is answerable for it, and this one would name nobody. " +
						"A warning in v0.15.0 so existing catalogues still build; an error " +
						"in a later release"})
			case strings.TrimSpace(owner.GetTeam()) == "":
				out = append(out, Diag{Rule: "O1", Path: path, Warn: true,
					Msg: "(garm.meta.v1.owner) has an empty team; the team is what a card " +
						"shows and what a ledger row will be charged to, and contact alone " +
						"does not say who. A warning in v0.15.0 so existing catalogues still " +
						"build; an error in a later release"})
			}
		}
	}
	return out
}

// serviceIsGoverned says whether O1 applies: the service carries an agent
// manifest, or at least one of its methods is a mounted tool. A service with
// nothing governed on it — a plain RPC service in the same tree — has no card
// and needs no owner.
func serviceIsGoverned(svc protoreflect.ServiceDescriptor) bool {
	if ServiceAgentPolicy(svc) != nil {
		return true
	}
	for j := 0; j < svc.Methods().Len(); j++ {
		if tp := methodPolicy(svc.Methods().Get(j)); tp != nil && !tp.GetExclude() {
			return true
		}
	}
	return false
}

// serviceOwner returns the owner option on svc, or nil when it carries none.
// The type assertion mirrors methodPolicy; see ServiceAgentPolicy.
func serviceOwner(svc protoreflect.ServiceDescriptor) *metav1.Owner {
	opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
	if !ok || !proto.HasExtension(opts, metav1.E_Owner) {
		return nil
	}
	o, _ := proto.GetExtension(opts, metav1.E_Owner).(*metav1.Owner)
	return o
}
