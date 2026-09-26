package migrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration/migrationtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/transfer"
)

// payloadSum is the SHA-256 of a tree's tar archive as the source would
// send it (the archive is deterministic).
func payloadSum(t *testing.T, h *migrationtest.Host, dir string) string {
	t.Helper()
	fs, err := h.Opener()(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := transfer.NewWriter(io.Discard, 0)
	if _, err := migration.WriteTree(testutil.Context(t), fs, w); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return w.Summary().SHA256
}

func sameTree(t *testing.T, what string, want, got map[string]migrationtest.Snapshot) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: %d entries, want %d", what, len(got), len(want))
	}
	for k, w := range want {
		g, ok := got[k]
		if w.Type == "symlink" {
			w.MTime, w.ATime, w.Mode = g.MTime, g.ATime, g.Mode
		}
		if !ok || g != w {
			t.Errorf("%s %s:\n got %+v\nwant %+v", what, k, g, w)
		}
	}
}

// TestStackMigrationMovesProjectAndVolume (#35 Done-when 1, with
// simulated agents): the stack with a relative bind directory and a named
// volume moves; every part's checksum matches on source, manager and
// destination and equals the source data's; the locally built image is
// copied; the source is stopped (reverse dependency order through the
// lifecycle) and stays stopped with its data untouched; the stack record
// keeps its ID and moves to the destination, which deploys it.
func TestStackMigrationMovesProjectAndVolume(t *testing.T) {
	w := newWorld(t)
	srcProject := w.src.Host.Tree(w.src.ProjectDir("shop"))
	srcVolume := w.src.Host.Tree(w.src.VolumesDir + "/shop_dbdata/_data")
	projectSum := payloadSum(t, w.src.Host, w.src.ProjectDir("shop"))
	volumeSum := payloadSum(t, w.src.Host, w.src.VolumesDir+"/shop_dbdata/_data")

	plan, err := w.svc.PreviewStack(w.ctx, w.user, false, w.stack(), StackRequest{TargetEnvironmentID: dstEnv})
	if err != nil || !plan.Allowed() {
		t.Fatalf("preview %+v %v", plan.Blockers, err)
	}
	if w.agents.called(srcEnv, protocol.ReqMigrationStop) != 0 {
		t.Fatal("the preview stopped something")
	}
	j := w.start(Selection{})
	st, _, err := w.run(j, nil)
	if err != nil || st.Outcome.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v %v", st.Outcome, err)
	}
	// Destination: the project directory (with its bind directory) and the
	// volume, byte for byte with owners, modes and times.
	sameTree(t, "project", srcProject, w.dst.Host.Tree(w.dst.ProjectDir("shop")))
	sameTree(t, "volume", srcVolume, w.dst.Host.Tree(w.dst.VolumesDir+"/shop_dbdata/_data"))
	if w.dst.Host.Exists(w.dst.ProjectDir(protocol.MigrationStagingDir + "/" + j.ID)) {
		t.Error("the staging directory must be cleaned after completion")
	}
	v, err := w.dst.Engine.InspectVolume(w.ctx, "shop_dbdata")
	if err != nil || v.Labels["com.docker.compose.volume"] != "dbdata" || v.Labels[protocol.LabelMigration] != j.ID {
		t.Errorf("destination volume %+v %v", v, err)
	}
	if _, err := w.dst.Engine.InspectImage(w.ctx, "shop-web:local"); err != nil {
		t.Errorf("the locally built image was not copied: %v", err)
	}
	// Checksums: the record's parts equal the source data's own sums.
	m := w.record(j.ID)
	if m.State != domain.MigrationCompleted || !m.CutOver || m.TargetPartial || len(m.Parts) != 3 {
		t.Fatalf("record %+v", m)
	}
	sums := map[string]string{}
	for _, p := range m.Parts {
		sums[p.Name] = p.SHA256
	}
	if sums["project"] != projectSum || sums["volume:shop_dbdata"] != volumeSum || sums["image"] == "" {
		t.Errorf("checksums %v, source project %s volume %s", sums, projectSum, volumeSum)
	}
	if !m.Volumes[0].Copied || m.Volumes[0].SHA256 != volumeSum || !slices.Equal(m.SourceRunning, []string{"db", "web"}) {
		t.Errorf("volumes %+v running %v", m.Volumes, m.SourceRunning)
	}
	// Items carry the verified checksums (they are audited with the job).
	if len(st.Items) != 3 || !strings.Contains(st.Items[0].Message, projectSum) {
		t.Errorf("items %+v", st.Items)
	}
	// Source: stopped and untouched.
	if !w.stopped(w.src, "shop-db-1", "shop-web-1") {
		t.Error("the source must stay stopped")
	}
	sameTree(t, "source project", srcProject, w.src.Host.Tree(w.src.ProjectDir("shop")))
	sameTree(t, "source volume", srcVolume, w.src.Host.Tree(w.src.VolumesDir+"/shop_dbdata/_data"))
	// Stack record: same ID, destination placement; deployed by the initiator.
	stk := w.stack()
	if stk.EnvironmentID != dstEnv || stk.Root != protocol.RootStacks || stk.Dir != "shop" || stk.ID != "st-shop" {
		t.Errorf("stack %+v", stk)
	}
	if len(w.stacks.deploys) != 1 || w.stacks.deploys[0].UserID != "u-alice" {
		t.Errorf("deploys %+v", w.stacks.deploys)
	}
	if !w.running(w.dst, "shop-db-1", "shop-web-1") {
		t.Error("the destination does not run the stack")
	}
	// The compensation was released: nothing restarts the source.
	if w.agents.called(srcEnv, protocol.ReqMigrationStart) != 0 {
		t.Error("the source was restarted")
	}
}

