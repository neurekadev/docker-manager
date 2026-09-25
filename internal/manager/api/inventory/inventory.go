// Package inventory loads api/route-inventory.yaml — every /api/v1 route of
// the #4 endpoint catalog with its operation ID, capability, scope, owning
// issue, kind and status — and reconciles it with the generated OpenAPI
// document. The reconciliation runs in TestRouteInventory; the summary is
// printed by `go run ./tools/routeinventory`.
package inventory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neurekadev/dockyard/internal/manager/api"
)

// Route kinds.
const (
	KindJSON      = "json"
	KindStream    = "stream"
	KindWebSocket = "websocket"
)

// Route statuses.
const (
	StatusPlanned     = "planned"
	StatusImplemented = "implemented"
)

// Route is one public API operation.
type Route struct {
	Method           string   `yaml:"method"`
	Path             string   `yaml:"path"`
	OperationID      string   `yaml:"operationId"`
	Capability       string   `yaml:"capability"`
	CapabilityValues []string `yaml:"capabilityValues"`
	Scope            string   `yaml:"scope"`
	Owner            int      `yaml:"owner"`
	Kind             string   `yaml:"kind"`
	Status           string   `yaml:"status"`
	Notes            string   `yaml:"notes"`
	// SessionOnly marks routes API tokens can never call (#31): sign-in
	// and factor flows, token management, Recovery Key administration.
	// Owner routes and routes with an owner-only catalog capability are
	// session-only anyway and do not set it.
	SessionOnly bool `yaml:"sessionOnly"`
}

// AcceptsAPITokens reports whether an API token may call the route (#31).
func (r Route) AcceptsAPITokens() bool {
	op := api.Operation{Capability: api.Capability(r.Capability), SessionOnly: r.SessionOnly}
	for _, v := range r.CapabilityValues {
		op.CapabilityValues = append(op.CapabilityValues, api.Capability(v))
	}
	return op.AcceptsAPITokens()
}

// Key is "METHOD path".
func (r Route) Key() string { return r.Method + " " + r.Path }

// AgentRoute is one private /agent/v1 route (docs/protocol/agent-v1.md).
type AgentRoute struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
	Kind   string `yaml:"kind"`
	Auth   string `yaml:"auth"`
	Owner  int    `yaml:"owner"`
	Status string `yaml:"status"`
}

// Inventory is the parsed file.
type Inventory struct {
	Version     int          `yaml:"version"`
	Routes      []Route      `yaml:"routes"`
	AgentRoutes []AgentRoute `yaml:"agentRoutes"`
}

