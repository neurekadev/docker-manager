package registries_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/jobs/jobstest"
	"github.com/neurekadev/docker-manager/internal/manager/regclient"
	"github.com/neurekadev/docker-manager/internal/manager/regclient/regtest"
	"github.com/neurekadev/docker-manager/internal/manager/registries"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// guard is a switchable owner guard.
type guard struct {
	mu  sync.Mutex
	err error
}

func (g *guard) RequireOwner(context.Context, bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return "owner", g.err
}

func (g *guard) set(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

// recorder captures audit events.
type recorder struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (r *recorder) Record(_ context.Context, ev domain.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recorder) uses() []domain.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.AuditEvent
	for _, e := range r.events {
		if e.Action == registries.ActionUse {
			out = append(out, e)
		}
	}
	return out
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	dbPath  string
	db      *bun.DB
	clk     *clock.Fake
	guard   *guard
	audit   *recorder
	svc     *registries.Service
	reg     *regtest.Registry
	secrets *canary.Set
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-manager.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, dbPath: path, db: db, clk: testutil.FakeClock(), guard: &guard{}, audit: &recorder{}, secrets: canary.New()}
	f.reg = regtest.New(t, regtest.AuthBearer, "robot", f.secrets.New(canary.RegistryCredential, "registry password"))
	logger := f.secrets.CaptureLogger(t)
	client := regclient.New(regclient.Options{HTTP: f.reg.Client(), Clock: f.clk, Logger: logger, Jitter: func() float64 { return 0 }})
	f.svc, err = registries.New(registries.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: f.clk, Logger: logger,
		Guard: f.guard, Audit: f.audit, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.assertNoLeaks)
	return f
}

// assertNoLeaks scans the whole database file (including the WAL), the
// audit events and the registry's received headers for plaintext secrets.
func (f *fixture) assertNoLeaks() {
	t := f.t
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Errorf("checkpoint: %v", err)
	}
	for _, p := range []string{f.dbPath, f.dbPath + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
		f.secrets.AssertClean(t, "database file "+filepath.Base(p), b)
	}
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	f.secrets.AssertClean(t, "audit events", f.audit.events)
}

func (f *fixture) create(in domain.RegistryConnectionInput) domain.RegistryConnection {
	f.t.Helper()
	c, err := f.svc.Create(f.ctx, in)
	if err != nil {
		f.t.Fatalf("create %s: %v", in.Name, err)
	}
	return c
}

func (f *fixture) robot(name, pattern string) domain.RegistryConnection {
	return f.create(domain.RegistryConnectionInput{Name: name, Host: f.reg.Host(), Username: "robot", Secret: f.reg.Password,
		RepositoryPattern: pattern})
}

func TestCreateSealsTheSecretAndReturnsMetadataOnly(t *testing.T) {
	f := newFixture(t)
	c := f.create(domain.RegistryConnectionInput{Name: "Docker Hub", Host: "index.docker.io", Username: "acme",
		Secret: f.secrets.New(canary.RegistryCredential, "hub token"), RepositoryPattern: "acme/*"})
	if c.Host != "docker.io" || c.RepositoryPattern != "acme/*" || c.CredentialType != domain.RegistryCredentialToken ||
		!c.Active() || c.SecretVersion != 1 || !strings.HasPrefix(c.SecretFingerprint, "fp_") || c.Revision != 1 {
		t.Fatalf("%+v", c)
	}
	got, err := f.svc.Get(f.ctx, c.ID)
	if err != nil || got.SecretFingerprint != c.SecretFingerprint {
		t.Fatalf("%+v %v", got, err)
	}
	var sealed string
	if err := f.db.NewSelect().Table("registry_connections").Column("secret_sealed").Where("id = ?", c.ID).Scan(f.ctx, &sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "dy1.") {
		t.Fatalf("secret not sealed: %q", sealed[:8])
	}
	f.secrets.AssertClean(t, "connection", got)
}

