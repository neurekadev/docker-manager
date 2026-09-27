package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentbackups "code.neureka.dev/docker-manager/docker-manager/internal/agent/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	agentjobs "code.neureka.dev/docker-manager/docker-manager/internal/agent/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	agentresources "code.neureka.dev/docker-manager/docker-manager/internal/agent/resources"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/state"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups/s3probe/s3probetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic/restictest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// Backups (#10) through the real manager and a real agent session: the
// agent runs the production backup executors over a fake Engine; restic
// is the in-memory restictest store shared by manager and agent (an S3
// destination both can reach).

type backupEnv struct {
	*env
	store *restictest.Store
	// opener is restic for the manager and the agents (the in-memory store).
	opener  restic.Opener
	s3      *s3probetest.Server
	root    string
	stacks  string
	volumes string
	agent   *testAgent
	fe      *enginefake.Engine
	stackID string
}

type loaderFunc struct{}

func (loaderFunc) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

func newBackupEnv(t *testing.T) *backupEnv {
	t.Helper()
	access := "AKIADYTESTACCESS0001"
	return newBackupEnvWith(t, restictest.New(nil), s3probetest.New(t, access, "backups"))
}

// newBackupEnvWith starts a manager on its own data directory that reaches
// the given repositories (a fresh manager importing another's backups).
func newBackupEnvWith(t *testing.T, store *restictest.Store, s3 *s3probetest.Server) *backupEnv {
	t.Helper()
	return newBackupEnvOn(t, store, store, s3)
}

// newBackupEnvOn starts a manager whose restic is opener (store may be
// nil for the real restic; s3 nil for a real S3 endpoint) and further
// options.
func newBackupEnvOn(t *testing.T, opener restic.Opener, store *restictest.Store, s3 *s3probetest.Server, with ...func(*Options)) *backupEnv {
	t.Helper()
	root := t.TempDir()
	e := newEnv(t, append([]func(*Options){func(o *Options) {
		o.Restic = opener
		if s3 != nil {
			o.BackupHTTPClient = s3.Client()
		}
		o.Config.BackupLocalRoots = []string{filepath.ToSlash(filepath.Join(root, "manager-backups"))}
	}}, with...)...)
	b := &backupEnv{env: e, store: store, opener: opener, s3: s3, root: root, stacks: filepath.Join(root, "stacks"), volumes: filepath.Join(root, "volumes")}
	t.Cleanup(func() { scanDatabase(t, e) })
	return b
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// connectBackupAgent enrolls an environment whose agent serves backups
// over fe and a stack root in the test directory.
func (b *backupEnv) connectBackupAgent(name string) {
	b.t.Helper()
	h := b.connectHost(hostOpts{name: name, stacks: b.stacks, volumes: b.volumes})
	b.fe, b.agent = h.fe, h.agent
	b.seedStack()
}

// hostOpts describes one backup host of a test: its stack and volume
// roots, and for a re-attach the environment and the Engine it keeps.
type hostOpts struct {
	name, stacks, volumes string
	// reattach enrolls with intent reattach:<environmentId> and reuses fe.
	reattach string
	fe       *enginefake.Engine
	// localRoots are the agent's DOCKER_MANAGER_BACKUP_LOCAL_ROOTS.
	localRoots []string
}

type backupHost struct {
	fe    *enginefake.Engine
	agent *testAgent
}

// connectHost enrolls an agent serving backups over a fake Engine (a new
// one with the "app" stack and the "uploads" volume, or o.fe) and waits
// until its environment is online.
func (b *backupEnv) connectHost(o hostOpts) backupHost {
	e := b.env
	t := e.t
	t.Helper()
	ctx := testutil.Context(t)
	if o.fe != nil {
		return b.enrollHost(ctx, o, o.fe)
	}
	fe := enginefake.New("ENGINE-" + o.name)
	project := filepath.Join(o.stacks, "app")
	writeFile(t, filepath.Join(project, "compose.yaml"), "services:\n  db:\n    image: postgres:16\n    volumes:\n      - dbdata:/data\n"+
		"  web:\n    image: nginx:1.27\n    depends_on: [db]\n    volumes:\n      - ./html:/usr/share/nginx/html\nvolumes:\n  dbdata:\n")
	writeFile(t, filepath.Join(project, ".env"), "PASSWORD="+e.secrets.New(canary.EnvValue, "stack env value")+"\n")
	writeFile(t, filepath.Join(project, "html", "index.html"), "<h1>backup me</h1>")
	lbl := func(svc, deps string) map[string]string {
		l := map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: svc}
		if deps != "" {
			l[lifecycle.DependsOnLabel] = deps
		}
		return l
	}
	fe.AddContainer(engine.ContainerSpec{Name: "app-db-1", Image: "postgres:16", Labels: lbl("db", ""),
		Mounts: []engine.MountSpec{{Type: "volume", Source: "app_dbdata", Target: "/data"}}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "app-web-1", Image: "nginx:1.27", Labels: lbl("web", "db:service_started:false:true")}, true)
	dbdata := filepath.Join(o.volumes, "app_dbdata", "_data")
	writeFile(t, filepath.Join(dbdata, "PG_VERSION"), "16")
	fe.SetVolumeMountpoint("app_dbdata", filepath.ToSlash(dbdata))
	fe.AddVolume("uploads", nil)
	up := filepath.Join(o.volumes, "uploads", "_data")
	writeFile(t, filepath.Join(up, "photo.jpg"), "jpeg bytes")
	fe.SetVolumeMountpoint("uploads", filepath.ToSlash(up))
	return b.enrollHost(ctx, o, fe)
}

