package protocol

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// WebSocket close codes of the agent session (docs/internal/protocol/agent-v1.md).
// Application codes mirror the HTTP status they correspond to (4000+status).
const (
	// CloseNormal: orderly shutdown by either side (agent stopping).
	CloseNormal = websocket.StatusNormalClosure // 1000
	// CloseGoingAway: the manager is shutting down; reconnect with backoff.
	CloseGoingAway = websocket.StatusGoingAway // 1001
	// CloseTooLarge: a frame exceeded MaxFrameSize.
	CloseTooLarge = websocket.StatusMessageTooBig // 1009
	// CloseInternal: unexpected failure; reconnect with backoff.
	CloseInternal = websocket.StatusInternalError // 1011
	// CloseProtocolError: invalid frame, unexpected frame type for the
	// session state, or hello missing/late. Reconnect with backoff.
	CloseProtocolError websocket.StatusCode = 4400
	// CloseUnauthorized: the credential stopped being valid mid-session
	// (expired rotation). Do not retry with the same credential.
	CloseUnauthorized websocket.StatusCode = 4401
	// CloseRevoked: the agent was removed or its credential revoked. Stop
	// reconnecting; the agent needs a new enrollment.
	CloseRevoked websocket.StatusCode = 4403
	// CloseHeartbeatTimeout: nothing received within HeartbeatTimeout.
	CloseHeartbeatTimeout websocket.StatusCode = 4408
	// CloseReplaced: a newer session with the same credential took over
	// (the live agent already runs that session). Do not reconnect: a second
	// process sharing the credential must not fight the first one.
	CloseReplaced websocket.StatusCode = 4409
	// CloseManagerSuperseded: sent by the agent after refusing
	// manager.identity: the manager's generation is lower than the highest
	// the agent has seen (an old manager after a move,
	// docs/internal/architecture/manager-move.md). The agent reconnects
	// with backoff and keeps its credential.
	CloseManagerSuperseded websocket.StatusCode = 4421
	// CloseVersionUnsupported: the agent version is outside the window
	// (CheckAgentVersion) or the protocol was not negotiated. Upgrade first.
	CloseVersionUnsupported websocket.StatusCode = 4426
)

// ReconnectAllowed reports whether an agent should reconnect (with
// backoff) after the session closed with code.
func ReconnectAllowed(code websocket.StatusCode) bool {
	switch code {
	case CloseRevoked, CloseUnauthorized, CloseReplaced, CloseVersionUnsupported:
		return false
	}
	return true
}

// ReadFrame reads one text message from c and decodes it. It sets the
// connection read limit to MaxFrameSize so oversized frames close the
// connection instead of being buffered.
func ReadFrame(ctx context.Context, c *websocket.Conn) (*Frame, error) {
	c.SetReadLimit(MaxFrameSize)
	typ, b, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("%w: binary message", ErrInvalidFrame)
	}
	return Decode(b)
}

// WriteFrame encodes f and writes it as one text message.
func WriteFrame(ctx context.Context, c *websocket.Conn, f *Frame) error {
	b, err := Encode(f)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}
