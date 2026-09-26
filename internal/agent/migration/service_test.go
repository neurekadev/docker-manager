package migration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration/migrationtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux/muxtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/transfer"
)

const migID = "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001"

func env(t *testing.T, name string) *migrationtest.Env {
	t.Helper()
	return migrationtest.NewEnv(t, name, filepath.ToSlash(filepath.Join(t.TempDir(), name)), 10<<30)
}

func request[T any](t *testing.T, e *migrationtest.Env, name string, in any) (T, error) {
	t.Helper()
	var out T
	raw, _ := json.Marshal(in)
	res, err := e.Service.Requests()[name](testutil.Context(t), raw)
	if err != nil {
		return out, err
	}
	b, _ := json.Marshal(res)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func code(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	var ce *streammux.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func pipe(t *testing.T, e *migrationtest.Env) *muxtest.Pipe {
	t.Helper()
	h := map[string]muxtest.Handler{}
	for k, v := range e.Service.Streams() {
		h[k] = muxtest.Handler(v)
	}
	return muxtest.New(t, h)
}

// TestPreviewSourceStack: the source's facts about a stack (services,
// images, named and anonymous volumes with their sizes, networks, binds
// inside and outside the project, ports, running services).
func TestPreviewSourceStack(t *testing.T) {
	src := env(t, "src")
	ref := migrationtest.SeedShop(src)
	// An anonymous volume of the project.
	src.Engine.AddContainer(engine.ContainerSpec{Name: "shop-cache-1", Image: "postgres:17", Labels: map[string]string{
		protocol.ComposeProjectLabel: "shop", protocol.ComposeServiceLabel: "cache"},
		Mounts: []engine.MountSpec{{Type: "volume", Target: "/tmp/cache"}}}, false)
	out, err := request[protocol.MigrationPreviewOutput](t, src, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{
		Role: protocol.RoleSource, Source: &protocol.MigrationSourceQuery{Stack: &ref, Measure: true}})
	if err != nil {
		t.Fatal(err)
	}
	f := out.Source
	if f == nil || f.Platform != "linux/amd64" || f.Project == nil {
		t.Fatalf("facts %+v", f)
	}
	p := f.Project
	if p.Name != "shop" || p.Protected || len(p.Services) != 2 || p.DirEntries < 6 || p.DirBytes == 0 {
		t.Fatalf("project %+v", p)
	}
	web := p.Services[1]
	if web.Name != "web" || web.ImageID == "" || len(web.RepoDigests) != 0 || web.ImagePlatform != "linux/amd64" || !web.Running ||
		len(web.Ports) != 1 || web.Ports[0].Published != 8080 || !slices.Equal(web.ContainerNames, []string{"shop-web-1"}) {
		t.Errorf("web %+v", web)
	}
	if db := p.Services[0]; db.Name != "db" || len(db.RepoDigests) != 1 {
		t.Errorf("db %+v", db)
	}
	var named, anon *protocol.MigrationVolumeFacts
	for i, v := range p.Volumes {
		switch {
		case v.Anonymous:
			anon = &p.Volumes[i]
		case v.Key == "dbdata":
			named = &p.Volumes[i]
		}
	}
	if named == nil || named.Name != "shop_dbdata" || !named.Exists || !named.Supported || named.Bytes < 300_000 || named.Entries != 4 ||
		named.Labels["com.docker.compose.volume"] != "dbdata" {
		t.Errorf("named volume %+v", named)
	}
	if anon == nil || !anon.Supported {
		t.Errorf("anonymous volume %+v", anon)
	}
	var rel, ext int
	for _, b := range p.Binds {
		switch {
		case b.RelPath == "data" && !b.External:
			rel++
		case b.External && b.Source == "/srv/shared":
			ext++
		}
	}
	if rel != 1 || ext != 1 {
		t.Errorf("binds %+v", p.Binds)
	}
	if len(p.Networks) != 1 || p.Networks[0].Name != "shop_default" {
		t.Errorf("networks %+v", p.Networks)
	}
}

// TestPreviewSourceRefusesOutsideRoots: a project outside the verified
// roots is never read.
func TestPreviewSourceRefusesOutsideRoots(t *testing.T) {
	src := env(t, "src")
	migrationtest.SeedShop(src)
	ref := protocol.ProjectRef{Root: protocol.RootBind, RootPath: "/etc", Dir: "shop", ProjectName: "shop"}
	_, err := request[protocol.MigrationPreviewOutput](t, src, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{
		Role: protocol.RoleSource, Source: &protocol.MigrationSourceQuery{Stack: &ref}})
	if code(err) != protocol.CodeForbiddenPath {
		t.Fatalf("error %v", err)
	}
}

// TestPreviewDestination: what the destination would collide with.
func TestPreviewDestination(t *testing.T) {
	dst := env(t, "dst")
	dst.Engine.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1", Labels: map[string]string{protocol.ComposeProjectLabel: "shop"}}, false)
	dst.Engine.AddContainer(engine.ContainerSpec{Name: "proxy", Image: "caddy:2", Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: 8080}}}, true)
	dst.Engine.AddContainer(engine.ContainerSpec{Name: "stopped", Image: "caddy:2", Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: 9090}}}, false)
	dst.AddVolume("shop_dbdata", map[string]string{protocol.LabelMigration: "old-migration"})
	dst.Engine.AddNetwork("shop_default", nil)
	dst.Host.MkdirAll(dst.ProjectDir("shop"))
	dst.Host.MkdirAll(dst.ProjectDir(protocol.MigrationStagingDir + "/" + migID))
	out, err := request[protocol.MigrationPreviewOutput](t, dst, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{
		Role: protocol.RoleDestination, Destination: &protocol.MigrationDestinationQuery{ProjectName: "shop", Dir: "shop",
			ContainerNames: []string{"shop-db-1", "shop-web-1"}, Volumes: []string{"shop_dbdata"}, Networks: []string{"shop_default"},
			ExternalNetworks: []string{"corp"}, ExternalVolumes: []string{"shop_dbdata"},
			Ports:  []protocol.MigrationPort{{Published: 8080, Protocol: "tcp"}, {Published: 9090, Protocol: "tcp"}, {Published: 8080, Protocol: "udp"}},
			Images: []string{"caddy:2", "postgres:17"}}})
	if err != nil {
		t.Fatal(err)
	}
	f := out.Destination
	if !f.StacksOK || !f.VolumesOK || f.StacksFree != 10<<30 || !f.DirExists {
		t.Errorf("storage %+v", f)
	}
	if !slices.Equal(f.ProjectContainers, []string{"shop-web-1"}) || !slices.Equal(f.Containers, []string{"shop-web-1"}) {
		t.Errorf("containers %v %v", f.ProjectContainers, f.Containers)
	}
	if len(f.Volumes) != 1 || f.Volumes[0].Migration != "old-migration" || len(f.MissingVolumes) != 0 {
		t.Errorf("volumes %+v missing %v", f.Volumes, f.MissingVolumes)
	}
	if !slices.Equal(f.Networks, []string{"shop_default"}) || !slices.Equal(f.MissingNetworks, []string{"corp"}) {
		t.Errorf("networks %v missing %v", f.Networks, f.MissingNetworks)
	}
	// Only the running container's TCP port collides.
	if len(f.PortConflicts) != 1 || f.PortConflicts[0].Container != "proxy" || f.PortConflicts[0].Port.Published != 8080 {
		t.Errorf("ports %+v", f.PortConflicts)
	}
	if !slices.Equal(f.ImagesPresent, []string{"caddy:2"}) || !slices.Equal(f.Staged, []string{migID}) {
		t.Errorf("images %v staged %v", f.ImagesPresent, f.Staged)
	}
}