func (b *backupEnv) enrollHost(ctx context.Context, o hostOpts, fe *enginefake.Engine) backupHost {
	e := b.env
	t := e.t
	t.Helper()
	name := o.name
	res := &storage.Result{StacksDir: filepath.ToSlash(o.stacks), VolumesDir: filepath.ToSlash(o.volumes),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(o.stacks), OK: true},
			{Kind: storage.KindVolumes, Path: filepath.ToSlash(o.volumes), OK: true}}}
	agentLog := b.secrets.CaptureLogger(t) // agent logs are checked for canaries too
	guard := protect.New(protect.Options{StacksVolume: "docker-manager_stacks", Logger: agentLog})
	svc := agentbackups.New(agentbackups.Options{Engine: func() engine.Engine { return fe }, Loader: func() agentbackups.Loader { return loaderFunc{} },
		Storage: func() *storage.Result { return res }, Guard: guard, Restic: b.opener, LocalRoots: o.localRoots, Clock: e.clk, Logger: agentLog,
		WaitTimeout: time.Second})
	requests := svc.Requests()
	resourceRequests := agentresources.New(agentresources.Options{Engine: func() engine.Engine { return fe }, Guard: guard, Logger: agentLog}).Requests()
	requests[protocol.ReqVolumeList] = resourceRequests[protocol.ReqVolumeList]
	requests[protocol.ReqContainerList] = resourceRequests[protocol.ReqContainerList]

	sub := e.m.Events().Subscribe(256, func(ev events.Event) bool { return ev.Type == events.EnvironmentOnline })
	defer sub.Close()
	spec := domain.EnrollmentSpec{EnvironmentName: name}
	if o.reattach != "" {
		spec = domain.EnrollmentSpec{Intent: domain.IntentReattach, TargetID: o.reattach}
	}
	created, err := e.m.Agents().CreateEnrollment(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	install, _ := st.InstallID()
	id := fe.Identity()
	info := protocol.EngineInfo{ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion, MinAPIVersion: id.MinAPIVersion, OS: id.OS, Arch: id.Arch}
	body, _ := json.Marshal(protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: buildinfo.Get().Version, InstallID: install,
		Engine: info, Hostname: "host-" + name})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+protocol.EnrollPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var er protocol.EnrollResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &er) != nil {
		t.Fatalf("enroll: %d %s", resp.StatusCode, raw)
	}
	if err := st.SaveCredential(state.Credential{AgentID: er.AgentID, EnvironmentID: er.EnvironmentID, Credential: er.Credential,
		ManagerURL: e.srv.URL}); err != nil {
		t.Fatal(err)
	}
	execs := svc.Executors()
	a := &testAgent{env: er.EnvironmentID, engine: fe, done: make(chan error, 1)}
	client := session.New(session.Options{
		State: st, Clock: e.clk, Logger: agentLog, URL: "ws" + strings.TrimPrefix(e.srv.URL, "http") + protocol.SessionPath,
		DialOptions: func(h http.Header) *websocket.DialOptions {
			return &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{protocol.Version}}
		},
		AgentVersion: buildinfo.Get().Version, UserAgent: "docker-agent/test",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			var cmds []string
			for _, x := range execs {
				cmds = append(cmds, string(x.Kind))
			}
			return protocol.CapabilitiesPayload{AgentVersion: buildinfo.Get().Version, Protocols: []string{protocol.Version}, OS: "linux",
				Arch: "amd64", Engine: info, Commands: cmds, Requests: []string{protocol.ReqVolumeList, protocol.ReqContainerList}, Streams: []string{},
				Features:  []string{protocol.FeatureRestoreSelection},
				Transport: protocol.TransportInfo{ManagerURL: e.srv.URL, PlainHTTP: true}}, true
		},
		Requests: requests, Streams: svc.Streams(),
		Backoff: session.Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: func() float64 { return 0 }},
	})
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.runner, err = agentjobs.New(runCtx, agentjobs.Options{StateDir: st.Dir(), Clock: e.clk, Logger: agentLog, Sender: client, Executors: execs})
	if err != nil {
		t.Fatal(err)
	}
	client.SetRunner(a.runner)
	go func() { a.done <- client.Run(runCtx) }()
	t.Cleanup(a.stop)
	for {
		select {
		case ev := <-sub.C():
			if ev.ResourceID == er.EnvironmentID || ev.EnvironmentID == er.EnvironmentID {
				return backupHost{fe: fe, agent: a}
			}
		case <-ctx.Done():
			t.Fatal("environment did not come online")
		}
	}
}

// seedStack records the Compose project as a Docker Manager stack.
func (b *backupEnv) seedStack() {
	now := b.clk.Now().UTC()
	st := domain.Stack{ID: "0190a6e0-0000-7000-8000-00000000a001", EnvironmentID: b.agent.env, Name: "app", Root: protocol.RootStacks,
		Dir: "app", Origin: "imported", Status: domain.StackDeployed, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if err := store.InsertStack(testutil.Context(b.t), b.m.DB(), &st); err != nil {
		b.t.Fatal(err)
	}
	b.stackID = st.ID
}

type createdRepo struct {
	Repository struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Kind  string `json:"kind"`
	} `json:"repository"`
	RecoveryKey *struct {
		Key         string   `json:"key"`
		Fingerprint string   `json:"fingerprint"`
		Notice      []string `json:"notice"`
	} `json:"recoveryKey"`
	KeyState struct {
		PendingFingerprint string `json:"pendingFingerprint"`
		Fingerprint        string `json:"fingerprint"`
		Generation         int    `json:"generation"`
	} `json:"keyState"`
}

