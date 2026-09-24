package engine

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/container"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

func TestPullImageSendsPerOperationAuth(t *testing.T) {
	canaries := canary.New()
	password := canaries.New(canary.RegistryCredential, "registry password")
	logger := canaries.CaptureLogger(t)

	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodPost, "/images/create", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("fromImage") {
		case "registry.example:5000/app":
			enginetest.Stream(w,
				map[string]any{"status": "Pulling from app", "id": "v1"},
				map[string]any{"status": "Downloading", "id": "abc", "progressDetail": map[string]int{"current": 5, "total": 10}},
				map[string]any{"status": "Digest: sha256:1111111111111111111111111111111111111111111111111111111111111111"},
				map[string]any{"status": "Status: Downloaded newer image for registry.example:5000/app:v1"})
		case "registry.example:5000/limited":
			enginetest.Stream(w, map[string]any{"errorDetail": map[string]string{"message": "toomanyrequests: rate limit exceeded"}, "error": "toomanyrequests"})
		default:
			enginetest.Stream(w, map[string]any{"errorDetail": map[string]string{"message": "unauthorized: authentication required"}, "error": "unauthorized"})
		}
	})
	fake.Handle(http.MethodGet, "/images/.*/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "sha256:image", "RepoTags": []string{"registry.example:5000/app:v1"}})
	})
	c, err := Connect(testutil.Context(t), Options{Host: fake.Host, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := testutil.Context(t)
	auth := &RegistryAuth{ServerAddress: "registry.example:5000", Username: "dockyard", Password: logging.Secret(password)}

	var progress []Progress
	res, err := c.PullImage(ctx, "registry.example:5000/app:v1", PullOptions{Auth: auth, Progress: func(p Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Digest, "sha256:1111") || res.ImageID != "sha256:image" {
		t.Errorf("result %+v", res)
	}
	if len(progress) != 4 || progress[1].Current != 5 || progress[1].Total != 10 {
		t.Errorf("progress %+v", progress)
	}
	reqs := fake.Find(http.MethodPost, "/images/create")
	got, err := authconfig.Decode(reqs[0].Header.Get("X-Registry-Auth"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "dockyard" || got.Password != password || got.ServerAddress != "registry.example:5000" {
		t.Errorf("X-Registry-Auth = %+v", got)
	}

	_, err = c.PullImage(ctx, "registry.example:5000/limited:v1", PullOptions{Auth: auth})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("rate-limited pull: %v (%s)", err, CodeOf(err))
	}
	_, err = c.PullImage(ctx, "registry.example:5000/private:v1", PullOptions{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("anonymous pull: %v (%s)", err, CodeOf(err))
	}
	if len(fake.Find(http.MethodPost, "/images/create")[2].Header.Get("X-Registry-Auth")) > 0 &&
		fake.Find(http.MethodPost, "/images/create")[2].Header.Get("X-Registry-Auth") != "e30=" {
		// The SDK sends "{}" or nothing for anonymous pulls; never another credential.
		t.Errorf("anonymous pull sent credentials")
	}
	canaries.AssertClean(t, "pull errors", []error{err})
}

func TestLogsDemultiplexes(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(enginetest.Multiplexed(1, []byte("2026-09-24T12:00:00.000000001Z hello\n")))
	raw.Write(enginetest.Multiplexed(2, []byte("2026-09-24T12:00:01Z oops\n2026-09-24T12:00:02Z partial")))
	raw.Write(enginetest.Multiplexed(1, []byte("2026-09-24T12:00:03Z bye\n")))

	var got []LogEntry
	if err := demuxLogs(&raw, false, true, func(e LogEntry) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		stream LogStream
		data   string
		sec    int
	}{{Stdout, "hello\n", 0}, {Stderr, "oops\n", 1}, {Stdout, "bye\n", 3}, {Stderr, "partial", 2}}
	if len(got) != len(want) {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Stream != w.stream || string(got[i].Data) != w.data || got[i].Time.Second() != w.sec {
			t.Errorf("entry %d = %s %q %v, want %s %q :%02d", i, got[i].Stream, got[i].Data, got[i].Time, w.stream, w.data, w.sec)
		}
	}

	// TTY streams are raw; a callback error stops the stream and is returned.
	stop := errors.New("enough")
	n := 0
	err := demuxLogs(strings.NewReader("a\nb\nc\n"), true, false, func(LogEntry) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	var se *stopError
	if !errors.As(err, &se) || !errors.Is(se.err, stop) || n != 2 {
		t.Fatalf("err %v after %d entries", err, n)
	}

	// Long lines are split into bounded chunks.
	var chunks int
	_ = demuxLogs(strings.NewReader(strings.Repeat("x", 2*maxLogChunk+5)), true, false, func(e LogEntry) error {
		if len(e.Data) > maxLogChunk {
			t.Errorf("chunk of %d bytes", len(e.Data))
		}
		chunks++
		return nil
	})
	if chunks != 3 {
		t.Errorf("%d chunks, want 3", chunks)
	}
}

func TestLogsThroughEngine(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/containers/app/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "abc123", "Name": "/app", "Config": map[string]any{"Tty": false}})
	})
	fake.Handle(http.MethodGet, "/containers/abc123/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(enginetest.Multiplexed(1, []byte("line 1\nline 2\n")))
	})
	c := connect(t, fake)
	var lines []string
	err := c.Logs(testutil.Context(t), "app", LogOptions{Tail: 10}, func(e LogEntry) error {
		lines = append(lines, string(e.Data))
		return nil
	})
	if err != nil || strings.Join(lines, "") != "line 1\nline 2\n" {
		t.Fatalf("lines %q err %v", lines, err)
	}
	q := fake.Find(http.MethodGet, "/containers/abc123/logs")[0].Query
	if q.Get("tail") != "10" || q.Get("stdout") != "1" || q.Get("stderr") != "1" || q.Get("follow") != "" {
		t.Errorf("query %v", q)
	}
}

func TestStatsComputation(t *testing.T) {
	s := &container.StatsResponse{
		Read:        time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		PreCPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 1_000_000_000}, SystemUsage: 100_000_000_000},
		CPUStats:    container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 1_500_000_000}, SystemUsage: 102_000_000_000, OnlineCPUs: 4},
		MemoryStats: container.MemoryStats{Usage: 300 << 20, Limit: 1 << 30, Stats: map[string]uint64{"inactive_file": 100 << 20}},
		Networks:    map[string]container.NetworkStats{"eth0": {RxBytes: 10, TxBytes: 20}, "eth1": {RxBytes: 1, TxBytes: 2}},
		BlkioStats: container.BlkioStats{IoServiceBytesRecursive: []container.BlkioStatEntry{
			{Op: "read", Value: 4096}, {Op: "Write", Value: 8192}, {Op: "total", Value: 1},
		}},
		PidsStats: container.PidsStats{Current: 7},
	}
	got := statsFrom(s)
	if got.CPUPercent != 100 || got.OnlineCPUs != 4 {
		t.Errorf("cpu %.2f%% on %d CPUs, want 100%% on 4", got.CPUPercent, got.OnlineCPUs)
	}
	if got.MemoryUsage != 200<<20 || got.MemoryLimit != 1<<30 || got.MemoryPercent < 19.5 || got.MemoryPercent > 19.6 {
		t.Errorf("memory %d/%d (%.2f%%)", got.MemoryUsage, got.MemoryLimit, got.MemoryPercent)
	}
	if got.NetworkRx != 11 || got.NetworkTx != 22 || got.BlockRead != 4096 || got.BlockWrite != 8192 || got.PIDs != 7 {
		t.Errorf("io %+v", got)
	}
	// First sample of a stream: no previous CPU sample, no CPU percentage.
	s.PreCPUStats = container.CPUStats{}
	if statsFrom(s).CPUPercent != 0 {
		t.Error("CPU percent without a previous sample")
	}
}

