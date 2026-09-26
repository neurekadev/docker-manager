package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func payload(n int) []byte {
	r := rand.New(rand.NewPCG(1, uint64(n))) //nolint:gosec // deterministic test data
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.IntN(256))
	}
	return b
}

func frame(t *testing.T, data []byte, chunk int) ([]byte, Summary) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, chunk)
	// Write in odd pieces.
	for p := data; len(p) > 0; {
		k := min(len(p), 7777)
		if _, err := w.Write(p[:k]); err != nil {
			t.Fatal(err)
		}
		p = p[k:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.Summary()
}

func TestRoundTripAndSummaries(t *testing.T) {
	for _, n := range []int{0, 1, 1000, DefaultChunk, DefaultChunk + 1, 3*DefaultChunk + 17} {
		data := payload(n)
		framed, ws := frame(t, data, 0)
		want := sha256.Sum256(data)
		if ws.Bytes != int64(n) || ws.SHA256 != hex.EncodeToString(want[:]) {
			t.Fatalf("n=%d writer summary %+v", n, ws)
		}
		r := NewReader(bytes.NewReader(framed))
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, data) || !r.Done() {
			t.Fatalf("n=%d read %d bytes, err %v, done %v", n, len(got), err, r.Done())
		}
		if r.Summary() != ws {
			t.Fatalf("n=%d reader summary %+v, writer %+v", n, r.Summary(), ws)
		}
		// The verifier accepts any split of the framed stream.
		v := NewVerifier()
		for p := framed; len(p) > 0; {
			k := min(len(p), 1+len(p)%13)
			if _, err := v.Write(p[:k]); err != nil {
				t.Fatalf("n=%d verifier: %v", n, err)
			}
			p = p[k:]
		}
		if err := v.Close(); err != nil || v.Summary() != ws {
			t.Fatalf("n=%d verifier close %v summary %+v", n, err, v.Summary())
		}
	}
}

// TestCorruptionIsDetected flips one byte at many offsets: the reader never
// returns the corrupted chunk's bytes and fails; the verifier fails too.
func TestCorruptionIsDetected(t *testing.T) {
	data := payload(3*4096 + 5)
	framed, _ := frame(t, data, 4096)
	for off := 0; off < len(framed); off += 97 {
		bad := bytes.Clone(framed)
		bad[off] ^= 0x5a
		r := NewReader(bytes.NewReader(bad))
		got, err := io.ReadAll(r)
		if err == nil {
			t.Fatalf("offset %d: corruption not detected", off)
		}
		if !errors.Is(err, ErrChecksum) && !errors.Is(err, ErrFormat) {
			t.Fatalf("offset %d: unexpected error %v", off, err)
		}
		// Everything returned before the failure is a verified prefix.
		if !bytes.Equal(got, data[:len(got)]) {
			t.Fatalf("offset %d: reader returned unverified bytes", off)
		}
		v := NewVerifier()
		_, werr := v.Write(bad)
		if werr == nil && v.Close() == nil {
			t.Fatalf("offset %d: verifier accepted a corrupted stream", off)
		}
	}
}

func TestTruncationAndTrailingData(t *testing.T) {
	framed, _ := frame(t, payload(10000), 4096)
	for _, cut := range []int{0, 3, len(Magic) + 2, 100, len(framed) - 1} {
		_, err := io.ReadAll(NewReader(bytes.NewReader(framed[:cut])))
		if !errors.Is(err, ErrFormat) {
			t.Errorf("cut %d: error %v, want ErrFormat", cut, err)
		}
		v := NewVerifier()
		_, _ = v.Write(framed[:cut])
		if !errors.Is(v.Close(), ErrFormat) {
			t.Errorf("cut %d: verifier accepted a truncated stream", cut)
		}
	}
	extra := append(bytes.Clone(framed), 'x')
	if _, err := io.ReadAll(NewReader(bytes.NewReader(extra))); !errors.Is(err, ErrFormat) {
		t.Errorf("trailing data: %v", err)
	}
	v := NewVerifier()
	if _, err := v.Write(extra); !errors.Is(err, ErrFormat) {
		t.Errorf("verifier trailing data: %v", err)
	}
}

// TestTrailerMismatch: a stream whose chunks are intact but whose trailer
// lies about the whole payload fails at the end.
func TestTrailerMismatch(t *testing.T) {
	framed, _ := frame(t, payload(5000), 4096)
	bad := bytes.Clone(framed)
	bad[len(bad)-trailerSize+8] ^= 1 // first byte of the whole-payload SHA-256
	if _, err := io.ReadAll(NewReader(bytes.NewReader(bad))); !errors.Is(err, ErrChecksum) {
		t.Fatalf("reader: %v", err)
	}
	v := NewVerifier()
	if _, err := v.Write(bad); !errors.Is(err, ErrChecksum) {
		t.Fatalf("verifier: %v", err)
	}
}

func TestOversizedChunkRefused(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(Magic)
	buf.Write([]byte{0, 0x10, 0, 0}) // 1 MiB chunk
	if _, err := io.ReadAll(NewReader(&buf)); !errors.Is(err, ErrFormat) {
		t.Fatalf("reader: %v", err)
	}
	v := NewVerifier()
	if _, err := v.Write(append([]byte(Magic), 0, 0x10, 0, 0)); !errors.Is(err, ErrFormat) {
		t.Fatalf("verifier: %v", err)
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "0": 0, "2048": 2048, "50MB": 50e6, "100MiB/s": 100 << 20, "1.5GB": 1.5e9,
		"64 kib": 64 << 10, "10mb/s": 10e6} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("ParseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"-1", "abc", "10 TB", "10mbit", "100", "1e999"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) accepted", bad)
		}
	}
}

// TestLimiterCapsRate moves 1 MiB through a 256 KiB/s limiter on a fake
// clock: it takes (1 MiB - burst) / rate of fake time, and no transfer
// completes early.
func TestLimiterCapsRate(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	l := NewLimiter(clk, 256<<10)
	if l.Rate() != 256<<10 {
		t.Fatal(l.Rate())
	}
	ctx := testutil.Context(t)
	start := clk.Now()
	err := testutil.DriveClock(ctx, clk, 50*time.Millisecond, func() error {
		for range 16 {
			if err := l.Wait(ctx, 64<<10); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := clk.Now().Sub(start)
	// The bucket starts full (64 KiB): 960 KiB at 256 KiB/s = 3.75 s.
	if elapsed < 3750*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("1 MiB took %v of fake time, want ~3.75s", elapsed)
	}
}

func TestNilLimiterDoesNotWait(t *testing.T) {
	var l *Limiter
	if err := l.Wait(testutil.Context(t), 1<<30); err != nil || l.Rate() != 0 {
		t.Fatal(err)
	}
	if NewLimiter(nil, 0) != nil {
		t.Fatal("zero rate must not limit")
	}
}
