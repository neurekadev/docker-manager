package migrations

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/streammux/muxtest"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// relayFixture is a fake source agent streaming size bytes of framed data
// and a fake destination agent consuming them (optionally gated), each on
// its own in-memory session.
type relayFixture struct {
	size     int
	written  atomic.Int64 // payload bytes the source has written
	consumed atomic.Int64 // payload bytes the destination has read
	maxLead  atomic.Int64
	gate     chan struct{} // closed: the destination reads freely
	lieSum   string        // the source reports this checksum instead
	src, dst *muxtest.Pipe
}

func (f *relayFixture) lead() {
	if l := f.written.Load() - f.consumed.Load(); l > f.maxLead.Load() {
		f.maxLead.Store(l)
	}
}

func newRelayFixture(t *testing.T, size int) *relayFixture {
	f := &relayFixture{size: size, gate: make(chan struct{})}
	f.src = muxtest.New(t, map[string]muxtest.Handler{protocol.StreamMigrationSend: func(_ context.Context, s *streammux.Stream) error {
		rng := rand.New(rand.NewPCG(7, uint64(size))) //nolint:gosec // test data
		w := transfer.NewWriter(countingWriter{s, f}, 0)
		buf := make([]byte, 32<<10)
		for sent := 0; sent < size; {
			n := min(len(buf), size-sent)
			for i := range buf[:n] {
				buf[i] = byte(rng.IntN(256))
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			sent += n
		}
		if err := w.Close(); err != nil {
			return err
		}
		sum := w.Summary()
		if f.lieSum != "" {
			sum.SHA256 = f.lieSum
		}
		return s.CloseWithResult(protocol.MigrationPartResult{Bytes: sum.Bytes, SHA256: sum.SHA256, Chunks: sum.Chunks})
	}})
	f.dst = muxtest.New(t, map[string]muxtest.Handler{protocol.StreamMigrationReceive: func(_ context.Context, s *streammux.Stream) error {
		r := transfer.NewReader(s)
		buf := make([]byte, 16<<10)
		for {
			<-f.gate
			n, err := r.Read(buf)
			f.consumed.Add(int64(n))
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		sum := r.Summary()
		return s.CloseWithResult(protocol.MigrationPartResult{Bytes: sum.Bytes, SHA256: sum.SHA256, Chunks: sum.Chunks})
	}})
	return f
}

// countingWriter counts what the source handed to its stream (framing
// included) and checks the lead over the destination after every write.
type countingWriter struct {
	s *streammux.Stream
	f *relayFixture
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.s.Write(p)
	c.f.written.Add(int64(n))
	c.f.lead()
	return n, err
}

func (f *relayFixture) open(t *testing.T, ctx context.Context) (src, dst *streammux.Stream) {
	t.Helper()
	dst, err := f.dst.Open(ctx, protocol.StreamMigrationReceive, protocol.MigrationReceiveInput{MigrationID: "x", Part: protocol.PartProject}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	src, err = f.src.Open(ctx, protocol.StreamMigrationSend, protocol.MigrationSendInput{MigrationID: "x", Part: protocol.PartProject}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return src, dst
}

// TestRelayBackpressureBoundsMemory: with the destination not reading,
// the source runs ahead by at most the two stream windows and the relay's
// copy buffer, however large the part: the manager holds at most one
// window plus its buffer and nothing on disk. Released, the part completes
// with matching checksums on all three parties.
func TestRelayBackpressureBoundsMemory(t *testing.T) {
	const size = 16 << 20
	ctx := testutil.Context(t)
	f := newRelayFixture(t, size)
	src, dst := f.open(t, ctx)
	type out struct {
		res RelayResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := Relay(ctx, src, dst, RelayOptions{})
		done <- out{res, err}
	}()
	// The source fills the pipeline without the destination reading.
	for f.written.Load() < protocol.StreamWindow {
		if ctx.Err() != nil {
			t.Fatal("the source never filled a window")
		}
		runtime.Gosched()
	}
	bound := int64(2*protocol.StreamWindow + DefaultRelayBuffer + 2*protocol.MaxChunk)
	if l := f.maxLead.Load(); l > bound {
		t.Fatalf("the source ran %d bytes ahead of a stalled destination (bound %d)", l, bound)
	}
	close(f.gate)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if l := f.maxLead.Load(); l > bound {
		t.Fatalf("lead %d exceeded the bound %d", l, bound)
	}
	if r.res.Relayed.Bytes != size || r.res.Source.SHA256 != r.res.Relayed.SHA256 || r.res.Destination.SHA256 != r.res.Relayed.SHA256 ||
		f.consumed.Load() != size {
		t.Fatalf("result %+v consumed %d", r.res, f.consumed.Load())
	}
}

// TestRelayBandwidthCap: a 2 MiB part through a 512 KiB/s cap takes about
// four seconds of (fake) time and never finishes early.
func TestRelayBandwidthCap(t *testing.T) {
	const size, rate = 2 << 20, 512 << 10
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	f := newRelayFixture(t, size)
	close(f.gate)
	src, dst := f.open(t, ctx)
	start := clk.Now()
	var res RelayResult
	err := testutil.DriveClock(ctx, clk, 50*time.Millisecond, func() error {
		var err error
		res, err = Relay(ctx, src, dst, RelayOptions{Limiter: transfer.NewLimiter(clk, rate)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := clk.Now().Sub(start)
	// The framed stream is slightly larger than the payload; the bucket
	// starts with a quarter second (128 KiB).
	minimum := time.Duration(float64(size-rate/4) / rate * float64(time.Second))
	if elapsed < minimum || elapsed > minimum+time.Second {
		t.Fatalf("relayed %d bytes in %v of fake time, want about %v", res.StreamBytes, elapsed, minimum)
	}
}

// TestRelayDetectsDisagreement: a source whose reported checksum differs
// from the bytes it sent fails the part (three-way comparison).
func TestRelayDetectsDisagreement(t *testing.T) {
	ctx := testutil.Context(t)
	f := newRelayFixture(t, 300_000)
	close(f.gate)
	f.lieSum = "0000000000000000000000000000000000000000000000000000000000000000"
	src, dst := f.open(t, ctx)
	_, err := Relay(ctx, src, dst, RelayOptions{})
	var pe *PartError
	if !errors.Is(err, ErrChecksumMismatch) || !errors.As(err, &pe) || pe.Code != protocol.CodeDigestMismatch {
		t.Fatalf("error %v", err)
	}
}

// TestRelayAbortsOnLostSession: a source session that ends mid-part fails
// the part as a lost session (retried once the agent is back) and aborts
// the destination's stream.
func TestRelayAbortsOnLostSession(t *testing.T) {
	ctx := testutil.Context(t)
	f := newRelayFixture(t, 8<<20)
	src, dst := f.open(t, ctx)
	done := make(chan error, 1)
	go func() {
		_, err := Relay(ctx, src, dst, RelayOptions{})
		done <- err
	}()
	for f.written.Load() < protocol.StreamWindow/2 {
		if ctx.Err() != nil {
			t.Fatal("the source never started")
		}
		runtime.Gosched()
	}
	f.src.Close()
	close(f.gate)
	err := <-done
	var pe *PartError
	if !errors.As(err, &pe) || !pe.SessionLost() || pe.Side != "source" {
		t.Fatalf("error %v", err)
	}
	select {
	case <-dst.Done():
	case <-ctx.Done():
		t.Fatal("the destination stream was not aborted")
	}
}
