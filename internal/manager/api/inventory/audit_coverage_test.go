package inventory

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// memAudit collects records in memory.
type memAudit struct {
	mu   sync.Mutex
	evs  []domain.AuditEvent
	ctxs []context.Context
}

func (m *memAudit) Record(ctx context.Context, ev domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, ev)
	m.ctxs = append(m.ctxs, ctx)
	return nil
}

func (m *memAudit) Records(context.Context, domain.AuditFilter) ([]domain.AuditRecord, error) {
	return nil, nil
}

var pathParam = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)\}`)

// TestEveryCatalogedMutatingRouteIsAudited is the #30 completeness check
// over the whole #4 catalog: every non-GET route in
// api/route-inventory.yaml — planned or implemented — is registered through
// api.Register with its inventory metadata and called; each call must
// append exactly one audit record with the route's action (its capability
// key, or the key derived from its operation ID for pseudo-capabilities and
// selectors), its operation ID, the caller, the path-parameter targets
// and a success outcome. Because api.Register is the only way to register
// an operation (TestOperationsRegisteredOnlyThroughRegister), a feature
// that implements one of these routes later cannot ship it unaudited.
// Served operations are checked with their real handlers in
// api.TestEveryServedMutatingOperationIsAudited.
func TestEveryCatalogedMutatingRouteIsAudited(t *testing.T) {
	inv := load(t)
	checked := 0
	for _, r := range inv.Routes {
		if r.Method == http.MethodGet || r.Status == StatusImplemented {
			continue // implemented ones: api.TestEveryServedMutatingOperationIsAudited
		}
		rec := &memAudit{}
		mux := http.NewServeMux()
		a := api.New(mux, api.Deps{Audit: rec})
		op := api.Operation{
			Operation:  huma.Operation{OperationID: r.OperationID, Method: r.Method, Path: r.Path, Summary: "stub"},
			Capability: api.Capability(r.Capability), Scope: api.Scope(r.Scope),
		}
		for _, v := range r.CapabilityValues {
			op.CapabilityValues = append(op.CapabilityValues, api.Capability(v))
		}
		api.Register(a, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

		target := pathParam.ReplaceAllString(r.Path, "v-$1")
		req := httptest.NewRequest(r.Method, target, nil)
		ctx, err := authz.WithPrincipal(req.Context(), authz.Principal{Kind: authz.KindAPIToken, UserID: "u-1", TokenID: "t-1"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		if w.Code >= 300 {
			t.Errorf("%s: stub answered %d", r.Key(), w.Code)
			continue
		}
		if len(rec.evs) != 1 {
			t.Errorf("%s (%s): %d audit records, want 1", r.Key(), r.OperationID, len(rec.evs))
			continue
		}
		ev := rec.evs[0]
		want := r.Capability
		if !api.IsCapabilityKey(want) {
			want = audit.ActionForOperation(r.OperationID)
		}
		spec := a.OpenAPI().Paths[r.Path]
		var hop *huma.Operation
		switch r.Method {
		case http.MethodPost:
			hop = spec.Post
		case http.MethodPut:
			hop = spec.Put
		case http.MethodPatch:
			hop = spec.Patch
		case http.MethodDelete:
			hop = spec.Delete
		}
		if ev.Action != want || hop == nil || hop.Extensions[api.ExtAudit] != want || ev.OperationID != r.OperationID ||
			ev.Actor != (domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: "u-1", TokenID: "t-1"}) ||
			ev.Outcome != domain.AuditSuccess || !audit.CategoryFor(ev.Action, ev.OperationID).Valid() {
			t.Errorf("%s: record %+v, want action %s", r.Key(), ev, want)
			continue
		}
		// Every path parameter is a target (the environment as the
		// record's environment when the path names other resources too).
		params := pathParam.FindAllStringSubmatch(r.Path, -1)
		for _, p := range params {
			v := "v-" + p[1]
			found := false
			for _, tg := range ev.Targets {
				if tg.ID == v {
					found = true
				}
			}
			if p[1] == "environmentId" && ev.EnvironmentID == v {
				found = true
			}
			if !found {
				t.Errorf("%s: path parameter %s is not recorded (%+v)", r.Key(), p[1], ev)
			}
		}
		if _, ok := audit.RecorderFrom(rec.ctxs[0]); !ok {
			t.Errorf("%s: handlers cannot reach audit.Record", r.Key())
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("only %d mutating routes checked", checked)
	}
	t.Logf("%d mutating catalog routes emit audit records", checked)
}

// TestOperationsRegisteredOnlyThroughRegister: huma's registration helpers
// bypass the contract and the audit middleware; only api.Register may
// call them.
func TestOperationsRegisteredOnlyThroughRegister(t *testing.T) {
	forbidden := regexp.MustCompile(`\bhuma\.(Register|Get|Post|Put|Patch|Delete)\(`)
	allowed := filepath.Join("internal", "manager", "api", "register.go")
	var offenders []string
	for _, dir := range []string{"internal", "cmd", "tools", "test"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if rel == allowed {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if forbidden.Match(b) {
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("operations registered outside api.Register (no contract metadata, no audit record): %v", offenders)
	}
	// The scan itself works.
	b, err := os.ReadFile(filepath.Join(root, allowed))
	if err != nil || !forbidden.Match(b) {
		t.Fatalf("scan does not recognize huma.Register in %s: %v", allowed, err)
	}
}
