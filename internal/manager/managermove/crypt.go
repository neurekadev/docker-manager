package managermove

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// The handoff stream is encrypted and authenticated end to end with the
// move code (the transport may be plain HTTP): XChaCha20-Poly1305 in
// chunks of streamChunk plaintext bytes (the STREAM construction). The
// stream starts with a header, packageMagic and a random 19-byte nonce
// prefix; chunk i is sealed with the nonce prefix || uint32 i (big
// endian) || last flag (1 on the final chunk, 0 before), with the header
// as associated data. The final chunk is shorter than streamChunk (it may
// be empty), so a stream cut at any point, reordered chunks or a changed
// byte fail to open. The key is HKDF-SHA256(secret, info PackageKeyInfo
// || move ID): only the holder of the code opens it, and only for this
// move.

// Package encryption parameters.
const (
	// PackageKeyInfo is the HKDF info prefix of the package key.
	PackageKeyInfo = "docker-manager-move/package/v1\x00"
	streamChunk    = 64 << 10
	noncePrefixLen = chacha20poly1305.NonceSizeX - 5
)

// packageMagic starts an encrypted handoff stream (format version 1).
var packageMagic = []byte("DMMPKG\x00\x01")

// headerLen is the stream header's length.
var headerLen = len(packageMagic) + noncePrefixLen

// errStream marks an encrypted stream that does not open (cut, damaged,
// or another key).
var errStream = errors.New("the handoff stream does not decrypt")

// packageKey derives the package key of moveID from code's secret.
func packageKey(code, moveID string) ([]byte, error) {
	_, secret, ok := codeSecretBytes(code)
	if !ok {
		return nil, errStream
	}
	return hkdf.Key(sha256.New, secret, nil, PackageKeyInfo+moveID, chacha20poly1305.KeySize)
}

func chunkNonce(prefix []byte, i uint32, last bool) []byte {
	n := make([]byte, chacha20poly1305.NonceSizeX)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[noncePrefixLen:], i)
	if last {
		n[len(n)-1] = 1
	}
	return n
}

// sealWriter encrypts a stream (newSealWriter); Close writes the final
// chunk and must be called.
type sealWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	prefix []byte
	buf    []byte
	i      uint32
	closed bool
}

func newSealWriter(w io.Writer, key []byte) (*sealWriter, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, noncePrefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	header := append(append([]byte{}, packageMagic...), prefix...)
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &sealWriter{w: w, aead: aead, header: header, prefix: prefix, buf: make([]byte, 0, streamChunk)}, nil
}

func (s *sealWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("write after close")
	}
	n := 0
	for len(p) > 0 {
		k := min(streamChunk-len(s.buf), len(p))
		s.buf = append(s.buf, p[:k]...)
		p, n = p[k:], n+k
		// A full chunk is never the last one: the final chunk is shorter.
		if len(s.buf) == streamChunk {
			if err := s.flush(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (s *sealWriter) flush(last bool) error {
	if s.i == ^uint32(0) {
		return errors.New("the handoff stream is too long")
	}
	ct := s.aead.Seal(nil, chunkNonce(s.prefix, s.i, last), s.buf, s.header)
	s.i++
	s.buf = s.buf[:0]
	_, err := s.w.Write(ct)
	return err
}

// Close writes the final (short, possibly empty) chunk.
func (s *sealWriter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.flush(true)
}

// openReader decrypts a stream written by sealWriter; a stream that is
// cut, reordered, damaged or sealed with another key fails with
// errStream (wrapped in errPackage: the transfer may be retried).
type openReader struct {
	r      io.Reader
	aead   cipher.AEAD
	header []byte
	prefix []byte
	chunk  []byte
	plain  []byte
	i      uint32
	done   bool
	err    error
}

func newOpenReader(r io.Reader, key []byte) (*openReader, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, packageError("the stream header is missing (%v)", err)
	}
	if !bytes.Equal(header[:len(packageMagic)], packageMagic) {
		return nil, packageError("the stream is not an encrypted handoff package")
	}
	return &openReader{r: r, aead: aead, header: header, prefix: header[len(packageMagic):],
		chunk: make([]byte, streamChunk+aead.Overhead())}, nil
}

func (o *openReader) Read(p []byte) (int, error) {
	for len(o.plain) == 0 {
		if o.err != nil {
			return 0, o.err
		}
		if o.done {
			return 0, io.EOF
		}
		o.next()
	}
	n := copy(p, o.plain)
	o.plain = o.plain[n:]
	return n, nil
}

// next reads and opens one chunk.
func (o *openReader) next() {
	n, err := io.ReadFull(o.r, o.chunk)
	last := false
	switch {
	case err == nil:
	case errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	case errors.Is(err, io.EOF):
		o.err = packageError("%v: the stream ended before its final chunk", errStream)
		return
	default:
		o.err = packageError("the stream broke off (%v)", err)
		return
	}
	plain, oerr := o.aead.Open(nil, chunkNonce(o.prefix, o.i, last), o.chunk[:n], o.header)
	if oerr != nil {
		o.err = packageError("%v (chunk %d)", errStream, o.i)
		return
	}
	o.i++
	o.plain = plain
	if last {
		o.done = true
		var one [1]byte
		if k, _ := io.ReadFull(o.r, one[:]); k != 0 {
			o.err = packageError("%v: data follows the final chunk", errStream)
		}
	}
}
