package agents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// TestStreamsRelayThroughRealSessions runs byte streams between the real
// manager session hub and the real agent session client (#15, #8): a
// download larger than several windows, an upload whose result travels in
// the agent's final close, an agent-side failure code, an unsupported kind,
// and a session end that fails an open stream on both sides.
func TestStreamsRelayThroughRealSessions(t *testing.T) {
	f := newFixture(t)
	payload := make([]byte, 3<<20+17)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	hold := make(chan *streammux.Stream, 1)
	a := f.newAgent("ENG-S", "host-s")
	a.streams = map[string]session.StreamHandler{
		protocol.StreamFilesDownload: func(ctx context.Context, s *streammux.Stream) error {
			var in struct{ Path string }
			_ = json.Unmarshal(s.Input(), &in)
			switch in.Path {
			case "missing":
				return &session.HandlerError{Code: protocol.CodeNotFound, Message: "no such file"}
			case "hold":
				hold <- s
				<-ctx.Done()
				return ctx.Err()
			}
			_, err := s.Write(payload)
			return err
		},
		protocol.StreamFilesUpload: func(ctx context.Context, s *streammux.Stream) error {
			b, err := io.ReadAll(s)
			if err != nil {
				return err
			}
			return s.CloseWithResult(map[string]int{"size": len(b)})
		},
	}
	resp := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	a.waitState(session.StateOnline)
	hub := f.svc.Hub()
	env := resp.EnvironmentID

	st, err := hub.OpenStream(f.ctx, env, protocol.StreamFilesDownload, map[string]string{"path": "big"}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(st)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("download: %d bytes, %v", len(got), err)
	}
	_ = st.CloseWrite()

	up, err := hub.OpenStream(f.ctx, env, protocol.StreamFilesUpload, nil, streammux.OpenOptions{MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := up.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := up.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	res, err := up.Result(f.ctx)
	if err != nil || string(res) != `{"size":3145745}` {
		t.Fatalf("upload result %s %v", res, err)
	}

	st, err = hub.OpenStream(f.ctx, env, protocol.StreamFilesDownload, map[string]string{"path": "missing"}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ce *streammux.CloseError
	if _, err := io.ReadAll(st); !errors.As(err, &ce) || ce.Code != protocol.CodeNotFound {
		t.Fatalf("agent failure: %v", err)
	}

	st, err = hub.OpenStream(f.ctx, env, protocol.StreamContainerLogs, nil, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(st); !errors.As(err, &ce) || ce.Code != protocol.CodeUnsupportedStream {
		t.Fatalf("unsupported kind: %v", err)
	}

	// The session ends while a stream is open: both ends fail it.
	st, err = hub.OpenStream(f.ctx, env, protocol.StreamFilesDownload, map[string]string{"path": "hold"}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agentSide := <-hold
	a.stop()
	<-agentSide.Done()
	if _, err := io.ReadAll(st); !errors.Is(err, streammux.ErrSessionClosed) {
		t.Fatalf("manager side after session end: %v", err)
	}
	if _, err := hub.OpenStream(f.ctx, env, protocol.StreamFilesDownload, nil, streammux.OpenOptions{}); !errors.Is(err, jobs.ErrAgentOffline) {
		t.Fatalf("offline: %v", err)
	}
}
