package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/manager/regclient"
	"github.com/neurekadev/dockyard/internal/manager/regclient/regtest"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// Registry connections (#19) through the real manager: owner-only
// administration, write-only secrets, shaping for other users, connection
// tests against a fake registry, match previews, audit and secrecy.

type registryBody struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Host              string   `json:"host"`
	Status            string   `json:"status"`
	View              string   `json:"view"`
	Actions           []string `json:"actions"`
	Username          string   `json:"username"`
	RepositoryPattern string   `json:"repositoryPattern"`
	Revision          int64    `json:"revision"`
	Secret            *struct {
		Set         bool   `json:"set"`
		Fingerprint string `json:"fingerprint"`
		Version     int    `json:"version"`
	} `json:"secret"`
	LastCheck *struct {
		Result string `json:"result"`
	} `json:"lastCheck"`
}

func registryEnv(t *testing.T) (*env, *regtest.Registry) {
	t.Helper()
	secrets := canary.New()
	reg := regtest.New(t, regtest.AuthBearer, "robot", secrets.New(canary.RegistryCredential, "fake registry password"))
	e := newEnv(t, func(o *Options) { o.RegistryHTTPClient = reg.Client() })
	for _, c := range secrets.All() {
		e.secrets.Register(c.Kind, c.Name, c.Value)
	}
	t.Cleanup(func() { scanDatabase(t, e) })
	return e, reg
}

// scanDatabase checks the whole database file (after a checkpoint) for
// plaintext secrets: connections, audit records, jobs, idempotency.
func scanDatabase(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.m.DB().ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Errorf("checkpoint: %v", err)
	}
	path := e.m.opts.Config.DatabasePath()
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
		e.secrets.AssertClean(t, "database file "+filepath.Base(p), b)
	}
}

