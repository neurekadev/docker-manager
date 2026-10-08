package fsroot

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

func unixNano(n int64) time.Time { return time.Unix(0, n).UTC() }

// errWalkLimit ends a walk that visited too many entries.
var errWalkLimit = errors.New("files: walk limit reached")

// walkFunc visits one entry (Lstat info: symlinks are not followed).
type walkFunc func(rel string, fi fs.FileInfo) error

// walk visits rel and, when recursive and rel is a directory, everything
// below it, parents before children, never following symlinks. A
// directory is descended only through a handle confirmed to be the
// directory that was Lstat'ed (a swap is reported as a conflict for that
// directory). At most limit entries are visited (errWalkLimit). In
// NoFollow scopes rel itself is reached without following any symlink
// (scopeRoot.at); a symlink at rel is visited as a link.
func walk(ctx context.Context, r *scopeRoot, rel string, recursive bool, limit int, fn walkFunc) error {
	dir, name, done, err := r.at(rel)
	if err != nil {
		return err
	}
	fi, err := dir.Lstat(name)
	done()
	if err != nil {
		return classify(err, rel)
	}
	n := 0
	return walkEntry(ctx, r.root, rel, fi, recursive, limit, &n, fn)
}

func walkEntry(ctx context.Context, r *os.Root, rel string, fi fs.FileInfo, recursive bool, limit int, n *int, fn walkFunc) error {
	if err := ctx.Err(); err != nil {
		return classify(err, rel)
	}
	if *n++; *n > limit {
		return errWalkLimit
	}
	if err := fn(rel, fi); err != nil {
		return err
	}
	if !recursive || !fi.IsDir() {
		return nil
	}
	sub, err := r.OpenRoot(rel)
	if err != nil {
		return classify(err, rel)
	}
	defer func() { _ = sub.Close() }()
	if st, err := sub.Stat("."); err != nil || !os.SameFile(st, fi) {
		return fail(protocol.CodeConflict, "%s changed during the operation", rel)
	}
	d, err := sub.Open(".")
	if err != nil {
		return classify(err, rel)
	}
	defer func() { _ = d.Close() }()
	for {
		batch, rerr := d.ReadDir(256)
		for _, de := range batch {
			cfi, err := sub.Lstat(de.Name())
			if err != nil {
				continue // removed meanwhile
			}
			if err := walkEntry(ctx, r, join(rel, de.Name()), cfi, true, limit, n, fn); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) || len(batch) == 0 {
			return nil
		}
		if rerr != nil {
			return classify(rerr, rel)
		}
	}
}

// impactOf counts an entry for a preview.
func impactOf(im *protocol.FileImpact, fi fs.FileInfo) {
	im.Entries++
	switch typeOf(fi.Mode()) {
	case protocol.FileTypeFile:
		im.Files++
		im.Bytes += fi.Size()
	case protocol.FileTypeDir:
		im.Dirs++
	case protocol.FileTypeSymlink:
		im.Symlinks++
	default:
		im.Other++
	}
}

