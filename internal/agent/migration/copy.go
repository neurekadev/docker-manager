package migration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/neurekadev/docker-manager/internal/humanize"
)

// Local tree copies (stack import by copy, #7): the same archive format
// as a migration, streamed from WriteTree into ExtractTree inside one
// agent, so a copy keeps exactly what a migration keeps (numeric owners,
// permission and special bits, modification and access times, symlinks
// as links, hard links, FIFOs; sockets and device nodes are skipped and
// listed), followed by extended attributes (CopyXattrs) and a metadata
// comparison of both trees (VerifyTree).

// CopyStats describes a local copy.
type CopyStats struct {
	ArchiveStats
	Extracted ExtractStats
}

// CopyTree copies the tree of src into dst, whose root must be a new,
// empty directory. maxBytes bounds the regular files' content (0:
// unbounded). The copy fails when the destination did not receive every
// archived entry and byte.
func CopyTree(ctx context.Context, src, dst FS, maxBytes int64) (CopyStats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	type result struct {
		stats ArchiveStats
		err   error
	}
	done := make(chan result, 1)
	go func() {
		st, err := WriteTree(ctx, src, pw)
		_ = pw.CloseWithError(err)
		done <- result{st, err}
	}()
	ex, xerr := ExtractTree(ctx, dst, pr, ExtractOptions{MaxBytes: maxBytes})
	if xerr != nil {
		cancel()
	} else {
		// The reader stops at the end-of-archive marker: drain the
		// trailer so the writer finishes.
		_, _ = io.Copy(io.Discard, pr)
	}
	_ = pr.CloseWithError(errors.New("extraction ended"))
	w := <-done
	st := CopyStats{ArchiveStats: w.stats, Extracted: ex}
	switch {
	case w.err != nil:
		return st, fmt.Errorf("read the source tree: %w", w.err)
	case xerr != nil:
		return st, fmt.Errorf("write the copy: %w", xerr)
	case ex.Entries != w.stats.Entries-w.stats.SkippedCount || ex.Bytes != w.stats.Bytes:
		sz := humanize.Sizes(ex.Bytes, w.stats.Bytes)
		return st, fmt.Errorf("the copy is incomplete: %d of %d entries, %s of %s",
			ex.Entries, w.stats.Entries-w.stats.SkippedCount, sz[0], sz[1])
	}
	return st, nil
}

// VerifyTree compares a copy with its source entry by entry: type, size,
// permission and special bits, numeric owner, modification time (not for
// symlinks, whose own times are not kept) and symlink targets. Entries
// the copy skipped (sockets, device nodes) must be absent from dst. It
// returns the first difference.
func VerifyTree(ctx context.Context, src, dst FS) error {
	var walk func(name string, depth int) error
	walk = func(name string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > MaxDepth {
			return fmt.Errorf("%s: deeper than %d levels", name, MaxDepth)
		}
		a, err := src.Lstat(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if t := a.Mode.Type(); t != 0 && t != fs.ModeDir && t != fs.ModeSymlink && t != fs.ModeNamedPipe {
			if _, err := dst.Lstat(name); err == nil {
				return fmt.Errorf("%s: a skipped special file exists in the copy", name)
			}
			return nil
		}
		b, err := dst.Lstat(name)
		if err != nil {
			return fmt.Errorf("%s is missing from the copy: %w", name, err)
		}
		if err := sameMeta(a, b); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		switch {
		case a.Mode&fs.ModeSymlink != 0:
			ta, err := src.Readlink(name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			tb, err := dst.Readlink(name)
			if err != nil || ta != tb {
				return fmt.Errorf("%s: the copied symlink points elsewhere", name)
			}
		case a.Mode.IsDir():
			names, err := src.ReadDir(name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			for _, n := range names {
				child := n
				if name != "." {
					child = name + "/" + n
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			// Nothing extra may have appeared in the copy.
			got, err := dst.ReadDir(name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if len(got) > len(names) {
				return fmt.Errorf("%s: the copy has entries the source does not", name)
			}
		}
		return nil
	}
	return walk(".", 0)
}

func sameMeta(a, b Info) error {
	switch {
	case a.Mode.Type() != b.Mode.Type():
		return fmt.Errorf("type %s copied as %s", a.Mode.Type(), b.Mode.Type())
	case a.Mode.IsRegular() && a.Size != b.Size:
		return fmt.Errorf("size %d copied as %d", a.Size, b.Size)
	case a.UID != b.UID || a.GID != b.GID:
		return fmt.Errorf("owner %d:%d copied as %d:%d", a.UID, a.GID, b.UID, b.GID)
	case a.Mode&fs.ModeSymlink != 0:
		return nil
	case a.Mode.Perm() != b.Mode.Perm() || a.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != b.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky):
		return fmt.Errorf("mode %s copied as %s", a.Mode, b.Mode)
	case !a.ModTime.Equal(b.ModTime):
		return fmt.Errorf("modification time %s copied as %s", a.ModTime, b.ModTime)
	}
	return nil
}

// FreeBytes returns the bytes available to root on the filesystem of dir
// (-1: unknown).
func FreeBytes(dir string) int64 { return freeBytes(dir) }

// XattrStats describes CopyXattrs.
type XattrStats struct {
	// Copied counts attributes written.
	Copied int
	// Failed lists attributes the destination refused ("path: name:
	// error", at most MaxXattrFailures).
	Failed []string
}

// MaxXattrFailures bounds XattrStats.Failed.
const MaxXattrFailures = 20
