package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// The Shoutrrr adapter (ADR 0004). Shoutrrr's own error texts and log
// lines can contain the address (webhook tokens, passwords), so nothing of
// them leaves this file: its logger discards everything, and a send is
// reduced to a stable error class (classify) from what the adapter saw on
// the wire (probe) and, for non-HTTP services, the kind of error.
//
// Every request goes through the adapter's HTTP client (services that
// accept one) and TCP dialer (SMTP): a fixed timeout per send, no
// cross-scheme redirects, at most maxRedirects same-scheme ones. There are
// no destination restrictions (owner decision): LAN, loopback and SMTP
// servers are allowed.

const (
	// DefaultTimeout bounds one send (connect, TLS, request, answer).
	DefaultTimeout = 15 * time.Second
	maxRedirects   = 3
	// MaxAddressLen bounds a stored address.
	MaxAddressLen = 4096
)

// discard is Shoutrrr's logger: its lines may contain the address.
var discard = log.New(io.Discard, "", 0)

var (
	errNoNetwork           = errors.New("notify: address validation makes no network requests")
	errCrossSchemeRedirect = errors.New("notify: redirect to another scheme refused")
	errTooManyRedirects    = errors.New("notify: too many redirects")
)

// probe records what one send did on the wire: the last HTTP status, the
// last transport or dial error, a refused redirect, a panic.
type probe struct {
	mu       sync.Mutex
	status   int
	netErr   error
	redirect bool
	offline  bool
	panicked bool
}

func (p *probe) setStatus(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = code
}

func (p *probe) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if errors.Is(err, errNoNetwork) {
		p.offline = true
		return
	}
	p.netErr = err
}

func (p *probe) snapshot() (status int, netErr error, redirect, offline, panicked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.netErr, p.redirect, p.offline, p.panicked
}

// recordingTransport passes requests to base and records their outcome.
type recordingTransport struct {
	base http.RoundTripper
	p    *probe
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.p.setErr(err)
		return nil, err
	}
	t.p.setStatus(resp.StatusCode)
	return resp, nil
}

// offlineTransport refuses every request (address validation).
type offlineTransport struct{ p *probe }

func (t offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.p.setErr(errNoNetwork)
	return nil, errNoNetwork
}

type dialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// wire is the network access of one send.
type wire struct {
	client *http.Client
	dial   dialFunc
	p      *probe
}

// newWire returns the HTTP client and dialer of one send (offline: every
// request and dial fails with errNoNetwork, for validation).
func newWire(timeout time.Duration, offline bool) *wire {
	p := &probe{}
	w := &wire{p: p}
	if offline {
		w.dial = func(context.Context, string, string) (net.Conn, error) {
			p.setErr(errNoNetwork)
			return nil, errNoNetwork
		}
		w.client = &http.Client{Transport: offlineTransport{p}, Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		return w
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: -1}
	w.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			p.setErr(err)
		}
		return c, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = w.dial
	base.TLSHandshakeTimeout = timeout
	base.ResponseHeaderTimeout = timeout
	base.DisableKeepAlives = true
	w.client = &http.Client{
		Transport: &recordingTransport{base: base, p: p},
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && req.URL.Scheme != via[0].URL.Scheme {
				p.mu.Lock()
				p.redirect = true
				p.mu.Unlock()
				return errCrossSchemeRedirect
			}
			if len(via) >= maxRedirects {
				p.mu.Lock()
				p.redirect = true
				p.mu.Unlock()
				return errTooManyRedirects
			}
			return nil
		},
	}
	return w
}

