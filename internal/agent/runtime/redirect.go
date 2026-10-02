package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/agent/transport"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Where the manager address the agent dials comes from (health file
// managerUrlSource, startup log).
const (
	// ManagerURLFromConfig: DOCKER_AGENT_MANAGER_URL.
	ManagerURLFromConfig = "config"
	// ManagerURLFromMove: the address a moving manager sent in
	// manager.redirect (manager.json).
	ManagerURLFromMove = "move"
)

// enableRedirect serves manager.redirect (docs/internal/architecture/manager-move.md,
// "Agents follow") unless the options supply their own handler.
func (a *Agent) enableRedirect() {
	if a.opts.Requests[protocol.ReqManagerRedirect] != nil {
		return
	}
	reqs := maps.Clone(a.opts.Requests)
	if reqs == nil {
		reqs = map[string]session.RequestHandler{}
	}
	reqs[protocol.ReqManagerRedirect] = a.managerRedirect
	a.opts.Requests = reqs
	a.redirectSecure = true
}

// currentTransport is the transport for the manager address the agent
// dials now.
func (a *Agent) currentTransport() *transport.Transport {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.transport
}

func (a *Agent) setTransport(tr *transport.Transport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transport, a.tinfo = tr, tr.Info()
}

// managerURLSource reports where the dialed address comes from.
func managerURLSource(tr *transport.Transport) string {
	if tr != nil && tr.Redirected() {
		return ManagerURLFromMove
	}
	return ManagerURLFromConfig
}

// sessionTarget is the session client's Target: asked before every dial,
// so a manager.redirect takes effect at the next connection.
func (a *Agent) sessionTarget(h http.Header) (string, *websocket.DialOptions) {
	tr := a.currentTransport()
	return tr.WebSocketURL(protocol.SessionPath), tr.DialOptions(h, protocol.Version)
}

// configuredOrigin is DOCKER_AGENT_MANAGER_URL as an origin, the value a
// redirect records as the one it replaces.
func (a *Agent) configuredOrigin() string {
	u := a.opts.Config.ManagerURL
	return u.Scheme + "://" + u.Host
}

// resolveTransport picks the manager address to dial at startup: the
// address of a manager.redirect kept in manager.json, unless
// DOCKER_AGENT_MANAGER_URL was changed since it arrived (the operator's
// new value wins and the redirect is forgotten), else configured
// (DOCKER_AGENT_MANAGER_URL). An unreadable manager.json or an invalid
// stored address falls back to configured.
func (a *Agent) resolveTransport(configured *transport.Transport) *transport.Transport {
	ms, err := a.store.ManagerState()
	if err != nil {
		a.log.Warn("cannot read the manager state; dialing DOCKER_AGENT_MANAGER_URL", "error", err)
		return configured
	}
	r := ms.Redirect
	if r == nil {
		return configured
	}
	if origin := configured.Info().ManagerURL; r.Replaces != origin {
		a.log.Info("DOCKER_AGENT_MANAGER_URL was changed since Docker Manager moved: dialing it and forgetting the address the move gave",
			"manager_url", origin, "redirect_url", r.URL)
		a.clearRedirect()
		return configured
	}
	tr, err := transport.NewRedirected(a.opts.Config, r.URL)
	if err != nil {
		a.log.Warn("the manager address the move gave is not usable; dialing DOCKER_AGENT_MANAGER_URL", "error", err)
		a.clearRedirect()
		return configured
	}
	return tr
}

func (a *Agent) clearRedirect() {
	if err := a.store.ClearManagerRedirect(); err != nil {
		a.log.Error("cannot forget the manager address the move gave", "error", err)
	}
}

