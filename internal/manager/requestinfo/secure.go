package requestinfo

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrInsecureOrigin is matched (errors.Is) by every CheckSecureOrigin failure.
var ErrInsecureOrigin = errors.New("insecure origin")

// Reasons reported by InsecureOriginError.
const (
	// ReasonPublicURLNotHTTPS: DOCKER_MANAGER_PUBLIC_URL is plain http outside the
	// localhost development mode.
	ReasonPublicURLNotHTTPS = "public_url_not_https"
	// ReasonRequestNotHTTPS: the request did not arrive over HTTPS (directly
	// or, per X-Forwarded-Proto, through a trusted proxy).
	ReasonRequestNotHTTPS = "request_not_https"
	// ReasonHostMismatch: the request addressed a different host than
	// DOCKER_MANAGER_PUBLIC_URL (e.g. the manager's internal address).
	ReasonHostMismatch = "host_mismatch"
)

// InsecureOriginError explains why a security-sensitive browser flow must
// not complete over this request. Explanation is safe to show to the user.
type InsecureOriginError struct {
	Reason      string
	Explanation string
}

func (e *InsecureOriginError) Error() string {
	return "insecure origin (" + e.Reason + "): " + e.Explanation
}

// Is makes errors.Is(err, ErrInsecureOrigin) true.
func (e *InsecureOriginError) Is(target error) bool { return target == ErrInsecureOrigin }

// CheckSecureOrigin decides whether a browser flow that creates credentials
// (first-run owner setup #16, and any later flow that needs a secure
// context: passkeys, Secure cookies) may complete over the request
// described by info.
//
// It passes when the request reached the configured public origin over
// HTTPS: publicURL is https, info.Scheme is https (TLS or X-Forwarded-Proto
// from a trusted proxy) and info.Host equals the public host (default ports
// normalized). The only exception is the explicit localhost development
// mode (DOCKER_MANAGER_PUBLIC_URL=http://localhost:<port>, config.LocalDevelopment),
// where plain http is accepted as long as the request addressed that host.
//
// Callers turn a failure into a 403 with code "insecure_origin" and show
// Explanation (see docs/internal/deployment.md, "First-run setup over HTTPS").
func CheckSecureOrigin(publicURL *url.URL, localDevelopment bool, info Info) error {
	if publicURL == nil || publicURL.Host == "" {
		return &InsecureOriginError{Reason: ReasonPublicURLNotHTTPS, Explanation: "DOCKER_MANAGER_PUBLIC_URL is not configured."}
	}
	public := publicURL.Scheme + "://" + publicURL.Host
	switch {
	case publicURL.Scheme == "https":
	case publicURL.Scheme == "http" && localDevelopment:
		if !sameHost(info.Host, publicURL.Host, "http") {
			return hostMismatch(public)
		}
		return nil
	default:
		return &InsecureOriginError{
			Reason: ReasonPublicURLNotHTTPS,
			Explanation: fmt.Sprintf("Docker Manager's public URL %s is not HTTPS. Passkeys, secure cookies and the installable app need HTTPS; "+
				"put Docker Manager behind a TLS-terminating reverse proxy and set DOCKER_MANAGER_PUBLIC_URL to its https:// origin. "+
				"Plain http is only allowed for http://localhost development.", public),
		}
	}
	if !info.Secure() {
		msg := fmt.Sprintf("This request did not arrive over HTTPS. Open %s to continue.", public)
		if !info.TrustedPeer {
			msg += " If you did use that address, the manager cannot see it: add your reverse proxy's IP address or network to " +
				"DOCKER_MANAGER_TRUSTED_PROXIES so the manager honors its X-Forwarded-Proto header."
		}
		return &InsecureOriginError{Reason: ReasonRequestNotHTTPS, Explanation: msg}
	}
	if !sameHost(info.Host, publicURL.Host, "https") {
		return hostMismatch(public)
	}
	return nil
}

func hostMismatch(public string) error {
	return &InsecureOriginError{
		Reason: ReasonHostMismatch,
		Explanation: fmt.Sprintf("This request addressed a different host than Docker Manager's public URL. Open %s to continue "+
			"(reverse proxies must pass the original Host header or X-Forwarded-Host).", public),
	}
}

// sameHost compares host[:port] values, treating the scheme's default port
// as equal to no port.
func sameHost(a, b, scheme string) bool {
	return normHost(a, scheme) == normHost(b, scheme)
}

func normHost(h, scheme string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return host
	}
	return net.JoinHostPort(host, port)
}
