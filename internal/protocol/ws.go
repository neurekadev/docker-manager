package protocol

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

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
