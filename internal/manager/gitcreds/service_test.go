package gitcreds_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/gitremote"
	"github.com/neurekadev/dockyard/internal/gitremote/gittest"
	"github.com/neurekadev/dockyard/internal/manager/gitcreds"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

type guard struct {
	mu  sync.Mutex
	err error
}

func (g *guard) RequireOwner(context.Context, bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return "owner", g.err
}

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

type fixture struct {
	t       *testing.T
	ctx     context.Context
	db      *bun.DB
	dbPath  string
	guard   *guard
	audit   *recorder
	svc     *gitcreds.Service
	git     *gittest.Server
	secrets *canary.Set
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "dockyard.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey(nil)
	set := canary.New()
	f := &fixture{t: t, ctx: ctx, db: db, dbPath: path, guard: &guard{}, audit: &recorder{}, secrets: set}
	f.git = gittest.New(t, true, "builder", set.New(canary.APIToken, "git token"))
	f.git.Add("/acme/app.git", &gittest.Repo{Head: "refs/heads/main", Private: true,
		Refs: map[string]string{"refs/heads/main": strings.Repeat("a", 40), "refs/tags/v1": strings.Repeat("b", 40)}})
	f.svc, err = gitcreds.New(gitcreds.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: testutil.FakeClock(),
		Logger: set.CaptureLogger(t), Guard: f.guard, Audit: f.audit, HTTP: f.git.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)")
		b, _ := os.ReadFile(path)
		set.AssertClean(t, "database file", b)
		f.audit.mu.Lock()
		set.AssertClean(t, "audit", f.audit.events)
		f.audit.mu.Unlock()
	})
	return f
}

func (f *fixture) host() string { return strings.TrimPrefix(f.git.Server.URL, "https://") }