// TestPreflightBlocksBeforeAnyStop: a blocker (a port the destination
// already publishes) refuses the migration before anything stops.
func TestPreflightBlocksBeforeAnyStop(t *testing.T) {
	w := newWorld(t)
	w.dst.Engine.AddContainer(engineSpec("proxy", 8080), true)
	_, _, err := w.svc.StartStack(w.ctx, w.user, w.stack(), StackRequest{TargetEnvironmentID: dstEnv})
	var be *BlockedError
	if !errors.As(err, &be) || be.Plan.Blockers[0].Code != FindingPortConflict {
		t.Fatalf("error %v", err)
	}
	if len(w.jobs.enqueued) != 0 || w.agents.called(srcEnv, protocol.ReqMigrationStop) != 0 || !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("a blocked migration changed something")
	}
}

// crashAt makes the named step "crash" the manager, before or after the
// step's own effect: the attempt is abandoned mid-step (like a killed
// process) and recovered as after a restart, with the agents not yet
// reconnected unless online is true.
func (w *world) crashAt(j domain.Job, step string, after, online bool) protocol.ResultPayload {
	w.t.Helper()
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	x := w.svc.stackExecutor()
	orig := x.Steps[step]
	x.Steps[step] = func(sctx context.Context, sc *jobexec.StepContext) error {
		if after {
			if err := orig(sctx, sc); err != nil {
				return err
			}
		}
		cancel()
		return sctx.Err()
	}
	st := &jobexec.State{JobID: j.ID, Attempt: 1, Kind: j.Kind, Input: j.Input}
	if _, err := jobexec.Run(ctx, x, st, jobexec.Options{Journal: memJournal{}}); !errors.Is(err, jobexec.ErrAbandoned) {
		w.t.Fatalf("crash at %s: %v", step, err)
	}
	if !online {
		w.agents.kill(srcEnv)
		w.agents.kill(dstEnv)
	}
	exec := w.svc.stackExecutor()
	res, err := jobexec.Recover(w.ctx, &exec, st, jobexec.Options{Journal: memJournal{}})
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

func engineSpec(name string, port uint16) engine.ContainerSpec {
	return engine.ContainerSpec{Name: name, Image: "caddy:2", Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: port, Protocol: "tcp"}}}
}

