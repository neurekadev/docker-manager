package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/enroll"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/state"
	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Agent connection states written to the health file (Status).
const (
	StatusNotEnrolled        = "not_enrolled"
	StatusEnrolling          = "enrolling"
	StatusEnrollmentFailed   = "enrollment_failed"
	StatusConnecting         = "connecting"
	StatusConnected          = "connected"
	StatusOnline             = "online"
	StatusDisconnected       = "disconnected"
	StatusUnauthorized       = "unauthorized"
	StatusReplaced           = "replaced"
	StatusVersionUnsupported = "version_unsupported"
)

// DefaultTokenPoll is how often the agent looks for an enrollment token
// handed over by `docker-agent enroll`.
const DefaultTokenPoll = 2 * time.Second

// control enrolls the agent when it has a usable token and keeps its
// session up. Token sources, in order: a token handed over through the
// state directory (`docker-agent enroll`, also while enrolled: the new
// token wins), then DOCKER_AGENT_ENROLLMENT_TOKEN(_FILE) while not enrolled.
// Used or refused tokens are remembered (state.MarkTokenUsed) and never
// retried.
func (a *Agent) control(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-a.engineReady:
	}
	envToken := strings.TrimSpace(string(a.opts.Config.EnrollmentToken))
	if envToken != "" && !strings.HasPrefix(envToken, protocol.EnrollmentTokenPrefix) {
		a.log.Error("DOCKER_AGENT_ENROLLMENT_TOKEN is not an enrollment token (they start with " + protocol.EnrollmentTokenPrefix + "); ignoring it")
		envToken = ""
	}
	refused := &tokenSet{} // refused in this process (e.g. version), not persisted
	attempt := 0
	for ctx.Err() == nil {
		cred, err := a.store.Credential()
		if err != nil {
			a.log.Error("cannot read the agent credential", "error", err)
			if !a.sleep(ctx, a.opts.TokenPoll) {
				return
			}
			continue
		}
		a.setIdentity(cred)
		if token := a.nextToken(cred == nil, envToken, refused); token != "" {
			ok, retry := a.enroll(ctx, token)
			switch {
			case retry:
				attempt++
				if !a.sleep(ctx, a.backoff().Delay(attempt)) {
					return
				}
			case !ok:
				attempt = 0
				refused.add(token)
			default:
				attempt = 0
			}
			continue
		}
		if cred == nil {
			// Keep a failure status (revoked credential, refused token)
			// visible until the next enrollment attempt.
			if st := a.Status(); st != StatusUnauthorized && st != StatusEnrollmentFailed {
				a.setStatus(StatusNotEnrolled)
			}
			if !a.waitForToken(ctx, envToken, refused, false) {
				return
			}
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		go a.watchHandover(sctx, cancel, refused)
		err = a.client.Run(sctx)
		cancel()
		var stop *session.StopError
		switch {
		case errors.As(err, &stop) && stop.Unauthorized():
			a.setStatus(StatusUnauthorized)
			a.log.Error("the manager no longer accepts this agent's credential (the agent was removed or replaced, or its "+
				"environment archived); hand it a new enrollment token with `docker-agent enroll`", "reason", stop.Reason)
			if err := a.store.ClearCredential(); err != nil {
				a.log.Error("cannot remove the revoked credential", "error", err)
			}
			a.setIdentity(nil)
		case errors.As(err, &stop) && stop.CloseCode == protocol.CloseReplaced:
			a.setStatus(StatusReplaced)
			a.log.Error("another process with this agent's credential took over the session; this one stays idle " +
				"(run only one agent per credential)")
			if !a.waitForToken(ctx, envToken, refused, true) {
				return
			}
		case errors.As(err, &stop):
			a.setStatus(StatusVersionUnsupported)
			a.log.Error("the manager refuses this agent version; upgrade it (manager first, then agents)", "reason", stop.Reason)
			if !a.waitForToken(ctx, envToken, refused, true) {
				return
			}
		case errors.Is(err, session.ErrNotEnrolled):
		case err != nil:
			a.log.Error("agent session failed", "error", err)
			if !a.sleep(ctx, a.opts.TokenPoll) {
				return
			}
		}
	}
}

