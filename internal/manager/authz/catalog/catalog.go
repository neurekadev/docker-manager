// Package catalog is Docker Manager's versioned permission catalog (#17): every
// grantable action as a stable capability key with its resource grouping,
// plain-language label, compatible scopes, risk hint and owner-only flag.
//
// Rules:
//
//   - Capability keys are stable identifiers stored in permission rules and
//     API tokens; never rename or reuse one. Add a key (with Since set to
//     the new Version) in the PR that adds the route, job kind or stream
//     needing it. A new key is denied to everyone but the owner until a rule
//     grants it: all-resources rules never grant future keys.
//   - There are no generic read/write/execute keys. Each action whose data
//     or risk differs has its own key (restart does not imply start, logs,
//     terminal or files).
//   - Scopes: instance (all resources of the capability's type, including
//     future ones), environment (all such resources in one environment) or
//     one resource of a compatible resource type. Rules on a parent resource
//     (a stack, a service) apply to its current and future children for the
//     capabilities explicitly granted there.
//   - OwnerOnly entries document the owner surface (users, groups,
//     invitations, security policy, credential administration): they can
//     never be granted; routes guarding them declare the "owner"
//     pseudo-capability.
//
// Completeness is enforced by tests: every capability of the route
// inventory (api/route-inventory.yaml), every job kind (internal/jobspec)
// and every event type of the manager bus has a catalog entry.
package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// Version is the catalog version. Bump it when adding capabilities and set
// their Since to the new value; editors show "new since your last edit".
const Version = 1

// Risk is the editor's risk hint.
type Risk string

// Risk levels.
const (
	RiskNormal Risk = "normal"
	// RiskHigh: shown distinctly in editors and previews (secrets, data
	// loss, code execution, cross-resource visibility).
	RiskHigh Risk = "high"
)

// Resource types. Instance is the pseudo-type of manager-wide capabilities.
const (
	TypeInstance          = "instance"
	TypeEnvironment       = "environment"
	TypeAgent             = "agent"
	TypeStack             = "stack"
	TypeService           = "service"
	TypeContainer         = "container"
	TypeImage             = "image"
	TypeVolume            = "volume"
	TypeNetwork           = "network"
	TypeBuildDefinition   = "build_definition"
	TypeUpdatePolicy      = "update_policy"
	TypeMaintenancePolicy = "maintenance_policy"
	TypeBackupRepository  = "backup_repository"
	TypeBackupPolicy      = "backup_policy"
	TypeBackup            = "backup"
	TypeRegistry          = "registry"
	TypeGitCredential     = "git_credential" //nolint:gosec // G101: a resource type name, not a credential
	TypeTemplate          = "template"
	TypeJob               = "job"
	TypeSchedule          = "schedule"
	TypeAPIToken          = "api_token"
	TypeAudit             = "audit"
	TypeSettings          = "settings"
	TypeSystem            = "system"
	TypeAdministration    = "administration"
)

// ResourceType groups capabilities and describes how resources of the type
// are located and scoped.
type ResourceType struct {
	Key   string
	Label string
	// Scopable: a rule may target one resource of this type.
	Scopable bool
	// EnvironmentBound: resources live in exactly one environment (at a
	// time; stacks move with environment migration, #35).
	EnvironmentBound bool
	// NamedPerEnvironment: the resource ID (a Docker name) is unique only
	// within its environment, so a rule on one resource records the
	// environment too. Stacks, services, agents, policies and definitions
	// have global Docker Manager IDs; their rules follow them when they move.
	NamedPerEnvironment bool
	// Parents are the resource types that may contain a resource of this
	// type (nearest first): rules on a parent apply to its children.
	Parents []string
	// Read is the capability that shows a resource in full. Any other
	// capability on a resource shows only its minimal identity/status view.
	Read string
	// Minimal lists the fields of the minimal (discovery) view.
	Minimal string
}

// Capability is one grantable action.
type Capability struct {
	Key string
	// Type is the resource type the action applies to (its grouping).
	Type        string
	Label       string
	Description string
	// Instance and Environment say whether a rule may use those scopes.
	Instance    bool
	Environment bool
	// Resources are the resource types a rule may target individually
	// (a stack or service for container actions: dynamic scope).
	Resources []string
	Risk      Risk
	// OwnerOnly capabilities can never be granted (owner surface).
	OwnerOnly bool
	// Advanced capabilities are collapsed in editors until needed.
	Advanced bool
	// Since is the catalog Version that introduced the key.
	Since int
}