// framed archives a tree of h and frames it.
func framed(t *testing.T, h *migrationtest.Host, dir string) ([]byte, transfer.Summary) {
	t.Helper()
	fs, err := h.Opener()(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := transfer.NewWriter(&buf, 4096)
	if _, err := migration.WriteTree(testutil.Context(t), fs, w); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.Summary()
}

// receive streams data into a migration.receive stream and returns the
// destination's result.
func receive(t *testing.T, p *muxtest.Pipe, in protocol.MigrationReceiveInput, data []byte) (protocol.MigrationPartResult, error) {
	t.Helper()
	ctx := testutil.Context(t)
	st, err := p.Open(ctx, protocol.StreamMigrationReceive, in, streammux.OpenOptions{JobID: in.MigrationID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write(data); err != nil {
		return protocol.MigrationPartResult{}, err
	}
	if err := st.CloseWrite(); err != nil {
		return protocol.MigrationPartResult{}, err
	}
	raw, err := st.Result(ctx)
	if err != nil {
		return protocol.MigrationPartResult{}, err
	}
	var res protocol.MigrationPartResult
	_ = json.Unmarshal(raw, &res)
	return res, nil
}

// TestProjectStagedCommittedAndCleaned: the destination stages a project
// part, commits it into a new directory only (never over an existing
// one), and a cleanup removes exactly what the migration created.
func TestProjectStagedCommittedAndCleaned(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	data, sum := framed(t, src.Host, src.ProjectDir("shop"))
	p := pipe(t, dst)
	res, err := receive(t, p, protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartProject}, data)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != sum.SHA256 || res.Bytes != sum.Bytes || res.Chunks != sum.Chunks || res.Entries == 0 {
		t.Fatalf("result %+v, sent %+v", res, sum)
	}
	staged := dst.ProjectDir(protocol.MigrationStagingDir + "/" + migID + "/project")
	if !dst.Host.Exists(staged+"/compose.yaml") || dst.Host.Exists(dst.ProjectDir("shop")) {
		t.Fatal("the part must be staged, not in place")
	}
	// A directory that exists is never replaced.
	dst.Host.MkdirAll(dst.ProjectDir("taken"))
	if _, err := request[protocol.MigrationCommitOutput](t, dst, protocol.ReqMigrationCommit, protocol.MigrationCommitInput{MigrationID: migID, Dir: "taken"}); code(err) != protocol.CodeAlreadyExists {
		t.Fatalf("commit over an existing directory: %v", err)
	}
	co, err := request[protocol.MigrationCommitOutput](t, dst, protocol.ReqMigrationCommit, protocol.MigrationCommitInput{MigrationID: migID, Dir: "shop"})
	if err != nil || co.Path != dst.ProjectDir("shop") {
		t.Fatalf("commit %+v %v", co, err)
	}
	if _, err := request[protocol.MigrationCommitOutput](t, dst, protocol.ReqMigrationCommit, protocol.MigrationCommitInput{MigrationID: migID, Dir: "shop"}); err != nil {
		t.Fatalf("a repeated commit is idempotent: %v", err)
	}
	want, got := src.Host.Tree(src.ProjectDir("shop")), dst.Host.Tree(dst.ProjectDir("shop"))
	for k, w := range want {
		g := got[k]
		if w.Type == "symlink" { // a symlink's own times and mode bits are not kept
			w.MTime, w.ATime, w.Mode = g.MTime, g.ATime, g.Mode
		}
		if g != w {
			t.Errorf("%s: got %+v want %+v", k, g, w)
		}
	}
	// A volume created by the migration, one that is not.
	dst.AddVolume("shop_dbdata", map[string]string{protocol.LabelMigration: migID})
	dst.AddVolume("foreign", nil)
	dst.Engine.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1", Labels: map[string]string{
		protocol.ComposeProjectLabel: "shop", protocol.ComposeWorkingDirLabel: dst.ProjectDir("shop")}}, true)
	dst.Engine.AddContainer(engine.ContainerSpec{Name: "other-shop", Image: "nginx:1", Labels: map[string]string{
		protocol.ComposeProjectLabel: "shop", protocol.ComposeWorkingDirLabel: "/elsewhere/shop"}}, true)
	cl, err := request[protocol.MigrationCleanupOutput](t, dst, protocol.ReqMigrationCleanup, protocol.MigrationCleanupInput{MigrationID: migID,
		Project: "shop", Volumes: []string{"shop_dbdata", "foreign"}})
	if err != nil {
		t.Fatal(err)
	}
	if dst.Host.Exists(dst.ProjectDir("shop")) || dst.Host.Exists(dst.ProjectDir(protocol.MigrationStagingDir+"/"+migID)) {
		t.Error("the committed directory and the staging directory must be removed")
	}
	if _, ok := dst.Engine.Container("shop-web-1"); ok {
		t.Error("the migration's container must be removed")
	}
	if _, ok := dst.Engine.Container("other-shop"); !ok {
		t.Error("a container of another working directory must stay")
	}
	if _, err := dst.Engine.InspectVolume(testutil.Context(t), "shop_dbdata"); err == nil {
		t.Error("the migration's volume must be removed")
	}
	if _, err := dst.Engine.InspectVolume(testutil.Context(t), "foreign"); err != nil {
		t.Error("a foreign volume must stay")
	}
	if len(cl.Kept) != 1 || !strings.Contains(cl.Kept[0], "foreign") {
		t.Errorf("kept %v", cl.Kept)
	}
}

