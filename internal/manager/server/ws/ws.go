// Package ws is the manager's WebSocket upgrade helper, shared by the agent
// session (/agent/v1/session, #3) and browser streams such as container
// exec (#6). It makes every WebSocket behave well behind reverse proxies
// (#27):
//
//   - pre-authentication read deadlines set on the HTTP request (the
//     /agent/v1 guard) are cleared before the connection is hijacked, so the
//     session enforces its own deadlines with contexts;
//   - the read limit is bounded (default protocol.MaxFrameSize);
//   - KeepAlive pings the peer every PingInterval (default 15 s,
//     DOCKYARD_STREAM_HEARTBEAT), below common proxy idle/read timeouts
//     (nginx proxy_read_timeout defaults to 60 s), and closes connections
//     whose pong does not arrive within PongTimeout.
//
// Usage:
//
//	c, err := ws.Accept(w, r, ws.Options{Subprotocols: []string{protocol.Version}, RequireSubprotocol: true, Clock: clk})
//	if err != nil { return } // the response was written
//	defer c.CloseNow()
//	go c.KeepAlive(ctx) // needs a concurrent reader: pongs are processed by Read
//	for { typ, b, err := c.Read(ctx); ... }
package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/server/sse"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// ErrSubprotocol is returned when RequireSubprotocol is set and the client
// offered none of Options.Subprotocols.
var ErrSubprotocol = errors.New("ws: no supported subprotocol offered")

// ErrPongTimeout is returned by KeepAlive when the peer did not answer a
// ping within PongTimeout.
var ErrPongTimeout = errors.New("ws: pong timeout")

// ErrNotWebSocket is returned for a request that is not a WebSocket upgrade.
var ErrNotWebSocket = errors.New("ws: not a WebSocket upgrade request")

// Options configures Accept.
type Options struct {
	// Subprotocols are offered in preference order.
	Subprotocols []string
	// RequireSubprotocol closes connections that negotiated none.
	RequireSubprotocol bool
	// OriginPatterns allow browser Origins other than the request's Host
	// (path.Match patterns, e.g. the public host). Requests without an
	// Origin header (agents, CLI clients) are always accepted.
	OriginPatterns []string
	// ReadLimit bounds one message (default protocol.MaxFrameSize).
	ReadLimit int64
	// PingInterval is the KeepAlive period (default sse.DefaultHeartbeat).
	PingInterval time.Duration
	// PongTimeout bounds the wait for each pong (default PingInterval).
	PongTimeout time.Duration
	// Clock drives KeepAlive (default the wall clock).
	Clock clock.Clock
}

// Conn is an accepted WebSocket connection.
type Conn struct {
	*websocket.Conn
	pingInterval time.Duration
	pongTimeout  time.Duration
	clk          clock.Clock
	afterPing    func(error) // test hook
}

// IsUpgrade reports whether r asks for a WebSocket upgrade.
func IsUpgrade(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") && headerHasToken(r.Header, "Upgrade", "websocket")
}

// Accept upgrades the request. On failure the error response has been
// written (426 in the standard error shape for plain HTTP requests, 403 for
// a foreign Origin, 400 for a malformed handshake).
func Accept(w http.ResponseWriter, r *http.Request, opts Options) (*Conn, error) {
	if !IsUpgrade(r) {
		w.Header().Set("Upgrade", "websocket")
		api.WriteError(w, r, api.NewError(http.StatusUpgradeRequired, "upgrade_required", "this endpoint requires a WebSocket upgrade"))
		return nil, ErrNotWebSocket
	}
	// Deadlines set before authentication (e.g. the /agent/v1 pre-auth
	// timeout) would otherwise stay on the hijacked connection.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    opts.Subprotocols,
		OriginPatterns:  opts.OriginPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	if opts.RequireSubprotocol && c.Subprotocol() == "" {
		_ = c.Close(websocket.StatusPolicyViolation, "unsupported subprotocol")
		return nil, ErrSubprotocol
	}
	if opts.ReadLimit <= 0 {
		opts.ReadLimit = protocol.MaxFrameSize
	}
	c.SetReadLimit(opts.ReadLimit)
	if opts.PingInterval <= 0 {
		opts.PingInterval = sse.DefaultHeartbeat
	}
	if opts.PongTimeout <= 0 {
		opts.PongTimeout = opts.PingInterval
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	return &Conn{Conn: c, pingInterval: opts.PingInterval, pongTimeout: opts.PongTimeout, clk: opts.Clock}, nil
}

// KeepAlive pings the peer every PingInterval until ctx ends (returning
// ctx.Err()) or a pong does not arrive within PongTimeout (closing the
// connection and returning the ping error). Pongs are only processed while
// another goroutine reads from the connection.
func (c *Conn) KeepAlive(ctx context.Context) error {
	t := c.clk.NewTicker(c.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C():
		}
		err := c.ping(ctx)
		if c.afterPing != nil {
			c.afterPing(err)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_ = c.CloseNow()
			return err
		}
	}
}

func (c *Conn) ping(ctx context.Context) error {
	pctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := c.clk.NewTimer(c.pongTimeout)
	defer timer.Stop()
	go func() {
		select {
		case <-timer.C():
			cancel(ErrPongTimeout)
		case <-pctx.Done():
		}
	}()
	err := c.Ping(pctx)
	if err != nil && errors.Is(context.Cause(pctx), ErrPongTimeout) {
		return fmt.Errorf("%w (%s)", ErrPongTimeout, c.pongTimeout)
	}
	return err
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}
