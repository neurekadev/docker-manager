package managermove

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// DefaultPort is the manager's port on a server when an address names
// none (the Quickstart publishes 8080).
const DefaultPort = "8080"

// NewServerManagerURL is the new manager's address for the new server's
// own agent (the service name in the generated compose.yaml).
const NewServerManagerURL = "http://docker-manager:" + DefaultPort

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// NormalizeServerAddress checks a server address as the owner typed it:
// an IP address or host name, optionally with a port (8080 when absent)
// or an http:// prefix. It returns host:port. field names the input in
// the error.
func NormalizeServerAddress(field, raw string) (string, error) {
	bad := func(msg string) error { return &domain.FieldError{Field: field, Message: msg} }
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", bad("enter the server's IP address or host name, like 192.168.1.10")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", bad("enter only the IP address or host name (and a port), like 192.168.1.10 or 192.168.1.10:8080")
		}
		s = u.Host
	}
	host, port := s, DefaultPort
	if h, p, err := net.SplitHostPort(s); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		host = s[1 : len(s)-1]
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", bad("the port must be a number between 1 and 65535")
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Is4In6() {
			a = a.Unmap()
		}
		if a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			return "", bad("enter the server's address on your network, not a loopback or wildcard address")
		}
		return net.JoinHostPort(a.String(), port), nil
	}
	if len(host) > 253 || !hostnameRE.MatchString(host) || strings.EqualFold(host, "localhost") {
		return "", bad("enter a valid IP address or host name, like 192.168.1.10 or nas.lan")
	}
	return net.JoinHostPort(strings.ToLower(host), port), nil
}

// serverURL is http://host:port of a normalized server address.
func serverURL(addr string) string { return "http://" + addr }

// hostOf is the host part of a normalized server address.
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// Files are the new server's compose.yaml and .env, shown once (the .env
// carries the move code and the enrollment token).
type Files struct {
	ComposeYAML string
	Env         string
}

// RenderInput is what the new server's files need (Options.Render; the
// app renders them with agents.MoveFiles, next to the install commands).
type RenderInput struct {
	PublicURL string
	// OldManagerURL is http://<this server>:<port>.
	OldManagerURL   string
	MoveCode        string
	EnrollmentToken string
	EnvironmentName string
}