func (a *Agent) backoff() session.Backoff {
	if a.opts.Backoff.Min > 0 {
		return a.opts.Backoff
	}
	return session.DefaultBackoff()
}

func (a *Agent) sleep(ctx context.Context, d time.Duration) bool {
	t := a.opts.Clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}

// nextToken returns a token to enroll with, or "".
func (a *Agent) nextToken(notEnrolled bool, envToken string, refused *tokenSet) string {
	pending, err := a.store.PendingToken()
	if err != nil {
		a.log.Error("cannot read the handed-over enrollment token", "error", err)
	}
	if pending != "" {
		used, err := a.store.TokenUsed(pending)
		switch {
		case !strings.HasPrefix(pending, protocol.EnrollmentTokenPrefix):
			a.finishHandover(pending, state.EnrollStatus{Status: state.EnrollFailed, Code: "invalid_token", Message: "not an enrollment token"})
		case err == nil && (used || refused.has(pending)):
			a.finishHandover(pending, state.EnrollStatus{Status: state.EnrollFailed, Code: "token_used",
				Message: "this token was already used by this agent; create a new enrollment"})
		default:
			return pending
		}
	}
	if !notEnrolled || envToken == "" || refused.has(envToken) {
		return ""
	}
	if used, err := a.store.TokenUsed(envToken); err != nil || used {
		if used {
			refused.add(envToken) // stay quiet about it from now on
			a.log.Info("DOCKER_AGENT_ENROLLMENT_TOKEN was already used by this agent; remove it from the configuration and hand over a new token with `docker-agent enroll`")
		}
		return ""
	}
	return envToken
}

// finishHandover records an outcome for a handed-over token and removes it.
func (a *Agent) finishHandover(token string, st state.EnrollStatus) {
	st.TokenID, st.At = state.TokenID(token), a.opts.Clock.Now().UTC()
	if err := a.store.WriteEnrollStatus(st); err != nil {
		a.log.Error("cannot record the enrollment status", "error", err)
	}
	if err := a.store.ClearPendingToken(token); err != nil {
		a.log.Error("cannot remove the handed-over enrollment token", "error", err)
	}
}