// managerRedirect serves manager.redirect: Docker Manager moved to a new
// server and tells the agent its new address and generation. The input is
// validated (an http or https origin; plain http allowed because this
// authenticated manager sent it; a generation higher than the one the
// agent follows), the address and the generation are written to
// manager.json before the answer, the transport is switched and the
// session ends after the answer (close 1001), so the agent reconnects to
// the new address with its credential. Invalid input changes nothing.
//
// At the generation the agent already follows, the manager it follows
// gives it another address of its own (after a move: its HTTPS public
// address instead of the plain-HTTP one the move gave). Only an https
// origin or DOCKER_AGENT_MANAGER_URL's origin (which forgets the stored
// redirect) is accepted then, so such a redirect never downgrades the
// transport (protocol.FeatureManagerRedirectSecure).
func (a *Agent) managerRedirect(_ context.Context, raw json.RawMessage) (any, error) {
	var in protocol.ManagerRedirectInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed manager redirect"}
	}
	if in.Generation < 1 {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "manager redirect: the generation must be at least 1"}
	}
	if a.store == nil {
		return nil, &session.HandlerError{Code: protocol.CodeInternal, Message: "the agent state is not open", Retryable: true}
	}
	tr, err := transport.NewRedirected(a.opts.Config, in.URL)
	if err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "manager redirect: " + err.Error()}
	}
	previous := ""
	if cur := a.currentTransport(); cur != nil {
		previous = cur.Info().ManagerURL
	}
	r := state.ManagerRedirect{URL: tr.Info().ManagerURL, Replaces: a.configuredOrigin(), At: a.opts.Clock.Now().UTC()}
	err = a.store.SaveManagerRedirect(in.Generation, r)
	sameGeneration := false
	if errors.Is(err, state.ErrGenerationNotNewer) && secureAddress(r, tr) {
		sameGeneration = true
		tr, err = a.replaceRedirect(in.Generation, r, tr)
	}
	if err != nil {
		if errors.Is(err, state.ErrGenerationNotNewer) {
			a.log.Warn("refused a manager redirect that does not raise the manager generation", "redirect_url", r.URL, "error", err)
			return nil, &session.HandlerError{Code: protocol.CodeConflict, Message: strings.TrimPrefix(err.Error(), "state: ")}
		}
		a.log.Error("cannot record the manager's new address; staying with the current manager", "error", err)
		return nil, &session.HandlerError{Code: protocol.CodeInternal, Message: "could not persist the manager address", Retryable: true}
	}
	a.setTransport(tr)
	if sameGeneration {
		a.log.Info("Docker Manager gave its secure address after moving: dialing it instead of the plain-HTTP address the move gave; "+
			"reconnecting", "manager_url", tr.Info().ManagerURL, "previous_manager_url", previous, "manager_url_source", managerURLSource(tr),
			"manager_generation", in.Generation)
		return nil, &session.EndSessionError{Output: struct{}{}, Code: protocol.CloseGoingAway, Reason: "following Docker Manager to its secure address"}
	}
	a.log.Info("Docker Manager moved to a new server: following it to its new address, which replaces DOCKER_AGENT_MANAGER_URL "+
		"until that variable is changed; reconnecting", "manager_url", r.URL, "previous_manager_url", previous,
		"manager_generation", in.Generation, "manager_plain_http", tr.Info().PlainHTTP)
	return nil, &session.EndSessionError{Output: struct{}{}, Code: protocol.CloseGoingAway, Reason: "following Docker Manager to its new address"}
}

// secureAddress reports whether a redirect may change the address at the
// generation the agent already follows: https, or the configured origin.
func secureAddress(r state.ManagerRedirect, tr *transport.Transport) bool {
	return !tr.Info().PlainHTTP || r.URL == r.Replaces
}

// replaceRedirect records a redirect at the current generation and
// returns the transport to dial: the configured one when the address is
// DOCKER_AGENT_MANAGER_URL's origin (the stored redirect is forgotten),
// else tr.
func (a *Agent) replaceRedirect(generation int64, r state.ManagerRedirect, tr *transport.Transport) (*transport.Transport, error) {
	if r.URL != r.Replaces {
		return tr, a.store.ReplaceManagerRedirect(generation, &r)
	}
	configured, err := transport.New(a.opts.Config)
	if err != nil {
		return nil, err
	}
	return configured, a.store.ReplaceManagerRedirect(generation, nil)
}
