package fsroot

import (
	"bufio"
	"context"
	"encoding/hex"
	"io"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Download writes one regular file (format raw, optionally a byte range)
// or a zip/tar.gz archive of the paths to w (the files.download stream).
func (s *Service) Download(ctx context.Context, in protocol.FilesDownloadInput, w io.Writer) error {
	if len(in.Paths) == 0 || len(in.Paths) > protocol.MaxOperationPaths {
		return fail(protocol.CodeInvalidFrame, "1 to %d paths", protocol.MaxOperationPaths)
	}
	paths := make([]string, 0, len(in.Paths))
	for _, p := range in.Paths {
		rel, err := cleanPath(p)
		if err != nil {
			return err
		}
		paths = append(paths, rel)
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return err
	}
	defer r.Close()
	switch in.Format {
	case protocol.FormatRaw:
		if len(paths) != 1 || in.Offset < 0 || in.Length < 0 {
			return fail(protocol.CodeInvalidFrame, "a raw download is one file and a non-negative range")
		}
		f, fi, err := openRegular(r.root, paths[0])
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if fi.Size() > s.limits.MaxDownload {
			return fail(protocol.CodeTooLarge, "the file exceeds the download limit")
		}
		if in.Offset > fi.Size() {
			return fail(protocol.CodeInvalidFrame, "the range starts after the end of the file")
		}
		n := fi.Size() - in.Offset
		if in.Length > 0 {
			n = min(n, in.Length)
		}
		if _, err := f.Seek(in.Offset, io.SeekStart); err != nil {
			return classify(err, paths[0])
		}
		if _, err := io.CopyN(w, ctxReader{ctx: ctx, r: f}, n); err != nil {
			return classify(err, paths[0])
		}
		return nil
	case protocol.FormatZip, protocol.FormatTarGz:
		// Coalesce the archive writer's small writes into large frames.
		bw := bufio.NewWriterSize(w, protocol.MaxChunk/2)
		if _, err := s.writeArchive(ctx, r, paths, in.Format, bw, ""); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return classify(err, ".")
		}
		return nil
	}
	return fail(protocol.CodeInvalidFrame, "format must be raw, zip or tar.gz")
}

// Upload writes body to a new or replaced file (the files.upload stream):
// into a temporary file in the target directory, verified (size, optional
// SHA-256), then moved into place after the precondition is re-checked.
func (s *Service) Upload(ctx context.Context, in protocol.FilesUploadInput, body io.Reader) (protocol.FilesUploadResult, error) {
	var none protocol.FilesUploadResult
	dir, err := cleanPath(in.Dir)
	if err != nil {
		return none, err
	}
	if !protocol.ValidFileName(in.Name) {
		return none, fail(protocol.CodeForbiddenPath, "invalid file name")
	}
	if !protocol.ValidConflict(in.Conflict) || boolCount(len(in.IfMatch) > 0, in.CreateOnly, in.Conflict != "" && in.Conflict != protocol.ConflictFail) != 1 {
		return none, fail(protocol.CodeInvalidFrame, "exactly one of ifMatch, createOnly and a conflict policy is required")
	}
	if in.Size < 0 || in.Size > s.limits.MaxUpload {
		return none, fail(protocol.CodeTooLarge, "uploads are limited to %d bytes", s.limits.MaxUpload)
	}
	var want []byte
	if in.SHA256 != "" {
		if want, err = hex.DecodeString(in.SHA256); err != nil || len(want) != 32 {
			return none, fail(protocol.CodeInvalidFrame, "sha256 must be 64 hex digits")
		}
	}
	rel := join(dir, in.Name)
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return none, err
	}
	defer r.Close()
	t, err := openTarget(r, rel)
	if err != nil {
		return none, err
	}
	defer t.Close()

	// Fail fast before any byte is stored; the check is repeated right
	// before the file is moved into place.
	pre := precondition{ifMatch: in.IfMatch, createOnly: in.CreateOnly, overwrite: in.Conflict == protocol.ConflictOverwrite}
	var cur *current
	switch in.Conflict {
	case protocol.ConflictSkip, protocol.ConflictKeepBoth:
		name, c, skip, err := s.resolveConflict(ctx, t, in.Conflict)
		if err != nil {
			return none, err
		}
		if skip {
			e, err := s.stat(ctx, r, rel, false)
			if err != nil {
				return none, err
			}
			return protocol.FilesUploadResult{Entry: e, Skipped: true}, nil
		}
		t.name, t.rel, cur = name, join(dir, name), c
	default:
		if cur, err = s.check(ctx, t, pre); err != nil {
			return none, err
		}
	}
	tmp, n, sum, err := s.writeTemp(ctx, t, body, in.Size, cur)
	if err != nil {
		return none, err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = t.dir.Remove(tmp)
		}
	}()
	if n != in.Size {
		return none, fail(protocol.CodeInvalidFrame, "received %d bytes, expected %d", n, in.Size)
	}
	if want != nil && !slices.Equal(want, sum) {
		return none, fail(protocol.CodeDigestMismatch, "the content does not match its SHA-256")
	}
	unlock := s.lock(r.key + "\x00" + t.rel)
	defer unlock()
	noClobber := in.CreateOnly || in.Conflict == protocol.ConflictKeepBoth
	if in.Conflict == protocol.ConflictKeepBoth {
		// Another writer may have taken the free name meanwhile.
		for range 10 {
			err = s.commit(t, tmp, cur, true)
			if codeOf(err) != protocol.CodeAlreadyExists {
				break
			}
			name, ferr := freeName(t.dir, in.Name)
			if ferr != nil {
				return none, ferr
			}
			t.name, t.rel = name, join(dir, name)
		}
	} else {
		if len(in.IfMatch) > 0 && !slices.Contains(in.IfMatch, "*") {
			// The content may have changed while the upload was running.
			if _, err := s.check(ctx, t, pre); err != nil {
				return none, err
			}
		}
		err = s.commit(t, tmp, cur, noClobber)
	}
	if err != nil {
		return none, err
	}
	removeTmp = false
	s.invalidate(in.Scope, t.rel)
	e, err := s.entryWithSum(t, t.rel, sum)
	if err != nil {
		return none, err
	}
	return protocol.FilesUploadResult{Entry: e}, nil
}
