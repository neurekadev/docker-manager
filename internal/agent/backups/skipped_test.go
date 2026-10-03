package backups

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func memberStates(out protocol.BackupRunOutput) map[string]backup.Member {
	m := map[string]backup.Member{}
	for _, mem := range out.Members {
		m[mem.Item] = mem
	}
	return m
}

func itemStatus(res protocol.ResultPayload, name string) (protocol.ItemPayload, bool) {
	for _, it := range res.Items {
		if it.Name == name {
			return it, true
		}
	}
	return protocol.ItemPayload{}, false
}

// hostManifest reads the host manifest a run wrote.
func (e *env) hostManifest(t *testing.T, out protocol.BackupRunOutput, key string) backup.Manifest {
	t.Helper()
	repoPath := e.repoRef().Destination.Repository(e.repoRef().Scope)
	var buf strings.Builder
	if err := e.store.Open(restic.Location{Repository: repoPath}, key).Dump(testutil.Context(t), out.ManifestSnapshotID, "/"+backup.ManifestFile, &buf); err != nil {
		t.Fatal(err)
	}
	m, err := backup.DecodeManifest([]byte(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBackupRunSkipsVolumeRemovedBeforeItsTurn: a standalone volume removed
// after the run was planned (here: while the stack before it is backed up)
// is skipped, not failed; the job succeeds and the host manifest is
// complete.
func TestBackupRunSkipsVolumeRemovedBeforeItsTurn(t *testing.T) {
	e := newEnv(t)
	e.store.OnBackup = func(_ context.Context, req restic.BackupRequest) error {
		if req.Stdin == nil && slices.Contains(req.Paths, e.project) {
			if err := e.eng.RemoveVolume(testutil.Context(t), "uploads", false); err != nil {
				t.Errorf("remove the volume: %v", err)
			}
		}
		return nil
	}
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{}),
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	out := outputOf(t, res)
	members := memberStates(out)
	if m := members[backup.StackItem("st-app")]; m.State != backup.StateComplete || m.SnapshotID == "" {
		t.Errorf("stack member = %+v", m)
	}
	vol := members[backup.VolumeItem("uploads")]
	if vol.State != backup.StateSkipped || vol.ErrorClass != backup.ClassItemGone || vol.SnapshotID != "" {
		t.Errorf("volume member = %+v", vol)
	}
	if it, ok := itemStatus(res, backup.VolumeItem("uploads")); !ok || it.Status != domain.ItemSkipped ||
		!strings.Contains(it.Message, "was removed before its turn") {
		t.Errorf("job item = %+v (%v)", it, ok)
	}
	m := e.hostManifest(t, out, "DYRK-TEST")
	if m.Completeness != backup.StateComplete {
		t.Errorf("host manifest completeness = %s", m.Completeness)
	}
	for _, mem := range m.Members {
		if mem.Item == backup.VolumeItem("uploads") && mem.State != backup.StateSkipped {
			t.Errorf("host manifest member = %+v", mem)
		}
	}
}

// TestBackupRunWithOnlyRemovedItemsSucceeds: every item gone before the
// run began is skipped; the job succeeds and records a skipped set part.
func TestBackupRunWithOnlyRemovedItemsSucceeds(t *testing.T) {
	e := newEnv(t)
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false,
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "ci-job-tmp"}), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	out := outputOf(t, res)
	if m := memberStates(out)[backup.VolumeItem("ci-job-tmp")]; m.State != backup.StateSkipped || m.ErrorClass != backup.ClassItemGone {
		t.Errorf("member = %+v", m)
	}
	if m := e.hostManifest(t, out, "DYRK-TEST"); m.Completeness != backup.StateSkipped {
		t.Errorf("host manifest completeness = %s", m.Completeness)
	}
}

// TestBackupRunFailsVolumeOnOtherErrors: only a Not Found makes an item
// gone; a volume that cannot be inspected, or whose data directory is
// missing, still fails.
func TestBackupRunFailsVolumeOnOtherErrors(t *testing.T) {
	e := newEnv(t)
	// The volume item is planned first and takes the injected failure.
	e.eng.Fail("volume.inspect", enginefake.Err("volume.inspect", engine.CodeEngineError, "the Engine is busy"))
	// A volume the Engine knows, whose data directory does not exist.
	e.eng.AddVolume("broken", nil)
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false,
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"},
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "broken"},
		stackItem(protocol.BackupRules{})), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomePartial {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	members := memberStates(outputOf(t, res))
	for _, v := range []string{"uploads", "broken"} {
		if m := members[backup.VolumeItem(v)]; m.State != backup.StateFailed || m.ErrorClass == backup.ClassItemGone {
			t.Errorf("volume %s member = %+v", v, m)
		}
	}
	if m := members[backup.StackItem("st-app")]; m.State != backup.StateComplete {
		t.Errorf("stack member = %+v", m)
	}
}

