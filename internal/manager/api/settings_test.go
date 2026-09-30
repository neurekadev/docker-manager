package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/settings"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type settingsFixture struct {
	h   http.Handler
	bus *events.Bus
	log *audit.Log
}

func newSettingsHandler(t *testing.T, pol *authztest.Policy) http.Handler {
	return newSettingsFixture(t, pol).h
}

func newSettingsFixture(t *testing.T, pol *authztest.Policy) settingsFixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log, err := audit.New(audit.Options{DB: db, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(clk)
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Clock: clk, Settings: settings.New(db, clk), InstanceID: "inst-1", Audit: log, Events: bus,
		SSEHeartbeat: 20 * time.Second, FileLimits: domain.FileLimits{Upload: 1 << 20, Edit: 2 << 20}, Idempotency: &memIdempotency{}, Builds: emptyBuilds{},
		Deployment: DeploymentInfo{PublicURL: "https://docker.example.com", TrustedProxies: 2, MetricsEnabled: true}})
	return settingsFixture{h: authztest.Authenticate(withTestContext(t, mux, "")), bus: bus, log: log}
}

// GET needs settings.read, PATCH settings.manage (instance scope); the
// owner holds both; restricted members neither.
func TestSettingsRoutesAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("rita", "restricted").
		Member("rea", "readers").Group("readers", "allow settings.read @all").
		Member("sue", "settings").Group("settings", "allow settings.read @all", "allow settings.manage @all")
	h := newSettingsHandler(t, pol)
	calls := authztest.Routes(t, nil, "/api/v1/settings")
	var own []authztest.Call
	for _, c := range calls {
		if c.OperationID != "get-settings" && c.OperationID != "update-settings" {
			continue // /settings/security is the owner's (identity tests)
		}
		c.Headers = map[string]string{"If-Match": "*"}
		if c.OperationID == "update-settings" {
			c.Body = map[string]any{"name": "Lab"}
		}
		own = append(own, c)
	}
	if len(own) != 2 {
		t.Fatalf("settings routes: %+v", own)
	}
	allowed, denied := authztest.Split(own, "settings.read", "settings.manage")
	authztest.AssertOnly(t, h, "sue", allowed, denied)
	readOnly, writes := authztest.Split(own, "settings.read")
	authztest.AssertOnly(t, h, "rea", readOnly, writes)
	authztest.AssertOnly(t, h, "rita", nil, own)
	authztest.AssertOnly(t, h, "olga", own, nil)
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/settings"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

func TestSettingsReadAndEdit(t *testing.T) {
	f := newSettingsFixture(t, authztest.New().Owner("olga"))
	h := f.h
	changes := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.ResourceChanged })
	defer changes.Close()
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/settings"})
	var s InstanceSettings
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"1"` || json.Unmarshal(r.Body, &s) != nil {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Body)
	}
	// Unset file limits show the defaults.
	want := DeploymentSettings{PublicURL: "https://docker.example.com", TrustedProxyCount: 2, StreamHeartbeatSeconds: 20,
		FilesMaxUploadBytes: 1 << 20, FilesMaxEditBytes: 2 << 20, FilesMaxDownloadBytes: 10 << 30, FilesMaxExtractBytes: 10 << 30,
		FilesMaxExtractRatio: 100, FilesMaxArchiveEntries: 100_000, MetricsEndpoint: true}
	if s.Name != "Docker Manager" || s.InstanceID != "inst-1" || s.Revision != 1 || s.Deployment != want {
		t.Fatalf("%+v", s)
	}
	patch := func(ifMatch string, body any) authztest.Response {
		hd := map[string]string{}
		if ifMatch != "" {
			hd["If-Match"] = ifMatch
		}
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/settings", Body: body, Headers: hd})
	}
	if r := patch("", map[string]any{"name": "Lab"}); r.Status != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.Status)
	}
	for _, bad := range []string{"   ", "Lab\x07", strings.Repeat("x", 65), ""} {
		r := patch(`"1"`, map[string]any{"name": bad})
		if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), `"body.name"`) {
			t.Fatalf("name %q: %d %s", bad, r.Status, r.Body)
		}
	}
	// The deployment summary is read-only: unknown fields are refused.
	if r := patch(`"1"`, map[string]any{"deployment": map[string]any{"publicUrl": "https://evil.example"}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("deployment write: %d %s", r.Status, r.Body)
	}
	r = patch(`"1"`, map[string]any{"name": "  Homelab  "})
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"2"` || json.Unmarshal(r.Body, &s) != nil || s.Name != "Homelab" ||
		s.Revision != 2 || !s.UpdatedAt.Equal(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	// Audited with a diff and announced to live streams (#23).
	if len(changes.C()) != 1 {
		t.Fatalf("%d change events", len(changes.C()))
	}
	if e := <-changes.C(); e.ResourceType != "settings" || e.ResourceID != SettingsInstanceID || e.Attributes["action"] != "settings.manage" {
		t.Fatalf("event %+v", e)
	}
	recs, err := f.log.Records(testutil.Context(t), domain.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var diff string
	for _, rec := range recs {
		if rec.OperationID == "update-settings" && rec.Outcome == domain.AuditSuccess {
			diff = string(rec.Details)
		}
	}
	if !strings.Contains(diff, `"Docker Manager"`) || !strings.Contains(diff, `"Homelab"`) {
		t.Fatalf("audit details %s", diff)
	}
	if r := patch(`"1"`, map[string]any{"name": "Other"}); r.Status != http.StatusPreconditionFailed || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("stale: %d %v", r.Status, r.Header)
	}
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/settings"})
	if json.Unmarshal(r.Body, &s) != nil || s.Name != "Homelab" || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("after: %s", r.Body)
	}
}
