package builds_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/gitremote/gittest"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/builds"
	"github.com/neurekadev/dockyard/internal/manager/gitcreds"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/registries"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

type allowOwner struct{}

func (allowOwner) RequireOwner(context.Context, bool) (string, error) { return "owner", nil }

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
	clk     *clock.Fake
	disp    *jobstest.Dispatcher
	eng     *jobs.Engine
	git     *gitcreds.Service
	regs    *registries.Service
	svc     *builds.Service
	server  *gittest.Server
	audit   *recorder
	secrets *canary.Set
	owner   authz.Principal
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
	set := canary.New()
	f := &fixture{t: t, ctx: ctx, db: db, clk: testutil.FakeClock(), disp: jobstest.New(), audit: &recorder{}, secrets: set,
		owner: authz.Principal{Kind: authz.KindUser, UserID: "u-owner"}}
	f.disp.Connect("env-1")
	now := f.clk.Now().UTC()
	for _, id := range []string{"env-1", "env-2"} {
		env := domain.Environment{ID: id, Name: id, EngineID: "E-" + id, InstallID: "i-" + id, Status: domain.EnvironmentActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &env); err != nil {
			t.Fatal(err)
		}
	}
	key, _ := secrets.GenerateKey(nil)
	kr := secrets.NewKeyring(key)
	logger := set.CaptureLogger(t)
	f.server = gittest.New(t, true, "builder", set.New(canary.APIToken, "git token"))
	f.git, err = gitcreds.New(gitcreds.Options{DB: db, Keyring: kr, Clock: f.clk, Logger: logger, Guard: allowOwner{}, Audit: f.audit, HTTP: f.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	f.regs, err = registries.New(registries.Options{DB: db, Keyring: kr, Clock: f.clk, Logger: logger, Guard: allowOwner{}, Audit: f.audit})
	if err != nil {
		t.Fatal(err)
	}
	f.eng, err = jobs.New(jobs.Options{DB: db, Clock: f.clk, Logger: logger, Dispatcher: f.disp,
		Authorizer: authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision {
			return authz.Allow("test")
		}),
		CommandSecrets: func(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
			r, err := f.regs.CommandSecrets(ctx, j)
			if err != nil {
				return nil, err
			}
			g, err := f.git.CommandSecrets(ctx, j)
			if err != nil {
				return nil, err
			}
			s := &protocol.CommandSecrets{Registries: r, Git: g}
			if s.Empty() {
				return nil, nil
			}
			return s, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.eng.Close)
	f.svc, err = builds.New(builds.Options{DB: db, Clock: f.clk, Logger: logger, Jobs: f.eng, Git: f.git, Registries: f.regs})
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

func (f *fixture) host() string { return strings.TrimPrefix(f.server.Server.URL, "https://") }

func (f *fixture) source(mut func(*domain.BuildSource)) domain.BuildSource {
	s := domain.BuildSource{GitURL: f.server.URL("/acme/app.git"), Ref: "main", ContextPath: "svc/api/", Dockerfile: "Dockerfile",
		BuildArgs: map[string]string{"VERSION": "1.2.3"}, Tags: []string{"acme/app:1.2.3", "acme/app:latest"}}
	if mut != nil {
		mut(&s)
	}
	return s
}

// command dispatches pending jobs and returns the single command sent.
func (f *fixture) command() (*protocol.Frame, protocol.CommandPayload) {
	f.t.Helper()
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	var out []*protocol.Frame
	for _, fr := range f.disp.Drain("env-1") {
		if fr.Type == protocol.TypeCommand {
			out = append(out, fr)
		}
	}
	if len(out) != 1 {
		f.t.Fatalf("%d commands", len(out))
	}
	p, err := protocol.DecodePayload[protocol.CommandPayload](out[0])
	if err != nil {
		f.t.Fatal(err)
	}
	return out[0], p
}

func (f *fixture) finish(cmd *protocol.Frame, res protocol.ResultPayload) {
	f.t.Helper()
	ack, _ := protocol.NewFrame(protocol.TypeAck, "ack-"+cmd.ID, cmd.ID, cmd.Ref(), protocol.AckPayload{Accepted: true})
	if _, err := f.eng.HandleAgentFrame(f.ctx, "env-1", ack); err != nil {
		f.t.Fatal(err)
	}
	f.clk.Advance(90 * time.Second)
	fr, _ := protocol.NewFrame(protocol.TypeResult, "res-"+cmd.ID, cmd.ID, cmd.Ref(), res)
	if _, err := f.eng.HandleAgentFrame(f.ctx, "env-1", fr); err != nil {
		f.t.Fatal(err)
	}
}

func TestPrivateBuildEndToEndOnTheManager(t *testing.T) {
	f := newFixture(t)
	gc, err := f.git.Create(f.ctx, domain.GitCredentialInput{Name: "fake", Host: f.host(), Username: "builder", Secret: f.server.Password})
	if err != nil {
		t.Fatal(err)
	}
	regSecret := f.secrets.New(canary.RegistryCredential, "ghcr token")
	ghcr, err := f.regs.Create(f.ctx, domain.RegistryConnectionInput{Name: "ghcr", Host: "ghcr.io", Username: "bot", Secret: regSecret})
	if err != nil {
		t.Fatal(err)
	}
	// Not offered automatically: repository-specific, bound elsewhere, tied.
	for _, in := range []domain.RegistryConnectionInput{
		{Name: "specific", Host: "docker.io", Username: "u", Secret: "s-11111111", RepositoryPattern: "acme/*"},
		{Name: "other env", Host: "registry.lan:5000", Username: "u", Secret: "s-22222222", EnvironmentID: "env-2"},
		{Name: "quay a", Host: "quay.io", Username: "u", Secret: "s-33333333"},
		{Name: "quay b", Host: "quay.io", Username: "u", Secret: "s-44444444"},
	} {
		if _, err := f.regs.Create(f.ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	b, job, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(nil), "", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != job.ID || b.Status != domain.BuildQueued || b.GitCredentialID != gc.ID || !slices.Equal(b.RegistryConnectionIDs, []string{ghcr.ID}) ||
		b.ContextPath != "svc/api" || !slices.Equal(b.BuildArgKeys, []string{"VERSION"}) {
		t.Fatalf("record %+v", b)
	}
	if len(job.Targets) != 2 || job.Targets[0].Type != domain.TargetImage {
		t.Fatalf("targets %+v", job.Targets)
	}
	// Idempotent replay returns the same build.
	again, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(nil), "", "key-1")
	if err != nil || again.ID != b.ID {
		t.Fatalf("replay %+v %v", again, err)
	}

	cmd, p := f.command()
	in, err := jobspec.DecodeImageBuildInput(p.Input)
	if err != nil {
		t.Fatal(err)
	}
	if in.GitURL != f.server.URL("/acme/app.git") || in.Ref != "main" || !slices.Equal(in.GitCredentials, []string{gc.ID}) ||
		!slices.Equal(in.RegistryConnections, []string{ghcr.ID}) || in.BuildArgs["VERSION"] != "1.2.3" {
		t.Fatalf("input %+v", in)
	}
	if p.Secrets == nil || len(p.Secrets.Git) != 1 || p.Secrets.Git[0].Secret != f.server.Password || p.Secrets.Git[0].Host != f.host() ||
		len(p.Secrets.Registries) != 1 || p.Secrets.Registries[0].Secret != regSecret {
		t.Fatalf("secrets %v", p.Secrets)
	}
	f.secrets.AssertClean(t, "job input", p.Input)

	commit := strings.Repeat("c", 40)
	f.finish(cmd, protocol.ResultPayload{Outcome: "succeeded", Items: []protocol.ItemPayload{
		{Name: jobspec.BuildItemCommit, Status: domain.ItemSucceeded, Message: commit},
		{Name: jobspec.BuildItemRef, Status: domain.ItemSucceeded, Message: "refs/heads/main"},
		{Name: jobspec.BuildItemImage, Status: domain.ItemSucceeded, Message: "sha256:" + strings.Repeat("d", 64)}}})
	got, err := f.svc.Get(f.ctx, b.ID)
	if err != nil || got.Status != domain.BuildSucceeded || got.ResolvedCommit != commit || got.ResolvedRef != "refs/heads/main" ||
		!strings.HasPrefix(got.ImageID, "sha256:") || got.FinishedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := f.svc.List(f.ctx, "env-1", "", "", 10)
	if err != nil || len(list) != 1 || list[0].Status != domain.BuildSucceeded {
		t.Fatalf("%+v %v", list, err)
	}
	// Uses are audited with IDs only (the cleanup scans the audit for secrets).
	var uses []string
	for _, e := range f.audit.events {
		if e.Action == gitcreds.ActionUse || e.Action == registries.ActionUse {
			uses = append(uses, e.Action)
		}
	}
	if len(uses) != 2 {
		t.Fatalf("uses %v", uses)
	}
}

func TestRotationAndDeletionBeforeDispatch(t *testing.T) {
	f := newFixture(t)
	gc, _ := f.git.Create(f.ctx, domain.GitCredentialInput{Name: "fake", Host: f.host(), Username: "builder", Secret: f.server.Password})
	if _, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(nil), "", ""); err != nil {
		t.Fatal(err)
	}
	rotated := f.secrets.New(canary.APIToken, "rotated git token")
	gc, err := f.git.Update(f.ctx, gc.ID, gc.Revision, domain.GitCredentialPatch{Secret: &rotated})
	if err != nil {
		t.Fatal(err)
	}
	cmd, p := f.command()
	if p.Secrets.Git[0].Secret != rotated {
		t.Fatal("the queued build did not get the rotated token")
	}
	// The per-environment build cap (1 by default) holds the next build
	// until this one finishes.
	b, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(func(s *domain.BuildSource) { s.Tags = []string{"acme/app:2"} }), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := f.eng.Get(f.ctx, b.JobID); j.State != domain.JobBlocked || j.BlockedReason != domain.BlockedConcurrency {
		t.Fatalf("second build %s %s", j.State, j.BlockedReason)
	}
	f.finish(cmd, protocol.ResultPayload{Outcome: "succeeded"})
	if err := f.git.Delete(f.ctx, gc.ID, gc.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(f.ctx, b.ID)
	if got.Status != domain.BuildFailed || got.ErrorClass != domain.ErrorCredentialUnavailable {
		t.Fatalf("%+v", got)
	}
}

func TestValidationAndCredentialSelection(t *testing.T) {
	f := newFixture(t)
	var fe *domain.FieldError
	for name, mut := range map[string]func(*domain.BuildSource){
		"ssh":            func(s *domain.BuildSource) { s.GitURL = "ssh://git@github.com/acme/app.git" },
		"credentials":    func(s *domain.BuildSource) { s.GitURL = "https://u:p@github.com/acme/app.git" },
		"fragment":       func(s *domain.BuildSource) { s.GitURL = "https://github.com/acme/app.git#main" },
		"no tags":        func(s *domain.BuildSource) { s.Tags = nil },
		"digest tag":     func(s *domain.BuildSource) { s.Tags = []string{"acme/app@sha256:" + strings.Repeat("a", 64)} },
		"duplicate tags": func(s *domain.BuildSource) { s.Tags = []string{"a:1", "a:1"} },
		"context escape": func(s *domain.BuildSource) { s.ContextPath = "../x" },
		"ref":            func(s *domain.BuildSource) { s.Ref = "--upload-pack=x" },
		"arg name":       func(s *domain.BuildSource) { s.BuildArgs = map[string]string{"A-B": "x"} },
		"platform":       func(s *domain.BuildSource) { s.Platform = "linux" },
		"timeout":        func(s *domain.BuildSource) { s.TimeoutSeconds = 99999 },
	} {
		if _, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(mut), "", ""); !errors.As(err, &fe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := f.svc.Start(f.ctx, f.owner, "missing", f.source(nil), "", ""); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Fatal(err)
	}
	// Two host-wide credentials tie: explicit selection required.
	a, _ := f.git.Create(f.ctx, domain.GitCredentialInput{Name: "a", Host: f.host(), Username: "u", Secret: "tok-aaaaaaaa"})
	if _, err := f.git.Create(f.ctx, domain.GitCredentialInput{Name: "b", Host: f.host(), Username: "u", Secret: "tok-bbbbbbbb"}); err != nil {
		t.Fatal(err)
	}
	var amb *domain.AmbiguousGitCredentialError
	if _, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(nil), "", ""); !errors.As(err, &amb) {
		t.Fatalf("%v", err)
	}
	b, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(func(s *domain.BuildSource) { s.GitCredentialID = a.ID }), "", "")
	if err != nil || b.GitCredentialID != a.ID {
		t.Fatalf("%+v %v", b, err)
	}
	// A plain-HTTP repository never gets a credential that does not allow it.
	if _, err := f.git.Create(f.ctx, domain.GitCredentialInput{Name: "plain", Host: "git.lan", Username: "u", Secret: "tok-cccccccc"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(func(s *domain.BuildSource) { s.GitURL = "http://git.lan/acme/app.git" }), "", ""); !errors.As(err, &fe) {
		t.Fatalf("%v", err)
	}
	// Explicit registry connections must exist.
	if _, _, err := f.svc.Start(f.ctx, f.owner, "env-1", f.source(func(s *domain.BuildSource) {
		s.GitCredentialID = a.ID
		s.RegistryConnectionIDs = []string{"missing"}
	}), "", ""); !errors.Is(err, domain.ErrRegistryConnectionNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDefinitionsAndRuns(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.CreateDefinition(f.ctx, "env-1", "api", "the API", f.source(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateDefinition(f.ctx, "env-1", "API", "", f.source(nil)); !errors.Is(err, domain.ErrBuildDefinitionNameTaken) {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateDefinition(f.ctx, "env-2", "api", "", f.source(nil)); err != nil {
		t.Fatalf("same name in another environment: %v", err)
	}
	name := "api v2"
	src := f.source(func(s *domain.BuildSource) { s.Ref = "v1"; s.Tags = []string{"acme/app:2"} })
	d, err = f.svc.UpdateDefinition(f.ctx, d.ID, d.Revision, domain.BuildDefinitionPatch{Name: &name, Source: &src})
	if err != nil || d.Revision != 2 || d.Source.Ref != "v1" || d.Name != "api v2" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := f.svc.UpdateDefinition(f.ctx, d.ID, 1, domain.BuildDefinitionPatch{Name: &name}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatal(err)
	}
	b, job, err := f.svc.RunDefinition(f.ctx, f.owner, d.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.DefinitionID != d.ID || b.Ref != "v1" || job.Targets[0].Type != domain.TargetBuildDefinition || job.Targets[0].ID != d.ID {
		t.Fatalf("%+v %+v", b, job.Targets)
	}
	d, _ = f.svc.GetDefinition(f.ctx, d.ID)
	if d.LastBuildID != b.ID {
		t.Fatalf("last build %q", d.LastBuildID)
	}
	runs, _ := f.svc.List(f.ctx, "env-1", d.ID, "", 10)
	if len(runs) != 1 {
		t.Fatalf("runs %d", len(runs))
	}
	if err := f.svc.DeleteDefinition(f.ctx, d.ID, d.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(f.ctx, b.ID); err != nil {
		t.Fatal("the build record went with the definition")
	}
	raw, _ := json.Marshal(d)
	_ = raw
}
