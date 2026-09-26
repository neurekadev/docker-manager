package backups

import (
	"context"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Recovery Key lifecycle (#10, #24, #25 Q7):
//
//  1. The first repository created while no key exists generates one; it
//     is returned exactly once (with its fingerprint) and stored sealed as
//     "pending". Nothing is initialized yet.
//  2. The owner confirms it by re-entering it (POST .../recovery-
//     confirmations): the pending key becomes the current key and that
//     repository becomes ready. Every other repository is confirmed the
//     same way (re-entering the current key), so each new destination
//     re-proves that the owner still holds the key.
//  3. A rotation (POST .../key-rotations, owner, step-up) generates a new
//     pending key, returned once. Confirming it makes it current and keeps
//     the old one as "previous" until every known location has moved:
//     each location moves the next time a job opens it (backup.OpenLocation
//     adds the new key, removes the old one), and verification jobs are
//     queued for every location right away. While locations remain on the
//     previous key the rotation is partial and both keys are needed for a
//     fresh import of those locations.
//
// The key is never returned by read routes, never logged, audited or put
// in URLs, arguments or job inputs; jobs receive it in their command's
// secrets at dispatch.

const (
	sealCurrent  = "backup_key_state/current"
	sealPending  = "backup_key_state/pending"
	sealPrevious = "backup_key_state/previous"
)

// KeyState returns the instance Recovery Key state (fingerprints only).
func (s *Service) KeyState(ctx context.Context) (domain.BackupKeyState, error) {
	rec, _, err := store.GetBackupKey(ctx, s.db)
	return rec.State, err
}

func (s *Service) open(sealed, context string) (RecoveryKey, error) {
	b, err := s.opts.Keyring.Open(sealed, context)
	if err != nil {
		return RecoveryKey{}, fmt.Errorf("backups: open recovery key: %w", err)
	}
	return ParseRecoveryKey(string(b))
}

func (s *Service) seal(k RecoveryKey, context string) (string, error) {
	return s.opts.Keyring.Seal([]byte(k.String()), context)
}

// currentKeys returns the plaintext current and previous keys for a job
// ("" when absent) and the current generation.
func (s *Service) currentKeys(ctx context.Context, db bun.IDB) (current, previous string, gen int, fingerprint string, err error) {
	rec, found, err := store.GetBackupKey(ctx, db)
	if err != nil {
		return "", "", 0, "", err
	}
	if !found || rec.State.Generation == 0 {
		return "", "", 0, "", domain.ErrRecoveryKeyNotConfirmed
	}
	cur, err := s.open(rec.Sealed.Current, sealCurrent)
	if err != nil {
		return "", "", 0, "", err
	}
	if rec.Sealed.Previous != "" {
		prev, err := s.open(rec.Sealed.Previous, sealPrevious)
		if err != nil {
			return "", "", 0, "", err
		}
		previous = prev.String()
	}
	return cur.String(), previous, rec.State.Generation, rec.State.Fingerprint, nil
}

// newPending generates and stores a pending key inside tx.
func (s *Service) newPending(ctx context.Context, tx bun.IDB, rec store.BackupKeyRecord, found bool) (RecoveryKey, store.BackupKeyRecord, error) {
	k, err := GenerateRecoveryKey(s.opts.Random)
	if err != nil {
		return RecoveryKey{}, rec, err
	}
	sealed, err := s.seal(k, sealPending)
	if err != nil {
		return RecoveryKey{}, rec, err
	}
	now := s.now()
	rec.Sealed.Pending, rec.State.PendingFingerprint, rec.State.PendingCreatedAt = sealed, k.Fingerprint(), &now
	expect := int64(0)
	if found {
		expect = rec.State.Revision
	}
	if err := store.PutBackupKey(ctx, tx, rec, expect, now); err != nil {
		return RecoveryKey{}, rec, err
	}
	rec.State.Revision = expect + 1
	return k, rec, nil
}

// KeyConfirmation is the result of a confirmation.
type KeyConfirmation struct {
	Repository domain.BackupRepository
	Key        domain.BackupKeyState
	// Activated: the confirmed key was pending and is now current.
	Activated bool
	// RotationJobs are the verification jobs queued to move locations to
	// a newly activated key.
	RotationJobs []domain.Job
}

// ConfirmKey is the re-entry challenge: the owner types the Recovery Key.
// A pending key that matches becomes current; otherwise the input must be
// the current key. The repository becomes ready.
func (s *Service) ConfirmKey(ctx context.Context, repositoryID, input string, backedUp bool) (KeyConfirmation, error) {
	if err := s.owner(ctx, false); err != nil {
		return KeyConfirmation{}, err
	}
	if !backedUp {
		return KeyConfirmation{}, fieldErr("backedUp", "confirm that the Recovery Key is saved outside Docker Manager")
	}
	typed, err := ParseRecoveryKey(input)
	if err != nil {
		return KeyConfirmation{}, err
	}
	var out KeyConfirmation
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		repo, err := store.GetBackupRepository(ctx, tx, repositoryID)
		if err != nil {
			return err
		}
		rec, found, err := store.GetBackupKey(ctx, tx)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrRecoveryKeyNotConfirmed
		}
		now := s.now()
		matched := false
		if rec.Sealed.Pending != "" {
			pending, err := s.open(rec.Sealed.Pending, sealPending)
			if err != nil {
				return err
			}
			if pending.Equal(typed) {
				cur, err := s.seal(pending, sealCurrent)
				if err != nil {
					return err
				}
				if rec.State.Generation > 0 {
					if rec.Sealed.Previous != "" {
						return domain.ErrKeyRotationInProgress
					}
					// Rotation: the old key stays usable until every
					// location has moved.
					old, err := s.open(rec.Sealed.Current, sealCurrent)
					if err != nil {
						return err
					}
					if rec.Sealed.Previous, err = s.seal(old, sealPrevious); err != nil {
						return err
					}
					rec.State.PreviousFingerprint, rec.State.RotationStartedAt = rec.State.Fingerprint, &now
				}
				rec.Sealed.Current, rec.State.Fingerprint = cur, pending.Fingerprint()
				rec.State.Generation++
				rec.State.CreatedAt, rec.State.ConfirmedAt = rec.State.PendingCreatedAt, &now
				rec.Sealed.Pending, rec.State.PendingFingerprint, rec.State.PendingCreatedAt = "", "", nil
				if err := store.PutBackupKey(ctx, tx, rec, rec.State.Revision, now); err != nil {
					return err
				}
				rec.State.Revision++
				matched, out.Activated = true, true
			}
		}
		if !matched && rec.Sealed.Current != "" {
			cur, err := s.open(rec.Sealed.Current, sealCurrent)
			if err != nil {
				return err
			}
			matched = cur.Equal(typed)
		}
		if !matched {
			return domain.ErrRecoveryKeyMismatch
		}
		if repo.State != domain.BackupRepositoryReady {
			rev := repo.Revision
			repo.State, repo.ConfirmedAt, repo.Revision, repo.UpdatedAt = domain.BackupRepositoryReady, &now, rev+1, now
			if err := store.UpdateBackupRepository(ctx, tx, &repo, rev, nil); err != nil {
				return err
			}
		}
		out.Repository, out.Key = repo, rec.State
		return nil
	})
	if err != nil {
		return KeyConfirmation{}, err
	}
	audit.SetDetail(ctx, "keyFingerprint", out.Key.Fingerprint)
	audit.SetDetail(ctx, "keyGeneration", out.Key.Generation)
	audit.SetDetail(ctx, "activated", out.Activated)
	if out.Activated && out.Key.RotationInProgress() {
		out.RotationJobs = s.queueKeyMigration(ctx, out.Key.Generation)
	}
	s.notify()
	return out, nil
}

