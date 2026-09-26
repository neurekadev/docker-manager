package app

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// openLive opens GET /api/v1/live/stream as c and returns its lines.
func (c *client) openLive(query string) <-chan string {
	t := c.e.t
	t.Helper()
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, c.base+"/api/v1/live/stream"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", publicHost)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("live stream: %d", resp.StatusCode)
	}
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines
}

// waitLine returns the lines up to the first one containing want.
func waitLine(t *testing.T, lines <-chan string, want string) []string {
	t.Helper()
	var seen []string
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q:\n%s", want, strings.Join(seen, "\n"))
			}
			seen = append(seen, l)
			if strings.Contains(l, want) {
				return seen
			}
		case <-timeout.C:
			t.Fatalf("no %q within 10 s:\n%s", want, strings.Join(seen, "\n"))
		}
	}
}

// TestLiveStreamThroughTheRealManager (#17, #23): the real manager's live
// stream filters Docker events per user (a metrics-only user of container
// web learns nothing about db), relays jobs to those who may read them, and
// a permission change ends the affected user's stream with
// permissions.changed and close (auth.Service.AccessChanged), so the
// client clears its cache and reconnects with the new permissions.
func TestLiveStreamThroughTheRealManager(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	ctx, cancel := context.WithCancel(testutil.Context(t))
	var wg sync.WaitGroup
	wg.Go(func() { e.m.Live().Run(ctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })

	metrics := owner.createGroup("Metrics")
	owner.putRules("/api/v1/groups/"+metrics.ID+"/permissions", "allow container.metrics.read @container:e1/web")
	mia, _, ms := e.newUser(owner, "mia")
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+ms.User.ID, map[string]string{"groupId": metrics.ID},
		header("If-Match", owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ms.User.ID, nil).header.Get("ETag")))

	miaLines := mia.openLive("")
	ownerLines := owner.openLive("?topics=containers,jobs")
	waitLine(t, miaLines, `"version":"docker-manager.live/v1"`)
	waitLine(t, ownerLines, `"version":"docker-manager.live/v1"`)

	bus := e.m.events
	bus.Publish(events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: "db", EnvironmentID: "e1",
		Attributes: map[string]string{"action": "die", "image": "postgres:16"}})
	bus.Publish(events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: "web", EnvironmentID: "e1",
		Attributes: map[string]string{"action": "die", "image": "nginx:1"}})
	seen := waitLine(t, miaLines, `"resourceId":"web"`)
	for _, l := range seen {
		if strings.Contains(l, `"db"`) || strings.Contains(l, "nginx") || strings.Contains(l, "postgres") {
			t.Fatalf("metrics-only user received %q", l)
		}
	}
	waitLine(t, ownerLines, `"resourceId":"db"`)

	// Jobs reach the owner through the engine's change listener.
	j := e.restartJob(ownerID, "e1", "web")
	e.m.liveJobs.Publish(ctx) // the batching window, without waiting for it
	waitLine(t, ownerLines, `"jobId":"`+j.ID+`"`)

	// Revoke: mia's group loses its rules; her stream ends.
	owner.putRules("/api/v1/groups/" + metrics.ID + "/permissions")
	end := waitLine(t, miaLines, `"reason":"permissions_changed"`)
	joined := strings.Join(end, "\n")
	if !strings.Contains(joined, "event: permissions.changed") || strings.Contains(joined, j.ID) {
		t.Fatalf("revocation:\n%s", joined)
	}
}
