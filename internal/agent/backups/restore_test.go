package backups

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const restoreKey = "DYRK-RESTORE-TEST"

// backedUp runs a backup of the stack and the uploads volume and returns
// the members by item.
func backedUp(t *testing.T, e *env) map[string]backup.Member {
	t.Helper()
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{}),
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}), e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("backup: %+v %v", res, err)
	}
	out := map[string]backup.Member{}
	for _, m := range outputOf(t, res).Members {
		out[m.Item] = m
	}
	return out
}

func (e *env) restoreInput(m backup.Member, scope string) protocol.RestoreRunInput {
	in := protocol.RestoreRunInput{Repository: e.repoRef(), SnapshotID: m.SnapshotID, Scope: scope, SnapshotPaths: m.Paths, Shutdown: true}
	switch scope {
	case protocol.RestoreScopeStack:
		in.StackID, in.StackName, in.Project, in.ProjectSource = m.StackID, m.StackName, stackItem(protocol.BackupRules{}).Project, m.ProjectPath
	case protocol.RestoreScopeVolume:
		for _, v := range m.Volumes {
			in.Volumes = append(in.Volumes, protocol.RestoreVolume{Name: v, Source: m.VolumePaths[v]})
		}
	}
	return in
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func restoreOutputOf(t *testing.T, res protocol.ResultPayload) protocol.RestoreRunOutput {
	t.Helper()
	var out protocol.RestoreRunOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output %s: %v", res.Output, err)
	}
	return out
}

func TestRestoreVolumeReplacesContentAndRestartsUsers(t *testing.T) {
	e := newEnv(t)
	// A standalone container uses the volume.
	e.eng.AddContainer(engine.ContainerSpec{Name: "gallery", Image: "example/gallery:1",
		Mounts: []engine.MountSpec{{Type: "volume", Source: "uploads", Target: "/srv"}}}, true)
	members := backedUp(t, e)
	up := filepath.Join(e.volumes, "uploads", "_data")
	write(t, filepath.Join(up, "a.jpg"), "changed")
	write(t, filepath.Join(up, "new.txt"), "added after the backup")
	_ = os.Remove(filepath.Join(up, "thumbs", "a.png"))

	in := e.restoreInput(members[backup.VolumeItem("uploads")], protocol.RestoreScopeVolume)
	// The preview counts what changes and lists the container.
	raw, _ := json.Marshal(protocol.RestorePreviewInput{Input: in, Credential: &e.credential(restoreKey).Repositories[0]})
	pvAny, err := e.svc.restorePreview(testutil.Context(t), raw)
	if err != nil {
		t.Fatal(err)
	}
	pv := pvAny.(protocol.RestorePreviewOutput)
	if len(pv.Targets) != 1 || pv.Targets[0].Overwritten != 1 || pv.Targets[0].Removed != 1 || pv.Targets[0].Added != 1 ||
		len(pv.Blocked) != 0 || !slices.ContainsFunc(pv.Affected, func(a protocol.AffectedContainer) bool { return a.Name == "gallery" && a.Running }) {
		t.Fatalf("preview %+v", pv)
	}

	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if read(t, filepath.Join(up, "a.jpg")) != "jpeg" || read(t, filepath.Join(up, "thumbs", "a.png")) != "png" || read(t, filepath.Join(up, "new.txt")) != "<missing>" {
		t.Errorf("volume content after restore: %q %q %q", read(t, filepath.Join(up, "a.jpg")), read(t, filepath.Join(up, "thumbs", "a.png")),
			read(t, filepath.Join(up, "new.txt")))
	}
	if log := e.eng.log(); !slices.Equal(log, []string{"stop:", "start:"}) {
		// standalone containers have no service label: the names are empty
		t.Errorf("lifecycle %v", log)
	}
	if !e.eng.containerRunning("gallery") {
		t.Error("the gallery container was not restarted")
	}
	entries, _ := os.ReadDir(filepath.Dir(up))
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".docker-manager-") {
			t.Errorf("left behind: %s", en.Name())
		}
	}
	out := restoreOutputOf(t, res)
	if out.RedeploySuggested || len(out.Targets) != 1 || out.Targets[0].FilesChanged == 0 {
		t.Errorf("output %+v", out)
	}
}