// enroll exchanges token for a credential. ok: enrolled; retry: a
// transient failure (try the same token again after a backoff).
func (a *Agent) enroll(ctx context.Context, token string) (ok, retry bool) {
	a.setStatus(StatusEnrolling)
	tokenID := state.TokenID(token)
	_ = a.store.WriteEnrollStatus(state.EnrollStatus{TokenID: tokenID, Status: state.EnrollPending, At: a.opts.Clock.Now().UTC()})
	caps, have := a.CapabilitiesPayload()
	installID, err := a.store.InstallID()
	if !have || err != nil {
		a.log.Error("cannot enroll yet: the Engine identity or install ID is unavailable", "error", err)
		return false, true
	}
	var hostname string
	if c := a.Capabilities(); c.Engine != nil {
		hostname = c.Engine.Name
	}
	info := buildinfo.Get()
	req := protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: info.Version, InstallID: installID, Engine: caps.Engine,
		Hostname: truncate(hostname, protocol.MaxHostname), EnvironmentName: a.opts.Config.EnvironmentName}
	a.log.Info("enrolling with the manager", "manager_url", a.transport.Info().ManagerURL, "engine_id", caps.Engine.ID, "install_id", installID)
	ectx, cancel := context.WithTimeout(ctx, time.Minute)
	resp, err := enroll.Enroll(ectx, a.transport.HTTPClient(), a.transport.URL(protocol.EnrollPath), token, userAgent(), req)
	cancel()
	if err != nil {
		var ee *enroll.Error
		if errors.As(err, &ee) && ee.Retryable() {
			a.log.Warn("enrollment failed; retrying", "error", err)
			_ = a.store.WriteEnrollStatus(state.EnrollStatus{TokenID: tokenID, Status: state.EnrollPending, Message: err.Error(), At: a.opts.Clock.Now().UTC()})
			return false, true
		}
		code := "enrollment_failed"
		if ee != nil && ee.Code != "" {
			code = ee.Code
		}
		a.log.Error("enrollment refused; this token will not be retried", "code", code, "error", err)
		// A version refusal may succeed after an agent upgrade (a new process);
		// every other refusal is final for this token. The outcome is on disk
		// before the health status changes (observers read it after that).
		if ee == nil || ee.Status != http.StatusUpgradeRequired {
			if err := a.store.MarkTokenUsed(token); err != nil {
				a.log.Error("cannot record the refused token", "error", err)
			}
		}
		a.finishHandover(token, state.EnrollStatus{Status: state.EnrollFailed, Code: code, Message: err.Error()})
		a.setStatus(StatusEnrollmentFailed)
		return false, false
	}
	cred := state.Credential{AgentID: resp.AgentID, EnvironmentID: resp.EnvironmentID, EnvironmentName: resp.EnvironmentName,
		Credential: resp.Credential, ManagerURL: a.transport.Info().ManagerURL, EnrolledAt: a.opts.Clock.Now().UTC()}
	// The credential is on disk before any session uses it.
	if err := a.store.SaveCredential(cred); err != nil {
		a.log.Error("enrolled, but the credential could not be stored; create a new enrollment", "error", err)
		a.finishHandover(token, state.EnrollStatus{Status: state.EnrollFailed, Code: "state_write_failed", Message: err.Error()})
		a.setStatus(StatusEnrollmentFailed)
		return false, false
	}
	if err := a.store.MarkTokenUsed(token); err != nil {
		a.log.Error("cannot record the used token", "error", err)
	}
	a.finishHandover(token, state.EnrollStatus{Status: state.EnrollEnrolled, AgentID: resp.AgentID, EnvironmentID: resp.EnvironmentID})
	a.setIdentity(&cred)
	a.log.Info("agent enrolled", "agent_id", resp.AgentID, "environment_id", resp.EnvironmentID,
		"environment_name", resp.EnvironmentName, "reattached", resp.Reattached)
	return true, false
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func userAgent() string { return "docker-agent/" + buildinfo.Get().Version }

// waitForToken waits until a usable token appears. withCredential: the
// agent still holds a credential (only handed-over tokens count).
func (a *Agent) waitForToken(ctx context.Context, envToken string, refused *tokenSet, withCredential bool) bool {
	t := a.opts.Clock.NewTicker(a.opts.TokenPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C():
			if a.nextToken(!withCredential, envToken, refused) != "" {
				return true
			}
		}
	}
}

// watchHandover ends the running session when a new token is handed over
// (the control loop then enrolls with it).
func (a *Agent) watchHandover(ctx context.Context, cancel context.CancelFunc, refused *tokenSet) {
	t := a.opts.Clock.NewTicker(a.opts.TokenPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			if tok, err := a.store.PendingToken(); err == nil && tok != "" && !refused.has(tok) {
				a.log.Info("a new enrollment token was handed over; enrolling with it")
				cancel()
				return
			}
		}
	}
}

// onSessionStatus maps session states to the health status.
func (a *Agent) onSessionStatus(st session.Status) {
	switch st.State {
	case session.StateConnecting:
		a.setStatus(StatusConnecting)
	case session.StateConnected:
		a.setStatus(StatusConnected)
	case session.StateOnline:
		a.setStatus(StatusOnline)
	case session.StateDisconnected:
		a.setStatus(StatusDisconnected)
	}
}

// tokenSet is a concurrency-safe set of token values (in memory only).
type tokenSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func (t *tokenSet) add(tok string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]bool{}
	}
	t.m[tok] = true
}

func (t *tokenSet) has(tok string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[tok]
}