func TestAdministrationIsOwnerOnly(t *testing.T) {
	f := newFixture(t)
	c := f.robot("reg", "")
	for name, err := range map[string]error{"not owner": domain.ErrForbidden, "no step-up": domain.ErrStepUpRequired} {
		f.guard.set(err)
		name := name
		checks := map[string]error{}
		_, checks["create"] = f.svc.Create(f.ctx, domain.RegistryConnectionInput{Name: "x", Host: "ghcr.io", Username: "u", Secret: "s3cr3t-value"})
		_, checks["update"] = f.svc.Update(f.ctx, c.ID, c.Revision, domain.RegistryConnectionPatch{})
		_, checks["rotate"] = f.svc.Rotate(f.ctx, c.ID, c.Revision, domain.RegistryCredentialRotation{Secret: "s3cr3t-value"})
		checks["delete"] = f.svc.Delete(f.ctx, c.ID, c.Revision)
		_, checks["test"] = f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/app:1", "")
		for op, got := range checks {
			if !errors.Is(got, err) {
				t.Errorf("%s: %s = %v, want %v", name, op, got, err)
			}
		}
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	f.create(domain.RegistryConnectionInput{Name: "GHCR", Host: "ghcr.io", Username: "u", Secret: "pat-value-1"})
	cases := map[string]struct {
		in    domain.RegistryConnectionInput
		field string
	}{
		"host":        {domain.RegistryConnectionInput{Name: "a", Host: "not a host", Username: "u", Secret: "s"}, "host"},
		"single word": {domain.RegistryConnectionInput{Name: "a", Host: "intranet", Username: "u", Secret: "s"}, "host"},
		"pattern":     {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "s", RepositoryPattern: "a/*/b"}, "repositoryPattern"},
		"username":    {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "a:b", Secret: "s"}, "username"},
		"secret":      {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "a\nb"}, "secret"},
		"type":        {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "s", CredentialType: "ssh"}, "credentialType"},
		"environment": {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "s", EnvironmentID: "nope"}, "environmentId"},
		"both":        {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "s", EnvironmentID: "e", StackID: "s"}, "stackId"},
		"plain hub":   {domain.RegistryConnectionInput{Name: "a", Host: "docker.io", Username: "u", Secret: "s", PlainHTTP: true}, "plainHttp"},
		"priority":    {domain.RegistryConnectionInput{Name: "a", Host: "ghcr.io", Username: "u", Secret: "s", Priority: 5000}, "priority"},
	}
	for name, tc := range cases {
		_, err := f.svc.Create(f.ctx, tc.in)
		var fe *domain.FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%s: %v, want field %s", name, err, tc.field)
		}
	}
	if _, err := f.svc.Create(f.ctx, domain.RegistryConnectionInput{Name: " ghcr ", Host: "ghcr.io", Username: "u", Secret: "s"}); !errors.Is(err, domain.ErrRegistryConnectionNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
}