// Load reads and strictly parses an inventory file.
func Load(path string) (*Inventory, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse strictly parses inventory YAML (unknown fields are errors).
func Parse(b []byte) (*Inventory, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var inv Inventory
	if err := dec.Decode(&inv); err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	return &inv, nil
}

// CapabilityCatalog answers whether a capability key exists in the #17
// permission catalog.
type CapabilityCatalog interface {
	Has(key string) bool
}

var (
	methods     = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	pathParamRE = regexp.MustCompile(`\{([^}]*)\}`)
	camelRE     = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	segmentRE   = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*|\{[a-z][A-Za-z0-9]*\})$`)
)

// Validate checks every entry on its own and the inventory for duplicates.
func (inv *Inventory) Validate() []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if inv.Version != 1 {
		fail("inventory version %d, want 1", inv.Version)
	}
	byKey, byID := map[string]bool{}, map[string]string{}
	for _, r := range inv.Routes {
		where := r.Key()
		if !slices.Contains(methods, r.Method) {
			fail("%s: method must be one of %v", where, methods)
		}
		if !strings.HasPrefix(r.Path, api.BasePath+"/") {
			fail("%s: path must start with %s/", where, api.BasePath)
		}
		for _, seg := range strings.Split(strings.TrimPrefix(r.Path, api.BasePath+"/"), "/") {
			if !segmentRE.MatchString(seg) {
				fail("%s: path segment %q must be lower-kebab or a {camelCase} parameter", where, seg)
			}
		}
		for _, m := range pathParamRE.FindAllStringSubmatch(r.Path, -1) {
			if !camelRE.MatchString(m[1]) {
				fail("%s: path parameter %q must be camelCase", where, m[1])
			}
		}
		if byKey[where] {
			fail("%s: listed twice", where)
		}
		byKey[where] = true
		if prev, dup := byID[r.OperationID]; dup {
			fail("%s: operationId %q already used by %s", where, r.OperationID, prev)
		}
		byID[r.OperationID] = where
		op := api.Operation{Capability: api.Capability(r.Capability), Scope: api.Scope(r.Scope)}
		op.OperationID, op.Method, op.Path, op.Summary = r.OperationID, r.Method, r.Path, "inventory"
		for _, v := range r.CapabilityValues {
			op.CapabilityValues = append(op.CapabilityValues, api.Capability(v))
		}
		if err := op.Validate(); err != nil {
			fail("%s: %v", where, err)
		}
		if strings.Contains(r.Path, "{environmentId}") && api.IsCapabilityKey(r.Capability) &&
			r.Scope != string(api.ScopeEnvironment) && r.Scope != string(api.ScopeResource) {
			fail("%s: environment-scoped path needs scope environment or resource, not %s", where, r.Scope)
		}
		if r.Owner < 1 || r.Owner > 35 {
			fail("%s: owner must be a roadmap issue number (1-35)", where)
		}
		if !slices.Contains([]string{KindJSON, KindStream, KindWebSocket}, r.Kind) {
			fail("%s: kind %q must be json, stream or websocket", where, r.Kind)
		}
		if r.Status != StatusPlanned && r.Status != StatusImplemented {
			fail("%s: status %q must be planned or implemented", where, r.Status)
		}
		if r.SessionOnly && (r.Capability == string(api.CapabilityPublic) || r.Capability == string(api.CapabilityOwner)) {
			fail("%s: sessionOnly is for authenticated or capability routes (owner routes never accept API tokens; public routes need no credential)", where)
		}
	}
	for _, r := range inv.AgentRoutes {
		if !strings.HasPrefix(r.Path, "/agent/v1/") || !slices.Contains(methods, r.Method) ||
			(r.Kind != KindJSON && r.Kind != KindWebSocket) || r.Auth == "" ||
			(r.Status != StatusPlanned && r.Status != StatusImplemented) {
			fail("agent route %s %s is malformed", r.Method, r.Path)
		}
	}
	return errs
}

// specOperation is the part of an OpenAPI operation the reconciliation reads.
type specOperation struct {
	OperationID      string                     `json:"operationId"`
	Capability       string                     `json:"x-dockyard-capability"`
	CapabilityValues []string                   `json:"x-dockyard-capability-values"`
	Scope            string                     `json:"x-dockyard-scope"`
	Responses        map[string]json.RawMessage `json:"responses"`
	Security         []map[string][]string      `json:"security"`
}

// acceptsBearer reports whether the operation documents the API token
// (bearer) security scheme.
func (op specOperation) acceptsBearer() bool {
	for _, alt := range op.Security {
		if _, ok := alt[api.SecurityBearer]; ok {
			return true
		}
	}
	return false
}

type specResponse struct {
	Content map[string]json.RawMessage `json:"content"`
}

// kind derives json/stream/websocket from the documented responses.
func (op specOperation) kind() string {
	if _, ok := op.Responses["101"]; ok {
		return KindWebSocket
	}
	for code, raw := range op.Responses {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		var r specResponse
		_ = json.Unmarshal(raw, &r)
		for ct := range r.Content {
			if ct != "application/json" && !strings.HasSuffix(ct, "+json") {
				return KindStream
			}
		}
	}
	return KindJSON
}

// Report summarizes a reconciliation.
type Report struct {
	Errors []error
	// Counts by status, and planned routes per owning issue.
	Implemented, Planned int
	PlannedByOwner       map[int]int
	// CatalogChecked is false when no permission catalog was supplied.
	CatalogChecked bool
}

// Reconcile compares the inventory with an OpenAPI 3.1 document (JSON) and,
// when catalog is non-nil, every grantable capability key with the #17
// permission catalog.
func Reconcile(inv *Inventory, spec []byte, catalog CapabilityCatalog) Report {
	rep := Report{Errors: inv.Validate(), PlannedByOwner: map[int]int{}, CatalogChecked: catalog != nil}
	fail := func(format string, args ...any) { rep.Errors = append(rep.Errors, fmt.Errorf(format, args...)) }
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		fail("parse OpenAPI: %v", err)
		return rep
	}
	served := map[string]specOperation{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			m := strings.ToUpper(method)
			if !slices.Contains(methods, m) {
				continue
			}
			var op specOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				fail("parse %s %s: %v", m, path, err)
				continue
			}
			served[m+" "+path] = op
		}
	}
	listed := map[string]bool{}
	for _, r := range inv.Routes {
		listed[r.Key()] = true
		op, inSpec := served[r.Key()]
		switch r.Status {
		case StatusImplemented:
			rep.Implemented++
			if !inSpec {
				fail("%s (%s) is marked implemented but is not in the OpenAPI document", r.Key(), r.OperationID)
				continue
			}
			if op.OperationID != r.OperationID {
				fail("%s: operationId is %q in the spec, %q in the inventory", r.Key(), op.OperationID, r.OperationID)
			}
			if op.Capability != r.Capability {
				fail("%s: capability is %q in the spec, %q in the inventory", r.Key(), op.Capability, r.Capability)
			}
			if !slices.Equal(op.CapabilityValues, r.CapabilityValues) {
				fail("%s: capability values are %v in the spec, %v in the inventory", r.Key(), op.CapabilityValues, r.CapabilityValues)
			}
			if op.Scope != r.Scope {
				fail("%s: scope is %q in the spec, %q in the inventory", r.Key(), op.Scope, r.Scope)
			}
			if k := op.kind(); k != r.Kind {
				fail("%s: the spec documents a %s route, the inventory says %s", r.Key(), k, r.Kind)
			}
			if r.Capability != string(api.CapabilityPublic) && op.acceptsBearer() != r.AcceptsAPITokens() {
				fail("%s: API tokens (#31) are %s by the spec but %s by the inventory (sessionOnly, owner or owner-only capability)",
					r.Key(), acceptance(op.acceptsBearer()), acceptance(r.AcceptsAPITokens()))
			}
		case StatusPlanned:
			rep.Planned++
			rep.PlannedByOwner[r.Owner]++
			if inSpec {
				fail("%s (%s) is served but still marked planned; set status: implemented", r.Key(), r.OperationID)
			}
		}
		if catalog != nil {
			keys := append([]string{r.Capability}, r.CapabilityValues...)
			for _, k := range keys {
				if api.IsCapabilityKey(k) && !catalog.Has(k) {
					fail("%s: capability %q is not in the permission catalog (#17)", r.Key(), k)
				}
			}
		}
	}
	var extra []string
	for key, op := range served {
		if !listed[key] {
			extra = append(extra, fmt.Sprintf("%s (%s)", key, op.OperationID))
		}
	}
	sort.Strings(extra)
	for _, e := range extra {
		fail("%s is in the OpenAPI document but missing from api/route-inventory.yaml", e)
	}
	return rep
}

// Summary renders per-owner status counts as a Markdown table.
func Summary(inv *Inventory) string {
	type counts struct{ implemented, planned int }
	by := map[int]*counts{}
	for _, r := range inv.Routes {
		c := by[r.Owner]
		if c == nil {
			c = &counts{}
			by[r.Owner] = c
		}
		if r.Status == StatusImplemented {
			c.implemented++
		} else {
			c.planned++
		}
	}
	owners := make([]int, 0, len(by))
	for o := range by {
		owners = append(owners, o)
	}
	sort.Ints(owners)
	var sb strings.Builder
	sb.WriteString("| issue | implemented | planned |\n| --- | --- | --- |\n")
	ti, tp := 0, 0
	for _, o := range owners {
		c := by[o]
		ti += c.implemented
		tp += c.planned
		fmt.Fprintf(&sb, "| #%d | %d | %d |\n", o, c.implemented, c.planned)
	}
	fmt.Fprintf(&sb, "| **total** | **%d** | **%d** |\n", ti, tp)
	return sb.String()
}

func acceptance(ok bool) string {
	if ok {
		return "accepted"
	}
	return "refused"
}