func (b *backupEnv) createS3Repo(owner *client, name string) createdRepo {
	b.t.Helper()
	secret := b.secrets.New(canary.S3SecretKey, "s3 secret "+name)
	var out createdRepo
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-repositories", map[string]any{
		"name": name, "kind": "s3", "endpoint": b.s3.URL, "bucket": "backups", "prefix": "docker-manager", "pathStyle": true,
		"accessKeyId": b.s3.AccessKey, "secretAccessKey": secret,
	}, secretOK).json(b.t, &out)
	if out.RecoveryKey != nil {
		b.secrets.Register(canary.RecoveryKey, "recovery key", out.RecoveryKey.Key)
	}
	return out
}

func (b *backupEnv) runJobs(r response) []string {
	b.t.Helper()
	var run struct {
		Set struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"set"`
		Jobs []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"jobs"`
	}
	r.json(b.t, &run)
	var ids []string
	for _, j := range run.Jobs {
		if got := b.runJob(j.ID); got.State != domain.JobSucceeded {
			b.t.Fatalf("job %s (%s): %s %s %s", j.ID, j.Kind, got.State, got.ErrorClass, got.ErrorMessage)
		}
		ids = append(ids, j.ID)
	}
	return ids
}

func TestBackupsThroughTheAPI(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	env := b.agent.env

	// The first repository generates the Recovery Key: shown once.
	repo := b.createS3Repo(owner, "Offsite")
	if repo.RecoveryKey == nil || !strings.HasPrefix(repo.RecoveryKey.Key, "DYRK-") || repo.RecoveryKey.Fingerprint != repo.KeyState.PendingFingerprint ||
		repo.Repository.State != domain.BackupRepositoryAwaitingConfirmation || len(repo.RecoveryKey.Notice) == 0 {
		t.Fatalf("created %+v", repo)
	}
	key := repo.RecoveryKey.Key
	// A second repository reuses the instance key: nothing is shown.
	second := b.createS3Repo(owner, "Second")
	if second.RecoveryKey != nil {
		t.Fatal("the key was shown twice")
	}
	id := repo.Repository.ID
	// Read routes never return the key or credentials (canary-checked).
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id, nil)
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories", nil)

	// Policies cannot be enabled before the key is confirmed.
	policy := map[string]any{"name": "Nightly", "scope": "environment", "environmentId": env, "repositoryId": id, "includeManagerState": true,
		"schedule":  map[string]any{"cron": "0 2 * * *", "timeZone": "UTC", "enabled": true},
		"retention": map[string]any{"daily": 7, "minKeep": 2}}
	owner.fail(http.StatusConflict, "recovery_key_not_confirmed", http.MethodPost, "/api/v1/backup-policies", policy)

	// The re-entry challenge.
	confirm := "/api/v1/backup-repositories/" + id + "/recovery-confirmations"
	wrong, _ := backups.GenerateRecoveryKey(nil)
	owner.fail(http.StatusUnprocessableEntity, "recovery_key_mismatch", http.MethodPost, confirm, map[string]any{"recoveryKey": wrong.String(), "backedUp": true})
	// Change the last two characters (a random key may already end in "QQ").
	broken := key[:len(key)-2] + "QQ"
	if strings.HasSuffix(key, "QQ") {
		broken = key[:len(key)-2] + "RR"
	}
	owner.fail(http.StatusUnprocessableEntity, "recovery_key_malformed", http.MethodPost, confirm, map[string]any{"recoveryKey": broken, "backedUp": true})
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, confirm, map[string]any{"recoveryKey": key, "backedUp": false})
	var conf struct {
		Activated bool `json:"activated"`
		KeyState  struct {
			Generation  int    `json:"generation"`
			Fingerprint string `json:"fingerprint"`
		} `json:"keyState"`
		Repository struct {
			State string `json:"state"`
		} `json:"repository"`
	}
	owner.must(http.StatusOK, http.MethodPost, confirm, map[string]any{"recoveryKey": strings.ToLower(key), "backedUp": true}).json(t, &conf)
	if !conf.Activated || conf.KeyState.Generation != 1 || conf.KeyState.Fingerprint != repo.RecoveryKey.Fingerprint || conf.Repository.State != "ready" {
		t.Fatalf("confirmation %+v", conf)
	}

	// Connection test against the fake S3 endpoint.
	var test struct {
		OK        bool  `json:"ok"`
		CanDelete *bool `json:"canDelete"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/connection-tests", nil).json(t, &test)
	if !test.OK || test.CanDelete == nil || !*test.CanDelete {
		t.Errorf("connection test %+v", test)
	}

	var pol struct {
		ID       string `json:"id"`
		Enabled  bool   `json:"enabled"`
		Schedule struct {
			NextRun *time.Time `json:"nextRun"`
		} `json:"schedule"`
	}
	// The create wizard previews its draft before anything is saved.
	var draft struct {
		Environments []struct {
			ErrorClass string `json:"errorClass"`
			Items      []protocol.ScopePreviewItem
		} `json:"environments"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-policy-scope-previews", policy).json(t, &draft)
	if len(draft.Environments) != 1 || draft.Environments[0].ErrorClass != "" || len(draft.Environments[0].Items) != 2 {
		t.Fatalf("draft preview %+v", draft)
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", policy).json(t, &pol)
	if !pol.Enabled {
		t.Fatalf("policy %+v", pol)
	}
	owner.fail(http.StatusConflict, "backup_scope_overlap", http.MethodPost, "/api/v1/backup-policies",
		map[string]any{"name": "Overlapping", "scope": "all", "repositoryId": id})
	// A draft overlapping the saved policy is refused before it is saved.
	owner.fail(http.StatusConflict, "backup_scope_overlap", http.MethodPost, "/api/v1/backup-policy-scope-previews",
		map[string]any{"name": "Overlapping", "scope": "all", "repositoryId": id})

	// Scope preview from the agent.
	var pv struct {
		Manager *struct {
			MetricsIncluded bool `json:"metricsIncluded"`
		} `json:"manager"`
		Environments []struct {
			EnvironmentID string `json:"environmentId"`
			ErrorClass    string `json:"errorClass"`
			Items         []protocol.ScopePreviewItem
		} `json:"environments"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/scope-previews", nil).json(t, &pv)
	if pv.Manager == nil || pv.Manager.MetricsIncluded || len(pv.Environments) != 1 || pv.Environments[0].ErrorClass != "" ||
		len(pv.Environments[0].Items) != 2 {
		t.Fatalf("preview %+v", pv)
	}

	// Run it: one job for the environment, then the manager backup.
	jobs := b.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))
	if len(jobs) != 2 {
		t.Fatalf("jobs %v", jobs)
	}
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			State string `json:"state"`
			SetID string `json:"setId"`
			Scope string `json:"scope"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?policyId="+pol.ID, nil).json(t, &page)
	kinds := map[string]string{}
	for _, it := range page.Items {
		kinds[it.Kind] = it.ID
	}
	if len(page.Items) != 3 || kinds["stack"] == "" || kinds["volume"] == "" || kinds["manager_state"] == "" {
		t.Fatalf("backups %+v", page.Items)
	}
	for _, it := range page.Items {
		if (it.Kind == "manager_state") != (it.Scope == "manager") {
			t.Errorf("backup %+v in the wrong scope", it)
		}
	}

	// Each backup measured its location (#10): the environment's and the
	// manager state's, summed on the repository.
	var measured struct {
		Storage *struct {
			SizeBytes         int64   `json:"sizeBytes"`
			UncompressedBytes int64   `json:"uncompressedBytes"`
			CompressionRatio  float64 `json:"compressionRatio"`
			Snapshots         int64   `json:"snapshots"`
			Locations         []struct {
				Scope         string `json:"scope"`
				EnvironmentID string `json:"environmentId"`
			} `json:"locations"`
		} `json:"storage"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id, nil).json(t, &measured)
	// The fake stores every location at half its size (rounded down).
	if st := measured.Storage; st == nil || st.SizeBytes <= 0 || st.UncompressedBytes-2*st.SizeBytes > 2 ||
		math.Abs(st.CompressionRatio-2) > 0.01 || st.Snapshots == 0 || len(st.Locations) != 2 {
		t.Fatalf("storage %+v", measured.Storage)
	}
	for _, l := range measured.Storage.Locations {
		if (l.Scope == "manager") != (l.EnvironmentID == "") {
			t.Errorf("location %+v", l)
		}
	}
	// restic's own view (#10): every snapshot of both locations, the
	// manifests included, linked to the backups the index holds.
	var listing struct {
		Locations []struct {
			Scope      string `json:"scope"`
			ErrorClass string `json:"errorClass"`
			Snapshots  []struct {
				Class    string `json:"class"`
				BackupID string `json:"backupId"`
				SetID    string `json:"setId"`
			} `json:"snapshots"`
		} `json:"locations"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id+"/snapshots", nil).json(t, &listing)
	classes := map[string]int{}
	linked := 0
	for _, l := range listing.Locations {
		if l.ErrorClass != "" {
			t.Errorf("location %s: %s", l.Scope, l.ErrorClass)
		}
		for _, sn := range l.Snapshots {
			classes[sn.Class]++
			if sn.BackupID != "" {
				linked++
			}
		}
	}
	if len(listing.Locations) != 2 || classes["stack"] != 1 || classes["volume"] != 1 || classes["manager_state"] != 1 ||
		classes["set_manifest"] != 1 || classes["host_manifest"] != 1 || linked != 3 {
		t.Errorf("restic snapshots %v (%d linked): %+v", classes, linked, listing)
	}

	// Nothing runs any more.
	var running struct {
		Jobs []any `json:"jobs"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-activity", nil).json(t, &running)
	if len(running.Jobs) != 0 {
		t.Errorf("activity after the run: %+v", running.Jobs)
	}
	var detail struct {
		Set struct {
			State   string `json:"state"`
			Members []struct {
				State        string     `json:"state"`
				SnapshotTime *time.Time `json:"snapshotTime"`
			} `json:"members"`
		} `json:"set"`
		Consistency string `json:"consistency"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+kinds["stack"], nil).json(t, &detail)
	if detail.Set.State != "complete" || len(detail.Set.Members) != 3 || detail.Consistency != "live" {
		t.Fatalf("detail %+v", detail)
	}

	// Browse and download (the owner may; the .env value is in the file,
	// so the download response is exempt from the canary check).
	var contents struct {
		Entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+kinds["stack"]+"/contents?recursive=true", nil).json(t, &contents)
	var index string
	for _, en := range contents.Entries {
		if strings.HasSuffix(en.Path, "/html/index.html") {
			index = en.Path
		}
	}
	if index == "" {
		t.Fatalf("contents %+v", contents)
	}
	dl := owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+kinds["stack"]+"/contents/download?path="+index, nil)
	if string(dl.body) != "<h1>backup me</h1>" || dl.header.Get("Content-Disposition") == "" {
		t.Errorf("download %q %v", dl.body, dl.header)
	}
	owner.fail(http.StatusConflict, "backup_not_a_file", http.MethodGet, "/api/v1/backups/"+kinds["stack"]+"/contents/download?path="+
		strings.TrimSuffix(index, "/index.html"), nil)

	// One directory lists its entries, never itself (restic ls prints the
	// directory first; the file picker would open it inside itself).
	html := strings.TrimSuffix(index, "/index.html")
	var level struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
		Truncated bool `json:"truncated"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+kinds["stack"]+"/contents?limit=1&path="+html, nil).json(t, &level)
	if len(level.Entries) != 1 || level.Entries[0].Path != index || level.Truncated {
		t.Errorf("listing of %s: %+v", html, level)
	}

	// The manager-state snapshot holds the database, the sealed secret-key
	// bundle and state.json; the manifest describes the whole set.
	var mcontents struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+kinds["manager_state"]+"/contents?recursive=true", nil).json(t, &mcontents)
	var names []string
	for _, en := range mcontents.Entries {
		names = append(names, filepath.Base(en.Path))
	}
	for _, want := range []string{"docker-manager.db", backups.BundleFile, "state.json"} {
		if !slices.Contains(names, want) {
			t.Errorf("manager snapshot lacks %s: %v", want, names)
		}
	}
	if slices.Contains(names, "metrics.db") {
		t.Error("metrics.db is included by default")
	}
	b.assertSetManifest(key, env)

	// Retention preview keeps the floor.
	var rp struct {
		Locations []struct {
			Keep   int `json:"keep"`
			Forget int `json:"forget"`
		} `json:"locations"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/retention-previews", nil).json(t, &rp)
	if len(rp.Locations) != 2 || rp.Locations[0].Forget != 0 {
		t.Errorf("retention preview %+v", rp)
	}

	// Health.
	var h struct {
		Healthy   bool `json:"healthy"`
		Snapshots int  `json:"snapshots"`
		Locations []struct {
			Scope         string `json:"scope"`
			KeyGeneration int    `json:"keyGeneration"`
		} `json:"locations"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id+"/health", nil).json(t, &h)
	if !h.Healthy || h.Snapshots != 3 || len(h.Locations) != 2 {
		t.Errorf("health %+v", h)
	}

	// Nothing secret reached audit, jobs or the index.
	b.secrets.AssertClean(t, "stored rows", b.tableDump("audit_events", "jobs", "job_events", "backup_sets", "backup_snapshots",
		"backup_locations", "backup_policies"))
	// Key administration is audited by fingerprint.
	var created, confirmed bool
	for _, row := range b.auditRows() {
		switch row.Operation {
		case "create-backup-repository":
			created = created || (strings.Contains(row.Details, `"keyGenerated":true`) && strings.Contains(row.Details, repo.RecoveryKey.Fingerprint))
		case "create-backup-repository-recovery-confirmation":
			confirmed = confirmed || (row.Outcome == "success" && strings.Contains(row.Details, `"keyFingerprint":"`+repo.RecoveryKey.Fingerprint))
		}
	}
	if !created || !confirmed {
		t.Errorf("key administration audit: created %v confirmed %v", created, confirmed)
	}
}

