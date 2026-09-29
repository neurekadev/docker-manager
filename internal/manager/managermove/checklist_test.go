package managermove

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestChecklist: the arrived move lists the environments whose agent
// dialed another address than the public URL (the old server's
// co-located agent: set the address, then migrate or archive), and
// whether the new server's environment was added after the arrival.
func TestChecklist(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	now := f.clk.Now().UTC()
	arrivedAt := now.Add(-time.Minute)
	m := domain.ManagerMove{ID: "move-1", State: domain.MoveArrived, CreatedAt: now.Add(-time.Hour), ExpiresAt: now, ArrivedAt: &arrivedAt,
		SourceURL: "https://old.example.com", UpdatedAt: now}
	if err := store.InsertManagerMove(f.ctx, f.db, &m, "v"); err != nil {
		t.Fatal(err)
	}
	env := func(id, managerURL string, created time.Time, stacks int) {
		t.Helper()
		e := domain.Environment{ID: id, Name: id, EngineID: "ENG-" + id, InstallID: "inst-" + id, AgentID: "agent-" + id,
			Status: domain.EnvironmentActive, Revision: 1, CreatedAt: created, UpdatedAt: created}
		if err := store.InsertEnvironment(f.ctx, f.db, &e); err != nil {
			t.Fatal(err)
		}
		caps, _ := json.Marshal(protocol.CapabilitiesPayload{Transport: protocol.TransportInfo{ManagerURL: managerURL}})
		a := domain.Agent{ID: "agent-" + id, EnvironmentID: id, EnrollmentID: "enr-" + id, InstallID: "inst-" + id, EngineID: "ENG-" + id,
			Version: "1.5.0", VersionStatus: "current", Status: domain.AgentActive, Capabilities: string(caps), Revision: 1, CreatedAt: created, UpdatedAt: created}
		if err := store.InsertAgent(f.ctx, f.db, &a); err != nil {
			t.Fatal(err)
		}
		for i := range stacks {
			s := domain.Stack{ID: id + "-stack-" + string(rune('a'+i)), EnvironmentID: id, Name: "app" + string(rune('a'+i)), Root: protocol.RootStacks,
				Dir: "app" + string(rune('a'+i)), Origin: domain.StackOriginCreated, Status: domain.StackDeployed, Revision: 1, CreatedAt: created, UpdatedAt: created}
			if err := store.InsertStack(f.ctx, f.db, &s); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := now.Add(-2 * time.Hour)
	env("old-server", "http://docker-manager:8080", before, 2)
	env("empty-old", "http://docker-manager:8080", before, 0)
	env("remote", "https://docker.example.com:443/", before, 1)

	v, err := f.svc.Current(f.ctx)
	if err != nil || v.Checklist == nil {
		t.Fatalf("current %+v %v", v, err)
	}
	c := *v.Checklist
	if c.NewEnvironmentAdded {
		t.Fatal("no environment was added after the move")
	}
	byID := map[string]ChecklistEnvironment{}
	for _, e := range c.Environments {
		byID[e.ID] = e
	}
	if len(byID) != 2 {
		t.Fatalf("checklist %+v", c.Environments)
	}
	if e := byID["old-server"]; e.StackCount != 2 || !slices.Equal(e.Actions, []string{ActionSetManagerURL, ActionMigrate}) ||
		e.ManagerURL != "http://docker-manager:8080" {
		t.Fatalf("old server %+v", e)
	}
	if e := byID["empty-old"]; !slices.Equal(e.Actions, []string{ActionSetManagerURL, ActionArchive}) {
		t.Fatalf("empty old environment %+v", e)
	}

	env("new-server", "https://docker.example.com", now, 0)
	if v, _ := f.svc.Current(f.ctx); !v.Checklist.NewEnvironmentAdded {
		t.Fatal("the new server's environment is not seen")
	}
}

func TestSameOrigin(t *testing.T) {
	pub, _ := url.Parse("https://docker.example.com")
	for raw, want := range map[string]bool{
		"https://docker.example.com":      true,
		"https://DOCKER.example.com:443/": true,
		"http://docker.example.com":       false,
		"https://docker.example.com:8443": false,
		"http://docker-manager:8080":      false,
	} {
		if got := SameOrigin(raw, pub); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}