// TestManagerCrashAtEverySafePoint (#35 Done-when 3, manager side): a
// manager killed before or after any step leaves the job interrupted with
// guidance and the source recoverable: the stack record points at the
// source again, the source's data is untouched and its services are
// started again when its agent is reachable (otherwise the guidance says
// to start them); a completed cut-over is never rolled back.
func TestManagerCrashAtEverySafePoint(t *testing.T) {
	cases := []struct {
		step  string
		after bool
	}{{"prepare", false}, {"stop_source", false}, {"stop_source", true}, {"transfer", false}, {"transfer", true},
		{"deploy_destination", false}, {"deploy_destination", true}, {"finalize", true}}
	for _, c := range cases {
		for _, online := range []bool{false, true} {
			name := c.step + map[bool]string{true: "/after", false: "/before"}[c.after] + map[bool]string{true: "/agents_up", false: "/agents_down"}[online]
			t.Run(name, func(t *testing.T) {
				w := newWorld(t)
				srcProject := w.src.Host.Tree(w.src.ProjectDir("shop"))
				j := w.start(Selection{})
				res := w.crashAt(j, c.step, c.after, online)
				if res.Outcome != jobexec.OutcomeInterrupted {
					t.Fatalf("outcome %+v", res)
				}
				sameTree(t, "source project", srcProject, w.src.Host.Tree(w.src.ProjectDir("shop")))
				stk, m := w.stack(), w.record(j.ID)
				if c.step == "finalize" {
					if stk.EnvironmentID != dstEnv || m.State != domain.MigrationCompleted || w.agents.called(srcEnv, protocol.ReqMigrationStart) != 0 {
						t.Fatalf("a completed cut-over was rolled back: %+v %+v", stk, m)
					}
					return
				}
				if stk.EnvironmentID != srcEnv || m.CutOver {
					t.Fatalf("the stack record must point at the source: %+v cutOver %v", stk, m.CutOver)
				}
				stopped := w.agents.called(srcEnv, protocol.ReqMigrationStop) > 0
				switch {
				case !stopped:
					// Nothing changed: the job engine records it interrupted
					// with its default guidance (resumable steps only).
					if !w.running(w.src, "shop-db-1", "shop-web-1") || !res.Resumable {
						t.Fatalf("nothing stopped, yet the source is down (or %+v)", res)
					}
					return
				case online:
					if !w.running(w.src, "shop-db-1", "shop-web-1") {
						t.Fatal("the source's services were not started again")
					}
				default:
					if !w.stopped(w.src, "shop-db-1", "shop-web-1") || !strings.Contains(res.Recovery, "start the stack once it reconnects") {
						t.Fatalf("guidance %q", res.Recovery)
					}
				}
				if res.Recovery == "" || res.ErrorClass == "" {
					t.Errorf("interrupted without guidance: %+v", res)
				}
			})
		}
	}
}

// dieOnce runs die the first time the host opens name: an agent killed
// while the source reads that file. fail also fails the read (the dying
// agent is the reader).
func dieOnce(h *migrationtest.Host, name string, fail bool, die func()) *atomic.Bool {
	var fired atomic.Bool
	h.FailOpen = func(n string) error {
		if n == name && fired.CompareAndSwap(false, true) {
			die()
			if fail {
				return errors.New("the agent died")
			}
		}
		return nil
	}
	return &fired
}