// parseAddress checks the shape of an address and returns its URL and
// Shoutrrr service (the scheme before any "+", lower case).
func parseAddress(address string) (*url.URL, string, error) {
	if address == "" || len(address) > MaxAddressLen || strings.ContainsFunc(address, func(r rune) bool {
		return r < 0x20 || r == 0x7f || r == ' '
	}) {
		return nil, "", errInvalidAddress
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme == "" {
		return nil, "", errInvalidAddress
	}
	service, _, _ := strings.Cut(strings.ToLower(u.Scheme), "+")
	if service == "" || len(service) > 32 {
		return nil, "", errInvalidAddress
	}
	return u, service, nil
}

var errInvalidAddress = errors.New("notify: invalid address")

// locate initializes the Shoutrrr service of address on w (like Shoutrrr's
// router, but the HTTP client and dialer are set before Initialize too, so
// a service that talks to its server while initializing uses them).
func locate(address string, w *wire) (svc types.Service, err error) {
	defer func() {
		if r := recover(); r != nil {
			svc, err = nil, errInvalidAddress
		}
	}()
	u, service, err := parseAddress(address)
	if err != nil {
		return nil, err
	}
	var r router.ServiceRouter
	svc, err = r.NewService(service)
	if err != nil {
		return nil, errInvalidAddress
	}
	if !strings.EqualFold(u.Scheme, service) {
		custom, ok := svc.(types.CustomURLService)
		if !ok {
			return nil, errInvalidAddress
		}
		if u, err = custom.GetServiceURLFromCustom(u); err != nil {
			return nil, errInvalidAddress
		}
	}
	inject := func() {
		if s, ok := svc.(types.HTTPClientSetter); ok {
			s.SetHTTPClient(w.client)
		}
		if s, ok := svc.(types.DialContextSetter); ok {
			s.SetDialContext(w.dial)
		}
	}
	inject()
	if err := svc.Initialize(u, discard); err != nil {
		return nil, fmt.Errorf("%w: %w", errInitialize, err)
	}
	inject()
	return svc, nil
}

var errInitialize = errors.New("notify: initialize the service")

// knownService reports whether Shoutrrr has a service of this name.
func knownService(service string) bool {
	var r router.ServiceRouter
	_, err := r.NewService(service)
	return err == nil
}

// validate checks an address without sending anything: it must parse, name
// a Shoutrrr service and initialize it. A service that contacts its
// server while initializing (Matrix signs in) counts as valid once it
// tried to.
func validate(address string, timeout time.Duration) (service string, err error) {
	_, service, err = parseAddress(address)
	if err != nil {
		return "", err
	}
	if !knownService(service) {
		return "", errUnknownService
	}
	w := newWire(timeout, true)
	if _, err := locate(address, w); err != nil {
		if _, _, _, offline, _ := w.p.snapshot(); offline {
			return service, nil
		}
		return "", errInvalidAddress
	}
	return service, nil
}

var errUnknownService = errors.New("notify: unknown service")

// deliver sends one message to address and returns "" or an error class.
func deliver(ctx context.Context, address string, msg domain.NotificationMessage, timeout time.Duration) string {
	w := newWire(timeout, false)
	svc, err := locate(address, w)
	if err != nil {
		// A service that failed while contacting its server (Matrix
		// signing in) is classified like a send; anything else is an
		// address Shoutrrr cannot use.
		status, netErr, redirect, _, _ := w.p.snapshot()
		if errors.Is(err, errInitialize) && (status != 0 || netErr != nil || redirect) {
			return classify(ctx, err, w.p)
		}
		return domain.NotifyErrInvalidURL
	}
	params := types.Params{}
	if msg.Title != "" {
		params.SetTitle(msg.Title)
	}
	body := msg.Body
	if msg.URL != "" {
		body = strings.TrimRight(body, "\n") + "\n\n" + msg.URL
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				w.p.mu.Lock()
				w.p.panicked = true
				w.p.mu.Unlock()
				done <- errPanicked
			}
		}()
		if cs, ok := svc.(types.ContextSender); ok {
			done <- cs.SendContext(ctx, body, &params)
			return
		}
		// Services without a context stop at the HTTP client's timeout.
		done <- svc.Send(body, &params)
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil {
		return ""
	}
	return classify(ctx, err, w.p)
}

var errPanicked = errors.New("notify: the service failed")
