package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestStackEventStream: the stack stream starts with the stack, relays this
// stack's events (and engine events of its containers) and nothing else,
// and ends after stack.removed.
func TestStackEventStream(t *testing.T) {
	svc := newFakeStacks()
	pol := authztest.New().Owner("own")
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if ref.Type == catalog.TypeStack {
			if st, ok := svc.stacks[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: st.EnvironmentID}
			}
		}
		return policy.Location{}
	})
	bus := events.New(testutil.FakeClock())
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: svc, Authorizer: pol, Clock: testutil.FakeClock(), Events: bus, Idempotency: &memIdempotency{}})
	srv := httptest.NewServer(authztest.Authenticate(withTestContext(t, mux, "")))
	defer srv.Close()
	ctx := testutil.Context(t)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stacks/st-1/events/stream", nil)
	req.Header.Set(authztest.UserHeader, "own")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d %v", resp.StatusCode, resp.Header)
	}
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	var got []string
	next := func() (string, bool) {
		select {
		case l, ok := <-lines:
			return l, ok
		case <-time.After(10 * time.Second):
			t.Fatal("stream stalled")
		}
		return "", false
	}
	for {
		l, ok := next()
		if !ok {
			t.Fatal("stream ended before the snapshot")
		}
		if l == "event: stack" {
			break
		}
	}
	bus.Publish(events.Event{Type: events.StackUpdated, ResourceType: events.ResourceStack, ResourceID: "st-other", EnvironmentID: "env-1"})
	bus.Publish(events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: "other-web-1", EnvironmentID: "env-1",
		Attributes: map[string]string{composeProjectAttribute: "other"}})
	bus.Publish(events.Event{Type: events.StackUpdated, ResourceType: events.ResourceStack, ResourceID: "st-1", EnvironmentID: "env-1",
		Attributes: map[string]string{"jobId": "job-9"}})
	bus.Publish(events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: "shop-web-1", EnvironmentID: "env-1",
		Attributes: map[string]string{composeProjectAttribute: "shop", "action": "die"}})
	bus.Publish(events.Event{Type: events.StackRemoved, ResourceType: events.ResourceStack, ResourceID: "st-1", EnvironmentID: "env-1"})
	for {
		l, ok := next()
		if !ok {
			break
		}
		if strings.HasPrefix(l, "event: ") || strings.HasPrefix(l, "data: ") {
			got = append(got, l)
		}
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"event: stack.updated", `"jobId":"job-9"`, "event: engine", `"resourceId":"shop-web-1"`, "event: stack.removed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("stream lacks %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"st-other", "other-web-1"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("stream leaked %q:\n%s", unwanted, joined)
		}
	}
}