func TestConnectionTestAgainstFakeRegistry(t *testing.T) {
	f := newFixture(t)
	f.reg.Private["team/app"] = true
	d := f.reg.Put("team/app", "1.0", regclient.MediaOCIManifest, []byte(regtest.ManifestBody))
	c := f.robot("fake", "team/*")

	res, err := f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/team/app:1.0", "linux/amd64")
	if err != nil || !res.OK || res.Digest != d || res.PlatformDigest != d {
		t.Fatalf("%+v %v", res, err)
	}
	got, _ := f.svc.Get(f.ctx, c.ID)
	if got.LastCheckResult != domain.RegistryCheckOK || got.LastUsedAt == nil || got.LastCheckAt == nil {
		t.Fatalf("%+v", got)
	}
	// The credential reached the registry only as basic auth to the token
	// service; tokens (not the secret) authenticated the manifest requests.
	for _, h := range f.reg.AuthHeaders() {
		if strings.HasPrefix(h, "Basic ") && !strings.Contains(h, f.reg.BasicCredential()) {
			t.Fatalf("unexpected basic credential")
		}
	}

	// References must fit the connection.
	for ref, field := range map[string]string{"ghcr.io/team/app:1": "imageReference", f.reg.Host() + "/other/app:1": "imageReference"} {
		_, err := f.svc.ConnectionTest(f.ctx, c.ID, ref, "")
		var fe *domain.FieldError
		if !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: %v", ref, err)
		}
	}

	// A wrong credential is reported, not retried anonymously.
	wrong := f.secrets.New(canary.RegistryCredential, "wrong password")
	c, err = f.svc.Rotate(f.ctx, c.ID, got.Revision, domain.RegistryCredentialRotation{Secret: wrong})
	if err != nil {
		t.Fatal(err)
	}
	_, _, anonBefore := f.reg.Counts()
	res, err = f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/team/app:1.0", "")
	if err != nil || res.OK || res.ErrorClass != regclient.ClassUnauthorized {
		t.Fatalf("%+v %v", res, err)
	}
	if _, _, anon := f.reg.Counts(); anon-anonBefore > 1 {
		t.Fatalf("anonymous retries: %d", anon-anonBefore-1)
	}
	f.secrets.AssertClean(t, "test result", res)

	// 403 and 429 are classified; a revoked connection cannot be tested.
	c, _ = f.svc.Rotate(f.ctx, c.ID, c.Revision, domain.RegistryCredentialRotation{Secret: f.reg.Password})
	f.reg.Denied["team/app"] = true
	if res, _ := f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/team/app:1.0", ""); res.ErrorClass != regclient.ClassForbidden {
		t.Fatalf("403: %+v", res)
	}
	f.reg.Denied["team/app"] = false
	f.reg.FailNext(http.StatusTooManyRequests, map[string]string{"Retry-After": "3600"}, 1)
	if res, _ := f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/team/app:1.0", ""); res.ErrorClass != regclient.ClassRateLimited || res.RetryAfter != time.Hour {
		t.Fatalf("429: %+v", res)
	}
	got, _ = f.svc.Get(f.ctx, c.ID)
	if got.LastCheckResult != regclient.ClassRateLimited {
		t.Fatalf("last check %q", got.LastCheckResult)
	}
	revoked := domain.RegistryConnectionRevoked
	if _, err := f.svc.Update(f.ctx, c.ID, got.Revision, domain.RegistryConnectionPatch{Status: &revoked}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ConnectionTest(f.ctx, c.ID, f.reg.Host()+"/team/app:1.0", ""); !errors.Is(err, domain.ErrRegistryConnectionRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestRotationAndRevocation(t *testing.T) {
	f := newFixture(t)
	first := f.secrets.New(canary.RegistryCredential, "first token")
	c := f.create(domain.RegistryConnectionInput{Name: "GHCR", Host: "ghcr.io", Username: "bot", Secret: first})
	second := f.secrets.New(canary.RegistryCredential, "second token")
	if _, err := f.svc.Rotate(f.ctx, c.ID, c.Revision+1, domain.RegistryCredentialRotation{Secret: second}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale rotate: %v", err)
	}
	user := "bot2"
	r, err := f.svc.Rotate(f.ctx, c.ID, c.Revision, domain.RegistryCredentialRotation{Secret: second, Username: &user})
	if err != nil {
		t.Fatal(err)
	}
	if r.SecretVersion != 2 || r.SecretFingerprint == c.SecretFingerprint || r.Username != "bot2" || r.Revision != 2 {
		t.Fatalf("%+v", r)
	}
	revoked := domain.RegistryConnectionRevoked
	r, err = f.svc.Update(f.ctx, c.ID, r.Revision, domain.RegistryConnectionPatch{Status: &revoked})
	if err != nil {
		t.Fatal(err)
	}
	if r.Active() || r.SecretFingerprint != "" || r.RevokedAt == nil || r.Revision != 3 {
		t.Fatalf("%+v", r)
	}
	var sealed string
	if err := f.db.NewSelect().Table("registry_connections").Column("secret_sealed").Where("id = ?", c.ID).Scan(f.ctx, &sealed); err != nil || sealed != "" {
		t.Fatalf("sealed %q %v", sealed, err)
	}
	active := domain.RegistryConnectionActive
	var fe *domain.FieldError
	if _, err := f.svc.Update(f.ctx, c.ID, r.Revision, domain.RegistryConnectionPatch{Status: &active}); !errors.As(err, &fe) {
		t.Fatalf("re-activate without a credential: %v", err)
	}
	r, err = f.svc.Rotate(f.ctx, c.ID, r.Revision, domain.RegistryCredentialRotation{Secret: first})
	if err != nil || !r.Active() || r.SecretVersion != 3 || r.RevokedAt != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, r.Revision-1); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(f.ctx, c.ID); !errors.Is(err, domain.ErrRegistryConnectionNotFound) {
		t.Fatal(err)
	}
}

func TestSelectErrors(t *testing.T) {
	f := newFixture(t)
	a := f.create(domain.RegistryConnectionInput{Name: "quay a", Host: "quay.io", Username: "u", Secret: "token-a-123"})
	b := f.create(domain.RegistryConnectionInput{Name: "quay b", Host: "quay.io", Username: "u", Secret: "token-b-123"})
	_, err := f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "quay.io/org/app:1"})
	var amb *domain.AmbiguousRegistryError
	if !errors.As(err, &amb) || len(amb.CandidateIDs) != 2 {
		t.Fatalf("%v", err)
	}
	sel, err := f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "quay.io/org/app:1", ConnectionID: b.ID})
	if err != nil || sel.Selected.ID != b.ID || !sel.Explicit {
		t.Fatalf("%+v %v", sel, err)
	}
	if _, err := f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "ghcr.io/org/app", ConnectionID: a.ID}); !errors.Is(err, domain.ErrRegistryConnectionMismatch) {
		t.Fatalf("%v", err)
	}
	if _, err := f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "quay.io/org/app", ConnectionID: "missing"}); !errors.Is(err, domain.ErrRegistryConnectionNotFound) {
		t.Fatalf("%v", err)
	}
	sel, err = f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "nginx"})
	if err != nil || !sel.Anonymous() {
		t.Fatalf("%+v %v", sel, err)
	}
	prio := 5
	if _, err := f.svc.Update(f.ctx, a.ID, a.Revision, domain.RegistryConnectionPatch{Priority: &prio}); err != nil {
		t.Fatal(err)
	}
	a, _ = f.svc.Get(f.ctx, a.ID)
	revoked := domain.RegistryConnectionRevoked
	if _, err := f.svc.Update(f.ctx, a.ID, a.Revision, domain.RegistryConnectionPatch{Status: &revoked}); err != nil {
		t.Fatal(err)
	}
	// The revoked, higher-priority connection stays selected: no fallback
	// to the other credential or to anonymous access.
	if _, err := f.svc.Select(f.ctx, domain.RegistrySelectRequest{Reference: "quay.io/org/app"}); !errors.Is(err, domain.ErrRegistryConnectionRevoked) {
		t.Fatalf("%v", err)
	}
}

