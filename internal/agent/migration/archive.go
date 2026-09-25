package migration

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/neurekadev/dockyard/internal/protocol"
)

// Archive limits.
const (
	// MaxEntries bounds the entries of one archived tree.
	MaxEntries = 20_000_000
	// MaxDepth bounds the directory depth of an archived tree.
	MaxDepth = 1024
)

// ErrChanged is returned when a file changed size while it was archived
// (the part is retried from the start).
var ErrChanged = errors.New("a file changed while it was archived")

// ArchiveStats describes an archived tree.
type ArchiveStats struct {
	Entries int64
	// Bytes is the regular files' content.
	Bytes        int64
	Skipped      []protocol.MigrationSkipped
	SkippedCount int64
}

func (s *ArchiveStats) skip(name, reason string) {
	s.SkippedCount++
	if len(s.Skipped) < protocol.MaxMigrationSkipped {
		s.Skipped = append(s.Skipped, protocol.MigrationSkipped{Path: name, Reason: reason})
	}
}

// tarMode converts permission and special bits to tar's mode field.
func tarMode(m fs.FileMode) int64 {
	mode := int64(m.Perm())
	if m&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	return mode
}

// fileMode converts tar's mode field back.
func fileMode(mode int64) fs.FileMode {
	m := fs.FileMode(mode & 0o777) //nolint:gosec // masked
	if mode&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

type inode struct{ dev, ino uint64 }

// WriteTree writes the tree of fsys as a PAX tar archive to w: the root
// directory itself ("./", so its owner, mode and times are migrated too)
// and every entry below it in sorted depth-first order. It never follows a
// symlink: symlinks are archived as links with their target text, files
// with several hard links once as a file and then as hard links to it.
// Numeric owners, permission and special bits, modification and access
// times (nanoseconds) are kept. Sockets and device nodes cannot be
// migrated and are skipped (listed in the stats).
func WriteTree(ctx context.Context, fsys FS, w io.Writer) (ArchiveStats, error) {
	tw := tar.NewWriter(w)
	a := &archiver{ctx: ctx, fs: fsys, tw: tw, links: map[inode]string{}}
	if err := a.walk(".", 0); err != nil {
		return a.stats, err
	}
	return a.stats, tw.Close()
}

type archiver struct {
	ctx   context.Context
	fs    FS
	tw    *tar.Writer
	links map[inode]string
	stats ArchiveStats
}

func (a *archiver) walk(name string, depth int) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if depth > MaxDepth {
		return fmt.Errorf("%s: deeper than %d levels", name, MaxDepth)
	}
	info, err := a.fs.Lstat(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if a.stats.Entries++; a.stats.Entries > MaxEntries {
		return fmt.Errorf("more than %d entries", MaxEntries)
	}
	hdr := &tar.Header{Format: tar.FormatPAX, Name: name, Mode: tarMode(info.Mode), Uid: info.UID, Gid: info.GID,
		ModTime: info.ModTime, AccessTime: info.AccessTime}
	if name == "." {
		hdr.Name = "./"
	}
	m := info.Mode
	switch {
	case m.IsDir():
		hdr.Typeflag = tar.TypeDir
		if name != "." {
			hdr.Name = name + "/"
		}
		if err := a.tw.WriteHeader(hdr); err != nil {
			return err
		}
		names, err := a.fs.ReadDir(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, n := range names {
			child := n
			if name != "." {
				child = name + "/" + n
			}
			if err := a.walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case m&fs.ModeSymlink != 0:
		target, err := a.fs.Readlink(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, target
		return a.tw.WriteHeader(hdr)
	case m&fs.ModeNamedPipe != 0:
		hdr.Typeflag = tar.TypeFifo
		return a.tw.WriteHeader(hdr)
	case m&fs.ModeSocket != 0:
		a.stats.skip(name, "socket")
		return nil
	case m&fs.ModeDevice != 0:
		a.stats.skip(name, "device node")
		return nil
	case !m.IsRegular():
		a.stats.skip(name, "unsupported file type")
		return nil
	}
	if info.Nlink > 1 && info.Ino != 0 {
		key := inode{info.Dev, info.Ino}
		if first, ok := a.links[key]; ok {
			hdr.Typeflag, hdr.Linkname = tar.TypeLink, first
			return a.tw.WriteHeader(hdr)
		}
		a.links[key] = name
	}
	hdr.Typeflag, hdr.Size = tar.TypeReg, info.Size
	if err := a.tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := a.fs.Open(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	n, err := io.CopyN(a.tw, ctxReader{a.ctx, f}, info.Size)
	a.stats.Bytes += n
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w (it shrank)", path.Clean(name), ErrChanged)
	}
	return err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// MeasureTree walks a tree like WriteTree and returns its entry count and
// content bytes, stopping (truncated) after maxEntries entries or when
// ctx ends. Skipped counts entries a transfer would skip.
func MeasureTree(ctx context.Context, fsys FS, maxEntries int64) (entries, bytes, skipped int64, truncated bool) {
	var walk func(name string, depth int) bool
	walk = func(name string, depth int) bool {
		if ctx.Err() != nil || entries >= maxEntries || depth > MaxDepth {
			truncated = true
			return false
		}
		info, err := fsys.Lstat(name)
		if err != nil {
			return true
		}
		entries++
		switch m := info.Mode; {
		case m.IsDir():
			names, err := fsys.ReadDir(name)
			if err != nil {
				return true
			}
			for _, n := range names {
				child := n
				if name != "." {
					child = name + "/" + n
				}
				if !walk(child, depth+1) {
					return false
				}
			}
		case m.IsRegular():
			bytes += info.Size
		case m&(fs.ModeSocket|fs.ModeDevice) != 0:
			skipped++
		}
		return true
	}
	walk(".", 0)
	return entries, bytes, skipped, truncated
}
