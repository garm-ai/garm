// Package claimscheck reads the vocabulary a claims policy references —
// the compartments and tool sets its roles grant — so a CI gate can compare
// that vocabulary against what a tool catalogue declares.
package claimscheck

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Refs is the set of compartment and tool-set names a policy references,
// deduplicated and sorted, plus the number of roles that produced them.
type Refs struct {
	Compartments []string
	ToolSets     []string
	Roles        int

	// CompartmentRoles and ToolSetRoles map each name in Compartments and
	// ToolSets to the sorted, deduplicated list of role names whose roles:
	// entry named it. A consumer reporting a name the catalogue does not
	// declare needs this to say where to look, not just what is wrong.
	CompartmentRoles map[string][]string
	ToolSetRoles     map[string][]string
}

// policyFile is deliberately permissive: the STS's claims.yaml carries
// roles:, segments:, agents:; devkit's personas.yaml carries roles:, users:,
// agents:. The two shapes differ everywhere outside roles:, and a third
// shape will differ again. This reader only cares that roles: values have
// compartments: and tool_sets: lists, so it decodes just that and ignores
// the rest of the document. That is why this does NOT use
// yaml.Decoder.KnownFields(true) — turning it on would make this reader
// reject either file shape (or the next one) over fields it was never
// meant to understand.
type policyFile struct {
	Roles map[string]struct {
		Compartments []string `yaml:"compartments"`
		ToolSets     []string `yaml:"tool_sets"`
	} `yaml:"roles"`
}

// ReadPolicy reads the claims policy at path and returns the vocabulary its
// roles reference. A file that yields zero roles is not a policy this
// command understands — a Kubernetes manifest, a half-written file, a path
// pointing at the wrong thing all parse as YAML but reference no
// compartments, and reporting that as "zero references" would let this gate
// pass having checked nothing. So it is an error instead.
func ReadPolicy(path string) (*Refs, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("claimscheck: reading policy %s: %w", path, err)
	}

	var doc policyFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("claimscheck: parsing policy %s as YAML: %w", path, err)
	}

	if len(doc.Roles) == 0 {
		return nil, fmt.Errorf(
			"claimscheck: %s has no roles: map with entries; expected a claims "+
				"policy (roles: mapping role names to compartments/tool_sets), "+
				"got a document with zero roles", path)
	}

	compartments := map[string]struct{}{}
	toolSets := map[string]struct{}{}
	compartmentRoles := map[string]map[string]struct{}{}
	toolSetRoles := map[string]map[string]struct{}{}
	for roleName, role := range doc.Roles {
		for _, c := range role.Compartments {
			compartments[c] = struct{}{}
			addRoleRef(compartmentRoles, c, roleName)
		}
		for _, ts := range role.ToolSets {
			toolSets[ts] = struct{}{}
			addRoleRef(toolSetRoles, ts, roleName)
		}
	}

	return &Refs{
		Compartments:     sortedKeys(compartments),
		ToolSets:         sortedKeys(toolSets),
		Roles:            len(doc.Roles),
		CompartmentRoles: sortedRoleRefs(compartmentRoles),
		ToolSetRoles:     sortedRoleRefs(toolSetRoles),
	}, nil
}

func addRoleRef(m map[string]map[string]struct{}, name, role string) {
	if m[name] == nil {
		m[name] = map[string]struct{}{}
	}
	m[name][role] = struct{}{}
}

func sortedRoleRefs(m map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(m))
	for name, roles := range m {
		out[name] = sortedKeys(roles)
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