func TestCheckIsCachedAndAudited(t *testing.T) {
	f := newFixture(t)
	f.reg.Private["team/app"] = true
	d := f.reg.Put("team/app", "1", regclient.MediaOCIManifest, []byte(regtest.ManifestBody))
	c := f.robot("fake", "")
	req := registries.CheckRequest{RegistrySelectRequest: domain.RegistrySelectRequest{Reference: f.reg.Host() + "/team/app:1", EnvironmentID: "env-1"},
		Platform: "linux/amd64", JobID: "job-1"}
	for i := range 3 {
		res, err := f.svc.Check(f.ctx, req)
		if err != nil || res.Result.PlatformDigest != d || res.Selection.Selected.ID != c.ID || res.Result.Cached != (i > 0) {
			t.Fatalf("check %d: %+v %v", i, res, err)
		}
	}
	if n, _, _ := f.reg.Counts(); n != 2 { // challenge + authenticated HEAD, once
		t.Fatalf("manifest requests = %d", n)
	}
	uses := f.audit.uses()
	if len(uses) != 1 || uses[0].JobID != "job-1" || uses[0].Targets[0].ID != c.ID || uses[0].Outcome != domain.AuditSuccess {
		t.Fatalf("uses %+v", uses)
	}
	// Rate limits: classified, audited as a failed use, no anonymous retry.
	f.clk.Advance(regclient.DefaultCacheTTL)
	f.reg.FailNext(http.StatusTooManyRequests, map[string]string{"Retry-After": "600"}, 1)
	_, _, anonBefore := f.reg.Counts()
	_, err := f.svc.Check(f.ctx, req)
	if regclient.ClassOf(err) != regclient.ClassRateLimited {
		t.Fatalf("%v", err)
	}
	if _, _, anon := f.reg.Counts(); anon != anonBefore { // the cached token authenticated the request
		t.Fatalf("anonymous requests %d -> %d", anonBefore, anon)
	}
	if uses := f.audit.uses(); len(uses) != 2 || uses[1].ErrorClass != regclient.ClassRateLimited {
		t.Fatalf("uses %+v", uses)
	}
}