// TestStackWithDeletedProjectDirectoryIsGone: a deleted project directory
// (its parent still there) makes the stack gone; one below a missing
// directory is not "deleted" and still fails.
func TestStackWithDeletedProjectDirectoryIsGone(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	if err := os.RemoveAll(e.project); err != nil {
		t.Fatal(err)
	}
	p := e.svc.plan(ctx, stackItem(protocol.BackupRules{}), false)
	if !isGone(p.err) || !strings.Contains(p.err.Error(), "was removed before its turn") {
		t.Errorf("deleted project directory: %v", p.err)
	}
	nested := stackItem(protocol.BackupRules{})
	nested.Project.Dir = "unmounted/app"
	if p := e.svc.plan(ctx, nested, false); p.err == nil || isGone(p.err) {
		t.Errorf("project directory below a missing directory: %v", p.err)
	}

	res, _, err := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{}),
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	members := memberStates(outputOf(t, res))
	if m := members[backup.StackItem("st-app")]; m.State != backup.StateSkipped || m.ErrorClass != backup.ClassItemGone {
		t.Errorf("stack member = %+v", m)
	}
	if m := members[backup.VolumeItem("uploads")]; m.State != backup.StateComplete {
		t.Errorf("volume member = %+v", m)
	}
}

// TestTemporaryContainersDoNotAddVolumes: anonymous volumes that only
// temporary containers (Compose's replacement, a container set aside by
// Docker Manager) mount are not backed up with the stack; a volume a real
// container also mounts still is.
func TestTemporaryContainersDoNotAddVolumes(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	project := func(svc string, extra map[string]string) map[string]string {
		l := map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: svc}
		for k, v := range extra {
			l[k] = v
		}
		return l
	}
	e.eng.AddContainer(engine.ContainerSpec{Name: "0123456789ab_app-db-1", Image: "postgres:16",
		Labels: project("db", map[string]string{protocol.ComposeReplaceLabel: "app-db-1"}),
		Mounts: []engine.MountSpec{{Type: "volume", Source: e.anon, Target: "/scratch"}, {Type: "volume", Target: "/tmp-only"}}}, false)
	e.eng.AddContainer(engine.ContainerSpec{Name: "app-web-1-docker-manager-update-abcdef012345", Image: "nginx:1.27",
		Labels: project("web", nil), Mounts: []engine.MountSpec{{Type: "volume", Target: "/aside-only"}}}, false)
	var helperOnly []string
	for _, name := range []string{"0123456789ab_app-db-1", "app-web-1-docker-manager-update-abcdef012345"} {
		c, ok := e.eng.Container(name)
		if !ok {
			t.Fatalf("container %s missing", name)
		}
		for _, m := range c.Details.Mounts {
			if m.Name != e.anon {
				helperOnly = append(helperOnly, m.Name)
				// A data directory, so only the rule can leave it out.
				mp := filepath.Join(e.volumes, m.Name, "_data")
				write(t, filepath.Join(mp, "x"), "x")
				e.eng.SetVolumeMountpoint(m.Name, filepath.ToSlash(mp))
			}
		}
	}
	if len(helperOnly) != 2 {
		t.Fatalf("temporary containers' own volumes = %v", helperOnly)
	}
	p := e.svc.plan(ctx, stackItem(protocol.BackupRules{AnonymousVolumes: true}), false)
	if p.err != nil {
		t.Fatal(p.err)
	}
	if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceIncluded {
		t.Errorf("volume of a real container: %+v", s)
	}
	for _, name := range helperOnly {
		s, ok := sourceState(p, protocol.SourceAnonymous, name)
		if !ok || s.State != protocol.SourceExcluded || s.Reason != helperOnlyReason {
			t.Errorf("volume %s of a temporary container only: %+v (%v)", name, s, ok)
		}
		if slices.Contains(p.volumes, name) {
			t.Errorf("volume %s of a temporary container only is backed up", name)
		}
	}
}