func TestEventsStream(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/events", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Stream(w,
			map[string]any{"Type": "container", "Action": "start", "Actor": map[string]any{"ID": "c1", "Attributes": map[string]string{"name": "web"}}, "scope": "local", "timeNano": int64(1790000000000000001)},
			map[string]any{"Type": "container", "Action": "die", "Actor": map[string]any{"ID": "c1"}, "time": int64(1790000001)},
			map[string]any{"Type": "volume", "Action": "create", "Actor": map[string]any{"ID": "v1"}, "time": int64(1790000002)})
	})
	c := connect(t, fake)
	var got []Event
	err := c.Events(testutil.Context(t), EventFilter{Types: []string{"container", "volume"}, Labels: []string{"com.docker.compose.project=app"}, Since: time.Unix(1790000000, 5)}, func(e Event) error {
		got = append(got, e)
		if len(got) == 2 {
			return io.ErrShortWrite // stop early; returned as is
		}
		return nil
	})
	if !errors.Is(err, io.ErrShortWrite) || len(got) != 2 {
		t.Fatalf("err %v, %d events", err, len(got))
	}
	if got[0].Action != "start" || got[0].ActorID != "c1" || got[0].Attributes["name"] != "web" || got[0].Time.UnixNano() != 1790000000000000001 {
		t.Errorf("event 0 %+v", got[0])
	}
	if got[1].Time.Unix() != 1790000001 {
		t.Errorf("event 1 time %v", got[1].Time)
	}
	var filters map[string]map[string]bool
	_ = json.Unmarshal([]byte(fake.Find(http.MethodGet, "/events")[0].Query.Get("filters")), &filters)
	if !filters["type"]["container"] || !filters["label"]["com.docker.compose.project=app"] {
		t.Errorf("filters %v", filters)
	}
	// The Engine reads the fraction as a decimal: nanoseconds are zero-padded.
	if since := fake.Find(http.MethodGet, "/events")[0].Query.Get("since"); since != "1790000000.000000005" {
		t.Errorf("since = %q", since)
	}

	// A stream the Engine ends cleanly returns nil.
	got = nil
	if err := c.Events(testutil.Context(t), EventFilter{}, func(e Event) error { got = append(got, e); return nil }); err != nil || len(got) != 3 {
		t.Fatalf("clean end: %v, %d events", err, len(got))
	}
}

