package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/stacks"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/agent/watch"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestFilesWatcherAndBackupsShareTheIdenticalPathMount (#28, #15, #23,
// #10): the file manager, the watcher and restic's backup sources resolve
// a local named volume to the same directory — its mountpoint under
// Docker's volume directory, the identical-path mount the storage check
// verified — through the agent's one storage result; a file written through
// the file manager is the file the watcher reports and the backup reads.
// A remote-backed local volume is refused by all three alike.
func TestFilesWatcherAndBackupsShareTheIdenticalPathMount(t *testing.T) {
	a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
		Geteuid: func() int { return 0 }, Files: true, Backups: true,
		NewNotifier: func() (watch.Notifier, error) { return nil, errors.New("polled in this test") }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testutil.Context(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	volumesDir := filepath.ToSlash(filepath.Join(root, "var", "lib", "docker", "volumes"))
	eng := enginefake.New("ENG-SAME")
	eng.SetVolumeRoot(volumesDir)
	if _, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: "appdata"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: "nfsdata", DriverOpts: map[string]string{"type": "nfs", "o": "addr=10.0.0.2"}}); err != nil {
		t.Fatal(err)
	}
	vol, err := eng.InspectVolume(ctx, "appdata")
	if err != nil {
		t.Fatal(err)
	}
	mount := filepath.FromSlash(vol.Mountpoint)
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	stacksDir := volumesDir + "/docker-manager_stacks/_data"
	a.mu.Lock()
	a.eng = eng
	a.storage = &storage.Result{VolumesDir: volumesDir, StacksDir: stacksDir, Roots: []storage.Root{
		{Kind: storage.KindVolumes, Path: volumesDir, OK: true}, {Kind: storage.KindStacks, Path: stacksDir, OK: true}}}
	a.mu.Unlock()

	call := func(name string, in any) (any, error) {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		return a.opts.Requests[name](ctx, b)
	}

	// File manager: write through the volume scope.
	scope := protocol.FileScope{Kind: protocol.ScopeVolume, ID: "appdata"}
	if _, err := call(protocol.ReqFilesWrite, protocol.FilesWriteInput{Scope: scope, Path: "conf.ini", Data: []byte("a=1\n"), CreateOnly: true}); err != nil {
		t.Fatalf("file manager write: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(mount, "conf.ini")); err != nil || string(b) != "a=1\n" {
		t.Fatalf("the file manager did not write into the volume's mountpoint: %q %v", b, err)
	}

	// Watcher: the same directory.
	out, err := call(protocol.ReqFilesWatch, protocol.FilesWatchInput{Scopes: []protocol.FileScope{scope, {Kind: protocol.ScopeVolume, ID: "nfsdata"}}})
	if err != nil {
		t.Fatal(err)
	}
	ws := out.(protocol.FilesWatchOutput)
	if ws.Scopes[0].Mode == protocol.WatchUnavailable || ws.Scopes[1].Mode != protocol.WatchUnavailable || ws.Scopes[1].Reason != protocol.WatchReasonUnsupported {
		t.Fatalf("watch set %+v", ws)
	}
	filesDir, err := a.watcher.Rescan(ctx, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: protocol.ScopeVolume, ID: "appdata"}, Path: ".", MaxEntries: 10})
	if err != nil || filesDir.Entries != 1 { // conf.ini, seen through the same directory
		t.Fatalf("watcher scan %+v %v", filesDir, err)
	}

	// Backups: restic's source path of the volume is the same directory.
	prev, err := call(protocol.ReqBackupScopePreview, protocol.BackupScopePreviewInput{Items: []protocol.BackupItem{
		{Kind: backup.MemberVolume, Volume: "appdata"}, {Kind: backup.MemberVolume, Volume: "nfsdata"}}})
	if err != nil {
		t.Fatal(err)
	}
	items := prev.(protocol.BackupScopePreviewOutput).Items
	if len(items) != 2 {
		t.Fatalf("preview %+v", items)
	}
	resolvedMount, _ := filepath.EvalSymlinks(mount)
	if src := items[0].Sources; len(src) != 1 || src[0].State != protocol.SourceIncluded ||
		!slices.Contains(items[0].Paths, filepath.ToSlash(resolvedMount)) && !slices.Contains(items[0].Paths, resolvedMount) {
		t.Fatalf("backup source %+v paths %v, want %s", src, items[0].Paths, resolvedMount)
	}
	if src := items[1].Sources; len(src) != 1 || src[0].State != protocol.SourceBlocked {
		t.Fatalf("remote-backed volume in a backup: %+v", src)
	}
	// Stacks: the file manager and the watcher use the project directory
	// below the verified stacks root that deploys and backups resolve.
	project := filepath.Join(filepath.FromSlash(stacksDir), "shop")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stackScope := protocol.FileScope{Kind: protocol.ScopeStack, ID: "s1", Dir: filepath.ToSlash(project)}
	if _, err := call(protocol.ReqFilesWrite, protocol.FilesWriteInput{Scope: stackScope, Path: "compose.yaml", Data: []byte("services: {}\n"), CreateOnly: true}); err != nil {
		t.Fatalf("file manager write in the stack: %v", err)
	}
	backupDir, err := stacks.ResolveProjectDir(a.Capabilities().Storage, protocol.ProjectRef{Root: protocol.RootStacks, Dir: "shop", ProjectName: "shop"})
	if err != nil || backupDir != project {
		t.Fatalf("backup/deploy project directory %q %v, want %q", backupDir, err, project)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "compose.yaml")); err != nil {
		t.Fatalf("the backup source does not see the file manager's file: %v", err)
	}
	if _, err := call(protocol.ReqFilesWatch, protocol.FilesWatchInput{Scopes: []protocol.FileScope{scope, stackScope}}); err != nil {
		t.Fatal(err)
	}
	if res, err := a.watcher.Rescan(ctx, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: protocol.ScopeStack, ID: "s1"}, Path: ".", MaxEntries: 10}); err != nil || res.Entries != 1 {
		t.Fatalf("watcher scan of the stack %+v %v", res, err)
	}

	// The file manager refuses the remote-backed volume the same way.
	_, err = call(protocol.ReqFilesList, protocol.FilesListInput{Scope: protocol.FileScope{Kind: protocol.ScopeVolume, ID: "nfsdata"}, Path: "."})
	var he *session.HandlerError
	if !errors.As(err, &he) || he.Code != protocol.CodeUnsupportedVolume {
		t.Fatalf("file manager on a remote-backed volume: %v", err)
	}
}