// assertSetManifest decodes the set manifest written into the manager
// scope with the Recovery Key alone.
func (b *backupEnv) assertSetManifest(key, env string) {
	t := b.t
	t.Helper()
	repoString := ""
	for _, r := range b.store.Repositories() {
		if strings.HasSuffix(r, "/docker-manager") {
			repoString = r
		}
	}
	snaps := b.store.Snapshots(repoString)
	var manifest restic.Snapshot
	for _, sn := range snaps {
		if sn.HasTag(backup.TagManifest) {
			manifest = sn
		}
	}
	var buf bytes.Buffer
	if err := b.store.Open(restic.Location{Repository: repoString}, key).Dump(testutil.Context(t), manifest.ID, "/"+backup.ManifestFile, &buf); err != nil {
		t.Fatal(err)
	}
	m, err := backup.DecodeManifest(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != backup.ManifestSet || m.Schema == nil || m.Schema.Latest() == "" || len(m.Members) != 3 || m.Completeness != backup.StateComplete ||
		len(m.Locations) != 2 || len(m.Repositories) != 1 {
		t.Fatalf("set manifest %+v", m)
	}
	for _, l := range m.Locations {
		if l.Scope == backup.EnvironmentScope(env) && (l.EnvironmentName != "prod" || l.EngineID == "") {
			t.Errorf("location %+v", l)
		}
	}
	b.secrets.AssertClean(t, "manifest", buf.String())
}

func TestBackupAuthorizationAndSessionOnlyKeyAdministration(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	repo := b.createS3Repo(owner, "Offsite")
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Data", "scope": "all", "repositoryId": id,
		"includeManagerState": true}).json(t, &pol)
	b.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups", nil).json(t, &page)
	var stackBackup, managerBackup string
	for _, it := range page.Items {
		switch it.Kind {
		case "stack":
			stackBackup = it.ID
		case "manager_state":
			managerBackup = it.ID
		}
	}

	// Rita may read and browse backups but not the stack's definition.
	rita, _ := b.opsUser(owner, "allow backup.read @all", "allow backup.contents.read @all", "allow backup_repository.read @all",
		"allow backup_repository.manage @all", "allow backup_policy.read @all", "allow api_tokens.create @all")
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+stackBackup, nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/backups/"+stackBackup+"/contents", nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/backups/"+managerBackup+"/contents", nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/backups/"+stackBackup+"/contents/download?path=/x", nil)
	// Recovery Key administration is the owner's, whatever is granted.
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/backup-repositories/"+id+"/key-rotations", nil)
	// Including the manager state is owner-only.
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "X", "scope": "all", "repositoryId": id,
		"includeManagerState": true})
	// And API tokens never reach Recovery Key administration.
	_, secret := rita.createToken("ci", "allow backup_repository.manage @all")
	b.secrets.Register(canary.APIToken, "token", secret)
	bot := b.bot(secret)
	bot.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	bot.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodPost, "/api/v1/backup-repositories/"+id+"/key-rotations", nil)
	bot.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodPost, "/api/v1/backup-repositories", map[string]any{"name": "Y", "kind": "s3"})

	// Owner rotation needs a recent step-up and returns the new key once.
	b.clk.Advance(11 * time.Minute)
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPost, "/api/v1/backup-repositories/"+id+"/key-rotations", nil)
	owner.stepUp()
	var rot struct {
		RecoveryKey struct {
			Key         string `json:"key"`
			Fingerprint string `json:"fingerprint"`
		} `json:"recoveryKey"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/key-rotations", nil, secretOK).json(t, &rot)
	b.secrets.Register(canary.RecoveryKey, "rotated key", rot.RecoveryKey.Key)
	var conf struct {
		KeyState struct {
			RotationInProgress bool     `json:"rotationInProgress"`
			PendingLocations   []string `json:"pendingLocations"`
		} `json:"keyState"`
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": rot.RecoveryKey.Key, "backedUp": true}).json(t, &conf)
	if !conf.KeyState.RotationInProgress || len(conf.KeyState.PendingLocations) != 2 || len(conf.Jobs) != 2 {
		t.Fatalf("rotation %+v", conf)
	}
	for _, j := range conf.Jobs {
		if got := b.runJob(j.ID); got.State != domain.JobSucceeded {
			t.Fatalf("key migration job: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
		}
	}
	var h struct {
		KeyState struct {
			RotationInProgress bool   `json:"rotationInProgress"`
			Generation         int    `json:"generation"`
			Fingerprint        string `json:"fingerprint"`
		} `json:"keyState"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id+"/health", nil).json(t, &h)
	if h.KeyState.RotationInProgress || h.KeyState.Generation != 2 || h.KeyState.Fingerprint != rot.RecoveryKey.Fingerprint {
		t.Fatalf("after migration %+v", h)
	}
	for _, r := range b.store.Repositories() {
		if pw := b.store.Passwords(r); len(pw) != 1 || pw[0] != rot.RecoveryKey.Key {
			t.Errorf("%s keys %d", r, len(pw))
		}
	}
	// Old snapshots still open with the new key.
	b.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))
}

