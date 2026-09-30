package agents

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/config"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/agent/runtime"
	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// runAgentRuntime runs the agent binary's runtime (Engine adapter against a
// fake Engine API, enrollment, credential storage, session, job runner)
// against the fixture's manager until the returned stop is called. The
// agent uses the wall clock with a short token poll (the handover path
// polls the state directory); assertions wait on events, never on time.
func (f *fixture) runAgentRuntime(stateDir, token, dockerHost string) (stop func() error) {
	f.t.Helper()
	u, _ := url.Parse(f.srv.URL)
	cfg := config.Config{ManagerURL: u, PlainHTTP: true, EnrollmentToken: logging.Secret(token), StateDir: stateDir,
		DockerHost: dockerHost, EnvironmentName: "from-agent", StacksVolume: config.DefaultStacksVolume}
	logger, logs := testutil.CaptureLogger()
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() {
		done <- runtime.Run(ctx, runtime.Options{Config: cfg, Logger: logger, Clock: clock.Real(), Geteuid: func() int { return 0 },
			TokenPoll: 20 * time.Millisecond})
	}()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		err := <-done
		if strings.Contains(logs.String(), token[len(token)-20:]) {
			f.t.Errorf("agent logged its enrollment token")
		}
		if f.t.Failed() {
			f.t.Logf("agent logs:\n%s", logs.String())
		}
		return err
	}
	f.t.Cleanup(func() { _ = stop() })
	return stop
}

// TestAgentRuntimeEnrollsReconnectsAndReenrolls drives the agent's control
// loop end to end: enrollment from DOCKER_AGENT_ENROLLMENT_TOKEN, the
// credential persisted 0600 before the session, online; a restart reuses
// the credential and never re-sends the used token; a removed agent drops
// its credential and waits; a token handed over through the state
// directory (`docker-agent enroll`) re-attaches the environment.
func TestAgentRuntimeEnrollsReconnectsAndReenrolls(t *testing.T) {
	f := newFixture(t, fixtureOptions{managerVersion: buildinfo.Get().Version})
	eng := enginetest.Start(t, enginetest.Options{APIVersion: "1.51"})
	stateDir := filepath.Join(t.TempDir(), "state")
	c := f.createEnrollment(domain.EnrollmentSpec{EnvironmentName: "Edge"})

	stop := f.runAgentRuntime(stateDir, c.Token, eng.Host)
	enrolled := f.waitEvent(events.AgentEnrolled, "")
	f.waitOnline(enrolled.EnvironmentID)
	env, _ := f.svc.GetEnvironment(f.ctx, enrolled.EnvironmentID)
	if env.Name != "Edge" || env.EngineID != "FAKE:ENGINE:ID" {
		t.Fatalf("environment %+v", env)
	}
	st, _ := state.Open(stateDir)
	cred, err := st.Credential()
	if err != nil || cred == nil || cred.AgentID != enrolled.ResourceID {
		t.Fatalf("stored credential %+v %v", cred, err)
	}
	if fi, err := os.Stat(filepath.Join(stateDir, state.CredentialFile)); err != nil || (fi.Mode().Perm()&0o077 != 0 && !isWindows()) {
		t.Fatalf("credential file %v %v", fi, err)
	}
	waitHealth(t, f, stateDir, runtime.StatusOnline)
	sys, err := f.svc.EnvironmentSystem(f.ctx, env.ID)
	if err != nil || sys.Agent == nil || sys.Agent.Capabilities == "" || !strings.Contains(sys.Agent.Capabilities, `"plainHttp":true`) {
		t.Fatalf("capabilities not reported: %+v %v", sys.Agent, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	f.waitEvent(events.EnvironmentOffline, env.ID)

	// Restart with the same (now used) token still configured.
	stop = f.runAgentRuntime(stateDir, c.Token, eng.Host)
	f.waitOnline(env.ID)
	if list, _ := f.svc.ListAgents(f.ctx, domain.AgentFilter{}); len(list) != 1 {
		t.Fatalf("restart created agents: %d", len(list))
	}

	// Remove the agent: the runtime drops the revoked credential and waits.
	if _, err := f.svc.RemoveAgent(f.ctx, cred.AgentID, mustAgent(t, f, cred.AgentID).Revision); err != nil {
		t.Fatal(err)
	}
	waitHealth(t, f, stateDir, runtime.StatusUnauthorized)
	waitCond(t, f, func() bool { c, _ := st.Credential(); return c == nil })

	// Hand over a reattach token like `docker-agent enroll` does.
	re := f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReattach, TargetID: env.ID})
	res, err := runtime.HandOver(f.ctx, stateDir, re.Token, time.Minute, 20*time.Millisecond, clock.Real())
	if err != nil || res.Enrollment == nil || res.Enrollment.Status != state.EnrollEnrolled || !res.Online || res.Enrollment.EnvironmentID != env.ID {
		t.Fatalf("handover: %+v %v", res, err)
	}
	if tok, _ := st.PendingToken(); tok != "" {
		t.Fatal("handed-over token left in the state directory")
	}
	// A used token handed over again is refused locally.
	res, err = runtime.HandOver(f.ctx, stateDir, re.Token, time.Minute, 20*time.Millisecond, clock.Real())
	if err != nil || res.Enrollment == nil || res.Enrollment.Status != state.EnrollFailed || res.Enrollment.Code != "token_used" {
		t.Fatalf("reused handover: %+v %v", res.Enrollment, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// TestAgentRuntimeRefusedTokenIsNotRetried: an invalid token is reported
// and never retried.
func TestAgentRuntimeRefusedTokenIsNotRetried(t *testing.T) {
	f := newFixture(t, fixtureOptions{managerVersion: buildinfo.Get().Version})
	eng := enginetest.Start(t, enginetest.Options{})
	stateDir := t.TempDir()
	c := f.createEnrollment(domain.EnrollmentSpec{})
	if _, err := f.svc.RevokeEnrollment(f.ctx, c.Enrollment.ID); err != nil {
		t.Fatal(err)
	}
	f.runAgentRuntime(stateDir, c.Token, eng.Host)
	waitHealth(t, f, stateDir, runtime.StatusEnrollmentFailed)
	st, _ := state.Open(stateDir)
	if used, _ := st.TokenUsed(c.Token); !used {
		t.Fatal("refused token not remembered")
	}
	es, _ := st.EnrollStatus()
	if es == nil || es.Status != state.EnrollFailed || es.Code != "unauthenticated" {
		t.Fatalf("enroll status %+v", es)
	}
	if n := countRequests(f, "/agent/v1/enroll"); n != 1 {
		t.Fatalf("%d enrollment attempts, want 1", n)
	}
}

// countRequests counts access-log lines for path in the manager log.
func countRequests(f *fixture, path string) int {
	return strings.Count(f.logs.String(), `"path":"`+path+`"`)
}

func isWindows() bool { return filepath.Separator == '\\' }

// waitHealth waits until the agent's health file reports status.
func waitHealth(t *testing.T, f *fixture, stateDir, status string) {
	t.Helper()
	waitCond(t, f, func() bool {
		b, err := os.ReadFile(filepath.Join(stateDir, runtime.HealthFileName))
		return err == nil && strings.Contains(string(b), `"status":"`+status+`"`)
	})
}
