package agents

import (
	"net/http"
	"testing"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
)

// TestMovedManagerRefusesAgents (manager move): the welcome carries the
// instance's generation; while the manager drains, agents stay connected;
// once its state was handed to a new manager every open session closes
// with 1012 (service restart) and every enrollment or session upgrade,
// valid credential or not, answers 503 with Retry-After: 60, never 401
// (agents would drop their credential).
func TestMovedManagerRefusesAgents(t *testing.T) {
	lock := movelock.New()
	f := newFixture(t, fixtureOptions{moveLock: lock, generation: 3})
	r, hello := f.enrolledRaw("ENG-A")
	enroll := f.createEnrollment(domain.EnrollmentSpec{}).Token
	s := f.raw(r.Credential)
	if w := s.handshake(hello); w.Generation != 3 {
		t.Fatalf("welcome generation = %d, want 3", w.Generation)
	}
	f.waitOnline(r.EnvironmentID)

	lock.Set(movelock.ReadOnly)
	if !f.svc.Hub().Online(r.EnvironmentID) {
		t.Fatal("a draining manager disconnected its agent")
	}

	lock.Set(movelock.AgentsRefused)
	if got := s.closeCode(); got != websocket.StatusServiceRestart {
		t.Fatalf("open session closed with %d, want 1012", got)
	}
	forged, _ := authsep.MintAgentCredential("0190a6e0-0000-7000-8000-00000000beef")
	for name, cred := range map[string]string{"valid credential": r.Credential, "unknown credential": forged.Token} {
		c, resp, _ := f.dialRaw(cred)
		if c != nil {
			_ = c.CloseNow()
		}
		if resp == nil {
			t.Fatalf("%s: no response", name)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "60" {
			t.Fatalf("%s: upgrade %d (Retry-After %q), want 503 with Retry-After 60", name, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
	a := f.newAgent("ENG-B", "host-b")
	if status, body := f.enrollHTTP(enroll, a.enrollRequest()); status != http.StatusServiceUnavailable {
		t.Fatalf("enrollment while moved: %d %s, want 503", status, body)
	}

	// A cancelled move (resume here) accepts the agent again.
	lock.Set(movelock.Open)
	if got := f.upgradeStatus(r.Credential); got != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade after the lock opened: %d", got)
	}
}
