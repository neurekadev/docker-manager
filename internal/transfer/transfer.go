// Package transfer is the checksummed framing of migration data (#35): the
// byte stream a source agent sends through the manager to a destination
// agent (docs/internal/protocol/agent-v1.md, "Migration transfer relay").
//
// The payload (a tar archive of a project directory or volume, or an Engine
// image archive) is cut into chunks. Every chunk carries its own SHA-256,
// and a trailer carries the payload's total length, whole-payload SHA-256
// and chunk count:
//
//	magic   "DYXFER01"
//	chunk   uint32 BE length (1..MaxChunk) | payload bytes | SHA-256(payload)
//	...
//	end     uint32 0 | uint64 BE total | SHA-256(all payload) | uint64 BE chunks
//
// Writer frames a payload; Reader returns a chunk's bytes only after its
// checksum matched (a corrupted chunk never reaches the extractor) and
// io.EOF only after the trailer matched; Verifier checks a framed stream
// incrementally without buffering (the manager's relay). The stream
// layer (internal/streammux) additionally verifies the framed bytes'
// length and SHA-256 end to end.
package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Magic starts every framed stream.
const Magic = "DYXFER01"

// Chunk sizes.
const (
	// MaxChunk bounds one chunk's payload (a chunk plus its framing fits
	// into one 256 KiB stream_data frame).
	MaxChunk = 192 << 10
	// DefaultChunk is the Writer's default chunk size.
	DefaultChunk = 128 << 10
)

const (
	lenSize     = 4
	sumSize     = sha256.Size
	trailerSize = 8 + sumSize + 8
)

// Errors.
var (
	// ErrChecksum: a chunk or the whole payload does not match its SHA-256.
	ErrChecksum = errors.New("transfer: checksum mismatch")
	// ErrFormat: the stream is not a well-formed framed stream (bad magic,
	// oversized chunk, trailing bytes, truncated).
	ErrFormat = errors.New("transfer: malformed stream")
)

// Summary describes a framed payload.
type Summary struct {
	// Bytes is the payload length (without framing).
	Bytes int64
	// SHA256 is the hex SHA-256 of the whole payload.
	SHA256 string
	// Chunks is the number of chunks.
	Chunks int64
}

// Writer frames a payload written to it into w. Close writes the trailer;
// the Writer does not close w.
type Writer struct {
	w      io.Writer
	size   int
	buf    []byte
	whole  hash.Hash
	total  int64
	chunks int64
	err    error
	closed bool
}

// NewWriter returns a Writer with chunk size n (0: DefaultChunk; at most
// MaxChunk). The magic is written with the first chunk or at Close.
func NewWriter(w io.Writer, n int) *Writer {
	if n <= 0 {
		n = DefaultChunk
	}
	n = min(n, MaxChunk)
	return &Writer{w: w, size: n, whole: sha256.New()}
}

func (w *Writer) start() error {
	if w.chunks == 0 && w.total == 0 && w.buf == nil {
		w.buf = make([]byte, 0, w.size)
		if _, err := io.WriteString(w.w, Magic); err != nil {
			return err
		}
	}
	return nil
}

// Write buffers p and emits full chunks.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("transfer: write after close")
	}
	if err := w.start(); err != nil {
		w.err = err
		return 0, err
	}
	n := 0
	for len(p) > 0 {
		k := min(len(p), w.size-len(w.buf))
		w.buf = append(w.buf, p[:k]...)
		p, n = p[k:], n+k
		if len(w.buf) == w.size {
			if err := w.flush(); err != nil {
				w.err = err
				return n, err
			}
		}
	}
	return n, nil
}

func (w *Writer) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	sum := sha256.Sum256(w.buf)
	var hdr [lenSize]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(w.buf))) //nolint:gosec // bounded by MaxChunk
	for _, b := range [][]byte{hdr[:], w.buf, sum[:]} {
		if _, err := w.w.Write(b); err != nil {
			return err
		}
	}
	w.whole.Write(w.buf)
	w.total += int64(len(w.buf))
	w.chunks++
	w.buf = w.buf[:0]
	return nil
}