// TestJobsGetTheCurrentCredentialAtDispatch runs the job engine with the
// service as its CommandSecrets source: commands carry the credential of
// the connection named in the job input, a rotation applies to later jobs
// (and to resumed attempts), a deleted or revoked connection fails the job
// without sending anything, and no job row, event or audit record holds a
// secret.
// Created reads the image's creation time through the selected
// connection, audits the use once and serves repeats from the cache.
func TestCreatedUsesTheConnection(t *testing.T) {
	f := newFixture(t)
	f.reg.Private["team/app"] = true
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	d := f.reg.PutImage("team/app", "2", at, "")
	c := f.robot("fake", "")
	req := registries.CheckRequest{RegistrySelectRequest: domain.RegistrySelectRequest{Reference: f.reg.Host() + "/team/app:2", EnvironmentID: "env-1"},
		JobID: "job-1"}
	for i := range 2 {
		got, err := f.svc.Created(f.ctx, req, d)
		if err != nil || !got.Equal(at) {
			t.Fatalf("created %d: %v %v", i, got, err)
		}
	}
	uses := f.audit.uses()
	if len(uses) != 1 || uses[0].Targets[0].ID != c.ID || uses[0].Details["purpose"] != "image_created" || uses[0].Outcome != domain.AuditSuccess {
		t.Fatalf("uses %+v", uses)
	}
}

