package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux/muxtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeEngine serves volumes from a map (name -> volume) and lists
// containers.
type fakeEngine struct {
	volumes    map[string]engine.Volume
	containers []engine.Container
}

func (f *fakeEngine) InspectVolume(_ context.Context, name string) (engine.Volume, error) {
	v, ok := f.volumes[name]
	if !ok {
		return engine.Volume{}, &engine.Error{Code: engine.CodeNotFound, Op: "volume.inspect"}
	}
	return v, nil
}

func (f *fakeEngine) ListContainers(context.Context, engine.ContainerFilter) ([]engine.Container, error) {
	return f.containers, nil
}

// fixture is a service over a temporary "Docker volume directory" with a
// volume "data" (its _data is Root), a stacks volume with a project "app",
// and a sibling "outside" directory holding a secret that no operation may
// ever read or change.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	svc     *Service
	eng     *fakeEngine
	base    string // the volume directory (VolumesDir)
	root    string // volume "data" root
	stack   string // stack project directory
	outside string
	secret  string
	vol     protocol.FileScope
	stk     protocol.FileScope
	logs    *testutil.LogBuffer

	mu    sync.Mutex
	inval []protocol.FSInvalidationPayload
}

const secretContent = "OUTSIDE-SECRET-do-not-leak-7f3a9c"

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func slash(p string) string { return filepath.ToSlash(p) }

func volume(name, dir string) engine.Volume {
	return engine.Volume{Name: name, Driver: "local", Mountpoint: slash(dir)}
}

func newGzip(w io.Writer) *gzip.Writer { return gzip.NewWriter(w) }

func newFixture(t *testing.T, mods ...func(*Options)) *fixture {
	t.Helper()
	base := realPath(t, t.TempDir())
	f := &fixture{t: t, ctx: testutil.Context(t), base: base}
	f.root = filepath.Join(base, "data", "_data")
	stacks := filepath.Join(base, "docker-manager_stacks", "_data")
	f.stack = filepath.Join(stacks, "app")
	f.outside = filepath.Join(base, "outside")
	for _, d := range []string{f.root, f.stack, f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.secret = filepath.Join(f.outside, "secret.txt")
	if err := os.WriteFile(f.secret, []byte(secretContent), 0o600); err != nil {
		t.Fatal(err)
	}
	f.eng = &fakeEngine{volumes: map[string]engine.Volume{
		"data":                  {Name: "data", Driver: "local", Mountpoint: slash(f.root)},
		"docker-manager_stacks": {Name: "docker-manager_stacks", Driver: "local", Mountpoint: slash(stacks)},
		"nfs":                   {Name: "nfs", Driver: "local", Mountpoint: slash(filepath.Join(base, "nfs", "_data")), Options: map[string]string{"type": "nfs", "o": "addr=10.0.0.1"}},
		"plugin":                {Name: "plugin", Driver: "rexray/ebs", Mountpoint: "/var/lib/rexray/volumes/plugin"},
		"agentstate":            {Name: "agentstate", Driver: "local", Mountpoint: slash(filepath.Join(base, "agentstate", "_data"))},
	}, containers: []engine.Container{{ID: "a", Labels: map[string]string{RoleLabel: "agent"},
		Mounts: []engine.Mount{{Type: "volume", Name: "agentstate"}}}}}
	st := &storage.Result{DockerRootDir: slash(filepath.Dir(base)), VolumesDir: slash(base), StacksDir: slash(stacks),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: slash(stacks), OK: true}, {Kind: storage.KindVolumes, Path: slash(base), OK: true}}}
	logger, logs := testutil.CaptureLogger()
	f.logs = logs
	o := Options{
		Engine: func() Engine { return f.eng }, Storage: func() *storage.Result { return st },
		Clock: testutil.FakeClock(), Logger: logger,
		Invalidate: func(p protocol.FSInvalidationPayload) {
			f.mu.Lock()
			f.inval = append(f.inval, p)
			f.mu.Unlock()
		},
	}
	for _, m := range mods {
		m(&o)
	}
	f.svc = New(o)
	f.vol = protocol.FileScope{Kind: protocol.ScopeVolume, ID: "data"}
	f.stk = protocol.FileScope{Kind: protocol.ScopeStack, ID: "0190a6e0-0000-7000-8000-00000000aaaa", Dir: slash(f.stack)}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("agent logs:\n%s", logs.String())
		}
	})
	return f
}