// Close flushes the last chunk and writes the trailer.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return nil
	}
	if err := w.start(); err != nil {
		w.err = err
		return err
	}
	if err := w.flush(); err != nil {
		w.err = err
		return err
	}
	w.closed = true
	var end [lenSize + trailerSize]byte
	binary.BigEndian.PutUint64(end[lenSize:], uint64(w.total)) //nolint:gosec // a length
	copy(end[lenSize+8:], w.whole.Sum(nil))
	binary.BigEndian.PutUint64(end[lenSize+8+sumSize:], uint64(w.chunks)) //nolint:gosec // a count
	_, err := w.w.Write(end[:])
	if err != nil {
		w.err = err
	}
	return err
}

// Summary returns the framed payload's summary (complete after Close).
func (w *Writer) Summary() Summary {
	return Summary{Bytes: w.total, SHA256: hex.EncodeToString(w.whole.Sum(nil)), Chunks: w.chunks}
}

// Reader reads the payload of a framed stream, verifying each chunk before
// returning its bytes and the trailer before returning io.EOF.
type Reader struct {
	r      io.Reader
	magic  bool
	buf    []byte
	off    int
	whole  hash.Hash
	total  int64
	chunks int64
	done   bool
	err    error
}

// NewReader returns a Reader of the framed stream r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r, whole: sha256.New()}
}

func (r *Reader) fail(err error) error {
	if r.err == nil {
		r.err = err
	}
	return r.err
}

func (r *Reader) full(p []byte) error {
	if _, err := io.ReadFull(r.r, p); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: truncated stream", ErrFormat)
		}
		return err
	}
	return nil
}

