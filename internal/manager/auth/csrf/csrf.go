// Package csrf rejects cross-origin browser requests to /api/v1 (#16, #18)
// with Go's net/http CrossOriginProtection: unsafe methods (POST, PUT,
// PATCH, DELETE) must carry Sec-Fetch-Site: same-origin/none, or an Origin
// equal to the request's Host or to DOCKYARD_PUBLIC_URL. The public origin
// is trusted explicitly because a reverse proxy may forward a different
// Host (#27). Requests without either header are not from a browser (curl,
// scripts) and pass; they cannot ride a victim's cookie.
//
// Bearer-token requests (#31) are exempt: they are not authenticated by
// ambient cookies, and the session middleware ignores cookies on them.
// SameSite=Strict on the session cookie is the second, independent layer.
// WebSocket upgrades (GET) are origin-checked by server/ws.
package csrf

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/neurekadev/dockyard/internal/manager/authsep"
)

// ErrCrossOrigin is returned for rejected requests.
var ErrCrossOrigin = errors.New("csrf: cross-origin request rejected")

// Guard checks requests.
type Guard struct {
	cop *http.CrossOriginProtection
}

// New returns a guard trusting publicURL's origin.
func New(publicURL *url.URL) (*Guard, error) {
	cop := http.NewCrossOriginProtection()
	if publicURL != nil && publicURL.Host != "" {
		if err := cop.AddTrustedOrigin(publicURL.Scheme + "://" + publicURL.Host); err != nil {
			return nil, fmt.Errorf("csrf: trust %s: %w", publicURL, err)
		}
	}
	return &Guard{cop: cop}, nil
}

// Check returns ErrCrossOrigin (wrapping the reason) when r is a
// cross-origin browser request with an unsafe method.
func (g *Guard) Check(r *http.Request) error {
	if _, ok := authsep.BearerToken(r.Header); ok {
		return nil
	}
	if err := g.cop.Check(r); err != nil {
		return fmt.Errorf("%w: %w", ErrCrossOrigin, err)
	}
	return nil
}