func TestRegistryConnectionsThroughTheAPI(t *testing.T) {
	e, reg := registryEnv(t)
	owner, _ := e.setupOwner()
	reg.Private["team/app"] = true
	digest := reg.Put("team/app", "1.0", regclient.MediaOCIManifest, []byte(regtest.ManifestBody))

	// Create: 201 with metadata and fingerprint only.
	var c registryBody
	r := owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{
		"name": "Fake", "host": "https://" + reg.Host() + "/", "username": "robot", "secret": reg.Password, "repositoryPattern": "team/*",
	})
	r.json(t, &c)
	if c.Host != reg.Host() || c.Status != "active" || c.View != "full" || c.Secret == nil || !c.Secret.Set ||
		!strings.HasPrefix(c.Secret.Fingerprint, "fp_") || c.Secret.Version != 1 || r.header.Get("ETag") == "" {
		t.Fatalf("created %+v %v", c, r.header)
	}
	if strings.Contains(string(r.body), `"secret":"`) {
		t.Fatalf("secret echoed: %s", r.body)
	}
	hub := map[string]any{"name": "Hub", "host": "registry-1.docker.io", "username": "acme",
		"secret": e.secrets.New(canary.RegistryCredential, "hub token"), "repositoryPattern": "acme/*"}
	var h registryBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", hub).json(t, &h)
	if h.Host != "docker.io" {
		t.Fatalf("hub host %q", h.Host)
	}
	owner.fail(http.StatusConflict, "registry_connection_name_taken", http.MethodPost, "/api/v1/registries", hub)
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/registries",
		map[string]any{"name": "Bad", "host": "intranet", "username": "u", "secret": "whatever-123"})

	// List and get.
	var page struct {
		Items []registryBody `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/registries", nil).json(t, &page)
	if len(page.Items) != 2 || page.Items[0].ID != c.ID {
		t.Fatalf("list %+v", page.Items)
	}
	got := owner.must(http.StatusOK, http.MethodGet, "/api/v1/registries/"+c.ID, nil)
	etag := got.header.Get("ETag")

	// Connection test against the fake registry (bearer token flow).
	var test struct {
		OK             bool   `json:"ok"`
		ErrorClass     string `json:"errorClass"`
		Digest         string `json:"digest"`
		PlatformDigest string `json:"platformDigest"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/"+c.ID+"/connection-tests",
		map[string]any{"imageReference": reg.Host() + "/team/app:1.0", "platform": "linux/amd64"}).json(t, &test)
	if !test.OK || test.Digest != digest || test.PlatformDigest != digest {
		t.Fatalf("test %+v", test)
	}
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/registries/"+c.ID+"/connection-tests",
		map[string]any{"imageReference": "ghcr.io/team/app:1"})
	// A connection test does not change the configuration revision.
	if cur := owner.must(http.StatusOK, http.MethodGet, "/api/v1/registries/"+c.ID, nil); cur.header.Get("ETag") != etag {
		t.Fatalf("etag %s -> %s", etag, cur.header.Get("ETag"))
	}

	// Match preview: selected connection, metadata only.
	var m struct {
		Selection  string        `json:"selection"`
		Host       string        `json:"host"`
		Repository string        `json:"repository"`
		Selected   *registryBody `json:"selected"`
		Candidates []struct {
			Connection registryBody `json:"connection"`
		} `json:"candidates"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": "index.docker.io/acme/app:2"}).json(t, &m)
	if m.Selection != "connection" || m.Host != "docker.io" || m.Repository != "acme/app" || m.Selected == nil || m.Selected.ID != h.ID ||
		len(m.Candidates) != 1 {
		t.Fatalf("match %+v", m)
	}
	m.Selected = nil
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": "nginx"}).json(t, &m)
	if m.Selection != "anonymous" || m.Selected != nil {
		t.Fatalf("anonymous match %+v", m)
	}

	// PATCH needs If-Match; rotation; revocation; delete.
	owner.fail(http.StatusPreconditionRequired, "precondition_required", http.MethodPatch, "/api/v1/registries/"+c.ID, map[string]any{"priority": 5})
	var upd registryBody
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/registries/"+c.ID, map[string]any{"priority": 5}, header("If-Match", etag)).json(t, &upd)
	owner.fail(http.StatusPreconditionFailed, "precondition_failed", http.MethodPatch, "/api/v1/registries/"+c.ID, map[string]any{"priority": 6},
		header("If-Match", etag))
	rotated := e.secrets.New(canary.RegistryCredential, "rotated registry password")
	var rot registryBody
	r = owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/"+c.ID+"/credential-rotations", map[string]any{"secret": rotated},
		header("If-Match", `"`+itoa(int(upd.Revision))+`"`))
	r.json(t, &rot)
	if rot.Secret.Version != 2 || rot.Secret.Fingerprint == c.Secret.Fingerprint {
		t.Fatalf("rotated %+v", rot)
	}
	// The fake registry does not know the rotated password.
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/"+c.ID+"/connection-tests",
		map[string]any{"imageReference": reg.Host() + "/team/app:1.0"}).json(t, &test)
	if test.OK || test.ErrorClass != "unauthorized" {
		t.Fatalf("test with rotated credential %+v", test)
	}
	var rev registryBody
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/registries/"+c.ID, map[string]any{"status": "revoked"},
		header("If-Match", r.header.Get("ETag"))).json(t, &rev)
	if rev.Status != "revoked" || rev.Secret.Set || rev.Secret.Fingerprint != "" {
		t.Fatalf("revoked %+v", rev)
	}
	owner.fail(http.StatusConflict, "registry_connection_revoked", http.MethodPost, "/api/v1/registries/"+c.ID+"/connection-tests",
		map[string]any{"imageReference": reg.Host() + "/team/app:1.0"})
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": reg.Host() + "/team/app:1"}).json(t, &m)
	if m.Selection != "revoked" {
		t.Fatalf("revoked match %+v", m)
	}
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/registries/"+c.ID, nil, header("If-Match", `"`+itoa(int(rev.Revision))+`"`))
	owner.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/registries/"+c.ID, nil)

	// Every change is audited; the trail holds IDs and fingerprints, no
	// secret (scanDatabase checks the whole file at cleanup).
	var actions []string
	if err := e.m.DB().NewRaw("SELECT action FROM audit_events WHERE action LIKE 'registry%' ORDER BY seq").Scan(testutil.Context(t), &actions); err != nil {
		t.Fatal(err)
	}
	want := []string{"registry.create", "registry.create", "registry.create", "registry.create", "registry_connection_test.create",
		"registry_connection_test.create", "registry.match", "registry.match"}
	for _, w := range want {
		if !containsString(actions, w) {
			t.Fatalf("audit actions %v lack %s", actions, w)
		}
	}
	for _, w := range []string{"registry.update", "registry_credential_rotation.create", "registry.delete"} {
		if !containsString(actions, w) {
			t.Fatalf("audit actions %v lack %s", actions, w)
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestRegistryAccessForOtherUsers: administration is owner-only (also for
// API tokens, even the owner's); other users see connections only with
// registry.read, shaped per connection, and never a secret.
func TestRegistryAccessForOtherUsers(t *testing.T) {
	e, reg := registryEnv(t)
	owner, _ := e.setupOwner()
	var a, b registryBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{"name": "A", "host": reg.Host(),
		"username": "robot", "secret": reg.Password}).json(t, &a)
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{"name": "B", "host": "ghcr.io",
		"username": "bot", "secret": e.secrets.New(canary.RegistryCredential, "ghcr token")}).json(t, &b)

	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all")
	var page struct {
		Items []registryBody `json:"items"`
	}
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/registries", nil).json(t, &page)
	if len(page.Items) != 0 {
		t.Fatalf("rita sees %+v", page.Items)
	}
	rita.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/registries/"+a.ID, nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/registries", map[string]any{"name": "X", "host": "quay.io",
		"username": "u", "secret": "whatever-12345"})
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/registries/"+a.ID+"/connection-tests",
		map[string]any{"imageReference": reg.Host() + "/x:1"})
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": "nginx"})

	// registry.read on one connection: full view of that one only.
	owner.putRules("/api/v1/users/"+ritaID+"/permissions", "allow registry.read @registry:"+a.ID)
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/registries", nil).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Items[0].View != "full" || page.Items[0].Username != "robot" {
		t.Fatalf("rita sees %+v", page.Items)
	}
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": "nginx"})
	// Still not an administrator; the owner check comes before any lookup,
	// so existing and unknown IDs look alike.
	for _, id := range []string{a.ID, b.ID, "0190a6e0-0000-7000-8000-000000000000"} {
		rita.fail(http.StatusForbidden, "forbidden", http.MethodPatch, "/api/v1/registries/"+id, map[string]any{"priority": 1},
			header("If-Match", `"1"`))
		rita.fail(http.StatusForbidden, "forbidden", http.MethodDelete, "/api/v1/registries/"+id, nil, header("If-Match", `"1"`))
	}

	// Instance-wide registry.read allows match previews.
	owner.putRules("/api/v1/users/"+ritaID+"/permissions", "allow registry.read @all")
	var m struct {
		Selection string `json:"selection"`
	}
	rita.must(http.StatusOK, http.MethodPost, "/api/v1/registries/matches", map[string]any{"imageReference": "ghcr.io/o/app"}).json(t, &m)
	if m.Selection != "connection" {
		t.Fatalf("match %+v", m)
	}

	// API tokens never administer registry credentials, not even the owner's.
	_, secret := owner.createToken("ci", "allow registry.read @all")
	bt := e.bot(secret)
	bt.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodPost, "/api/v1/registries",
		map[string]any{"name": "T", "host": "quay.io", "username": "u", "secret": "whatever-12345"})
	bt.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodPost, "/api/v1/registries/"+a.ID+"/credential-rotations",
		map[string]any{"secret": "whatever-12345"}, header("If-Match", `"1"`))
	bt.must(http.StatusOK, http.MethodGet, "/api/v1/registries", nil).json(t, &page)
	if len(page.Items) != 2 {
		t.Fatalf("token sees %d", len(page.Items))
	}
}