func traceMessage(t *testing.T, st *controlapi.StatusResponse) map[string]any {
	t.Helper()
	dt, err := st.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(dt) // BuildKit sends the protobuf as a JSON byte string
	return map[string]any{"id": "moby.buildkit.trace", "aux": json.RawMessage(raw)}
}

func TestBuildUsesBuildKitAndReportsProgress(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	var sawFailure bool
	fake.Handle(http.MethodPost, "/build", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "fail:1" {
			sawFailure = true
			enginetest.Stream(w, map[string]any{"errorDetail": map[string]string{"message": "failed to solve: process \"/bin/false\" did not complete successfully"}})
			return
		}
		now := timestamppb.Now()
		enginetest.Stream(w,
			traceMessage(t, &controlapi.StatusResponse{Vertexes: []*controlapi.Vertex{{Digest: "sha256:v1", Name: "[1/2] FROM scratch", Started: now}}}),
			traceMessage(t, &controlapi.StatusResponse{
				Vertexes: []*controlapi.Vertex{{Digest: "sha256:v1", Name: "[1/2] FROM scratch", Started: now, Completed: now, Cached: true}},
				Logs:     []*controlapi.VertexLog{{Vertex: "sha256:v1", Msg: []byte("log line\n")}},
			}),
			map[string]any{"id": "moby.image.id", "aux": map[string]string{"ID": "sha256:built"}})
	})
	c := connect(t, fake)
	ctx := testutil.Context(t)

	var events []BuildEvent
	res, err := c.Build(ctx, BuildSpec{
		RemoteContext: "http://gitserver/public.git#main:sub", Tags: []string{"app:1"},
		BuildArgs: map[string]string{"A": "1"}, Target: "final", Pull: true,
		Progress: func(e BuildEvent) { events = append(events, e) },
	})
	if err != nil || res.ImageID != "sha256:built" {
		t.Fatalf("build = %+v, %v", res, err)
	}
	q := fake.Find(http.MethodPost, "/build")[0].Query
	if q.Get("version") != "2" || q.Get("remote") != "http://gitserver/public.git#main:sub" || q.Get("target") != "final" ||
		q.Get("pull") != "1" || !strings.Contains(q.Get("buildargs"), `"A":"1"`) {
		t.Errorf("build query %v", q)
	}
	statuses := []string{}
	for _, e := range events {
		statuses = append(statuses, e.Status)
	}
	if strings.Join(statuses, ",") != "started,cached,log" || string(events[2].Log) != "log line\n" || events[2].Step != "[1/2] FROM scratch" {
		t.Errorf("events %+v", events)
	}

	_, err = c.Build(ctx, BuildSpec{RemoteContext: "http://gitserver/public.git", Tags: []string{"fail:1"}})
	if CodeOf(err) != CodeBuildFailed || !sawFailure || !strings.Contains(err.Error(), "did not complete successfully") {
		t.Errorf("failed build: %v", err)
	}

	for _, bad := range []BuildSpec{
		{},
		{ContextDir: t.TempDir(), RemoteContext: "https://x/y.git"},
		{RemoteContext: "git@github.com:x/y.git"},
		{RemoteContext: "ssh://git@host/x.git"},
		{RemoteContext: "https://user:token@host/x.git"},
		{RemoteContext: "https://host/x.git", DockerfileInline: "FROM scratch"},
		{ContextDir: t.TempDir(), Dockerfile: "../Dockerfile"},
	} {
		if _, err := c.Build(ctx, bad); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("Build(%+v) = %v, want invalid_argument", bad, err)
		}
	}
}

