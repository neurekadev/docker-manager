package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// maxItems bounds the per-item results one job reports (the rest is
// summarized in one item).
const maxItems = 200

// items reports per-item outcomes of a job, bounded.
type items struct {
	sc      *jobexec.StepContext
	ctx     context.Context
	n       int
	dropped int
	failed  int
}

func (it *items) add(name, status, message string) {
	if status == domain.ItemFailed {
		it.failed++
	}
	if it.n >= maxItems {
		it.dropped++
		return
	}
	it.n++
	it.sc.Item(it.ctx, name, status, message)
}

func (it *items) finish() {
	if it.dropped > 0 {
		it.sc.Item(it.ctx, "…", domain.ItemSkipped, fmt.Sprintf("%d more results not listed", it.dropped))
	}
}

// jobInput decodes and validates a file job's input.
func jobInput(sc *jobexec.StepContext) (protocol.FilesJobInput, []string, error) {
	var in protocol.FilesJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, nil, fmt.Errorf("malformed input")
	}
	if len(in.Paths) == 0 || len(in.Paths) > protocol.MaxOperationPaths {
		return in, nil, fmt.Errorf("1 to %d paths are required", protocol.MaxOperationPaths)
	}
	paths := make([]string, 0, len(in.Paths))
	for _, p := range in.Paths {
		rel, ok := protocol.CleanRelativePath(p)
		if !ok {
			return in, nil, fmt.Errorf("invalid path")
		}
		paths = append(paths, rel)
	}
	if !protocol.ValidConflict(in.Conflict) {
		return in, nil, fmt.Errorf("invalid conflict policy")
	}
	if in.Name != "" && (len(paths) != 1 || !protocol.ValidFileName(in.Name)) {
		return in, nil, fmt.Errorf("a new name needs exactly one source and a single path component")
	}
	return in, paths, nil
}

// errMessage is a step failure message without host paths or contents
// (protocol errors name root-relative paths only).
func errMessage(err error) error {
	if errors.Is(err, errWalkLimit) {
		return errors.New("too many entries")
	}
	var he *session.HandlerError
	if errors.As(err, &he) {
		return errors.New(he.Code + ": " + he.Message)
	}
	return errors.New("internal agent error")
}

// Executors returns the files.* job executors.
func (s *Service) Executors() []jobexec.Executor {
	return []jobexec.Executor{
		{Kind: jobspec.FilesDelete, Steps: map[string]jobexec.StepFunc{"delete": s.jobDelete}},
		{Kind: jobspec.FilesCopy, Steps: map[string]jobexec.StepFunc{"copy": s.jobCopy}},
		{Kind: jobspec.FilesMove, Steps: map[string]jobexec.StepFunc{"move": s.jobMove}},
		{Kind: jobspec.FilesArchive, Steps: map[string]jobexec.StepFunc{"archive": s.jobArchive}},
		{Kind: jobspec.FilesExtract, Steps: map[string]jobexec.StepFunc{"extract": s.jobExtract}},
		{Kind: jobspec.FilesMetadata, Steps: map[string]jobexec.StepFunc{"apply": s.jobMetadata}},
	}
}

func (s *Service) jobDelete(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	it := &items{sc: sc, ctx: ctx}
	var changed []string
	for i, p := range paths {
		sc.Progress(ctx, i*100/len(paths), "")
		if p == "." {
			it.add(p, domain.ItemFailed, "the scope root cannot be deleted")
			continue
		}
		if _, err := r.root.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			it.add(p, domain.ItemSkipped, "does not exist")
			continue
		}
		// RemoveAll inside the root unlinks symlinks, never their targets.
		if err := r.root.RemoveAll(p); err != nil {
			it.add(p, domain.ItemFailed, codeOf(classify(err, p)))
			continue
		}
		it.add(p, domain.ItemSucceeded, "")
		changed = append(changed, p)
	}
	it.finish()
	s.invalidate(in.Scope, changed...)
	return nil
}

