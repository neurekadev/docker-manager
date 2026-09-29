// Package transport builds the agent's outbound HTTP and WebSocket client
// for the manager (#27):
//
//   - https manager URLs use normal certificate validation against the
//     system roots plus the optional DOCKER_AGENT_MANAGER_CA_FILE bundle (private
//     PKI); verification is never disabled;
//   - http manager URLs exist only with DOCKER_AGENT_MANAGER_ALLOW_HTTP=true
//     (validated by internal/agent/config) or as the address a moving
//     manager sent in manager.redirect (NewRedirected, the one narrow
//     exception), and are reported as flagged in the agent's capabilities
//     (protocol.TransportInfo);
//   - redirects are never followed, so enrollment tokens and credentials
//     cannot be forwarded to another origin or downgraded to http.
//
// The agent only dials out; nothing here listens.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/config"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// ErrRedirect is returned when the manager (or something in between)
// answers with a redirect.
var ErrRedirect = errors.New("manager responded with a redirect; refusing to follow it (check DOCKER_AGENT_MANAGER_URL)")

// Transport is the agent's connection setup for one manager origin.
type Transport struct {
	client     *http.Client
	info       protocol.TransportInfo
	base       string
	redirected bool
}

// New builds the transport for DOCKER_AGENT_MANAGER_URL from the validated
// configuration.
func New(cfg config.Config) (*Transport, error) {
	if cfg.ManagerURL == nil {
		return nil, errors.New("transport: manager URL is required")
	}
	return build(cfg, cfg.ManagerURL, cfg.PlainHTTP, false)
}

// NewRedirected builds the transport for the manager address received in
// manager.redirect (validated by config.ParseRedirectURL): the only case
// where the agent uses plain http without DOCKER_AGENT_MANAGER_ALLOW_HTTP,
// because the authenticated manager it was connected to sent it. https
// keeps the same TLS trust as New (system roots plus
// DOCKER_AGENT_MANAGER_CA_FILE); redirects are still never followed.
func NewRedirected(cfg config.Config, rawURL string) (*Transport, error) {
	u, err := config.ParseRedirectURL(rawURL)
	if err != nil {
		return nil, err
	}
	return build(cfg, u, u.Scheme == "http", true)
}

func build(cfg config.Config, origin *url.URL, plainHTTP, redirected bool) (*Transport, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if len(cfg.ManagerCAPEM) > 0 && !roots.AppendCertsFromPEM(cfg.ManagerCAPEM) {
		return nil, fmt.Errorf("%s: no usable certificates", config.EnvManagerCAFile)
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("transport: unexpected default transport")
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	tr.ResponseHeaderTimeout = 30 * time.Second
	base := origin.Scheme + "://" + origin.Host
	return &Transport{
		client: &http.Client{
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return ErrRedirect
			},
		},
		info:       protocol.TransportInfo{ManagerURL: base, PlainHTTP: plainHTTP, CustomCA: len(cfg.ManagerCAPEM) > 0},
		base:       base,
		redirected: redirected,
	}, nil
}

// Redirected reports whether this transport dials the address of a
// manager.redirect instead of DOCKER_AGENT_MANAGER_URL.
func (t *Transport) Redirected() bool { return t.redirected }

// HTTPClient is the client for manager requests (enrollment, #3).
func (t *Transport) HTTPClient() *http.Client { return t.client }

// URL joins the manager origin and path (e.g. "/agent/v1/enroll").
func (t *Transport) URL(path string) string { return t.base + path }

// WebSocketURL returns the ws:// or wss:// URL for path.
func (t *Transport) WebSocketURL(path string) string {
	if t.info.PlainHTTP {
		return "ws" + t.base[len("http"):] + path
	}
	return "wss" + t.base[len("https"):] + path
}

// DialOptions returns WebSocket dial options using the same TLS
// configuration, offering subprotocols and sending header (e.g. the
// Authorization bearer credential, #3).
func (t *Transport) DialOptions(header http.Header, subprotocols ...string) *websocket.DialOptions {
	return &websocket.DialOptions{HTTPClient: t.client, HTTPHeader: header, Subprotocols: subprotocols}
}

// Info is the transport report for the agent's capabilities.
func (t *Transport) Info() protocol.TransportInfo { return t.info }