// TestRestoresThroughTheAPI: previews and restores of a volume, a stack's
// definition and one file through the real agent session, with the
// authorization of every target and the manager-state refusal.
func TestRestoresThroughTheAPI(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	repo := b.createS3Repo(owner, "Offsite")
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "All", "scope": "all", "repositoryId": id,
		"includeManagerState": true}).json(t, &pol)
	b.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups", nil).json(t, &page)
	ids := map[string]string{}
	for _, it := range page.Items {
		ids[it.Kind] = it.ID
	}
	// A volume's backups: its own and the stack backups that hold it.
	var byVolume struct {
		Items []struct {
			ID          string            `json:"id"`
			ProjectPath string            `json:"projectPath"`
			VolumePaths map[string]string `json:"volumePaths"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?volume=app_dbdata", nil).json(t, &byVolume)
	if len(byVolume.Items) != 1 || byVolume.Items[0].ID != ids["stack"] || byVolume.Items[0].ProjectPath == "" ||
		byVolume.Items[0].VolumePaths["app_dbdata"] == "" {
		t.Errorf("backups of app_dbdata %+v", byVolume.Items)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?volume=uploads", nil).json(t, &byVolume)
	if len(byVolume.Items) != 1 || byVolume.Items[0].ID != ids["volume"] {
		t.Errorf("backups of uploads %+v", byVolume.Items)
	}

	// Damage the live data.
	photo := filepath.Join(b.volumes, "uploads", "_data", "photo.jpg")
	writeFile(t, photo, "overwritten")
	writeFile(t, filepath.Join(b.volumes, "uploads", "_data", "junk.tmp"), "junk")
	index := filepath.Join(b.stacks, "app", "html", "index.html")
	writeFile(t, index, "<h1>defaced</h1>")
	dbfile := filepath.Join(b.volumes, "app_dbdata", "_data", "PG_VERSION")
	writeFile(t, dbfile, "99")

	// Preview a volume restore: counts and the containers of the stack
	// that mounts... (uploads is standalone: nothing runs on it).
	var pv struct {
		Targets []struct {
			Kind        string `json:"kind"`
			Overwritten int64  `json:"overwritten"`
			Removed     int64  `json:"removed"`
		} `json:"targets"`
		CanRestore bool `json:"canRestore"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backups/"+ids["volume"]+"/restore-previews", map[string]any{"scope": "volume"}).json(t, &pv)
	if !pv.CanRestore || len(pv.Targets) != 1 || pv.Targets[0].Overwritten != 1 || pv.Targets[0].Removed != 1 {
		t.Fatalf("volume preview %+v", pv)
	}
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/backups/"+ids["volume"]+"/restores",
		map[string]any{"scope": "volume"})
	j := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["volume"]+"/restores",
		map[string]any{"scope": "volume", "confirm": true}))
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("volume restore: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if b, _ := os.ReadFile(photo); string(b) != "jpeg bytes" {
		t.Errorf("photo after restore %q", b)
	}
	if _, err := os.Stat(filepath.Join(b.volumes, "uploads", "_data", "junk.tmp")); err == nil {
		t.Error("a file created after the backup survived the volume restore")
	}

	// A stack restore brings the definition and workspace back and leaves
	// the database volume alone; the running services restart.
	var spv struct {
		AffectedContainers []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"affectedContainers"`
		Warnings []string `json:"warnings"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restore-previews", map[string]any{"scope": "stack"}).json(t, &spv)
	if len(spv.AffectedContainers) != 2 || len(spv.Warnings) == 0 {
		t.Errorf("stack preview %+v", spv)
	}
	j = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "stack", "confirm": true}))
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("stack restore: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if got, _ := os.ReadFile(index); string(got) != "<h1>backup me</h1>" {
		t.Errorf("index after restore %q", got)
	}
	if got, _ := os.ReadFile(dbfile); string(got) != "99" {
		t.Error("the stack restore overwrote the database volume")
	}
	for _, c := range []string{"app-db-1", "app-web-1"} {
		if ct, _ := b.fe.Container(c); !ct.Details.State.Running {
			t.Errorf("%s not restarted", c)
		}
	}
	// The database volume of the stack backup, then one file.
	j = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "volume", "volumes": []string{"app_dbdata"}, "confirm": true}))
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("stack volume restore: %s %s", got.State, got.ErrorMessage)
	}
	if got, _ := os.ReadFile(dbfile); string(got) != "16" {
		t.Errorf("database volume after restore %q", got)
	}
	writeFile(t, index, "<h1>again</h1>")
	var contents struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+ids["stack"]+"/contents?recursive=true", nil).json(t, &contents)
	var snapIndex string
	for _, en := range contents.Entries {
		if strings.HasSuffix(en.Path, "/html/index.html") {
			snapIndex = en.Path
		}
	}
	j = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "file", "path": snapIndex, "confirm": true}))
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("file restore: %s %s", got.State, got.ErrorMessage)
	}
	if got, _ := os.ReadFile(index); string(got) != "<h1>backup me</h1>" {
		t.Errorf("file after restore %q", got)
	}

	// Selected paths (#10): a file of the project directory and one of the
	// stack's volume, in place, in one job.
	var snapDB string
	for _, en := range contents.Entries {
		if strings.HasSuffix(en.Path, "/app_dbdata/_data/PG_VERSION") {
			snapDB = en.Path
		}
	}
	writeFile(t, index, "<h1>paths</h1>")
	writeFile(t, dbfile, "77")
	j = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "paths", "paths": []string{snapIndex, snapDB}, "confirm": true}))
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("paths restore: %s %s", got.State, got.ErrorMessage)
	}
	if got, _ := os.ReadFile(index); string(got) != "<h1>backup me</h1>" {
		t.Errorf("index after the paths restore %q", got)
	}
	if got, _ := os.ReadFile(dbfile); string(got) != "16" {
		t.Errorf("database file after the paths restore %q", got)
	}
	owner.fail(http.StatusConflict, "restore_refused", http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restore-previews",
		map[string]any{"scope": "paths", "paths": []string{"/nowhere/at/all"}})
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restore-previews",
		map[string]any{"scope": "stack", "redeploy": true})

	// A full restore puts the definition and the volumes back in one job;
	// until it ended, nothing starts the stack's containers; afterwards
	// the stack is deployed with the services that were running.
	deployed := make(chan struct{}, 1)
	b.m.Jobs().OnChange(func([]string) {
		select {
		case deployed <- struct{}{}:
		default:
		}
	})
	writeFile(t, index, "<h1>full</h1>")
	writeFile(t, dbfile, "42")
	var fpv struct {
		Targets []struct {
			Kind string `json:"kind"`
		} `json:"targets"`
		CanRestore bool `json:"canRestore"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restore-previews",
		map[string]any{"scope": "full", "redeploy": true}).json(t, &fpv)
	if !fpv.CanRestore || len(fpv.Targets) != 2 {
		t.Fatalf("full preview %+v", fpv)
	}
	j = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "full", "redeploy": true, "confirm": true}))
	owner.fail(http.StatusConflict, "restore_in_progress", http.MethodPost, "/api/v1/stacks/"+b.stackID+"/operations",
		map[string]any{"action": "start"})
	if got := b.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("full restore: %s %s", got.State, got.ErrorMessage)
	}
	if got, _ := os.ReadFile(index); string(got) != "<h1>backup me</h1>" {
		t.Errorf("index after the full restore %q", got)
	}
	if got, _ := os.ReadFile(dbfile); string(got) != "16" {
		t.Errorf("database file after the full restore %q", got)
	}
	ctx := testutil.Context(t)
	for {
		list, err := b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.StackDeploy}})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			var in protocol.StackJobInput
			if err := json.Unmarshal(list[0].Input, &in); err != nil || !slices.Equal(in.Services, []string{"db", "web"}) ||
				list[0].InitiatorUserID == "" {
				t.Errorf("redeploy %+v (%s) %v", in, list[0].Origin, err)
			}
			break
		}
		select {
		case <-deployed:
		case <-ctx.Done():
			t.Fatal("the full restore did not deploy the stack")
		}
	}

	// Manager state: the owner procedure, not a host restore.
	owner.fail(http.StatusConflict, "manager_restore_required", http.MethodPost, "/api/v1/backups/"+ids["manager_state"]+"/restore-previews",
		map[string]any{"scope": "volume"})
	// Restoring needs backup.restore on the stack too.
	rita, _ := b.opsUser(owner, "allow backup.read @all", "allow backup.restore @backup_repository:"+id)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/backups/"+ids["stack"]+"/restores",
		map[string]any{"scope": "stack", "confirm": true})
}

// TestBackupPartialSetRetryAndIdempotency: a set with a failed member is
// partial (never complete); retrying runs only the missing member and
// completes the same set; a repeated request with the same Idempotency-Key
// returns the same set and jobs.
func TestBackupPartialSetRetryAndIdempotency(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	repo := b.createS3Repo(owner, "Offsite")
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Vols", "scope": "environment", "environmentId": b.agent.env, "repositoryId": id,
		"excludeStacks": []string{b.stackID}}).json(t, &pol)
	// The volume is discoverable, but its data path is unavailable until the retry.
	b.fe.AddVolume("later", nil)
	type run struct {
		Set struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Members []struct {
				Item       string `json:"item"`
				State      string `json:"state"`
				ErrorClass string `json:"errorClass"`
			} `json:"members"`
		} `json:"set"`
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	var first, again run
	r := owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil, header("Idempotency-Key", "nightly-1"))
	r.json(t, &first)
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil, header("Idempotency-Key", "nightly-1")).json(t, &again)
	if again.Set.ID != first.Set.ID || len(again.Jobs) != 1 || again.Jobs[0].ID != first.Jobs[0].ID {
		t.Fatalf("repeated request: %+v vs %+v", again, first)
	}
	if got := b.runJob(first.Jobs[0].ID); got.State != domain.JobPartial {
		t.Fatalf("job state %s", got.State)
	}
	var set struct {
		Set struct {
			State string `json:"state"`
		} `json:"set"`
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?setId="+first.Set.ID, nil).json(t, &list)
	if len(list.Items) != 1 {
		t.Fatalf("backups of the partial set %+v", list.Items)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+list.Items[0].ID, nil).json(t, &set)
	if set.Set.State != "partial" {
		t.Fatalf("set state %s", set.Set.State)
	}
	// The missing volume appears; retry only it.
	later := filepath.Join(b.volumes, "later", "_data")
	writeFile(t, filepath.Join(later, "x"), "x")
	b.fe.SetVolumeMountpoint("later", filepath.ToSlash(later))
	var retry run
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", map[string]any{"retrySetId": first.Set.ID}).json(t, &retry)
	if retry.Set.ID != first.Set.ID || len(retry.Jobs) != 1 {
		t.Fatalf("retry %+v", retry)
	}
	if got := b.runJob(retry.Jobs[0].ID); got.State != domain.JobSucceeded {
		t.Fatalf("retry job %s %s", got.State, got.ErrorMessage)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+list.Items[0].ID, nil).json(t, &set)
	if set.Set.State != "complete" {
		t.Fatalf("after retry: %s", set.Set.State)
	}
	owner.fail(http.StatusConflict, "nothing_to_retry", http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", map[string]any{"retrySetId": first.Set.ID})
}

// TestScheduledBackupSurvivesCreatorRemoval (#10 Done-when 1): a policy
// and its snapshots belong to the instance; the scheduled run proceeds as
// the service identity after the user who created the policy is deleted,
// and the history stays visible to users with the scope.
func TestScheduledBackupSurvivesCreatorRemoval(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	repo := b.createS3Repo(owner, "Offsite")
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	rita, ritaID := b.opsUser(owner, "allow backup_policy.manage @all", "allow backup_policy.read @all", "allow backup.run @all",
		"allow backup.read @all")
	var pol struct {
		ID string `json:"id"`
	}
	rita.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Rita's", "scope": "environment", "environmentId": b.agent.env, "repositoryId": id,
		"excludeStacks": []string{b.stackID},
		"schedule":      map[string]any{"cron": "*/5 * * * *", "timeZone": "UTC", "enabled": true}}).json(t, &pol)
	ctx := testutil.Context(t)
	if err := b.m.Scheduler().Tick(ctx); err != nil {
		t.Fatal(err)
	}
	owner.stepUp()
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/users/"+ritaID, nil)

	b.clk.Advance(5 * time.Minute)
	if err := b.m.Scheduler().Tick(ctx); err != nil {
		t.Fatal(err)
	}
	js, err := b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"backup.run"}})
	if err != nil || len(js) != 1 || js[0].Origin != domain.OriginScheduled || js[0].PolicyID != pol.ID || js[0].InitiatorUserID != "" {
		t.Fatalf("scheduled jobs %+v %v", js, err)
	}
	if got := b.runJob(js[0].ID); got.State != domain.JobSucceeded {
		t.Fatalf("scheduled backup: %s %s", got.State, got.ErrorMessage)
	}
	// Another user with the scope sees the shared history.
	sam, _, _ := b.newUser(owner, "sam")
	owner.putRules("/api/v1/users/"+b.userID("sam")+"/permissions", "allow backup.read @all")
	var page struct {
		Items []struct {
			PolicyID string `json:"policyId"`
			View     string `json:"view"`
		} `json:"items"`
	}
	sam.must(http.StatusOK, http.MethodGet, "/api/v1/backups", nil).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].PolicyID != pol.ID || page.Items[0].View != "full" {
		t.Fatalf("sam's backups %+v", page.Items)
	}
}