// destinationFor resolves the destination of one source in a directory
// under a conflict policy: the name to use, whether to skip, and whether
// an existing entry must be removed first (overwrite). Copying an entry
// into its own directory is a duplicate: only keep_both (a new name) can do
// it; every other combination of the same source and destination fails.
func (s *Service) destinationFor(r *scopeRoot, src, destDir, name, policy string, copying bool) (string, bool, bool, error) {
	if name == "" {
		name = path.Base(src)
	}
	dest := join(destDir, name)
	if dest == src && (!copying || policy != protocol.ConflictKeepBoth) {
		return "", false, false, fail(protocol.CodeConflict, "the source and the destination are the same")
	}
	if strings.HasPrefix(destDir+"/", src+"/") {
		return "", false, false, fail(protocol.CodeConflict, "cannot copy or move a directory into itself")
	}
	fi, err := r.root.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return dest, false, false, nil
	}
	if err != nil {
		return "", false, false, classify(err, dest)
	}
	switch policy {
	case protocol.ConflictSkip:
		return "", true, false, nil
	case protocol.ConflictOverwrite:
		return dest, false, true, nil
	case protocol.ConflictKeepBoth:
		dir, err := r.root.OpenRoot(destDir)
		if err != nil {
			return "", false, false, classify(err, destDir)
		}
		defer func() { _ = dir.Close() }()
		n, err := freeName(dir, name)
		return join(destDir, n), false, false, err
	}
	_ = fi
	return "", false, false, fail(protocol.CodeAlreadyExists, "%s already exists", dest)
}