// KeyRotation is a started rotation: the new key, shown once.
type KeyRotation struct {
	Key   RecoveryKey
	State domain.BackupKeyState
}

// RotateKey generates a new pending key (owner, recent authentication).
// Before any key was confirmed it replaces the unconfirmed pending key
// (e.g. when its one-time display was lost).
func (s *Service) RotateKey(ctx context.Context) (KeyRotation, error) {
	if err := s.owner(ctx, true); err != nil {
		return KeyRotation{}, err
	}
	var out KeyRotation
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		rec, found, err := store.GetBackupKey(ctx, tx)
		if err != nil {
			return err
		}
		if rec.State.RotationInProgress() {
			return domain.ErrKeyRotationInProgress
		}
		k, rec, err := s.newPending(ctx, tx, rec, found)
		if err != nil {
			return err
		}
		out = KeyRotation{Key: k, State: rec.State}
		return nil
	})
	if err != nil {
		return KeyRotation{}, err
	}
	audit.SetDetail(ctx, "pendingKeyFingerprint", out.State.PendingFingerprint)
	return out, nil
}

// queueKeyMigration queues a verification of every known location so each
// moves to the new key promptly (service identity; idempotent per
// generation).
func (s *Service) queueKeyMigration(ctx context.Context, gen int) []domain.Job {
	locs, err := store.ListBackupLocations(ctx, s.db, "")
	if err != nil {
		s.log.Warn("could not list backup locations for the key rotation", "error", err)
		return nil
	}
	var out []domain.Job
	for _, l := range locs {
		if l.KeyGeneration >= gen {
			continue
		}
		req, err := s.verifyRequest(ctx, l.RepositoryID, l.Scope, "")
		if err != nil {
			s.log.Warn("could not plan a key migration", "repository_id", l.RepositoryID, "scope", l.Scope, "error", err)
			continue
		}
		req.Principal = authz.Service()
		req.IdempotencyKey = fmt.Sprintf("key-rotation:%d:%s:%s", gen, l.RepositoryID, l.Scope)
		j, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			s.log.Warn("could not queue a key migration", "repository_id", l.RepositoryID, "scope", l.Scope, "error", err)
			continue
		}
		out = append(out, j)
	}
	return out
}