// write creates a file inside the volume root.
func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// resetRoots empties the volume and stack roots (never following links:
// os.RemoveAll unlinks symlinks, not their targets).
func (f *fixture) resetRoots() {
	f.t.Helper()
	for _, dir := range []string{f.root, f.stack} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

// symlink creates a symlink inside the volume root, skipping the test
// where the platform cannot create symlinks.
func (f *fixture) symlink(target, rel string) {
	f.t.Helper()
	if err := os.Symlink(filepath.FromSlash(target), filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
		if runtime.GOOS == "windows" {
			f.t.Skipf("symlinks unavailable on this Windows host: %v", err)
		}
		f.t.Fatal(err)
	}
}

// snapshot lists everything outside the volume and stack roots (path,
// size, mode, content hash) so a test can prove nothing there changed.
func (f *fixture) snapshot() string {
	f.t.Helper()
	var lines []string
	err := filepath.WalkDir(f.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == f.root || p == f.stack {
			return filepath.SkipDir
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := slash(strings.TrimPrefix(p, f.base)) + " " + fi.Mode().String()
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += " " + string(b)
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	// Parents of the base (the temp dir) must not gain files either.
	parent, _ := os.ReadDir(filepath.Dir(f.base))
	for _, e := range parent {
		lines = append(lines, "../"+e.Name())
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// code returns the protocol code of an error (or "" for nil).
func code(err error) string {
	if err == nil {
		return ""
	}
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	var ce *streammux.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return "untyped: " + err.Error()
}

// memJournal is an in-memory jobexec journal.
type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

// runJob runs a file job executor like the agent job runner does.
func (f *fixture) runJob(kind domain.JobKind, in protocol.FilesJobInput) protocol.ResultPayload {
	f.t.Helper()
	var exec jobexec.Executor
	for _, e := range f.svc.Executors() {
		if e.Kind == kind {
			exec = e
		}
	}
	if err := exec.Validate(domain.ExecutorAgent); err != nil {
		f.t.Fatal(err)
	}
	b, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal(err)
	}
	st := &jobexec.State{JobID: "job-1", Attempt: 1, FencingToken: 1, Kind: kind, Input: b}
	res, err := jobexec.Run(f.ctx, exec, st, jobexec.Options{Journal: memJournal{}})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// pipe connects a manager-side stream opener to the service's handlers.
func (f *fixture) pipe() *muxtest.Pipe {
	hs := map[string]muxtest.Handler{}
	for k, h := range f.svc.Streams() {
		hs[k] = muxtest.Handler(h)
	}
	return muxtest.New(f.t, hs)
}

// download runs a files.download stream and returns the bytes.
func (f *fixture) download(in protocol.FilesDownloadInput) ([]byte, error) {
	s, err := f.pipe().Open(f.ctx, protocol.StreamFilesDownload, in, streammux.OpenOptions{})
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(s)
	_ = s.CloseWrite()
	return b, err
}

// upload runs a files.upload stream.
func (f *fixture) upload(in protocol.FilesUploadInput, body []byte) (protocol.FilesUploadResult, error) {
	var out protocol.FilesUploadResult
	s, err := f.pipe().Open(f.ctx, protocol.StreamFilesUpload, in, streammux.OpenOptions{MaxBytes: in.Size + 1})
	if err != nil {
		return out, err
	}
	_, werr := io.Copy(s, bytes.NewReader(body))
	if werr != nil && !errors.Is(werr, streammux.ErrPeerClosed) {
		<-s.Done()
		return out, s.Err()
	}
	_ = s.CloseWrite()
	res, err := s.Result(f.ctx)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(res, &out)
	return out, err
}

func (f *fixture) readFile(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(rel)))
	return err == nil
}
