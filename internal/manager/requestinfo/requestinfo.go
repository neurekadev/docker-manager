// Package requestinfo resolves who sent a request and how it reached the
// public origin: the client IP, the scheme and the host, honoring
// X-Forwarded-For/Proto/Host only when the direct peer is a trusted reverse
// proxy (DOCKYARD_TRUSTED_PROXIES, #27).
//
// The manager's HTTP stack resolves an Info for every request and stores it
// in the request context (internal/manager/server). Features read it with
// From or ClientIP — rate limits (#16, /agent/v1), audit (#30), the first-run
// HTTPS check (CheckSecureOrigin) — and never parse forwarding headers
// themselves: the server strips them before any handler runs.
package requestinfo

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Forwarding headers honored from trusted proxies and stripped otherwise.
const (
	HeaderForwardedFor   = "X-Forwarded-For"
	HeaderForwardedProto = "X-Forwarded-Proto"
	HeaderForwardedHost  = "X-Forwarded-Host"
	// HeaderForwarded (RFC 7239) is never honored; it is always stripped
	// so no handler can read a client-supplied value.
	HeaderForwarded = "Forwarded"
)

// Info describes the origin of one request.
type Info struct {
	// ClientIP is the address of the client that sent the request: the
	// direct peer, or (behind trusted proxies) the rightmost untrusted
	// X-Forwarded-For hop. Invalid when the peer address is unparsable
	// (only in tests with synthetic RemoteAddr values).
	ClientIP netip.Addr
	// Peer is the direct TCP peer (the proxy when there is one).
	Peer netip.Addr
	// TrustedPeer is true when Peer is in the trusted proxy list, i.e. the
	// forwarding headers were honored.
	TrustedPeer bool
	// Scheme is "https" or "http" as seen by the client.
	Scheme string
	// Host is the host[:port] the client addressed (lower case).
	Host string
}

// Secure reports whether the client reached the manager over HTTPS.
func (i Info) Secure() bool { return i.Scheme == "https" }

type ctxKey struct{}

// With returns ctx carrying info.
func With(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// From returns the Info stored by the server middleware.
func From(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(ctxKey{}).(Info)
	return info, ok
}

// ClientIP returns the resolved client IP, or the zero (invalid) address
// when the context carries no Info.
func ClientIP(ctx context.Context) netip.Addr {
	info, _ := From(ctx)
	return info.ClientIP
}

// Resolver resolves Info from requests. The zero value trusts no proxy.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver returns a resolver trusting the given proxy networks.
func NewResolver(trusted []netip.Prefix) *Resolver {
	out := make([]netip.Prefix, 0, len(trusted))
	for _, p := range trusted {
		if p.IsValid() {
			out = append(out, p.Masked())
		}
	}
	return &Resolver{trusted: out}
}

// Trusts reports whether addr is a trusted proxy.
func (r *Resolver) Trusts(addr netip.Addr) bool {
	if r == nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Resolve computes the Info for req. It does not modify req.
//
// From an untrusted peer every forwarding header is ignored: the client is
// the peer, the scheme is https only for a direct TLS connection and the
// host is the Host header. From a trusted peer:
//   - X-Forwarded-For is read right to left (all header lines joined); the
//     first address that is not a trusted proxy is the client. Entries a
//     client prepended itself are therefore never reached. An unparsable
//     entry stops the walk at the last valid address.
//   - X-Forwarded-Proto: the rightmost value (set by the nearest proxy):
//     "https" or "wss" mean https, "http" or "ws" mean http (Traefik sends
//     ws/wss for WebSocket upgrades); anything else keeps the direct scheme.
//   - X-Forwarded-Host: the rightmost value when it is a syntactically valid
//     host[:port]; otherwise the Host header.
func (r *Resolver) Resolve(req *http.Request) Info {
	peer := parseRemoteAddr(req.RemoteAddr)
	info := Info{
		ClientIP: peer,
		Peer:     peer,
		Scheme:   "http",
		Host:     strings.ToLower(req.Host),
	}
	if req.TLS != nil {
		info.Scheme = "https"
	}
	if !r.Trusts(peer) {
		return info
	}
	info.TrustedPeer = true
	info.ClientIP = r.clientFromXFF(req.Header.Values(HeaderForwardedFor), peer)
	switch strings.ToLower(lastValue(req.Header.Values(HeaderForwardedProto))) {
	case "https", "wss": // Traefik reports WebSocket upgrades as ws/wss
		info.Scheme = "https"
	case "http", "ws":
		info.Scheme = "http"
	}
	if h := strings.ToLower(lastValue(req.Header.Values(HeaderForwardedHost))); validHost(h) {
		info.Host = h
	}
	return info
}

func (r *Resolver) clientFromXFF(lines []string, peer netip.Addr) netip.Addr {
	var hops []string
	for _, l := range lines {
		for _, h := range strings.Split(l, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			return client
		}
		client = a
		if !r.Trusts(a) {
			return a
		}
	}
	return client
}

// parseHop parses an X-Forwarded-For entry: an IP, optionally with a port
// ("1.2.3.4:5", "[::1]:5") or in brackets.
func parseHop(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}

func parseRemoteAddr(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

// lastValue returns the rightmost comma-separated value across all lines.
func lastValue(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		parts := strings.Split(lines[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			if v := strings.TrimSpace(parts[j]); v != "" {
				return v
			}
		}
	}
	return ""
}

// validHost accepts host names, IPv4 and bracketed IPv6 addresses with an
// optional port.
func validHost(h string) bool {
	if h == "" || len(h) > 261 {
		return false
	}
	host, port := h, ""
	if strings.HasPrefix(h, "[") {
		end := strings.IndexByte(h, ']')
		if end < 0 {
			return false
		}
		if _, err := netip.ParseAddr(h[1:end]); err != nil {
			return false
		}
		rest := h[end+1:]
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return false
		}
		host, port = "", strings.TrimPrefix(rest, ":")
		if rest != "" && port == "" {
			return false
		}
	} else if i := strings.LastIndexByte(h, ':'); i >= 0 {
		host, port = h[:i], h[i+1:]
		if port == "" {
			return false
		}
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	if len(port) > 5 {
		return false
	}
	if host == "" {
		return strings.HasPrefix(h, "[")
	}
	for _, c := range host {
		if !isHostChar(c) {
			return false
		}
	}
	return true
}

func isHostChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_'
}
