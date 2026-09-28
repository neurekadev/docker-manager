package backup

import (
	"context"
	"errors"

	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// Opened is a location opened for a job.
type Opened struct {
	Repo               restic.Repo
	ResticRepositoryID string
	// Initialized: the repository was created now.
	Initialized bool
	// Migrated: the location still used the previous Recovery Key and was
	// moved to the current one (key added, previous key removed).
	Migrated bool
	// PreviousRemoved: a previous key left over from an interrupted
	// rotation was removed.
	PreviousRemoved bool
	// CompressionIgnored: the location asked for a compression mode other
	// than auto, but the repository has format version 1 (created by
	// restic before 0.14), which cannot compress; Repo writes without the
	// mode.
	CompressionIgnored bool
}

// ErrKeyNotCurrent wraps a rejected key when no previous key could open
// the location either.
var ErrKeyNotCurrent = errors.New("neither the current nor the previous Recovery Key opens this repository")

// OpenLocation opens a physical repository with the current Recovery Key
// and finishes a key rotation on it (#10: rotation across repositories is
// applied location by location, so a partially rotated instance converges
// as each location is used):
//
//   - current key works: a previous key still present is removed;
//   - current key rejected but previous works: the current key is added
//     and the previous one removed;
//   - no repository: it is initialized with the current key when init is
//     true, otherwise restic.CodeRepositoryNotFound is returned.
//
// A repository of format version 1 cannot compress: a compression mode
// other than auto is dropped for it (Opened.CompressionIgnored) instead of
// failing every backup; a new repository is always version 2.
func OpenLocation(ctx context.Context, o restic.Opener, loc restic.Location, current, previous string, init bool) (Opened, error) {
	repo := o.Open(loc, current)
	cfg, err := repo.Config(ctx)
	switch {
	case err == nil:
		out := Opened{Repo: repo, ResticRepositoryID: cfg.ID}
		out.Repo, out.CompressionIgnored = compressible(o, repo, loc, current, cfg)
		if previous != "" && previous != current {
			removed, rerr := removeKeyOf(ctx, o, repo, loc, previous)
			if rerr != nil {
				return out, rerr
			}
			out.PreviousRemoved = removed
		}
		return out, nil
	case restic.IsCode(err, restic.CodeRepositoryNotFound):
		if !init {
			return Opened{}, err
		}
		id, ierr := repo.Init(ctx)
		if ierr != nil {
			return Opened{}, ierr
		}
		return Opened{Repo: repo, ResticRepositoryID: id, Initialized: true}, nil
	case restic.IsCode(err, restic.CodeKeyRejected) && previous != "" && previous != current:
		prev := o.Open(loc, previous)
		cfg, perr := prev.Config(ctx)
		if restic.IsCode(perr, restic.CodeKeyRejected) {
			return Opened{}, &restic.Error{Op: "open", Code: restic.CodeKeyRejected, Message: ErrKeyNotCurrent.Error()}
		}
		if perr != nil {
			return Opened{}, perr
		}
		if err := prev.AddKey(ctx, current); err != nil {
			return Opened{}, err
		}
		if _, err := removeKeyOf(ctx, o, repo, loc, previous); err != nil {
			return Opened{}, err
		}
		out := Opened{Repo: repo, ResticRepositoryID: cfg.ID, Migrated: true}
		out.Repo, out.CompressionIgnored = compressible(o, repo, loc, current, cfg)
		return out, nil
	}
	return Opened{}, err
}

// compressible returns repo, or, when loc asks for a compression mode the
// repository's format version cannot honor, the location reopened without
// it (and true).
func compressible(o restic.Opener, repo restic.Repo, loc restic.Location, password string, cfg restic.Config) (restic.Repo, bool) {
	if cfg.Version >= 2 || loc.Compression == "" || loc.Compression == restic.CompressionAuto {
		return repo, false
	}
	loc.Compression = ""
	return o.Open(loc, password), true
}

// removeKeyOf removes the key that password opens, using repo (opened
// with another key). It reports whether a key was removed.
func removeKeyOf(ctx context.Context, o restic.Opener, repo restic.Repo, loc restic.Location, password string) (bool, error) {
	keys, err := o.Open(loc, password).Keys(ctx)
	if restic.IsCode(err, restic.CodeKeyRejected) {
		return false, nil // already gone
	}
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if k.Current {
			if err := repo.RemoveKey(ctx, k.ID); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
