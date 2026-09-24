package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/containerd/platforms"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// CreateExec creates an exec instance in a running container and returns
// its ID.
func (c *Client) CreateExec(ctx context.Context, containerID string, spec ExecSpec) (string, error) {
	const op = "exec.create"
	if len(spec.Cmd) == 0 {
		return "", newError(op, CodeInvalidArgument, "exec needs a command")
	}
	ctx, cancel := c.bound(ctx)
	defer cancel()
	o := client.ExecCreateOptions{
		User: spec.User, TTY: spec.Tty, AttachStdin: spec.AttachStdin, AttachStdout: true, AttachStderr: true,
		Env: spec.Env, WorkingDir: spec.WorkingDir, Cmd: spec.Cmd,
	}
	if spec.Tty && (spec.Height > 0 || spec.Width > 0) {
		o.ConsoleSize = client.ConsoleSize{Height: spec.Height, Width: spec.Width}
	}
	res, err := c.api.ExecCreate(ctx, containerID, o)
	if err != nil {
		return "", wrap(op, err)
	}
	return res.ID, nil
}

// ExecIO connects an exec session. Stdin may be nil. Without a TTY the
// Engine multiplexes stdout and stderr; they are demultiplexed into Stdout
// and Stderr (Stderr nil discards).
type ExecIO struct {
	Tty    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// AttachExec starts the exec instance and copies its streams until the
// process exits or ctx ends (which closes the connection). When Stdin
// reaches EOF the write side is closed so the process sees end of input.
func (c *Client) AttachExec(ctx context.Context, execID string, stdio ExecIO) error {
	const op = "exec.attach"
	resp, err := c.api.ExecAttach(ctx, execID, client.ExecAttachOptions{TTY: stdio.Tty})
	if err != nil {
		return wrap(op, err)
	}
	conn := resp.HijackedResponse
	stop := context.AfterFunc(ctx, conn.Close)
	defer stop()
	defer conn.Close()

	if stdio.Stdin != nil {
		go func() {
			_, _ = io.Copy(conn.Conn, stdio.Stdin)
			if cw, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}
	stdout, stderr := stdio.Stdout, stdio.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if stdio.Tty {
		_, err = io.Copy(stdout, conn.Reader)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, conn.Reader)
	}
	if ctx.Err() != nil {
		return wrap(op, ctx.Err())
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		return wrap(op, err)
	}
	return nil
}

// ResizeExec resizes an exec's TTY.
func (c *Client) ResizeExec(ctx context.Context, execID string, height, width uint) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ExecResize(ctx, execID, client.ExecResizeOptions{Height: height, Width: width})
	return wrap("exec.resize", err)
}

// InspectExec reports whether an exec is running and its exit code.
func (c *Client) InspectExec(ctx context.Context, execID string) (ExecStatus, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil {
		return ExecStatus{}, wrap("exec.inspect", err)
	}
	return ExecStatus{Running: res.Running, ExitCode: res.ExitCode, Pid: res.PID}, nil
}

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// parsePlatform parses "os/arch[/variant]".
func parsePlatform(s string) (ocispec.Platform, error) {
	p, err := platforms.Parse(s)
	if err != nil {
		return ocispec.Platform{}, fmt.Errorf("invalid platform %q: %w", s, err)
	}
	return p, nil
}