// TestVolumeReceiveRefusesForeignVolume: the destination writes only into
// volumes it creates (or created for this migration before).
func TestVolumeReceiveRefusesForeignVolume(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	dst.AddVolume("shop_dbdata", nil)
	data, _ := framed(t, src.Host, src.VolumesDir+"/shop_dbdata/_data")
	_, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartVolume,
		Volume: &protocol.MigrationVolumeSpec{Name: "shop_dbdata"}}, data)
	if code(err) != protocol.CodeAlreadyExists {
		t.Fatalf("error %v", err)
	}
}

// TestVolumeReceiveRetriesStartOver: a retried part of this migration
// replaces the partial data it wrote before.
func TestVolumeReceiveRetriesStartOver(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	mp := dst.AddVolume("shop_dbdata", map[string]string{protocol.LabelMigration: migID})
	dst.Host.Put(mp+"/stale", migrationtest.Entry{Data: "partial"})
	data, sum := framed(t, src.Host, src.VolumesDir+"/shop_dbdata/_data")
	res, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartVolume,
		Volume: &protocol.MigrationVolumeSpec{Name: "shop_dbdata", Labels: map[string]string{"com.docker.compose.project": "shop"}}}, data)
	if err != nil || res.SHA256 != sum.SHA256 {
		t.Fatalf("%+v %v", res, err)
	}
	if dst.Host.Exists(mp + "/stale") {
		t.Error("stale partial data survived")
	}
	if got := dst.Host.Tree(mp); got["PG_VERSION"].UID != 999 || got["."].UID != 999 || got["."].Mode.Perm() != 0o700 {
		t.Errorf("ownership of the volume root and data: %+v", got)
	}
}

