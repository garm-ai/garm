package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/reflect/protoreflect"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Shape is the part of a principal that policy depends on. Two principals with
// the same shape resolve to the same plan, which is what makes the cache work.
type Shape struct {
	Clearance    toolv1.Clearance
	Compartments CompartmentSet
}

// Resolved is the short list of things to do for one shape.
type Resolved struct {
	Desc      protoreflect.MessageDescriptor // the message type this was resolved for
	Deny      []Action                       // only fields this shape fails
	Disclosed []string                       // audit_on_read fields this shape DOES see
	Hash      string                         // content address of Deny, for the ledger
}

// Cache memoizes resolution per (plan, shape).
type Cache struct {
	mu sync.RWMutex
	m  map[key]*Resolved
}

// key is keyed on descriptor identity, not full name (protoreflect.FullName
// is just a string). Two distinct descriptors — a dynamicpb message beside a
// generated type, or a second copy of a schema loaded from a runtime
// FileDescriptorSet — can share a full name; keying on the string would
// collide them in the cache, and Sanitize would then silently no-op (its
// Desc-identity guard would reject the mismatched message) or, worse, apply
// the wrong plan. protoreflect.MessageDescriptor is an interface backed by a
// pointer for every real implementation, so it is safe and cheap to compare
// and use as a map key.
type key struct {
	desc  protoreflect.MessageDescriptor
	shape Shape
}

func NewCache() *Cache { return &Cache{m: map[key]*Resolved{}} }

// Resolve returns the deny-list for a shape, computing it once.
func (c *Cache) Resolve(p *Plan, s Shape) *Resolved {
	k := key{desc: p.Desc, shape: s}

	c.mu.RLock()
	if r, ok := c.m[k]; ok {
		c.mu.RUnlock()
		return r
	}
	c.mu.RUnlock()

	r := resolve(p, s)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.m[k]; ok {
		return existing // lost a race; one object per key keeps identity stable
	}
	c.m[k] = r
	return r
}

func resolve(p *Plan, s Shape) *Resolved {
	r := &Resolved{Desc: p.Desc}
	var pruned []string

	for _, a := range p.Actions {
		// Skip anything under an already-denied subtree: the sanitizer will
		// delete the parent, so descending would be wasted work at best.
		if underAny(a.Name, pruned) {
			continue
		}
		if Allows(s.Clearance, a.Read) && s.Compartments.Covers(a.Need) {
			if a.AuditOnRead {
				r.Disclosed = append(r.Disclosed, a.Name)
			}
			continue
		}
		r.Deny = append(r.Deny, a)
		if a.IsSubtree {
			pruned = append(pruned, a.Name)
		}
	}
	r.Hash = hashActions(r.Deny)
	return r
}

// underAny reports whether name is at or below one of the given dotted paths.
// It matches on path segments ("prefix + .") rather than raw string prefix,
// so a sibling field whose name merely starts with a denied subtree's name
// (e.g. "billing_summary" next to a denied "billing") is not mistaken for a
// descendant and skipped.
func underAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p+".") {
			return true
		}
	}
	return false
}

// hashActions content-addresses a deny list so ledger rows can carry an id
// instead of repeating the same field list on every call (spec §9).
func hashActions(as []Action) string {
	h := sha256.New()
	for _, a := range as {
		fmt.Fprintf(h, "%s|%v|%d|%T\n", a.Name, a.Read, a.Need, a.OnDeny.GetKind())
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