// Catalog is an immutable, validated capability catalog.
type Catalog struct {
	version int
	types   []ResourceType
	caps    []Capability
	byKey   map[string]int
	byType  map[string]int
}

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// New validates and builds a catalog (tests build variants, e.g. a future
// version with extra keys).
func New(version int, types []ResourceType, caps []Capability) (*Catalog, error) {
	c := &Catalog{version: version, types: slices.Clone(types), caps: slices.Clone(caps),
		byKey: map[string]int{}, byType: map[string]int{}}
	for i, t := range c.types {
		if t.Key == "" || t.Label == "" {
			return nil, fmt.Errorf("catalog: resource type %d needs a key and label", i)
		}
		if _, dup := c.byType[t.Key]; dup {
			return nil, fmt.Errorf("catalog: duplicate resource type %q", t.Key)
		}
		c.byType[t.Key] = i
	}
	for _, t := range c.types {
		for _, p := range t.Parents {
			pt, ok := c.byType[p]
			if !ok || !c.types[pt].Scopable {
				return nil, fmt.Errorf("catalog: resource type %q has unknown or unscopable parent %q", t.Key, p)
			}
		}
		if t.NamedPerEnvironment && !t.EnvironmentBound {
			return nil, fmt.Errorf("catalog: resource type %q is named per environment but not environment-bound", t.Key)
		}
	}
	for i, cp := range c.caps {
		where := cp.Key
		switch {
		case !keyRE.MatchString(cp.Key):
			return nil, fmt.Errorf("catalog: capability %q must be a dotted lower-case key", cp.Key)
		case cp.Label == "" || cp.Description == "":
			return nil, fmt.Errorf("catalog: capability %s needs a label and a description", where)
		case cp.Since < 1 || cp.Since > version:
			return nil, fmt.Errorf("catalog: capability %s: since %d outside 1..%d", where, cp.Since, version)
		case cp.Risk != RiskNormal && cp.Risk != RiskHigh:
			return nil, fmt.Errorf("catalog: capability %s: risk %q", where, cp.Risk)
		}
		if _, dup := c.byKey[cp.Key]; dup {
			return nil, fmt.Errorf("catalog: duplicate capability %s", where)
		}
		if _, ok := c.byType[cp.Type]; !ok {
			return nil, fmt.Errorf("catalog: capability %s has unknown resource type %q", where, cp.Type)
		}
		if !cp.OwnerOnly && !cp.Instance && !cp.Environment && len(cp.Resources) == 0 {
			return nil, fmt.Errorf("catalog: capability %s has no compatible scope", where)
		}
		if cp.OwnerOnly && (cp.Environment || len(cp.Resources) > 0) {
			return nil, fmt.Errorf("catalog: owner-only capability %s is instance-wide", where)
		}
		for _, r := range cp.Resources {
			rt, ok := c.byType[r]
			if !ok || !c.types[rt].Scopable {
				return nil, fmt.Errorf("catalog: capability %s: resource scope %q is not a scopable resource type", where, r)
			}
		}
		c.byKey[cp.Key] = i
	}
	for _, t := range c.types {
		if t.Read == "" {
			continue
		}
		if cp, ok := c.Lookup(t.Read); !ok || cp.Type != t.Key {
			return nil, fmt.Errorf("catalog: read capability %q of resource type %q must be a capability of that type", t.Read, t.Key)
		}
	}
	return c, nil
}

// Version is the catalog version.
func (c *Catalog) Version() int { return c.version }

// Has reports whether key is a catalog capability (the route inventory's
// CapabilityCatalog interface).
func (c *Catalog) Has(key string) bool {
	_, ok := c.byKey[key]
	return ok
}

// Lookup returns a capability.
func (c *Catalog) Lookup(key string) (Capability, bool) {
	i, ok := c.byKey[key]
	if !ok {
		return Capability{}, false
	}
	return c.caps[i], true
}

// Type returns a resource type.
func (c *Catalog) Type(key string) (ResourceType, bool) {
	i, ok := c.byType[key]
	if !ok {
		return ResourceType{}, false
	}
	return c.types[i], true
}

// Capabilities returns every capability in catalog order.
func (c *Catalog) Capabilities() []Capability { return slices.Clone(c.caps) }

// Types returns every resource type in catalog order.
func (c *Catalog) Types() []ResourceType { return slices.Clone(c.types) }

// OfType returns the keys of a resource type's capabilities.
func (c *Catalog) OfType(typ string) []string {
	var out []string
	for _, cp := range c.caps {
		if cp.Type == typ {
			out = append(out, cp.Key)
		}
	}
	return out
}

// Applicable returns the grantable capabilities that can apply to a
// resource of type typ: those of the type itself plus those whose rules may
// target it (job.read on a container). Sorted.
func (c *Catalog) Applicable(typ string) []string {
	var out []string
	for _, cp := range c.caps {
		if cp.OwnerOnly {
			continue
		}
		if cp.Type == typ || slices.Contains(cp.Resources, typ) {
			out = append(out, cp.Key)
		}
	}
	sort.Strings(out)
	return out
}

// Keys returns every key, sorted.
func (c *Catalog) Keys() []string {
	out := make([]string, 0, len(c.caps))
	for _, cp := range c.caps {
		out = append(out, cp.Key)
	}
	sort.Strings(out)
	return out
}

// AllowsScope reports whether a rule for key may use the scope kind
// ("instance", "environment" or a resource type).
func (cp Capability) AllowsScope(kind, resourceType string) bool {
	if cp.OwnerOnly {
		return false
	}
	switch kind {
	case "instance":
		return cp.Instance
	case "environment":
		return cp.Environment
	case "resource":
		return slices.Contains(cp.Resources, resourceType)
	}
	return false
}

var defaultCatalog = func() *Catalog {
	c, err := New(Version, resourceTypes(), capabilities())
	if err != nil {
		panic(err)
	}
	return c
}()

// Default returns the v1 catalog.
func Default() *Catalog { return defaultCatalog }
