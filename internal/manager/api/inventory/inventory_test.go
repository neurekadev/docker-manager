package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/manager/api"
)

var root = filepath.Join("..", "..", "..", "..")

// permissionCatalog is the #17 permission catalog. It is nil until #17
// lands; #17 sets it (for example to its catalog's key set) so every
// grantable capability in the inventory must exist in the catalog.
// TODO(#17): wire the permission catalog here.
var permissionCatalog CapabilityCatalog

func load(t *testing.T) *Inventory {
	t.Helper()
	inv, err := Load(filepath.Join(root, "api", "route-inventory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// TestRouteInventory reconciles api/route-inventory.yaml with the OpenAPI
// document the manager serves.
func TestRouteInventory(t *testing.T) {
	inv := load(t)
	spec, err := api.SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	rep := Reconcile(inv, spec, permissionCatalog)
	for _, e := range rep.Errors {
		t.Error(e)
	}
	if rep.Implemented == 0 || rep.Planned+rep.Implemented != len(inv.Routes) {
		t.Fatalf("counts: %d implemented, %d planned of %d", rep.Implemented, rep.Planned, len(inv.Routes))
	}
	t.Logf("route inventory: %d routes, %d implemented, %d planned", len(inv.Routes), rep.Implemented, rep.Planned)
	t.Logf("planned per owning issue: %v", rep.PlannedByOwner)
	if !rep.CatalogChecked {
		t.Log("pending: capability keys are checked against the #17 permission catalog once it exists (permissionCatalog)")
	}
}

// TestAgentRoutesDocumented: every private agent route is specified in the
// protocol document.
func TestAgentRoutesDocumented(t *testing.T) {
	inv := load(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "protocol", "agent-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.AgentRoutes) != 2 {
		t.Fatalf("agent routes %v", inv.AgentRoutes)
	}
	for _, r := range inv.AgentRoutes {
		if !strings.Contains(string(b), r.Method+" "+r.Path) {
			t.Errorf("docs/protocol/agent-v1.md does not specify %s %s", r.Method, r.Path)
		}
	}
}

// TestStreamRoutesDocumented: every stream and WebSocket route has its wire
// contract in docs/api/streams.md.
func TestStreamRoutesDocumented(t *testing.T) {
	inv := load(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "api", "streams.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, r := range inv.Routes {
		if r.Kind == KindJSON {
			continue
		}
		if !strings.Contains(doc, strings.TrimPrefix(r.Path, api.BasePath)) && !strings.Contains(doc, "`"+r.OperationID+"`") {
			t.Errorf("docs/api/streams.md does not describe %s %s (%s)", r.Method, r.Path, r.OperationID)
		}
	}
}

type fakeCatalog map[string]bool

func (c fakeCatalog) Has(k string) bool { return c[k] }

const miniSpec = `{"openapi":"3.1.0","paths":{
 "/api/v1/things":{"get":{"operationId":"list-things","x-dockyard-capability":"thing.read","x-dockyard-scope":"resource",
   "responses":{"200":{"content":{"application/json":{}}}}}},
 "/api/v1/things/{thingId}/events":{"get":{"operationId":"stream-thing-events","x-dockyard-capability":"thing.read","x-dockyard-scope":"resource",
   "responses":{"200":{"content":{"text/event-stream":{}}}}}},
 "/api/v1/extra":{"post":{"operationId":"create-extra","x-dockyard-capability":"authenticated","x-dockyard-scope":"none","responses":{}}}
}}`

// TestReconcileCatchesDrift proves each rule fails when violated.
func TestReconcileCatchesDrift(t *testing.T) {
	inv, err := Parse([]byte(`version: 1
routes:
  - {method: GET, path: /api/v1/things, operationId: list-things, capability: thing.list, scope: resource, owner: 5, kind: json, status: implemented}
  - {method: GET, path: "/api/v1/things/{thingId}/events", operationId: stream-thing-events, capability: thing.read, scope: resource, owner: 5, kind: json, status: planned}
  - {method: POST, path: /api/v1/missing, operationId: create-missing, capability: thing.write, scope: resource, owner: 5, kind: json, status: implemented}
  - {method: POST, path: "/api/v1/stacks/{stackId}/operations", operationId: create-stack-operation, capability: "stack.{action}", capabilityValues: [stack.start, stack.stop], scope: resource, owner: 7, kind: json, status: planned}
`))
	if err != nil {
		t.Fatal(err)
	}
	rep := Reconcile(inv, []byte(miniSpec), fakeCatalog{"thing.read": true, "stack.start": true})
	want := []string{
		`GET /api/v1/things: capability is "thing.read" in the spec, "thing.list" in the inventory`,
		`GET /api/v1/things/{thingId}/events (stream-thing-events) is served but still marked planned`,
		`POST /api/v1/missing (create-missing) is marked implemented but is not in the OpenAPI document`,
		`POST /api/v1/extra (create-extra) is in the OpenAPI document but missing from api/route-inventory.yaml`,
		`capability "thing.list" is not in the permission catalog`,
		`capability "stack.stop" is not in the permission catalog`,
	}
	joined := ""
	for _, e := range rep.Errors {
		joined += e.Error() + "\n"
	}
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("reconcile did not report %q; got:\n%s", w, joined)
		}
	}
	if !rep.CatalogChecked || rep.Planned != 2 || rep.Implemented != 2 || rep.PlannedByOwner[7] != 1 {
		t.Fatalf("report %+v", rep)
	}

	// A kind mismatch on an implemented stream route is reported too.
	inv.Routes[1].Status = StatusImplemented
	rep = Reconcile(inv, []byte(miniSpec), nil)
	if !strings.Contains(errorsText(rep), "the spec documents a stream route, the inventory says json") || rep.CatalogChecked {
		t.Fatalf("kind drift not reported: %s", errorsText(rep))
	}
}

func errorsText(rep Report) string {
	var sb strings.Builder
	for _, e := range rep.Errors {
		sb.WriteString(e.Error() + "\n")
	}
	return sb.String()
}

func TestValidateRejectsMalformedEntries(t *testing.T) {
	inv, err := Parse([]byte(`version: 2
routes:
  - {method: FETCH, path: /v2/x, operationId: getX, capability: "Read All", scope: global, owner: 99, kind: grpc, status: done}
  - {method: GET, path: "/api/v1/environments/{environment_id}/Things", operationId: list-env-things, capability: thing.read, scope: resource, owner: 5, kind: json, status: planned}
  - {method: GET, path: "/api/v1/environments/{environmentId}/things", operationId: list-env-things-2, capability: thing.read, scope: instance, owner: 5, kind: json, status: planned}
  - {method: GET, path: "/api/v1/a", operationId: dup, capability: public, scope: none, owner: 2, kind: json, status: planned}
  - {method: GET, path: "/api/v1/a", operationId: dup, capability: public, scope: none, owner: 2, kind: json, status: planned}
  - {method: GET, path: "/api/v1/b", operationId: get-b, capability: owner, scope: resource, owner: 2, kind: json, status: planned}
agentRoutes:
  - {method: GET, path: /api/v1/session, kind: stream, auth: "", owner: 3, status: planned}
`))
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, e := range inv.Validate() {
		text += e.Error() + "\n"
	}
	for _, w := range []string{"version 2", "method must be", "path must start", "kebab-case", "owner must be", "kind \"grpc\"",
		"status \"done\"", "must be camelCase", "lower-kebab", "environment-scoped path", "listed twice", "already used by",
		"owner uses scope instance", "agent route"} {
		if !strings.Contains(text, w) {
			t.Errorf("Validate did not report %q; got:\n%s", w, text)
		}
	}
	if _, err := Parse([]byte("version: 1\nroutes:\n  - {method: GET, surprise: 1}\n")); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestSummary(t *testing.T) {
	s := Summary(load(t))
	if !strings.Contains(s, "| #26 | 4 | 0 |") || !strings.Contains(s, "**total**") {
		t.Fatalf("summary:\n%s", s)
	}
}
