package files

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
)

// Download streams one regular file (format raw, optionally a byte range)
// or a zip/tar.gz archive of the paths (files.download stream).
func (s *Service) Download(ctx context.Context, st *streammux.Stream) error {
	var in protocol.FilesDownloadInput
	if err := json.Unmarshal(st.Input(), &in); err != nil {
		return fail(protocol.CodeInvalidFrame, "malformed download input")
	}
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
		if _, err := io.CopyN(st, ctxReader{ctx: ctx, r: f}, n); err != nil {
			return classify(err, paths[0])
		}
		return st.CloseWrite()
	case protocol.FormatZip, protocol.FormatTarGz:
		// Coalesce the archive writer's small writes into large frames.
		bw := bufio.NewWriterSize(st, protocol.MaxChunk/2)
		if _, err := s.writeArchive(ctx, r, paths, in.Format, bw, ""); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return classify(err, ".")
		}
		return st.CloseWrite()
	}
	return fail(protocol.CodeInvalidFrame, "format must be raw, zip or tar.gz")
}

// Upload writes the stream's bytes to a new or replaced file (files.upload
// stream): into a temporary file in the target directory, verified
// (size, optional SHA-256), then moved into place after the precondition
// is re-checked. The result travels in the final stream_close.
func (s *Service) Upload(ctx context.Context, st *streammux.Stream) error {
	var in protocol.FilesUploadInput
	if err := json.Unmarshal(st.Input(), &in); err != nil {
		return fail(protocol.CodeInvalidFrame, "malformed upload input")
	}
	dir, err := cleanPath(in.Dir)
	if err != nil {
		return err
	}
	if !protocol.ValidFileName(in.Name) {
		return fail(protocol.CodeForbiddenPath, "invalid file name")
	}
	if !protocol.ValidConflict(in.Conflict) || boolCount(len(in.IfMatch) > 0, in.CreateOnly, in.Conflict != "" && in.Conflict != protocol.ConflictFail) != 1 {
		return fail(protocol.CodeInvalidFrame, "exactly one of ifMatch, createOnly and a conflict policy is required")
	}
	if in.Size < 0 || in.Size > s.limits.MaxUpload {
		return fail(protocol.CodeTooLarge, "uploads are limited to %d bytes", s.limits.MaxUpload)
	}
	var want []byte
	if in.SHA256 != "" {
		if want, err = hex.DecodeString(in.SHA256); err != nil || len(want) != 32 {
			return fail(protocol.CodeInvalidFrame, "sha256 must be 64 hex digits")
		}
	}
	rel := join(dir, in.Name)
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return err
	}
	defer r.Close()
	t, err := openTarget(r, rel)
	if err != nil {
		return err
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
			return err
		}
		if skip {
			e, err := s.stat(ctx, r, rel, false)
			if err != nil {
				return err
			}
			return st.CloseWithResult(protocol.FilesUploadResult{Entry: e, Skipped: true})
		}
		t.name, t.rel, cur = name, join(dir, name), c
	default:
		if cur, err = s.check(ctx, t, pre); err != nil {
			return err
		}
	}
	tmp, n, sum, err := s.writeTemp(ctx, t, st, in.Size, cur)
	if err != nil {
		return err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = t.dir.Remove(tmp)
		}
	}()
	if n != in.Size {
		return fail(protocol.CodeInvalidFrame, "received %d bytes, expected %d", n, in.Size)
	}
	if want != nil && !slices.Equal(want, sum) {
		return fail(protocol.CodeDigestMismatch, "the content does not match its SHA-256")
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
				return ferr
			}
			t.name, t.rel = name, join(dir, name)
		}
	} else {
		if len(in.IfMatch) > 0 && !slices.Contains(in.IfMatch, "*") {
			// The content may have changed while the upload was running.
			if _, err := s.check(ctx, t, pre); err != nil {
				return err
			}
		}
		err = s.commit(t, tmp, cur, noClobber)
	}
	if err != nil {
		return err
	}
	removeTmp = false
	s.invalidate(in.Scope, t.rel)
	e, err := s.entryWithSum(t, t.rel, sum)
	if err != nil {
		return err
	}
	return st.CloseWithResult(protocol.FilesUploadResult{Entry: e})
}