// Preview lists what an operation would overwrite and counts what it
// touches.
func (s *Service) Preview(ctx context.Context, in protocol.FilesPreviewInput) (protocol.FilesPreviewOutput, error) {
	out := protocol.FilesPreviewOutput{Conflicts: []protocol.FileConflict{}}
	if len(in.Paths) > protocol.MaxOperationPaths || len(in.Names) > protocol.MaxOperationPaths {
		return out, fail(protocol.CodeTooLarge, "at most %d paths per operation", protocol.MaxOperationPaths)
	}
	paths := make([]string, 0, len(in.Paths))
	for _, p := range in.Paths {
		rel, err := cleanPath(p)
		if err != nil {
			return out, err
		}
		paths = append(paths, rel)
	}
	dest := ""
	if in.Destination != "" || in.Operation == protocol.FileOpUpload {
		var err error
		if dest, err = cleanPath(in.Destination); err != nil {
			return out, err
		}
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return out, err
	}
	defer r.Close()
	budget := protocol.MaxPreviewEntries
	count := func(rel string, recursive bool) error {
		before := out.Impact.Entries
		err := walk(ctx, r, rel, recursive, budget, func(_ string, fi fs.FileInfo) error {
			impactOf(&out.Impact, fi)
			return nil
		})
		budget -= out.Impact.Entries - before
		if errors.Is(err, errWalkLimit) || budget <= 0 {
			out.Impact.Truncated = true
			return nil
		}
		return err
	}
	conflict := func(source, destRel string) error {
		dir, name, done, err := r.at(destRel)
		if codeOf(err) == protocol.CodeNotFound {
			return nil // the destination directory does not exist yet
		}
		if err != nil {
			return err
		}
		fi, err := dir.Lstat(name)
		done()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return classify(err, destRel)
		}
		if len(out.Conflicts) >= protocol.MaxConflicts {
			out.ConflictsTruncated = true
			return nil
		}
		out.Conflicts = append(out.Conflicts, protocol.FileConflict{Source: source, Destination: destRel, Existing: entryOf(destRel, fi)})
		return nil
	}
	switch in.Operation {
	case protocol.FileOpCopy, protocol.FileOpMove:
		if dest == "" {
			return out, fail(protocol.CodeInvalidFrame, "destination required")
		}
		for _, p := range paths {
			if p == "." {
				return out, fail(protocol.CodeForbiddenPath, "the scope root cannot be copied or moved")
			}
			name := path.Base(p)
			if len(paths) == 1 && len(in.Names) == 1 { // a rename or a copy under a new name
				if !protocol.ValidFileName(in.Names[0]) {
					return out, fail(protocol.CodeForbiddenPath, "invalid file name")
				}
				name = in.Names[0]
			}
			if err := conflict(p, join(dest, name)); err != nil {
				return out, err
			}
			if err := count(p, true); err != nil {
				return out, err
			}
		}
	case protocol.FileOpUpload:
		for _, n := range in.Names {
			if !protocol.ValidFileName(n) {
				return out, fail(protocol.CodeForbiddenPath, "invalid file name")
			}
			if err := conflict(n, join(dest, n)); err != nil {
				return out, err
			}
		}
	case protocol.FileOpDelete, protocol.FileOpMetadata:
		for _, p := range paths {
			if in.Operation == protocol.FileOpDelete && p == "." {
				return out, fail(protocol.CodeForbiddenPath, "the scope root cannot be deleted")
			}
			if err := count(p, in.Operation == protocol.FileOpDelete || in.Recursive); err != nil {
				return out, err
			}
		}
	case protocol.FileOpArchive:
		if dest == "" || dest == "." {
			return out, fail(protocol.CodeInvalidFrame, "destination archive path required")
		}
		if err := conflict(dest, dest); err != nil {
			return out, err
		}
		for _, p := range paths {
			if err := count(p, true); err != nil {
				return out, err
			}
		}
	case protocol.FileOpExtract:
		if len(paths) != 1 || dest == "" {
			return out, fail(protocol.CodeInvalidFrame, "extract needs one archive and a destination")
		}
		err := s.scanArchive(ctx, r, s.limitsFor(in.Limits), paths[0], nil, func(e archiveEntry) error {
			if out.Impact.Entries >= protocol.MaxPreviewEntries {
				out.Impact.Truncated = true
				return errWalkLimit
			}
			out.Impact.Entries++
			if e.name == "" { // refused at extraction; nothing to compare
				out.Impact.Other++
				return nil
			}
			switch e.typ {
			case protocol.FileTypeFile:
				out.Impact.Files++
				out.Impact.Bytes += e.size
			case protocol.FileTypeDir:
				out.Impact.Dirs++
			case protocol.FileTypeSymlink:
				out.Impact.Symlinks++
			default:
				out.Impact.Other++
			}
			if e.typ == protocol.FileTypeDir {
				return nil
			}
			return conflict(e.name, join(dest, e.name))
		})
		if err != nil && !errors.Is(err, errWalkLimit) {
			return out, err
		}
	default:
		return out, fail(protocol.CodeInvalidFrame, "unknown operation %q", in.Operation)
	}
	return out, nil
}