func (w *world) runDriven(j domain.Job) *protocol.ResultPayload {
	w.t.Helper()
	var res *protocol.ResultPayload
	err := testutil.DriveClock(w.ctx, w.clk, time.Second, func() error {
		st, _, err := w.run(j, nil)
		if st != nil {
			res = st.Outcome
		}
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

// TestSourceAgentKilledMidTransfer (#35 Done-when 3, agent side): the
// source agent dies during the volume copy and does not come back: the job
// ends interrupted with guidance, the destination is marked partial, the
// stack stays on its source with its data untouched.
func TestSourceAgentKilledMidTransfer(t *testing.T) {
	w := newWorld(t)
	srcVolume := w.src.Host.Tree(w.src.VolumesDir + "/shop_dbdata/_data")
	j := w.start(Selection{})
	fired := dieOnce(w.src.Host, "PG_VERSION", true, func() { w.agents.kill(srcEnv) })
	res := w.runDriven(j)
	if !fired.Load() || res.Outcome != jobexec.OutcomeInterrupted || res.ErrorClass != domain.ErrorAgentOffline {
		t.Fatalf("outcome %+v", res)
	}
	if !strings.Contains(res.Recovery, "start the stack once it reconnects") || !strings.Contains(res.Recovery, "partial data") {
		t.Errorf("guidance %q", res.Recovery)
	}
	m := w.record(j.ID)
	if !m.TargetPartial || m.CutOver || w.stack().EnvironmentID != srcEnv {
		t.Errorf("record %+v stack %+v", m, w.stack())
	}
	sameTree(t, "source volume", srcVolume, w.src.Host.Tree(w.src.VolumesDir+"/shop_dbdata/_data"))
}

// TestDestinationAgentKilledMidTransfer: the destination dies mid-copy;
// the source is reachable and is started again; the job is interrupted.
func TestDestinationAgentKilledMidTransfer(t *testing.T) {
	w := newWorld(t)
	j := w.start(Selection{})
	fired := dieOnce(w.src.Host, "PG_VERSION", false, func() { w.agents.kill(dstEnv) })
	res := w.runDriven(j)
	if !fired.Load() || res.Outcome != jobexec.OutcomeInterrupted || res.ErrorClass != domain.ErrorAgentOffline {
		t.Fatalf("outcome %+v", res)
	}
	if !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("the source was not started again")
	}
	if len(res.Compensations) != 1 || !res.Compensations[0].Done {
		t.Errorf("compensations %+v", res.Compensations)
	}
}

// TestAgentReconnectResumesAtPartBoundary: an agent that comes back within
// the reconnect window lets the transfer resume from the failed part; the
// migration completes without sending the verified parts again.
func TestAgentReconnectResumesAtPartBoundary(t *testing.T) {
	w := newWorld(t)
	j := w.start(Selection{})
	var sends atomic.Int32
	w.agents.onOpen = func(_, kind string) {
		if kind == protocol.StreamMigrationSend {
			sends.Add(1)
		}
	}
	dieOnce(w.src.Host, "PG_VERSION", true, func() {
		w.agents.kill(srcEnv)
		go w.agents.revive(srcEnv)
	})
	res := w.runDriven(j)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v", res)
	}
	if n := sends.Load(); n != 4 {
		t.Errorf("%d send streams, want 4 (project, volume twice, image)", n)
	}
}

// TestDeployFailureRollsBack: the destination deploy fails: the stack
// record returns to the source and the source runs again; the destination
// keeps its partial data for the next migration to remove.
func TestDeployFailureRollsBack(t *testing.T) {
	w := newWorld(t)
	w.jobs.deployOutcome = domain.JobFailed
	j := w.start(Selection{})
	st, _, err := w.run(j, nil)
	if err != nil || st.Outcome.Outcome != jobexec.OutcomeFailed || st.Outcome.ErrorClass != ClassDeployFailed {
		t.Fatalf("outcome %+v %v", st.Outcome, err)
	}
	if w.stack().EnvironmentID != srcEnv || !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("not rolled back")
	}
	m := w.record(j.ID)
	if m.CutOver || !m.TargetPartial {
		t.Errorf("record %+v", m)
	}
	// The next migration removes the leftovers first and succeeds.
	m.State = domain.MigrationFailed
	if err := w.svc.updateRecord(w.ctx, j.ID, func(r *domain.Migration) { r.State = domain.MigrationFailed }); err != nil {
		t.Fatal(err)
	}
	w.jobs.deployOutcome = domain.JobSucceeded
	plan, err := w.svc.PreviewStack(w.ctx, w.user, false, w.stack(), StackRequest{TargetEnvironmentID: dstEnv})
	if err != nil || !plan.Allowed() || !slices.Equal(plan.Leftovers, []string{j.ID}) {
		t.Fatalf("preview with leftovers %+v %+v %v", plan.Leftovers, plan.Blockers, err)
	}
	j2 := w.start(Selection{})
	st, _, err = w.run(j2, nil)
	if err != nil || st.Outcome.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("second migration %+v %v", st.Outcome, err)
	}
	if w.record(j.ID).TargetPartial {
		t.Error("the first migration's leftovers are still recorded")
	}
}