func TestJobsGetTheCurrentCredentialAtDispatch(t *testing.T) {
	f := newFixture(t)
	c := f.robot("fake", "")
	disp := jobstest.New()
	disp.Connect("env-1")
	eng, err := jobs.New(jobs.Options{DB: f.db, Clock: f.clk, Logger: f.secrets.CaptureLogger(t), Dispatcher: disp,
		Authorizer: authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision {
			return authz.Allow("test")
		}),
		CommandSecrets: func(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
			regs, err := f.svc.CommandSecrets(ctx, j)
			if err != nil || len(regs) == 0 {
				return nil, err
			}
			return &protocol.CommandSecrets{Registries: regs}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)

	pull := func(ref string, conns ...string) domain.Job {
		t.Helper()
		in, _ := json.Marshal(map[string]any{"reference": ref, "registryConnections": conns})
		j, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ImagePull, Principal: authz.Principal{Kind: authz.KindUser, UserID: "u1"},
			EnvironmentID: "env-1", Targets: []domain.JobTarget{{Type: domain.TargetImage, ID: ref}}, Input: in})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.DispatchPending(f.ctx); err != nil {
			t.Fatal(err)
		}
		return j
	}
	command := func() protocol.CommandPayload {
		t.Helper()
		var out []protocol.CommandPayload
		for _, fr := range disp.Drain("env-1") {
			if fr.Type == protocol.TypeCommand {
				p, err := protocol.DecodePayload[protocol.CommandPayload](fr)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, p)
			}
		}
		if len(out) != 1 {
			t.Fatalf("%d commands", len(out))
		}
		return out[0]
	}
	finish := func(j domain.Job) {
		t.Helper()
		cur, _ := eng.Get(f.ctx, j.ID)
		ref := protocol.JobRef{JobID: j.ID, Attempt: uint32(cur.Attempt), FencingToken: cur.FencingToken} //nolint:gosec // test
		res, _ := protocol.NewFrame(protocol.TypeResult, "res-"+j.ID, protocol.CommandFrameID(j.ID, ref.Attempt), ref,
			protocol.ResultPayload{Outcome: "succeeded"})
		if _, err := eng.HandleAgentFrame(f.ctx, "env-1", res); err != nil {
			t.Fatal(err)
		}
	}

	j1 := pull("app:1", c.ID)
	p := command()
	if p.Secrets == nil || len(p.Secrets.Registries) != 1 {
		t.Fatalf("secrets %v", p.Secrets)
	}
	rc := p.Secrets.Registries[0]
	if rc.ConnectionID != c.ID || rc.Host != c.Host || rc.ServerAddress != c.Host || rc.Username != "robot" || rc.Secret != f.reg.Password {
		t.Fatalf("credential for %s/%s", rc.ConnectionID, rc.Host)
	}
	finish(j1)

	// Rotation: the next job carries the new secret.
	rotated := f.secrets.New(canary.RegistryCredential, "rotated password")
	c, err = f.svc.Rotate(f.ctx, c.ID, c.Revision, domain.RegistryCredentialRotation{Secret: rotated})
	if err != nil {
		t.Fatal(err)
	}
	j2 := pull("app:2", c.ID)
	if p := command(); p.Secrets.Registries[0].Secret != rotated {
		t.Fatal("the job after the rotation did not get the new credential")
	}
	finish(j2)

	// A job without references carries no secrets.
	j3 := pull("public:1")
	if p := command(); p.Secrets != nil {
		t.Fatalf("unexpected secrets %v", p.Secrets)
	}
	finish(j3)

	// Revoked, then deleted: the job fails at dispatch, nothing is sent.
	revoked := domain.RegistryConnectionRevoked
	if c, err = f.svc.Update(f.ctx, c.ID, c.Revision, domain.RegistryConnectionPatch{Status: &revoked}); err != nil {
		t.Fatal(err)
	}
	j4 := pull("app:4", c.ID)
	if frames := disp.Drain("env-1"); len(frames) != 0 {
		t.Fatalf("frames sent for a revoked connection: %d", len(frames))
	}
	got, _ := eng.Get(f.ctx, j4.ID)
	if got.State != domain.JobFailed || got.ErrorClass != domain.ErrorCredentialUnavailable || !strings.Contains(got.ErrorMessage, "is revoked") {
		t.Fatalf("%s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision); err != nil {
		t.Fatal(err)
	}
	j5 := pull("app:5", c.ID)
	got, _ = eng.Get(f.ctx, j5.ID)
	if got.State != domain.JobFailed || !strings.Contains(got.ErrorMessage, "was deleted") {
		t.Fatalf("%s %s", got.State, got.ErrorMessage)
	}

	// Every use is audited with the connection ID only.
	uses := f.audit.uses()
	if len(uses) != 3 {
		t.Fatalf("%d uses: %+v", len(uses), uses)
	}
	for _, u := range uses[:2] {
		if u.Details["registryConnectionId"] != c.ID || u.JobID == "" || u.Category != domain.AuditCredentials {
			t.Fatalf("%+v", u)
		}
	}
	if uses[1].Details["secretVersion"] != 2 || uses[2].Outcome != domain.AuditFailure {
		t.Fatalf("%+v", uses)
	}
	// Job rows and events: no secret (the whole DB file is scanned at cleanup too).
	for _, id := range []string{j1.ID, j2.ID, j4.ID} {
		j, _ := eng.Get(f.ctx, id)
		evs, _ := eng.Events(f.ctx, id, 0, 100)
		f.secrets.AssertClean(t, "job "+id, j)
		f.secrets.AssertClean(t, "events of "+id, evs)
	}
}
