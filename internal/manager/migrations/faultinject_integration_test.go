//go:build integration && faultinject

package migrations_test

import (
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/migrations"
)

// Killing the manager or an agent in the middle of a migration's transfer
// (#35 Done-when 3) on real Engines: the job ends interrupted with
// guidance, the stack stays on (or returns to) its source, the source's
// data is untouched and the stack starts again there.

// waitOnline waits (setup deadline) until the environments are online.
func (r *rig) waitOnline(envs ...string) {
	r.t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for _, e := range envs {
		for !r.m.Agents().Hub().Online(e) {
			if time.Now().After(deadline) {
				r.logAgents()
				r.t.Fatalf("environment %s still offline", e)
			}
			time.Sleep(time.Second)
		}
	}
}

// waitItem waits (setup deadline) until the job reports a succeeded item.
func (r *rig) waitItem(id, item string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		j, err := r.m.Jobs().Get(r.ctx, id)
		if err != nil {
			r.t.Fatal(err)
		}
		for _, it := range j.Items {
			if it.Name == item && it.Status == domain.ItemSucceeded {
				return
			}
		}
		if j.State.Terminal() || time.Now().After(deadline) {
			r.t.Fatalf("job %s: %s without item %s (%s)", id, j.State, item, j.ErrorMessage)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startSource starts the stack on its source and waits for it.
func (r *rig) startSource(st domain.Stack) {
	r.t.Helper()
	j, err := r.m.Stacks().Operate(r.ctx, authz.Service(), st, "start", domain.StackJobRequest{})
	if err != nil {
		r.t.Fatal(err)
	}
	if j := r.waitJob(j.ID); j.State != domain.JobSucceeded {
		r.t.Fatalf("start on the source: %s %s", j.State, j.ErrorMessage)
	}
}

// TestEngineManagerKilledMidTransfer: the manager stops while the
// migration is between its project part (verified) and its volume part
// (fault point migration.part.verified blocks the transfer there) and
// restarts on the same data directory: the job is interrupted with
// guidance, the stack record points at the source, the source's data is
// intact and the stack starts there again.
func TestEngineManagerKilledMidTransfer(t *testing.T) {
	r := newRig(t, nil)
	st := r.deployShop()
	t.Setenv(faultinject.EnvVar, migrations.PointPartVerified+":"+string(faultinject.Block))
	j, _, err := r.m.Migrations().StartStack(r.ctx, authz.Service(), st, migrations.StackRequest{TargetEnvironmentID: r.envs[1]})
	if err != nil {
		t.Fatal(err)
	}
	r.waitItem(j.ID, "project")
	r.stop() // the manager goes away mid-transfer
	t.Setenv(faultinject.EnvVar, "")
	r.startManager()
	r.waitOnline(r.envs...)
	after, err := r.m.Jobs().Get(r.ctx, j.ID)
	if err != nil || after.State != domain.JobInterrupted || after.Recovery == "" {
		t.Fatalf("job after the restart: %+v %v", after, err)
	}
	stk, _ := r.m.Stacks().Get(r.ctx, st.ID)
	if stk.EnvironmentID != r.envs[0] {
		t.Fatalf("the stack record moved: %+v", stk)
	}
	if m, _ := r.m.Migrations().Get(r.ctx, j.ID); m.State != domain.MigrationInterrupted || !m.TargetPartial {
		t.Errorf("record %+v", m)
	}
	if code, _ := r.read(0, "shop_dbdata", "ready"); code != 0 {
		t.Error("the source volume lost its data")
	}
	r.startSource(stk)
	if c, ok := r.inspect(0, "shop-web-1"); !ok || !c.State.Running {
		t.Fatal("the source did not start again")
	}
}

// TestEngineSourceAgentKilledMidTransfer: the source agent is killed while
// a (rate-limited, so slow) volume copy runs and stays away: the job ends
// interrupted (agent_offline) with guidance; once the agent is back the
// stack starts again on the source, whose data is intact.
func TestEngineSourceAgentKilledMidTransfer(t *testing.T) {
	r := newRig(t, func(o *app.Options) {
		o.Config.MigrationBandwidthLimit = 64 << 10
		o.MigrationReconnectWait = 10 * time.Second
	})
	st := r.deployShop()
	// 4 MiB of volume data take about a minute at 64 KiB/s.
	r.engines[0].PutFiles(t, mount.Mount{Type: mount.TypeVolume, Source: "shop_dbdata"},
		map[string][]byte{"big.bin": []byte(strings.Repeat("0123456789abcdef", 256<<10))})
	j, _, err := r.m.Migrations().StartStack(r.ctx, authz.Service(), st, migrations.StackRequest{TargetEnvironmentID: r.envs[1]})
	if err != nil {
		t.Fatal(err)
	}
	r.waitItem(j.ID, "project")
	cli := r.engines[0].Client(t)
	if _, err := cli.ContainerKill(r.ctx, r.agents[0], client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
	done := r.waitJob(j.ID)
	if done.State != domain.JobInterrupted || done.ErrorClass != domain.ErrorAgentOffline || !strings.Contains(done.Recovery, "reconnects") {
		t.Fatalf("job %s %s %q", done.State, done.ErrorClass, done.Recovery)
	}
	if _, err := cli.ContainerStart(r.ctx, r.agents[0], client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	r.waitOnline(r.envs[0])
	stk, _ := r.m.Stacks().Get(r.ctx, st.ID)
	if stk.EnvironmentID != r.envs[0] {
		t.Fatalf("the stack record moved: %+v", stk)
	}
	if code, _ := r.read(0, "shop_dbdata", "big.bin"); code != 0 {
		t.Error("the source volume lost its data")
	}
	r.startSource(stk)
	if c, ok := r.inspect(0, "shop-web-1"); !ok || !c.State.Running {
		t.Fatal("the source did not start again")
	}
	// The destination holds partial data; the next migration removes it.
	if m, _ := r.m.Migrations().Get(r.ctx, j.ID); !m.TargetPartial {
		t.Errorf("record %+v", m)
	}
}