// completeRotation clears the previous key once every known location uses
// the current generation (inside a finish hook's transaction).
func (s *Service) completeRotation(ctx context.Context, tx bun.IDB) error {
	rec, found, err := store.GetBackupKey(ctx, tx)
	if err != nil || !found || !rec.State.RotationInProgress() {
		return err
	}
	locs, err := store.ListBackupLocations(ctx, tx, "")
	if err != nil {
		return err
	}
	for _, l := range locs {
		if l.KeyGeneration < rec.State.Generation {
			return nil
		}
	}
	rec.Sealed.Previous, rec.State.PreviousFingerprint, rec.State.RotationStartedAt = "", "", nil
	if err := store.PutBackupKey(ctx, tx, rec, rec.State.Revision, s.now()); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			return nil // a concurrent change; the next job completes it
		}
		return err
	}
	if s.opts.Audit != nil {
		return s.opts.Audit.RecordTx(ctx, tx, domain.AuditEvent{Action: "backup.key_rotation_completed", Actor: audit.ServiceActor(),
			Outcome: domain.AuditSuccess, Details: map[string]any{"keyFingerprint": rec.State.Fingerprint, "keyGeneration": rec.State.Generation}})
	}
	return nil
}

// LocationKeyStatus reports which locations still use an older key.
func (s *Service) LocationKeyStatus(ctx context.Context) (pending []domain.BackupLocation, err error) {
	rec, _, err := store.GetBackupKey(ctx, s.db)
	if err != nil {
		return nil, err
	}
	locs, err := store.ListBackupLocations(ctx, s.db, "")
	if err != nil {
		return nil, err
	}
	for _, l := range locs {
		if l.KeyGeneration < rec.State.Generation {
			pending = append(pending, l)
		}
	}
	return pending, nil
}

// verifyRequest builds the verification job of a location.
func (s *Service) verifyRequest(ctx context.Context, repositoryID, scope, subset string) (jobs.Request, error) {
	repo, err := store.GetBackupRepository(ctx, s.db, repositoryID)
	if err != nil {
		return jobs.Request{}, err
	}
	rec, _, err := store.GetBackupKey(ctx, s.db)
	if err != nil {
		return jobs.Request{}, err
	}
	if scope == backup.ScopeManager {
		return jobs.Request{Kind: jobspec.ManagerVerify, Targets: []domain.JobTarget{repoTarget(repositoryID)},
			Input: managerVerifyInput{RepositoryID: repositoryID, ReadDataSubset: subset}}, nil
	}
	env, ok := backup.ScopeEnvironment(scope)
	if !ok {
		return jobs.Request{}, fmt.Errorf("backups: invalid scope %q", scope)
	}
	ref := repositoryRef(repo, scope, rec.State)
	return jobs.Request{Kind: jobspec.BackupVerify, EnvironmentID: env, Targets: []domain.JobTarget{repoTarget(repositoryID)},
		Input: verifyInputFor(ref, subset)}, nil
}
