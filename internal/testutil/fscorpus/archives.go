package fscorpus

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

// Archive is one generated archive.
type Archive struct {
	// Name is the archive's file name.
	Name string
	// Format is "zip", "tar" or "tar.gz".
	Format string
	// Hazard describes what a safe extractor must refuse or contain.
	Hazard string
	Data   []byte
	// UncompressedSize is the total inflated size of the bombs (0 otherwise).
	UncompressedSize int64
}

// epoch keeps archives byte-for-byte reproducible.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Default bomb size for the committed corpus: large enough to exercise
// ratio limits (~1000:1), small enough to commit. Generate bigger bombs
// at test time with ZipBomb / TarGzBomb.
const committedBombSize = 32 << 20

// Archives returns the committed archive corpus.
func Archives() []Archive {
	return []Archive{
		{Name: "zip-slip.zip", Format: "zip", Hazard: "entries with ../, absolute, backslash and drive-letter names escape the target", Data: ZipSlip()},
		{Name: "tar-slip.tar", Format: "tar", Hazard: "../ and absolute names; symlink to / then a file through it; symlink ../.. then a file through it; hardlinks to absolute and ../ targets; device node; setuid file", Data: TarSlip()},
		{Name: "bomb-ratio.zip", Format: "zip", Hazard: "one entry inflating 32 MiB from ~32 KiB (ratio limit)", Data: ZipBomb(committedBombSize), UncompressedSize: committedBombSize},
		{Name: "bomb-lying-size.zip", Format: "zip", Hazard: "central directory claims 16 bytes but the entry inflates to 32 MiB (trust the stream, not the header)", Data: ZipLyingSize(committedBombSize), UncompressedSize: committedBombSize},
		{Name: "bomb.tar.gz", Format: "tar.gz", Hazard: "gzip stream inflating to a 32 MiB tar", Data: TarGzBomb(committedBombSize), UncompressedSize: committedBombSize},
		{Name: "bomb-nested.zip", Format: "zip", Hazard: "zip containing 4 zip bombs of 8 MiB each (recursive extraction multiplies size)", Data: ZipNested(ZipBomb(committedBombSize/4), 4), UncompressedSize: committedBombSize},
	}
}

// ZipSlip returns a zip whose entries try to escape the extraction root.
func ZipSlip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{
		"safe.txt",
		"../evil-parent.txt",
		"../../../../../../tmp/evil-deep.txt",
		"safe/../../evil-inner.txt",
		"/tmp/evil-absolute.txt",
		"..\\evil-backslash.txt",
		"C:\\evil-drive.txt",
		"C:/evil-drive-slash.txt",
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: epoch})
		must(err)
		_, err = io.WriteString(w, "zip-slip payload for "+name+"\n")
		must(err)
	}
	must(zw.Close())
	return buf.Bytes()
}

// TarSlip returns a tar whose entries try to escape the extraction root,
// including link-based escapes.
func TarSlip() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	file := func(name, body string, mode int64) {
		must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(body)), ModTime: epoch, Format: tar.FormatPAX}))
		_, err := io.WriteString(tw, body)
		must(err)
	}
	link := func(typ byte, name, target string) {
		must(tw.WriteHeader(&tar.Header{Typeflag: typ, Name: name, Linkname: target, Mode: 0o777, ModTime: epoch, Format: tar.FormatPAX}))
	}
	file("safe.txt", "safe\n", 0o644)
	file("../evil-parent.txt", "tar-slip\n", 0o644)
	file("/tmp/evil-absolute.txt", "tar-slip\n", 0o644)
	file("safe/../../evil-inner.txt", "tar-slip\n", 0o644)
	// Symlink to an absolute directory, then a file written "through" it.
	link(tar.TypeSymlink, "escape-abs", "/tmp")
	file("escape-abs/evil-through-abs-symlink.txt", "tar-slip\n", 0o644)
	// Relative symlink pointing up, then a file through it.
	link(tar.TypeSymlink, "escape-up", "../..")
	file("escape-up/evil-through-rel-symlink.txt", "tar-slip\n", 0o644)
	// Hardlinks to files outside the root.
	link(tar.TypeLink, "hardlink-abs", "/etc/passwd")
	link(tar.TypeLink, "hardlink-up", "../outside.txt")
	// Device node and a setuid binary.
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeChar, Name: "dev-null", Mode: 0o666, Devmajor: 1, Devminor: 3, ModTime: epoch, Format: tar.FormatPAX}))
	file("setuid-shell", "#!/bin/sh\n", 0o4755)
	must(tw.Close())
	return buf.Bytes()
}

// ZipBomb returns a zip with one entry of size zero bytes, deflated.
func ZipBomb(size int64) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "zeros.bin", Method: zip.Deflate, Modified: epoch})
	must(err)
	writeZeros(w, size)
	must(zw.Close())
	return buf.Bytes()
}

// ZipLyingSize returns a zip whose headers declare a 16-byte entry that
// actually inflates to size bytes.
func ZipLyingSize(size int64) []byte {
	var comp bytes.Buffer
	fw, err := flate.NewWriter(&comp, flate.BestCompression)
	must(err)
	crc := crc32.NewIEEE()
	writeZeros(io.MultiWriter(fw, crc), size)
	must(fw.Close())

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "small.txt",
		Method:             zip.Deflate,
		Modified:           epoch,
		CRC32:              crc.Sum32(),
		CompressedSize64:   uint64(comp.Len()), //nolint:gosec // G115: Len is never negative
		UncompressedSize64: 16,
	})
	must(err)
	_, err = w.Write(comp.Bytes())
	must(err)
	must(zw.Close())
	return buf.Bytes()
}

// TarGzBomb returns a gzip-compressed tar holding one file of size zero
// bytes.
func TarGzBomb(size int64) []byte {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	must(err)
	gw.ModTime = epoch
	tw := tar.NewWriter(gw)
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "zeros.bin", Mode: 0o644, Size: size, ModTime: epoch}))
	writeZeros(tw, size)
	must(tw.Close())
	must(gw.Close())
	return buf.Bytes()
}

// ZipNested returns a stored zip containing n copies of inner.
func ZipNested(inner []byte, n int) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range n {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("layer-%d.zip", i), Method: zip.Store, Modified: epoch})
		must(err)
		_, err = w.Write(inner)
		must(err)
	}
	must(zw.Close())
	return buf.Bytes()
}

// ZipManyEntries returns a zip with n empty entries (entry-count limits).
func ZipManyEntries(n int) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range n {
		_, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f/%07d", i), Method: zip.Store, Modified: epoch})
		must(err)
	}
	must(zw.Close())
	return buf.Bytes()
}

func writeZeros(w io.Writer, n int64) {
	chunk := make([]byte, 1<<20)
	for n > 0 {
		c := int64(len(chunk))
		if n < c {
			c = n
		}
		_, err := w.Write(chunk[:c])
		must(err)
		n -= c
	}
}

func must(err error) {
	if err != nil {
		panic("fscorpus: " + err.Error()) // in-memory writers do not fail
	}
}