func tarNames(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Typeflag == tar.TypeSymlink {
			out[h.Name] = "-> " + h.Linkname
		} else {
			out[h.Name] = string(b)
		}
	}
}

func TestContextArchive(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM scratch\n")
	write(".dockerignore", "secret.env\nDockerfile\n.dockerignore\nlogs/\n")
	write("secret.env", "TOKEN=x")
	write("app/main.txt", "main")
	write("logs/a.log", "log")

	rc, df, err := contextArchive(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, rc)
	_ = rc.Close()
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if df != "Dockerfile" || names["Dockerfile"] != "FROM scratch\n" || names["app/main.txt"] != "main" {
		t.Errorf("archive %v (dockerfile %s)", keys, df)
	}
	if _, ok := names["secret.env"]; ok {
		t.Error(".dockerignore'd secret.env was sent")
	}
	if slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, "logs") }) {
		t.Errorf("ignored logs/ sent: %v", keys)
	}
	if _, ok := names[".dockerignore"]; !ok {
		t.Error(".dockerignore itself must stay in the context")
	}

	rc, df, err = contextArchive(dir, "", "FROM scratch\nCOPY app /app\n")
	if err != nil {
		t.Fatal(err)
	}
	names = tarNames(t, rc)
	_ = rc.Close()
	if df != inlineDockerfileName || names[inlineDockerfileName] != "FROM scratch\nCOPY app /app\n" {
		t.Errorf("inline Dockerfile: %s %q", df, names[inlineDockerfileName])
	}

	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/passwd", filepath.Join(dir, "app", "link")); err != nil {
			t.Fatal(err)
		}
		rc, _, _ = contextArchive(dir, "", "")
		names = tarNames(t, rc)
		_ = rc.Close()
		if names["app/link"] != "-> /etc/passwd" {
			t.Errorf("symlink archived as %q, want the link itself", names["app/link"])
		}
	}
	if _, _, err := contextArchive(filepath.Join(dir, "missing"), "", ""); err == nil {
		t.Error("missing context accepted")
	}
}