// TestChecksumMismatchIsRetriedThenFails: a source whose data fails
// verification is retried; persistent corruption fails the job with
// checksum_mismatch and restarts the source.
func TestChecksumMismatchIsRetriedThenFails(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "persistent"}[persistent], func(t *testing.T) {
			w := newWorld(t)
			j := w.start(Selection{})
			a := w.agents.envs[srcEnv]
			orig := a.streams[protocol.StreamMigrationSend]
			corrupt := 0
			a.streams[protocol.StreamMigrationSend] = func(ctx context.Context, s *streammux.Stream) error {
				var in protocol.MigrationSendInput
				_ = json.Unmarshal(s.Input(), &in)
				if in.Part == protocol.PartVolume && (persistent || corrupt == 0) {
					corrupt++
					return corruptingSend(ctx, s)
				}
				return orig(ctx, s)
			}
			w.agents.revive(srcEnv)
			st, _, err := w.run(j, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !persistent {
				if st.Outcome.Outcome != jobexec.OutcomeSucceeded || corrupt != 1 {
					t.Fatalf("outcome %+v corrupt %d", st.Outcome, corrupt)
				}
				return
			}
			if st.Outcome.Outcome != jobexec.OutcomeFailed || st.Outcome.ErrorClass != ClassChecksumMismatch || corrupt != 3 {
				t.Fatalf("outcome %+v corrupt %d", st.Outcome, corrupt)
			}
			if !w.running(w.src, "shop-db-1", "shop-web-1") || w.stack().EnvironmentID != srcEnv {
				t.Fatal("not rolled back")
			}
		})
	}
}

// corruptingSend sends a framed payload with one flipped byte inside a
// chunk (its per-chunk checksum no longer matches).
func corruptingSend(_ context.Context, s *streammux.Stream) error {
	var buf bytes.Buffer
	w := transfer.NewWriter(&buf, 1024)
	_, _ = w.Write(bytes.Repeat([]byte("data"), 4096))
	_ = w.Close()
	b := buf.Bytes()
	b[len(transfer.Magic)+100] ^= 0x01
	if _, err := s.Write(b); err != nil {
		return err
	}
	return s.CloseWithResult(protocol.MigrationPartResult{Bytes: w.Summary().Bytes, SHA256: w.Summary().SHA256, Chunks: w.Summary().Chunks})
}

// TestDestinationAuthorizationRecheckedWhenTheJobRuns: losing stack.deploy
// on the destination between the request and the run fails the job
// before anything stops.
func TestDestinationAuthorizationRecheckedWhenTheJobRuns(t *testing.T) {
	w := newWorld(t)
	j := w.start(Selection{})
	w.auth.deny["stack.deploy"] = true
	st, _, err := w.run(j, nil)
	if err != nil || st.Outcome.Outcome != jobexec.OutcomeFailed || st.Outcome.ErrorClass != domain.ErrorAuthorizationRevoked {
		t.Fatalf("outcome %+v %v", st.Outcome, err)
	}
	if w.agents.called(srcEnv, protocol.ReqMigrationStop) != 0 || !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("something stopped")
	}
}

