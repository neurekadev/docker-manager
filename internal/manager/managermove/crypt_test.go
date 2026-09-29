package managermove

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
)

func sealStream(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := newSealWriter(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	// Written in odd pieces: chunking does not depend on write sizes.
	for len(plain) > 0 {
		n := min(len(plain), 7777)
		if _, err := w.Write(plain[:n]); err != nil {
			t.Fatal(err)
		}
		plain = plain[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openStream(key, sealed []byte) ([]byte, error) {
	r, err := newOpenReader(bytes.NewReader(sealed), key)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// TestPackageEncryptionRoundTrip: streams of every size around the chunk
// boundaries decrypt to what was written; two streams of the same bytes
// differ (random nonce prefix).
func TestPackageEncryptionRoundTrip(t *testing.T) {
	code, _ := authsep.MintMoveCode("move-1")
	key, err := packageKey(code.Token, "move-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, streamChunk - 1, streamChunk, streamChunk + 1, 3*streamChunk + 5} {
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		sealed := sealStream(t, key, plain)
		got, err := openStream(key, sealed)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: %v (got %d bytes)", size, err, len(got))
		}
		if size >= 64 && bytes.Contains(sealed, plain) {
			t.Fatalf("size %d: the plaintext is in the stream", size)
		}
	}
	plain := bytes.Repeat([]byte("same"), 1000)
	if bytes.Equal(sealStream(t, key, plain), sealStream(t, key, plain)) {
		t.Fatal("two streams of the same bytes are identical")
	}
}

// TestPackageEncryptionTamper: a changed byte, a stream cut at a chunk
// boundary or inside a chunk, reordered chunks, trailing data, another
// move's key and another code are all refused as a damaged package.
func TestPackageEncryptionTamper(t *testing.T) {
	code, _ := authsep.MintMoveCode("move-1")
	key, _ := packageKey(code.Token, "move-1")
	plain := make([]byte, 3*streamChunk+100)
	_, _ = rand.Read(plain)
	sealed := sealStream(t, key, plain)
	chunk := streamChunk + 16
	flip := func(i int) []byte {
		b := bytes.Clone(sealed)
		b[i] ^= 1
		return b
	}
	swapped := bytes.Clone(sealed)
	copy(swapped[headerLen:headerLen+chunk], sealed[headerLen+chunk:headerLen+2*chunk])
	copy(swapped[headerLen+chunk:headerLen+2*chunk], sealed[headerLen:headerLen+chunk])
	otherMove, _ := packageKey(code.Token, "move-2")
	otherCode, _ := authsep.MintMoveCode("move-1")
	otherKey, _ := packageKey(otherCode.Token, "move-1")
	for name, c := range map[string]struct {
		key, stream []byte
	}{
		"changed header":   {key, flip(3)},
		"changed nonce":    {key, flip(len(packageMagic) + 2)},
		"changed byte":     {key, flip(headerLen + chunk + 10)},
		"changed tag":      {key, flip(len(sealed) - 1)},
		"cut at a chunk":   {key, sealed[:headerLen+3*chunk]},
		"cut inside":       {key, sealed[:len(sealed)-50]},
		"header only":      {key, sealed[:headerLen]},
		"reordered chunks": {key, swapped},
		"trailing data":    {key, append(bytes.Clone(sealed), 0)},
		"another move":     {otherMove, sealed},
		"another code":     {otherKey, sealed},
	} {
		if _, err := openStream(c.key, c.stream); !errors.Is(err, errPackage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := openStream(key, []byte("plain tar")); !errors.Is(err, errPackage) {
		t.Errorf("not a stream: %v", err)
	}
}
