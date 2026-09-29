package managermove

import (
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestMoveComplete: an agent that got the new address never needs a fix
// (whatever address it reports, e.g. http://docker-manager:8080); one
// that did not get it needs the fix until it connected here since the
// move; the old server's environment shows its stacks left and the moved
// stacks' stopped copies until it is archived.
func TestMoveComplete(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	now := f.clk.Now().UTC()
	arrivedAt := now.Add(-time.Hour)
	f.addEnvironment("env-new", "new", "")
	old := f.addEnvironment("env-old", "old", "")
	f.addEnvironment("env-late", "late", "")
	f.addStack("env-old", "left-behind")
	// env-late's agent did not get the redirect but connected since.
	agent := domain.Agent{ID: "agent-late", EnvironmentID: "env-late", Status: domain.AgentActive, EngineID: "ENGINE-env-late",
		InstallID: "install-env-late", LastConnectedAt: &now, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if err := store.InsertAgent(f.ctx, f.db, &agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE environments SET agent_id = 'agent-late' WHERE id = 'env-late'"); err != nil {
		t.Fatal(err)
	}
	f.migr.retained = 3
	m := domain.ManagerMove{ID: "move-arrived", State: domain.MoveArrived, CreatedAt: arrivedAt.Add(-time.Hour), ExpiresAt: arrivedAt,
		ArrivedAt: &arrivedAt, SourceURL: "http://192.168.1.10:8080", SourceEnvironmentID: "env-old", MigrationID: "mig-1", UpdatedAt: now,
		Redirects: []domain.ManagerMoveRedirect{
			{EnvironmentID: "env-new", Role: domain.RedirectNewServer, URL: NewServerManagerURL, Sent: true},
			{EnvironmentID: "env-old", Role: domain.RedirectOldServer, URL: "http://192.168.1.20:8080", ErrorClass: RedirectOffline},
			{EnvironmentID: "env-late", Role: domain.RedirectOldServer, URL: "http://192.168.1.20:8080", ErrorClass: RedirectTimedOut},
		}}
	if err := store.InsertManagerMove(f.ctx, f.db, &m, ""); err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.Current(f.ctx)
	if err != nil || v.Complete == nil || v.Move.State != domain.MoveArrived {
		t.Fatalf("current %+v %v", v, err)
	}
	c := v.Complete
	if len(c.Redirects) != 3 {
		t.Fatalf("redirects %+v", c.Redirects)
	}
	if r := c.Redirects[0]; !r.Sent || r.NeedsFix {
		t.Errorf("redirected agent %+v", r)
	}
	if r := c.Redirects[1]; r.Sent || r.Connected || !r.NeedsFix || r.URL != "http://192.168.1.20:8080" {
		t.Errorf("offline agent %+v", r)
	}
	if r := c.Redirects[2]; !r.Connected || r.NeedsFix {
		t.Errorf("agent that connected since %+v", r)
	}
	if o := c.OldEnvironment; o == nil || o.EnvironmentID != "env-old" || o.StackCount != 1 || o.StoppedCopies != 3 || o.Archived ||
		o.MigrationID != "mig-1" {
		t.Fatalf("old environment %+v", o)
	}

	old.Status, old.ArchivedAt = domain.EnvironmentArchived, &now
	if err := store.UpdateEnvironment(f.ctx, f.db, &old, 0); err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.Current(f.ctx)
	if err != nil || v.Complete.OldEnvironment == nil || !v.Complete.OldEnvironment.Archived || v.Complete.OldEnvironment.StoppedCopies != 0 {
		t.Fatalf("archived %+v %v", v.Complete.OldEnvironment, err)
	}
}