// TestCorruptedPartIsRejected: a chunk whose checksum does not match is
// never extracted; the stream fails with digest_mismatch.
func TestCorruptedPartIsRejected(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	data, _ := framed(t, src.Host, src.ProjectDir("shop"))
	data[len(transfer.Magic)+10] ^= 0xff
	_, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartProject}, data)
	if code(err) != protocol.CodeDigestMismatch {
		t.Fatalf("error %v", err)
	}
	if dst.Host.Exists(dst.ProjectDir("shop")) {
		t.Fatal("nothing may be committed")
	}
}

// TestSendRefusesDockerManagerVolumes: Docker Manager's own volumes are never read (#32).
func TestSendRefusesDockerManagerVolumes(t *testing.T) {
	src := env(t, "src")
	src.AddVolume("docker-manager_data", nil)
	src.Engine.AddContainer(engine.ContainerSpec{Name: "docker-manager-1", Image: "code.neureka.dev/docker-manager/docker-manager:edge",
		Labels: map[string]string{protocol.LabelRole: "manager"}, Mounts: []engine.MountSpec{{Type: "volume", Source: "docker-manager_data", Target: "/var/lib/docker-manager"}}}, true)
	p := pipe(t, src)
	st, err := p.Open(testutil.Context(t), protocol.StreamMigrationSend, protocol.MigrationSendInput{MigrationID: migID, Part: protocol.PartVolume,
		Volume: "docker-manager_data"}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var buf [64]byte
	_, err = st.Read(buf[:])
	if code(err) != protocol.CodeUnsupportedVolume {
		t.Fatalf("error %v", err)
	}
}

// TestImagePartRoundTrip: a locally built image travels through image
// save/load (the fake Engine's archive).
func TestImagePartRoundTrip(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	ctx := testutil.Context(t)
	sp := pipe(t, src)
	st, err := sp.Open(ctx, protocol.StreamMigrationSend, protocol.MigrationSendInput{MigrationID: migID, Part: protocol.PartImage,
		Images: []string{"shop-web:local"}}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if _, err := data.ReadFrom(st); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseWrite()
	res, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartImage,
		Images: []string{"shop-web:local"}}, data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var sent protocol.MigrationPartResult
	_ = json.Unmarshal(st.RemoteClose().Result, &sent)
	if res.SHA256 != sent.SHA256 || res.Bytes == 0 {
		t.Fatalf("sent %+v received %+v", sent, res)
	}
	if img, err := dst.Engine.InspectImage(ctx, "shop-web:local"); err != nil || len(img.RepoDigests) != 0 {
		t.Fatalf("image on the destination: %+v %v", img, err)
	}
}

// TestStopAndStartFollowDependencies: the source stops dependents first
// and starts again exactly the services that ran, dependencies first.
func TestStopAndStartFollowDependencies(t *testing.T) {
	src := env(t, "src")
	ref := migrationtest.SeedShop(src)
	out, err := request[protocol.MigrationLifecycleOutput](t, src, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: migID, Stack: ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Before) != 2 || out.Before[0].Running != 1 || out.After[0].Running != 0 || out.After[1].Running != 0 {
		t.Fatalf("stop %+v", out)
	}
	// The order (dependents first) is internal/agent/lifecycle's, tested
	// there; here both containers are stopped through it.
	stops := 0
	for _, c := range src.Engine.Calls() {
		if c == "container.stop" {
			stops++
		}
	}
	if stops != 2 {
		t.Fatalf("calls %v", src.Engine.Calls())
	}
	if c, _ := src.Engine.Container("shop-web-1"); c.Details.State.Running {
		t.Fatal("web still runs")
	}
	if _, err := request[protocol.MigrationLifecycleOutput](t, src, protocol.ReqMigrationStart, protocol.MigrationStartInput{MigrationID: migID,
		Stack: ref, Services: []string{"db", "web"}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"shop-db-1", "shop-web-1"} {
		if c, _ := src.Engine.Container(n); !c.Details.State.Running {
			t.Errorf("%s not started again", n)
		}
	}
}

// TestStopRefusesDockerManagerProject: Docker Manager's own project is never stopped.
func TestStopRefusesDockerManagerProject(t *testing.T) {
	src := env(t, "src")
	src.Engine.Deploy(true)
	_, err := request[protocol.MigrationLifecycleOutput](t, src, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: migID,
		Stack: protocol.ProjectRef{Root: protocol.RootStacks, Dir: "docker-manager", ProjectName: "docker-manager"}})
	if code(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("error %v", err)
	}
}

type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

// TestSourceRemoval: the confirmed removal takes the source project down,
// removes the migrated volumes and the project directory; Docker Manager's own
// volumes are refused.
func TestSourceRemoval(t *testing.T) {
	src := env(t, "src")
	ref := migrationtest.SeedShop(src)
	src.Engine.AddNetwork("shop_default", map[string]string{protocol.ComposeProjectLabel: "shop"})
	exec := src.Service.Executors()[0]
	in, _ := json.Marshal(protocol.SourceRemovalInput{StackID: "s1", MigrationID: migID, Stack: ref, Volumes: []string{"shop_dbdata"}})
	st := &jobexec.State{JobID: "j1", Attempt: 1, Kind: jobspec.StackRemoveSource, Input: in}
	res, err := jobexec.Run(testutil.Context(t), exec, st, jobexec.Options{Journal: memJournal{}})
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("%+v %v", res, err)
	}
	var out protocol.SourceRemovalOutput
	_ = json.Unmarshal(res.Output, &out)
	if len(out.RemovedContainers) != 2 || !slices.Equal(out.RemovedVolumes, []string{"shop_dbdata"}) || !out.RemovedDirectory {
		t.Fatalf("output %+v", out)
	}
	if src.Host.Exists(src.ProjectDir("shop")) || !src.Host.Exists(src.StacksDir) {
		t.Fatal("only the project directory is removed")
	}
	if nets, _ := src.Engine.ListNetworks(testutil.Context(t), protocol.ComposeProjectLabel+"=shop"); len(nets) != 0 {
		t.Errorf("networks left %v", nets)
	}
	// Running it again is harmless (every step is idempotent).
	st2 := &jobexec.State{JobID: "j2", Attempt: 1, Kind: jobspec.StackRemoveSource, Input: in}
	if res, _ := jobexec.Run(testutil.Context(t), exec, st2, jobexec.Options{Journal: memJournal{}}); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("second run %+v", res)
	}
	// Docker Manager's own volume.
	d := src.Engine.Deploy(true)
	in, _ = json.Marshal(protocol.SourceRemovalInput{StackID: "s1", MigrationID: migID, Stack: protocol.ProjectRef{Root: protocol.RootStacks,
		Dir: "gone", ProjectName: "gone"}, Volumes: []string{d.ManagerData}})
	st3 := &jobexec.State{JobID: "j3", Attempt: 1, Kind: jobspec.StackRemoveSource, Input: in}
	res, _ = jobexec.Run(testutil.Context(t), exec, st3, jobexec.Options{Journal: memJournal{}})
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != "protected" {
		t.Fatalf("protected volume: %+v", res)
	}
	_ = domain.ItemSucceeded
}
