package requestinfo

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func prefixes(t *testing.T, s ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

func req(remote string, hdr ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://Docker.Example.com/api/v1/health", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return r
}

func TestResolveUntrustedPeerIgnoresForwardingHeaders(t *testing.T) {
	res := NewResolver(prefixes(t, "10.0.0.0/24"))
	r := req("203.0.113.7:5555",
		HeaderForwardedFor, "198.51.100.1",
		HeaderForwardedProto, "https",
		HeaderForwardedHost, "evil.example")
	got := res.Resolve(r)
	want := Info{
		ClientIP: netip.MustParseAddr("203.0.113.7"), Peer: netip.MustParseAddr("203.0.113.7"),
		Scheme: "http", Host: "docker.example.com",
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Without any trusted proxy configured, nothing is honored either.
	if got := (&Resolver{}).Resolve(r); got != want {
		t.Fatalf("zero resolver: %+v", got)
	}
	var nilRes *Resolver
	if got := nilRes.Resolve(r); got != want {
		t.Fatalf("nil resolver: %+v", got)
	}
}

func TestResolveTrustedProxy(t *testing.T) {
	res := NewResolver(prefixes(t, "10.0.0.0/24", "fd00::/64"))
	cases := []struct {
		name   string
		remote string
		hdr    []string
		client string
		scheme string
		host   string
	}{
		{"single proxy", "10.0.0.2:1234", []string{HeaderForwardedFor, "198.51.100.9", HeaderForwardedProto, "https", HeaderForwardedHost, "Docker.Example.com"},
			"198.51.100.9", "https", "docker.example.com"},
		{"client-prepended spoof is skipped", "10.0.0.2:1234", []string{HeaderForwardedFor, "1.2.3.4, 198.51.100.9"},
			"198.51.100.9", "http", "docker.example.com"},
		{"chain of trusted proxies", "10.0.0.2:1", []string{HeaderForwardedFor, "198.51.100.9, 10.0.0.3", HeaderForwardedFor, "10.0.0.4"},
			"198.51.100.9", "http", "docker.example.com"},
		{"all hops trusted: leftmost", "10.0.0.2:1", []string{HeaderForwardedFor, "10.0.0.5, 10.0.0.3"},
			"10.0.0.5", "http", "docker.example.com"},
		{"no XFF: peer", "10.0.0.2:1", nil, "10.0.0.2", "http", "docker.example.com"},
		{"garbage hop stops the walk", "10.0.0.2:1", []string{HeaderForwardedFor, "198.51.100.9, not-an-ip, 10.0.0.3"},
			"10.0.0.3", "http", "docker.example.com"},
		{"hop with port and IPv6", "[fd00::1]:9", []string{HeaderForwardedFor, "[2001:db8::7]:4711"},
			"2001:db8::7", "http", "docker.example.com"},
		{"IPv4 with port", "10.0.0.2:1", []string{HeaderForwardedFor, "198.51.100.9:80"}, "198.51.100.9", "http", "docker.example.com"},
		{"bracketed IPv6", "10.0.0.2:1", []string{HeaderForwardedFor, "[2001:db8::8]"}, "2001:db8::8", "http", "docker.example.com"},
		{"mapped IPv4 peer", "[::ffff:10.0.0.2]:1", []string{HeaderForwardedFor, "::ffff:198.51.100.9"}, "198.51.100.9", "http", "docker.example.com"},
		{"proto: rightmost wins", "10.0.0.2:1", []string{HeaderForwardedProto, "https, http"}, "10.0.0.2", "http", "docker.example.com"},
		{"proto: junk ignored", "10.0.0.2:1", []string{HeaderForwardedProto, "gopher"}, "10.0.0.2", "http", "docker.example.com"},
		{"proto: case-insensitive", "10.0.0.2:1", []string{HeaderForwardedProto, "HTTPS"}, "10.0.0.2", "https", "docker.example.com"},
		{"host with port", "10.0.0.2:1", []string{HeaderForwardedHost, "docker.example.com:8443"}, "10.0.0.2", "http", "docker.example.com:8443"},
		{"host: invalid ignored", "10.0.0.2:1", []string{HeaderForwardedHost, "evil.example/path"}, "10.0.0.2", "http", "docker.example.com"},
		{"host: IPv6 literal", "10.0.0.2:1", []string{HeaderForwardedHost, "[2001:db8::1]:443"}, "10.0.0.2", "http", "[2001:db8::1]:443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := res.Resolve(req(c.remote, c.hdr...))
			if !got.TrustedPeer || got.ClientIP != netip.MustParseAddr(c.client) || got.Scheme != c.scheme || got.Host != c.host {
				t.Fatalf("got %+v, want client %s scheme %s host %s", got, c.client, c.scheme, c.host)
			}
		})
	}
}

func TestResolveDirectTLSAndOddRemoteAddr(t *testing.T) {
	res := NewResolver(nil)
	r := req("192.0.2.1:443")
	r.TLS = &tls.ConnectionState{}
	if got := res.Resolve(r); got.Scheme != "https" || !got.Secure() {
		t.Fatalf("direct TLS: %+v", got)
	}
	if got := res.Resolve(req("pipe")); got.ClientIP.IsValid() || got.TrustedPeer {
		t.Fatalf("unparsable remote: %+v", got)
	}
	if got := res.Resolve(req("192.0.2.5")); got.ClientIP != netip.MustParseAddr("192.0.2.5") {
		t.Fatalf("remote without port: %+v", got)
	}
	if res.Trusts(netip.Addr{}) {
		t.Fatal("trusts the invalid address")
	}
}

func TestContextAccessors(t *testing.T) {
	ctx := t.Context()
	if _, ok := From(ctx); ok || ClientIP(ctx).IsValid() {
		t.Fatal("empty context has info")
	}
	info := Info{ClientIP: netip.MustParseAddr("192.0.2.3"), Scheme: "https"}
	ctx = With(ctx, info)
	if got, ok := From(ctx); !ok || got != info || ClientIP(ctx) != info.ClientIP {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestValidHost(t *testing.T) {
	for _, h := range []string{"a", "docker.example.com", "docker.example.com:443", "dockyard_manager:8080", "[::1]", "[::1]:8443", "127.0.0.1:1"} {
		if !validHost(h) {
			t.Errorf("%q rejected", h)
		}
	}
	for _, h := range []string{"", "a b", "a/b", "a:", "a:x", "a:123456", "[::1", "[nope]", "[::1]x", "[::1]:", "é.example", strings.Repeat("a", 300)} {
		if validHost(h) {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestCheckSecureOrigin(t *testing.T) {
	pub, _ := url.Parse("https://docker.example.com")
	dev, _ := url.Parse("http://localhost:8080")
	plain, _ := url.Parse("http://docker.example.com")
	ok := Info{Scheme: "https", Host: "docker.example.com", TrustedPeer: true}
	cases := []struct {
		name   string
		pub    *url.URL
		dev    bool
		info   Info
		reason string
	}{
		{"https via trusted proxy", pub, false, ok, ""},
		{"explicit default port", pub, false, Info{Scheme: "https", Host: "docker.example.com:443"}, ""},
		{"plain http request", pub, false, Info{Scheme: "http", Host: "docker.example.com"}, ReasonRequestNotHTTPS},
		{"internal address", pub, false, Info{Scheme: "https", Host: "dockyard-manager:8080"}, ReasonHostMismatch},
		{"localhost dev", dev, true, Info{Scheme: "http", Host: "localhost:8080"}, ""},
		{"localhost dev, other host", dev, true, Info{Scheme: "http", Host: "192.168.1.5:8080"}, ReasonHostMismatch},
		{"http public URL without dev mode", plain, false, Info{Scheme: "http", Host: "docker.example.com"}, ReasonPublicURLNotHTTPS},
		{"no public URL", nil, false, ok, ReasonPublicURLNotHTTPS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckSecureOrigin(c.pub, c.dev, c.info)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			var ie *InsecureOriginError
			if !errors.As(err, &ie) || ie.Reason != c.reason || !errors.Is(err, ErrInsecureOrigin) || ie.Explanation == "" {
				t.Fatalf("got %v, want reason %s", err, c.reason)
			}
		})
	}
	// The explanation points untrusted-proxy setups at DOCKYARD_TRUSTED_PROXIES.
	err := CheckSecureOrigin(pub, false, Info{Scheme: "http", Host: "docker.example.com"})
	if !strings.Contains(err.Error(), "DOCKYARD_TRUSTED_PROXIES") || !strings.Contains(err.Error(), "https://docker.example.com") {
		t.Fatalf("explanation %q", err)
	}
	err = CheckSecureOrigin(pub, false, Info{Scheme: "http", Host: "docker.example.com", TrustedPeer: true})
	if strings.Contains(err.Error(), "DOCKYARD_TRUSTED_PROXIES") {
		t.Fatalf("trusted peer should not get the proxy hint: %q", err)
	}
}

func FuzzResolve(f *testing.F) {
	f.Add("10.0.0.2:1", "1.2.3.4, 198.51.100.9", "https", "docker.example.com")
	f.Add("203.0.113.1:9", "", "", "")
	f.Add("[fd00::1]:1", "[2001:db8::1]:80,garbage", "http,https", "[::1]:8443")
	res := NewResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")})
	f.Fuzz(func(t *testing.T, remote, xff, proto, host string) {
		r := httptest.NewRequest(http.MethodGet, "http://h/", nil)
		r.RemoteAddr = remote
		r.Header.Set(HeaderForwardedFor, xff)
		r.Header.Set(HeaderForwardedProto, proto)
		r.Header.Set(HeaderForwardedHost, host)
		info := res.Resolve(r)
		if info.Scheme != "http" && info.Scheme != "https" {
			t.Fatalf("scheme %q", info.Scheme)
		}
		if !info.TrustedPeer && (info.ClientIP != info.Peer || info.Host != "h") {
			t.Fatalf("untrusted peer honored headers: %+v", info)
		}
		if info.TrustedPeer && info.Host != "h" && !validHost(info.Host) {
			t.Fatalf("invalid host %q", info.Host)
		}
	})
}