// TestVolumeMigrationPreservesOwnership (#35 Done-when 4): a standalone
// volume copies under a new name with owners, modes and times; a volume a
// running container uses needs the crash-consistency acknowledgement.
func TestVolumeMigrationPreservesOwnership(t *testing.T) {
	w := newWorld(t)
	want := w.src.Host.Tree(w.src.VolumesDir + "/shop_dbdata/_data")
	// shop-db-1 runs and mounts the volume.
	_, _, err := w.svc.StartVolume(w.ctx, w.user, srcEnv, "shop_dbdata", VolumeRequest{TargetEnvironmentID: dstEnv, TargetName: "dbcopy"})
	var be *BlockedError
	if !errors.As(err, &be) || be.Plan.Blockers[0].Code != FindingContainersRunning {
		t.Fatalf("error %v", err)
	}
	j, _, err := w.svc.StartVolume(w.ctx, w.user, srcEnv, "shop_dbdata", VolumeRequest{TargetEnvironmentID: dstEnv, TargetName: "dbcopy",
		AcknowledgeCrashConsistency: true})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := w.run(j, nil)
	if err != nil || st.Outcome.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v %v", st.Outcome, err)
	}
	sameTree(t, "volume copy", want, w.dst.Host.Tree(w.dst.VolumesDir+"/dbcopy/_data"))
	if got := w.dst.Host.Tree(w.dst.VolumesDir + "/dbcopy/_data"); got["."].UID != 999 || got["PG_VERSION"].Mode.Perm() != 0o600 {
		t.Errorf("ownership %+v", got["."])
	}
	if m := w.record(j.ID); m.State != domain.MigrationCompleted || len(m.Parts) != 1 || !m.Volumes[0].Copied {
		t.Errorf("record %+v", m)
	}
	if _, err := w.src.Engine.InspectVolume(w.ctx, "shop_dbdata"); err != nil || !w.running(w.src, "shop-db-1") {
		t.Error("the source volume and its user must stay")
	}
}

// TestSourceRemoval: a completed migration's source is removed on
// confirmation only, once.
func TestSourceRemoval(t *testing.T) {
	w := newWorld(t)
	j := w.start(Selection{})
	if _, err := w.svc.RemoveSource(w.ctx, w.user, "st-shop", j.ID, ""); !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("removal before completion: %v", err)
	}
	if st, _, err := w.run(j, nil); err != nil || st.Outcome.Outcome != jobexec.OutcomeSucceeded {
		t.Fatal(err)
	}
	rj, err := w.svc.RemoveSource(w.ctx, w.user, "st-shop", j.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if rj.Kind != jobspec.StackRemoveSource || rj.EnvironmentID != srcEnv {
		t.Fatalf("removal job %+v", rj)
	}
	var in protocol.SourceRemovalInput
	_ = json.Unmarshal(rj.Input, &in)
	if in.Stack.ProjectName != "shop" || in.Stack.Dir != "shop" || !slices.Equal(in.Volumes, []string{"shop_dbdata"}) {
		t.Errorf("input %+v", in)
	}
	targets := w.jobs.enqueued[len(w.jobs.enqueued)-1].Targets
	if len(targets) != 2 || targets[0].Type != domain.TargetStack || targets[1].ID != "shop_dbdata" {
		t.Errorf("targets %+v", targets)
	}
	if err := w.svc.onRemovalFinished(w.ctx, w.svc.db, domain.Job{ID: rj.ID, State: domain.JobSucceeded, Input: rj.Input,
		ResultOutput: json.RawMessage(`{"removedVolumes":["shop_dbdata"],"removedContainers":["shop-db-1"]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RemoveSource(w.ctx, w.user, "st-shop", j.ID, ""); !errors.Is(err, ErrSourceRemoved) {
		t.Fatalf("second removal: %v", err)
	}
}

// TestCancelBetweenParts: a cancellation during the transfer stops before
// the next part; the job ends cancelled and the source runs again.
func TestCancelBetweenParts(t *testing.T) {
	w := newWorld(t)
	j := w.start(Selection{})
	var cancelled atomic.Bool
	w.agents.onOpen = func(_, kind string) {
		if kind == protocol.StreamMigrationReceive {
			cancelled.Store(true) // requested while the first part runs
		}
	}
	x := w.svc.stackExecutor()
	st := &jobexec.State{JobID: j.ID, Attempt: 1, Kind: j.Kind, Input: j.Input}
	res, err := jobexec.Run(w.ctx, x, st, jobexec.Options{Journal: memJournal{}, CancelRequested: cancelled.Load})
	if err != nil || res.Outcome != jobexec.OutcomeCancelled {
		t.Fatalf("outcome %+v %v", res, err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "project" {
		t.Errorf("parts before the cancellation %+v", res.Items)
	}
	if !w.running(w.src, "shop-db-1", "shop-web-1") || w.stack().EnvironmentID != srcEnv {
		t.Fatal("the source was not put back")
	}
}