func TestRestoreStackLeavesVolumesAndSuggestsRedeploy(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	write(t, filepath.Join(e.project, "compose.yaml"), "services: {}\n")
	write(t, filepath.Join(e.project, "html", "index.html"), "<h1>edited</h1>")
	dbdata := filepath.Join(e.volumes, "app_dbdata", "_data", "PG_VERSION")
	write(t, dbdata, "17")

	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, e.restoreInput(members[backup.StackItem("st-app")], protocol.RestoreScopeStack),
		e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if !strings.Contains(read(t, filepath.Join(e.project, "compose.yaml")), "postgres:16") || read(t, filepath.Join(e.project, "html", "index.html")) != "<h1>hi</h1>" {
		t.Error("the project directory was not restored")
	}
	if read(t, dbdata) != "17" {
		t.Error("a stack restore overwrote a volume")
	}
	if !restoreOutputOf(t, res).RedeploySuggested {
		t.Error("no redeploy suggested")
	}
	want := []string{"stop:web", "stop:api", "stop:db", "start:db", "start:api", "start:web"}
	if got := e.eng.log(); !slices.Equal(got, want) {
		t.Errorf("lifecycle %v, want %v", got, want)
	}
	if e.eng.running("worker") {
		t.Error("the stopped worker was started")
	}
}

func TestRestoreSingleFileInPlace(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	target := filepath.Join(e.project, "html", "index.html")
	write(t, target, "<h1>broken</h1>")
	m := members[backup.StackItem("st-app")]
	in := e.restoreInput(m, protocol.RestoreScopeStack)
	in.Scope, in.File = protocol.RestoreScopeFile, snapPath(target)
	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if read(t, target) != "<h1>hi</h1>" {
		t.Errorf("file = %q", read(t, target))
	}
	// A file of a volume.
	vm := members[backup.VolumeItem("uploads")]
	vfile := filepath.Join(e.volumes, "uploads", "_data", "a.jpg")
	write(t, vfile, "corrupted")
	vin := protocol.RestoreRunInput{Repository: e.repoRef(), SnapshotID: vm.SnapshotID, Scope: protocol.RestoreScopeFile, SnapshotPaths: vm.Paths,
		File: snapPath(vfile), Shutdown: true}
	if res, _, _ := e.run(testutil.Context(t), jobspec.RestoreRun, vin, e.credential(restoreKey), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("volume file restore: %+v", res)
	}
	if read(t, vfile) != "jpeg" {
		t.Errorf("volume file = %q", read(t, vfile))
	}
	// Paths outside the stack and its volumes are refused.
	vin.File = "/etc/passwd"
	if res, _, _ := e.run(testutil.Context(t), jobspec.RestoreRun, vin, e.credential(restoreKey), nil); res.ErrorClass != "path_not_restorable" {
		t.Errorf("outside file: %+v", res)
	}
}