func (s *Service) jobMove(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	destDir, ok := protocol.CleanRelativePath(in.Destination)
	if !ok {
		return errors.New("invalid destination")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	if fi, err := r.root.Stat(destDir); err != nil || !fi.IsDir() {
		return errors.New("the destination is not a directory")
	}
	it := &items{sc: sc, ctx: ctx}
	var changed []string
	for i, src := range paths {
		sc.Progress(ctx, i*100/len(paths), "")
		if src == "." {
			it.add(src, domain.ItemFailed, "the scope root cannot be moved")
			continue
		}
		dest, skip, replace, err := s.destinationFor(r, src, destDir, in.Name, in.Conflict, false)
		switch {
		case err != nil:
			it.add(src, domain.ItemFailed, codeOf(err))
			continue
		case skip:
			it.add(src, domain.ItemSkipped, "exists")
			continue
		}
		if replace {
			if err := r.root.RemoveAll(dest); err != nil {
				it.add(src, domain.ItemFailed, codeOf(classify(err, dest)))
				continue
			}
		}
		// Rename works on the entry itself: a symlink moves as a symlink.
		if err := r.root.Rename(src, dest); err != nil {
			it.add(src, domain.ItemFailed, codeOf(classify(err, src)))
			continue
		}
		it.add(src, domain.ItemSucceeded, dest)
		changed = append(changed, src, dest)
	}
	it.finish()
	s.invalidate(in.Scope, changed...)
	return nil
}

func (s *Service) jobCopy(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	destDir, ok := protocol.CleanRelativePath(in.Destination)
	if !ok {
		return errors.New("invalid destination")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	if fi, err := r.root.Stat(destDir); err != nil || !fi.IsDir() {
		return errors.New("the destination is not a directory")
	}
	it := &items{sc: sc, ctx: ctx}
	var changed []string
	budget := s.limits.MaxWalk
	for i, src := range paths {
		sc.Progress(ctx, i*100/len(paths), "")
		if src == "." {
			it.add(src, domain.ItemFailed, "the scope root cannot be copied")
			continue
		}
		dest, skip, replace, err := s.destinationFor(r, src, destDir, in.Name, in.Conflict, true)
		switch {
		case err != nil:
			it.add(src, domain.ItemFailed, codeOf(err))
			continue
		case skip:
			it.add(src, domain.ItemSkipped, "exists")
			continue
		}
		if replace {
			if err := r.root.RemoveAll(dest); err != nil {
				it.add(src, domain.ItemFailed, codeOf(classify(err, dest)))
				continue
			}
		}
		failed := 0
		err = walk(ctx, r.root, src, true, budget, func(rel string, fi fs.FileInfo) error {
			budget--
			to := dest
			if rel != src {
				to = join(dest, strings.TrimPrefix(rel, src+"/"))
			}
			if err := s.copyEntry(ctx, r, rel, to, fi); err != nil {
				failed++
				it.add(rel, domain.ItemFailed, codeOf(err))
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errWalkLimit) || codeOf(err) == protocol.CodeCancelled {
				it.finish()
				return errMessage(err)
			}
			it.add(src, domain.ItemFailed, codeOf(err))
			continue
		}
		if failed == 0 {
			it.add(src, domain.ItemSucceeded, dest)
		}
		changed = append(changed, dest)
	}
	it.finish()
	s.invalidate(in.Scope, changed...)
	return nil
}

// copyEntry copies one entry (not recursing): directories are created,
// regular files copied through a temporary file, symlinks re-created
// with the same target (never followed); special and hard-linked files
// are refused.
func (s *Service) copyEntry(ctx context.Context, r *scopeRoot, from, to string, fi fs.FileInfo) error {
	switch typeOf(fi.Mode()) {
	case protocol.FileTypeDir:
		if err := r.root.Mkdir(to, fi.Mode().Perm()); err != nil {
			return classify(err, to)
		}
		return nil
	case protocol.FileTypeSymlink:
		target, err := r.root.Readlink(from)
		if err != nil {
			return classify(err, from)
		}
		return classify(r.root.Symlink(target, to), to)
	case protocol.FileTypeFile:
		f, ofi, err := openChecked(r.root, from, fi, os.O_RDONLY)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if openLinks(f, ofi) > 1 {
			return fail(protocol.CodeUnsupportedFile, "several hard links")
		}
		t, err := openTarget(r, to)
		if err != nil {
			return err
		}
		defer t.Close()
		tmp, _, _, err := s.writeTemp(ctx, t, f, s.limits.MaxDownload, &current{info: ofi})
		if err != nil {
			return err
		}
		if err := s.commit(t, tmp, &current{}, true); err != nil {
			_ = t.dir.Remove(tmp)
			return err
		}
		return nil
	}
	return fail(protocol.CodeUnsupportedFile, "special file")
}

func (s *Service) jobArchive(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	dest, ok := protocol.CleanRelativePath(in.Destination)
	if !ok || dest == "." {
		return errors.New("invalid destination")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	t, err := openTarget(r, dest)
	if err != nil {
		return errMessage(err)
	}
	defer t.Close()
	name, cur, skip, err := s.resolveConflict(ctx, t, in.Conflict)
	if err != nil {
		return errMessage(err)
	}
	if skip {
		sc.Item(ctx, dest, domain.ItemSkipped, "exists")
		return nil
	}
	t.name, t.rel = name, join(path.Dir(dest), name)
	tmp := tempName()
	f, err := t.dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errMessage(classify(err, dest))
	}
	tmpRel := join(path.Dir(dest), tmp)
	entries, werr := s.writeArchive(ctx, r, paths, in.Format, f, tmpRel)
	werr = errors.Join(werr, f.Chmod(0o644), f.Sync(), f.Close())
	if werr != nil {
		_ = t.dir.Remove(tmp)
		return errMessage(werr)
	}
	if err := s.commit(t, tmp, cur, cur.info == nil); err != nil {
		_ = t.dir.Remove(tmp)
		return errMessage(err)
	}
	sc.Item(ctx, t.rel, domain.ItemSucceeded, fmt.Sprintf("%d entries", entries))
	s.invalidate(in.Scope, t.rel)
	return nil
}

func (s *Service) jobExtract(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	if len(paths) != 1 {
		return errors.New("extract needs exactly one archive")
	}
	dest, ok := protocol.CleanRelativePath(in.Destination)
	if !ok {
		return errors.New("invalid destination")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	it := &items{sc: sc, ctx: ctx}
	changed, err := s.extract(ctx, r, paths[0], dest, in.Conflict, func(name, status, message string) {
		switch status {
		case "failed":
			it.add(name, domain.ItemFailed, message)
		case "skipped":
			it.add(name, domain.ItemSkipped, message)
		}
	})
	it.finish()
	if len(changed) > 0 {
		s.invalidate(in.Scope, dest)
	}
	if err != nil {
		return errMessage(err)
	}
	sc.Item(ctx, paths[0], domain.ItemSucceeded, fmt.Sprintf("%d entries written", len(changed)))
	return nil
}

func (s *Service) jobMetadata(ctx context.Context, sc *jobexec.StepContext) error {
	in, paths, err := jobInput(sc)
	if err != nil {
		return err
	}
	if in.Chmod == nil && in.Chown == nil {
		return errors.New("chmod or chown is required")
	}
	if in.Chmod != nil && (in.Chmod.Mode > 0o777 || (in.Chmod.DirMode != nil && *in.Chmod.DirMode > 0o777)) {
		return errors.New("only permission bits (0-0777) can be set")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return errMessage(err)
	}
	defer r.Close()
	it := &items{sc: sc, ctx: ctx}
	var changed []string
	for i, p := range paths {
		sc.Progress(ctx, i*100/len(paths), "")
		failed := 0
		err := walk(ctx, r.root, p, in.Recursive, s.limits.MaxWalk, func(rel string, fi fs.FileInfo) error {
			if err := s.applyMetadata(r, rel, fi, in.Chmod, in.Chown); err != nil {
				failed++
				it.add(rel, domain.ItemFailed, codeOf(err))
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errWalkLimit) || codeOf(err) == protocol.CodeCancelled {
				it.finish()
				return errMessage(err)
			}
			it.add(p, domain.ItemFailed, codeOf(err))
			continue
		}
		if failed == 0 {
			it.add(p, domain.ItemSucceeded, "")
		}
		changed = append(changed, p)
	}
	it.finish()
	s.invalidate(in.Scope, changed...)
	return nil
}

// applyMetadata changes one regular file or directory through its opened
// handle (fchmod/fchown: a swap to a symlink cannot redirect it).
// Symlinks are skipped (never followed); special files are refused.
func (s *Service) applyMetadata(r *scopeRoot, rel string, fi fs.FileInfo, chmod *protocol.ChmodSpec, chown *protocol.ChownSpec) error {
	switch typeOf(fi.Mode()) {
	case protocol.FileTypeSymlink:
		return nil
	case protocol.FileTypeFile, protocol.FileTypeDir:
	default:
		return fail(protocol.CodeUnsupportedFile, "special file")
	}
	f, ofi, err := openChecked(r.root, rel, fi, os.O_RDONLY|openNonBlocking)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if ofi.Mode().IsRegular() && openLinks(f, ofi) > 1 {
		// Mode and owner belong to the inode: changing them would change
		// the other names too, which may lie outside the root.
		return fail(protocol.CodeUnsupportedFile, "several hard links")
	}
	if chown != nil {
		uid, gid := -1, -1
		if chown.UID != nil {
			uid = int(*chown.UID)
		}
		if chown.GID != nil {
			gid = int(*chown.GID)
		}
		if err := f.Chown(uid, gid); err != nil {
			return classify(err, rel)
		}
	}
	if chmod != nil {
		mode := chmod.Mode
		if fi.IsDir() && chmod.DirMode != nil {
			mode = *chmod.DirMode
		}
		if err := f.Chmod(fs.FileMode(mode)); err != nil {
			return classify(err, rel)
		}
	}
	return nil
}
