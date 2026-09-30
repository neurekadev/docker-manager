package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestRealFilesystemLatency (#23, #25 Q5) measures, on this machine's real
// filesystem with the real kernel notifier (fsnotify: inotify on Linux,
// ReadDirectoryChangesW on Windows), how long a change made outside
// Docker Manager takes to become an fs_invalidation: file create, edit, rename
// and delete, in the root and in a nested directory created during the
// test. The target is 2 s at p95; the measured distribution is logged and
// recorded in docs/internal/support-matrix.md ("File watching").
func TestRealFilesystemLatency(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []protocol.FSInvalidationPayload
	arrived := make(chan struct{}, 1)
	w := New(Options{
		Clock: clock.Real(), Logger: testutil.Logger(t),
		// A volume scope carries no directory: it resolves to the test root.
		Resolve: func(context.Context, protocol.FileScope) (string, error) { return root, nil },
		Invalidate: func(p protocol.FSInvalidationPayload) {
			mu.Lock()
			got = append(got, p)
			mu.Unlock()
			select {
			case arrived <- struct{}{}:
			default:
			}
		},
	})
	t.Cleanup(func() { _ = w.Close() })
	sc := protocol.FileScope{Kind: protocol.ScopeVolume, ID: "data"}
	ctx, cancel := context.WithCancel(testutil.Context(t))
	out := w.SetScopes(ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{sc}})
	if out.Scopes[0].Mode != protocol.WatchInotify {
		t.Fatalf("kernel notifications unavailable on %s: %+v", runtime.GOOS, out)
	}
	w.reconcile(ctx, protocol.ScopeRef{Kind: sc.Kind, ID: sc.ID}) // registration, before the loop starts
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// waitFor returns when an invalidation lists want (or overflows).
	waitFor := func(want string) time.Duration {
		t.Helper()
		start := time.Now()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			mu.Lock()
			for _, p := range got {
				if p.Overflow || slices.Contains(p.Paths, want) {
					got = nil
					mu.Unlock()
					return time.Since(start)
				}
			}
			mu.Unlock()
			select {
			case <-arrived:
			case <-deadline.C:
				t.Fatalf("no invalidation for %s within 10 s", want)
			}
		}
	}
	measure := func(want string, op func() error) time.Duration {
		t.Helper()
		mu.Lock()
		got = nil
		mu.Unlock()
		start := time.Now()
		if err := op(); err != nil {
			t.Fatal(err)
		}
		waitFor(want)
		return time.Since(start)
	}
	var lat []time.Duration
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	lat = append(lat, waitFor("sub"))
	for i := range 6 {
		for _, dir := range []string{"", "sub"} {
			rel := func(name string) string {
				if dir == "" {
					return name
				}
				return dir + "/" + name
			}
			name := fmt.Sprintf("f%d.txt", i)
			abs := func(n string) string { return filepath.Join(root, filepath.FromSlash(rel(n))) }
			lat = append(lat,
				measure(rel(name), func() error { return os.WriteFile(abs(name), []byte("created"), 0o644) }),
				measure(rel(name), func() error {
					f, err := os.OpenFile(abs(name), os.O_WRONLY|os.O_APPEND, 0)
					if err != nil {
						return err
					}
					_, err = f.WriteString(" and edited")
					return errorsJoin(err, f.Close())
				}),
				measure(rel("r"+name), func() error { return os.Rename(abs(name), abs("r"+name)) }),
				measure(rel("r"+name), func() error { return os.Remove(abs("r" + name)) }),
			)
		}
	}
	slices.Sort(lat)
	p := func(q float64) time.Duration { return lat[min(len(lat)-1, int(q*float64(len(lat))))] }
	t.Logf("external change latency on %s/%s (fsnotify, debounce %v): n=%d p50=%v p95=%v max=%v",
		runtime.GOOS, runtime.GOARCH, DefaultDebounce, len(lat), p(0.5), p(0.95), lat[len(lat)-1])
	if p(0.95) > 2*time.Second {
		t.Errorf("p95 latency %v exceeds the 2 s target (#25 Q5)", p(0.95))
	}
}

func errorsJoin(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