func TestRestoreRecreatesAMissingVolume(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	m := members[backup.StackItem("st-app")]
	// A fresh host: neither the database container nor its volume exist.
	for _, c := range []string{"app-db-1", "reporting"} {
		if err := e.eng.RemoveContainer(testutil.Context(t), c, engine.RemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.eng.RemoveVolume(testutil.Context(t), "app_dbdata", true); err != nil {
		t.Fatal(err)
	}
	e.eng.SetVolumeMountpoint("app_dbdata", "")
	in := e.restoreInput(m, protocol.RestoreScopeVolume)
	in.Volumes[0].ComposeProject, in.Volumes[0].ComposeKey = "app", "dbdata"
	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	v, err := e.eng.InspectVolume(testutil.Context(t), "app_dbdata")
	if err != nil || v.Labels["com.docker.compose.project"] != "app" || v.Labels["com.docker.compose.volume"] != "dbdata" {
		t.Fatalf("recreated volume %+v %v", v, err)
	}
	if got := read(t, filepath.Join(osPath(v.Mountpoint), "PG_VERSION")); got != "16" {
		t.Errorf("recreated volume content %q", got)
	}
}

func TestRestoreFailureKeepsOriginalAndRestarts(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	up := filepath.Join(e.volumes, "uploads", "_data")
	write(t, filepath.Join(up, "a.jpg"), "current")
	e.store.Fail("restore", "", &restic.Error{Op: "restore", Code: restic.CodeRepositoryDamaged, Message: "invalid data returned"})
	e.eng.AddContainer(engine.ContainerSpec{Name: "gallery", Image: "example/gallery:1",
		Mounts: []engine.MountSpec{{Type: "volume", Source: "uploads", Target: "/srv"}}}, true)
	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, e.restoreInput(members[backup.VolumeItem("uploads")], protocol.RestoreScopeVolume),
		e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeRepositoryDamaged {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if read(t, filepath.Join(up, "a.jpg")) != "current" {
		t.Error("the original data was not kept")
	}
	if len(res.Compensations) != 1 || !res.Compensations[0].Done || !e.eng.containerRunning("gallery") {
		t.Errorf("containers not restarted: %+v", res.Compensations)
	}
}

func TestRestoreRefusals(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	ctx := testutil.Context(t)
	// Running users without shutdown: refused, nothing touched.
	in := e.restoreInput(members[backup.StackItem("st-app")], protocol.RestoreScopeStack)
	in.Shutdown = false
	if res, _, _ := e.run(ctx, jobspec.RestoreRun, in, e.credential(restoreKey), nil); res.ErrorClass != "restore_blocked" {
		t.Errorf("no shutdown: %+v", res)
	}
	// A protected (Docker Manager) container using the volume blocks it.
	e.eng.AddContainer(engine.ContainerSpec{Name: "docker-agent", Image: "docker-agent:edge",
		Labels: map[string]string{protocol.LabelRole: "agent"}, Mounts: []engine.MountSpec{{Type: "volume", Source: "uploads", Target: "/x"}}}, true)
	vin := e.restoreInput(members[backup.VolumeItem("uploads")], protocol.RestoreScopeVolume)
	if res, _, _ := e.run(ctx, jobspec.RestoreRun, vin, e.credential(restoreKey), nil); res.ErrorClass != "restore_blocked" ||
		!strings.Contains(res.Message, "Docker Manager") {
		t.Errorf("protected container: %+v", res)
	}
	// Invalid inputs.
	bad := vin
	bad.Volumes = nil
	if res, _, _ := e.run(ctx, jobspec.RestoreRun, bad, e.credential(restoreKey), nil); res.Outcome != jobexec.OutcomeFailed {
		t.Errorf("no volumes: %+v", res)
	}
	if len(e.eng.log()) != 0 {
		t.Errorf("containers touched by refused restores: %v", e.eng.log())
	}
}

func (r *recEngine) containerRunning(name string) bool {
	c, ok := r.Container(name)
	return ok && c.Details.State.Running
}

// TestRestoreFullStack (#10): a full restore puts back the project
// directory and every volume of the stack backup in one job, stops and
// restarts exactly what ran, and suggests a deploy unless the manager
// redeploys.
func TestRestoreFullStack(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	m := members[backup.StackItem("st-app")]
	write(t, filepath.Join(e.project, "compose.yaml"), "services: {}\n")
	dbdata := filepath.Join(e.volumes, "app_dbdata", "_data", "PG_VERSION")
	write(t, dbdata, "17")

	in := e.restoreInput(m, protocol.RestoreScopeStack)
	in.Scope = protocol.RestoreScopeFull
	for _, v := range m.Volumes {
		in.Volumes = append(in.Volumes, protocol.RestoreVolume{Name: v, Source: m.VolumePaths[v]})
	}
	if len(in.Volumes) == 0 {
		t.Fatal("the stack backup holds no volume")
	}
	raw, _ := json.Marshal(protocol.RestorePreviewInput{Input: in, Credential: &e.credential(restoreKey).Repositories[0]})
	pvAny, err := e.svc.restorePreview(testutil.Context(t), raw)
	if err != nil {
		t.Fatal(err)
	}
	pv := pvAny.(protocol.RestorePreviewOutput)
	kinds := []string{}
	for _, tg := range pv.Targets {
		kinds = append(kinds, tg.Kind)
	}
	if !slices.Contains(kinds, "project") || !slices.Contains(kinds, "volume") {
		t.Fatalf("preview targets %v", kinds)
	}
	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if !strings.Contains(read(t, filepath.Join(e.project, "compose.yaml")), "postgres:16") || read(t, dbdata) != "16" {
		t.Errorf("not restored: compose %q, PG_VERSION %q", read(t, filepath.Join(e.project, "compose.yaml")), read(t, dbdata))
	}
	out := restoreOutputOf(t, res)
	if !out.RedeploySuggested || len(out.Targets) != len(pv.Targets) {
		t.Errorf("output %+v", out)
	}
	if e.eng.running("worker") || !e.eng.containerRunning("reporting") {
		t.Error("the restart did not restore the previous states")
	}
	in.Redeploy = true
	res, _, _ = e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if res.Outcome != jobexec.OutcomeSucceeded || restoreOutputOf(t, res).RedeploySuggested {
		t.Errorf("redeploy restore: %+v", res)
	}
}

// TestRestorePaths (#10): selected files and directories are put back in
// place; a selected directory becomes identical to the backup, everything
// outside the selection stays as it is.
func TestRestorePaths(t *testing.T) {
	e := newEnv(t)
	members := backedUp(t, e)
	vm := members[backup.VolumeItem("uploads")]
	up := filepath.Join(e.volumes, "uploads", "_data")
	write(t, filepath.Join(up, "a.jpg"), "changed")
	write(t, filepath.Join(up, "thumbs", "a.png"), "changed")
	write(t, filepath.Join(up, "thumbs", "extra.png"), "added after the backup")
	write(t, filepath.Join(up, "new.txt"), "outside the selection")
	in := protocol.RestoreRunInput{Repository: e.repoRef(), SnapshotID: vm.SnapshotID, Scope: protocol.RestoreScopePaths,
		SnapshotPaths: vm.Paths, Shutdown: true, Paths: []string{snapPath(filepath.Join(up, "thumbs"))}}
	raw, _ := json.Marshal(protocol.RestorePreviewInput{Input: in, Credential: &e.credential(restoreKey).Repositories[0]})
	pvAny, err := e.svc.restorePreview(testutil.Context(t), raw)
	if err != nil {
		t.Fatal(err)
	}
	pv := pvAny.(protocol.RestorePreviewOutput)
	if len(pv.Targets) != 1 || pv.Targets[0].Kind != "dir" || pv.Targets[0].Overwritten != 1 || pv.Targets[0].Removed != 1 {
		t.Fatalf("preview %+v", pv.Targets)
	}
	res, _, err := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if read(t, filepath.Join(up, "thumbs", "a.png")) != "png" || read(t, filepath.Join(up, "thumbs", "extra.png")) != "<missing>" {
		t.Error("the selected directory is not identical to the backup")
	}
	if read(t, filepath.Join(up, "a.jpg")) != "changed" || read(t, filepath.Join(up, "new.txt")) != "outside the selection" {
		t.Error("a path outside the selection changed")
	}
	// Several paths across the stack's project directory; a missing one is
	// created.
	sm := members[backup.StackItem("st-app")]
	_ = os.RemoveAll(filepath.Join(e.project, "html"))
	write(t, filepath.Join(e.project, ".env"), "SECRET=changed\n")
	sin := e.restoreInput(sm, protocol.RestoreScopeStack)
	sin.Scope, sin.Paths = protocol.RestoreScopePaths, []string{snapPath(filepath.Join(e.project, "html")), snapPath(filepath.Join(e.project, ".env"))}
	if res, _, _ := e.run(testutil.Context(t), jobspec.RestoreRun, sin, e.credential(restoreKey), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("stack paths: %+v", res)
	}
	if read(t, filepath.Join(e.project, "html", "index.html")) != "<h1>hi</h1>" || read(t, filepath.Join(e.project, ".env")) != "SECRET=value\n" {
		t.Error("the stack paths were not restored")
	}
	entries, _ := os.ReadDir(e.project)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".docker-manager-") {
			t.Errorf("left behind: %s", en.Name())
		}
	}
	// A selected root is made identical to the backup in place.
	write(t, filepath.Join(up, "a.jpg"), "changed again")
	in.Paths = []string{snapPath(up)}
	if res, _, _ := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("root path: %+v", res)
	}
	if read(t, filepath.Join(up, "a.jpg")) != "jpeg" || read(t, filepath.Join(up, "new.txt")) != "<missing>" {
		t.Error("the selected volume root is not identical to the backup")
	}
	if fi, err := os.Stat(up); err != nil || !fi.IsDir() {
		t.Errorf("the volume's data directory is gone: %v", err)
	}
	// A path the backup does not hold is refused before anything stops.
	e.eng.reset()
	in.Paths = []string{snapPath(filepath.Join(up, "never"))}
	if res, _, _ := e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil); res.ErrorClass != "snapshot_path_unknown" {
		t.Errorf("unknown path: %+v", res)
	}
	if len(e.eng.log()) != 0 {
		t.Errorf("containers touched by a refused restore: %v", e.eng.log())
	}
}

func (r *recEngine) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}