func (f *fixture) create(name, host, prefix, secret string) domain.GitCredential {
	f.t.Helper()
	c, err := f.svc.Create(f.ctx, domain.GitCredentialInput{Name: name, Host: host, PathPrefix: prefix, Username: "builder", Secret: secret})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func TestCredentialLifecycleAndConnectionTest(t *testing.T) {
	f := newFixture(t)
	c := f.create("fake", "https://"+strings.ToUpper(f.host())+"/", "acme", f.git.Password)
	if c.Host != f.host() || c.PathPrefix != "acme" || !strings.HasPrefix(c.SecretFingerprint, "fp_") || !c.Active() {
		t.Fatalf("%+v", c)
	}
	res, err := f.svc.ConnectionTest(f.ctx, c.ID, f.git.URL("/acme/app.git"), "v1")
	if err != nil || !res.OK || res.Commit != strings.Repeat("b", 40) || res.Ref != "refs/tags/v1" || res.Head != "refs/heads/main" || res.RefCount < 2 {
		t.Fatalf("%+v %v", res, err)
	}
	got, _ := f.svc.Get(f.ctx, c.ID)
	if got.LastCheckResult != domain.RegistryCheckOK || got.LastUsedAt == nil {
		t.Fatalf("%+v", got)
	}
	// Outside the prefix / other host: validation errors.
	var fe *domain.FieldError
	for _, u := range []string{f.git.URL("/other/app.git"), "https://example.com/acme/app.git"} {
		if _, err := f.svc.ConnectionTest(f.ctx, c.ID, u, ""); !errors.As(err, &fe) {
			t.Errorf("%s: %v", u, err)
		}
	}
	// Rotation to a wrong token: the test reports unauthorized.
	wrong := f.secrets.New(canary.APIToken, "wrong token")
	c, err = f.svc.Update(f.ctx, c.ID, c.Revision, domain.GitCredentialPatch{Secret: &wrong})
	if err != nil || c.SecretVersion != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	res, err = f.svc.ConnectionTest(f.ctx, c.ID, f.git.URL("/acme/app.git"), "")
	if err != nil || res.OK || res.ErrorClass != gitremote.ClassUnauthorized {
		t.Fatalf("%+v %v", res, err)
	}
	f.secrets.AssertClean(t, "test", res)
	// Revocation, then re-activation by a new token.
	revoked := domain.RegistryConnectionRevoked
	c, err = f.svc.Update(f.ctx, c.ID, c.Revision, domain.GitCredentialPatch{Status: &revoked})
	if err != nil || c.Active() || c.SecretFingerprint != "" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := f.svc.ConnectionTest(f.ctx, c.ID, f.git.URL("/acme/app.git"), ""); !errors.Is(err, domain.ErrGitCredentialRevoked) {
		t.Fatal(err)
	}
	c, err = f.svc.Update(f.ctx, c.ID, c.Revision, domain.GitCredentialPatch{Secret: &f.git.Password})
	if err != nil || !c.Active() {
		t.Fatal(err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision-1); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatal(err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerOnlyAndValidation(t *testing.T) {
	f := newFixture(t)
	c := f.create("fake", f.host(), "", "tok-12345678")
	f.guard.err = domain.ErrForbidden
	if _, err := f.svc.Create(f.ctx, domain.GitCredentialInput{Name: "x", Host: "github.com", Username: "u", Secret: "s"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.GitCredentialPatch{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := f.svc.ConnectionTest(f.ctx, c.ID, f.git.URL("/acme/app.git"), ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	f.guard.err = nil
	for name, in := range map[string]domain.GitCredentialInput{
		"host":     {Name: "a", Host: "not a host", Username: "u", Secret: "s"},
		"path":     {Name: "a", Host: "github.com", Username: "u", Secret: "s", PathPrefix: "a/../b"},
		"username": {Name: "a", Host: "github.com", Username: "a:b", Secret: "s"},
		"secret":   {Name: "a", Host: "github.com", Username: "u", Secret: "a\nb"},
	} {
		var fe *domain.FieldError
		if _, err := f.svc.Create(f.ctx, in); !errors.As(err, &fe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.Create(f.ctx, domain.GitCredentialInput{Name: "FAKE", Host: "github.com", Username: "u", Secret: "s"}); !errors.Is(err, domain.ErrGitCredentialNameTaken) {
		t.Fatal(err)
	}
}

func TestSelect(t *testing.T) {
	f := newFixture(t)
	all := f.create("host-wide", "git.example.com", "", "tok-aaaaaaaa")
	team := f.create("team", "git.example.com", "acme/team", "tok-bbbbbbbb")
	repo := func(p string) gitremote.Repo {
		r, err := gitremote.ParseURL("https://git.example.com" + p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for p, want := range map[string]string{"/acme/team/app.git": team.ID, "/acme/other.git": all.ID, "/acme/teamx/app.git": all.ID} {
		c, err := f.svc.Select(f.ctx, repo(p), "")
		if err != nil || c == nil || c.ID != want {
			t.Errorf("%s: %+v %v", p, c, err)
		}
	}
	if c, err := f.svc.Select(f.ctx, repo("/acme/other.git"), team.ID); !errors.Is(err, domain.ErrGitCredentialMismatch) {
		t.Fatalf("%+v %v", c, err)
	}
	other, _ := gitremote.ParseURL("https://github.com/acme/app.git")
	if c, err := f.svc.Select(f.ctx, other, ""); err != nil || c != nil {
		t.Fatalf("anonymous: %+v %v", c, err)
	}
	f.create("host-wide 2", "git.example.com", "", "tok-cccccccc")
	var amb *domain.AmbiguousGitCredentialError
	if _, err := f.svc.Select(f.ctx, repo("/acme/other.git"), ""); !errors.As(err, &amb) || len(amb.CredentialIDs) != 2 {
		t.Fatalf("%v", err)
	}
	revoked := domain.RegistryConnectionRevoked
	if _, err := f.svc.Update(f.ctx, team.ID, team.Revision, domain.GitCredentialPatch{Status: &revoked}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Select(f.ctx, repo("/acme/team/app.git"), ""); !errors.Is(err, domain.ErrGitCredentialRevoked) {
		t.Fatalf("revoked winner: %v", err)
	}
}