// next reads and verifies the next chunk (or the trailer).
func (r *Reader) next() error {
	if !r.magic {
		var m [len(Magic)]byte
		if err := r.full(m[:]); err != nil {
			return err
		}
		if string(m[:]) != Magic {
			return fmt.Errorf("%w: bad magic", ErrFormat)
		}
		r.magic = true
	}
	var hdr [lenSize]byte
	if err := r.full(hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		var t [trailerSize]byte
		if err := r.full(t[:]); err != nil {
			return err
		}
		total := binary.BigEndian.Uint64(t[:8])
		chunks := binary.BigEndian.Uint64(t[8+sumSize:])
		if total != uint64(r.total) || chunks != uint64(r.chunks) { //nolint:gosec // non-negative counters
			return fmt.Errorf("%w: trailer reports %d bytes in %d chunks, received %d in %d", ErrChecksum, total, chunks, r.total, r.chunks)
		}
		if !bytes.Equal(t[8:8+sumSize], r.whole.Sum(nil)) {
			return fmt.Errorf("%w: whole-payload SHA-256", ErrChecksum)
		}
		// Nothing may follow the trailer, and the underlying stream must
		// end cleanly (a stream-level verification failure surfaces here).
		var extra [1]byte
		k, err := r.r.Read(extra[:])
		for k == 0 && err == nil {
			k, err = r.r.Read(extra[:])
		}
		if k > 0 {
			return fmt.Errorf("%w: data after the trailer", ErrFormat)
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		r.done = true
		return nil
	}
	if n > MaxChunk {
		return fmt.Errorf("%w: chunk of %d bytes exceeds %d", ErrFormat, n, MaxChunk)
	}
	if cap(r.buf) < int(n)+sumSize {
		r.buf = make([]byte, int(n)+sumSize)
	}
	r.buf = r.buf[:int(n)+sumSize]
	if err := r.full(r.buf); err != nil {
		return err
	}
	sum := sha256.Sum256(r.buf[:n])
	if !bytes.Equal(sum[:], r.buf[n:]) {
		return fmt.Errorf("%w: chunk %d", ErrChecksum, r.chunks+1)
	}
	r.buf = r.buf[:n]
	r.off = 0
	r.whole.Write(r.buf)
	r.total += int64(n)
	r.chunks++
	return nil
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	for r.off >= len(r.buf) {
		if r.done {
			return 0, io.EOF
		}
		if err := r.next(); err != nil {
			return 0, r.fail(err)
		}
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	return n, nil
}

// Done reports whether the trailer was read and verified.
func (r *Reader) Done() bool { return r.done }

// Summary returns what was read so far (complete once Done).
func (r *Reader) Summary() Summary {
	return Summary{Bytes: r.total, SHA256: hex.EncodeToString(r.whole.Sum(nil)), Chunks: r.chunks}
}

// Verifier checks a framed stream written to it in arbitrary pieces,
// without buffering payload: the manager's relay feeds it every byte it
// forwards. Write fails at the first malformed or mismatching byte;
// Close fails unless the trailer was complete and nothing followed it.
type Verifier struct {
	state   int // 0 magic, 1 length, 2 payload, 3 chunk sum, 4 trailer, 5 done
	hdr     []byte
	want    int // remaining payload bytes of the current chunk
	chunk   hash.Hash
	whole   hash.Hash
	sum     []byte
	trailer []byte
	total   int64
	chunks  int64
	err     error
}

// NewVerifier returns a Verifier.
func NewVerifier() *Verifier {
	return &Verifier{chunk: sha256.New(), whole: sha256.New()}
}

// Write consumes p.
func (v *Verifier) Write(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	n := len(p)
	for len(p) > 0 {
		switch v.state {
		case 0:
			k := min(len(p), len(Magic)-len(v.hdr))
			v.hdr = append(v.hdr, p[:k]...)
			p = p[k:]
			if len(v.hdr) == len(Magic) {
				if string(v.hdr) != Magic {
					return 0, v.failf(ErrFormat, "bad magic")
				}
				v.hdr, v.state = v.hdr[:0], 1
			}
		case 1:
			k := min(len(p), lenSize-len(v.hdr))
			v.hdr = append(v.hdr, p[:k]...)
			p = p[k:]
			if len(v.hdr) == lenSize {
				l := binary.BigEndian.Uint32(v.hdr)
				v.hdr = v.hdr[:0]
				switch {
				case l == 0:
					v.state = 4
				case l > MaxChunk:
					return 0, v.failf(ErrFormat, "chunk of %d bytes exceeds %d", l, MaxChunk)
				default:
					v.want, v.state = int(l), 2
					v.chunk.Reset()
				}
			}
		case 2:
			k := min(len(p), v.want)
			v.chunk.Write(p[:k])
			v.whole.Write(p[:k])
			v.total += int64(k)
			v.want -= k
			p = p[k:]
			if v.want == 0 {
				v.state = 3
			}
		case 3:
			k := min(len(p), sumSize-len(v.sum))
			v.sum = append(v.sum, p[:k]...)
			p = p[k:]
			if len(v.sum) == sumSize {
				v.chunks++
				if !bytes.Equal(v.sum, v.chunk.Sum(nil)) {
					return 0, v.failf(ErrChecksum, "chunk %d", v.chunks)
				}
				v.sum, v.state = v.sum[:0], 1
			}
		case 4:
			k := min(len(p), trailerSize-len(v.trailer))
			v.trailer = append(v.trailer, p[:k]...)
			p = p[k:]
			if len(v.trailer) == trailerSize {
				total := binary.BigEndian.Uint64(v.trailer[:8])
				chunks := binary.BigEndian.Uint64(v.trailer[8+sumSize:])
				if total != uint64(v.total) || chunks != uint64(v.chunks) { //nolint:gosec // non-negative counters
					return 0, v.failf(ErrChecksum, "trailer reports %d bytes in %d chunks, relayed %d in %d", total, chunks, v.total, v.chunks)
				}
				if !bytes.Equal(v.trailer[8:8+sumSize], v.whole.Sum(nil)) {
					return 0, v.failf(ErrChecksum, "whole-payload SHA-256")
				}
				v.state = 5
			}
		case 5:
			return 0, v.failf(ErrFormat, "data after the trailer")
		}
	}
	return n, nil
}

func (v *Verifier) failf(base error, format string, args ...any) error {
	v.err = fmt.Errorf("%w: %s", base, fmt.Sprintf(format, args...))
	return v.err
}

// Close reports whether the stream ended exactly after a verified trailer.
func (v *Verifier) Close() error {
	if v.err != nil {
		return v.err
	}
	if v.state != 5 {
		return v.failf(ErrFormat, "truncated stream")
	}
	return nil
}

// Summary returns the verified payload's summary (complete after Close).
func (v *Verifier) Summary() Summary {
	return Summary{Bytes: v.total, SHA256: hex.EncodeToString(v.whole.Sum(nil)), Chunks: v.chunks}
}
