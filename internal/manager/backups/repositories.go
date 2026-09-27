package backups

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups/s3probe"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// MaxNameLen bounds repository and policy names.
const MaxNameLen = 100

func accessContext(id string) string { return "backup_repositories/" + id + "/access_key" }
func secretContext(id string) string { return "backup_repositories/" + id + "/secret_key" }

func validName(name string) (string, error) {
	name = trimName(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return "", fieldErr("name", "must be 1 to %d characters", MaxNameLen)
	}
	return name, nil
}

// ListRepositories returns repositories in creation order.
func (s *Service) ListRepositories(ctx context.Context, afterID string, limit int) ([]domain.BackupRepository, error) {
	return store.ListBackupRepositories(ctx, s.db, afterID, limit)
}

// GetRepository returns one repository (no credentials).
func (s *Service) GetRepository(ctx context.Context, id string) (domain.BackupRepository, error) {
	return store.GetBackupRepository(ctx, s.db, id)
}

// CreatedRepository is a new repository; RecoveryKey is set exactly when
// its creation generated the instance Recovery Key (shown once).
type CreatedRepository struct {
	Repository  domain.BackupRepository
	RecoveryKey *RecoveryKey
	Key         domain.BackupKeyState
}

// CreateRepository stores a destination. While the instance has no
// Recovery Key, creating the first repository generates one (owner only);
// every repository starts awaiting confirmation of the key.
func (s *Service) CreateRepository(ctx context.Context, in domain.BackupRepositoryInput) (CreatedRepository, error) {
	name, err := validName(in.Name)
	if err != nil {
		return CreatedRepository{}, err
	}
	now := s.now()
	r := domain.BackupRepository{ID: ids.New(), Name: name, Kind: in.Kind, Executor: in.Executor, Path: in.Path, Endpoint: in.Endpoint,
		Bucket: in.Bucket, Prefix: in.Prefix, Region: in.Region, PathStyle: in.PathStyle, State: domain.BackupRepositoryAwaitingConfirmation,
		VerifyReadData: in.VerifyReadData, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.validateDestination(ctx, &r); err != nil {
		return CreatedRepository{}, err
	}
	if !protocol.ValidReadDataSubset(r.VerifyReadData) {
		return CreatedRepository{}, fieldErr("verifyReadData", "must be a percentage (5%%), a fraction (1/10) or a size (500M)")
	}
	r.VerifyCron, r.VerifyTimeZone = in.VerifyCron, in.VerifyTimeZone
	if r.VerifyCron == "" && s.opts.Scheduler != nil {
		if r.VerifyCron, r.VerifyTimeZone, err = s.opts.Scheduler.Default(ctx, scheduler.KindBackupVerification); err != nil {
			return CreatedRepository{}, err
		}
	}
	if r.VerifyCron == "" {
		r.VerifyCron, r.VerifyTimeZone = "0 5 * * 0", "UTC"
	}
	if err := scheduler.ValidateSpec(r.VerifyCron, r.VerifyTimeZone); err != nil {
		return CreatedRepository{}, fieldErr("verifySchedule", "%s", err.Error())
	}
	var sealed store.BackupRepositorySealed
	if r.Kind == backup.KindS3 {
		if in.AccessKeyID == "" || in.SecretAccessKey == "" || len(in.AccessKeyID) > 256 || len(in.SecretAccessKey) > 1024 {
			return CreatedRepository{}, fieldErr("secretAccessKey", "an S3 repository needs an access key ID and a secret access key")
		}
		if sealed, err = s.sealCredentials(r.ID, in.AccessKeyID, in.SecretAccessKey); err != nil {
			return CreatedRepository{}, err
		}
		r.CredentialFingerprint = s.opts.Keyring.Fingerprint([]byte(in.AccessKeyID+"\x00"+in.SecretAccessKey), "backup_repositories/"+r.ID+"/credentials")
	} else if in.AccessKeyID != "" || in.SecretAccessKey != "" {
		return CreatedRepository{}, fieldErr("accessKeyId", "a local repository has no credentials")
	}
	var out CreatedRepository
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		rec, found, err := store.GetBackupKey(ctx, tx)
		if err != nil {
			return err
		}
		if rec.State.Generation == 0 && rec.Sealed.Pending == "" {
			// The first repository generates the instance Recovery Key.
			if err := s.owner(ctx, false); err != nil {
				return err
			}
			k, nrec, err := s.newPending(ctx, tx, rec, found)
			if err != nil {
				return err
			}
			out.RecoveryKey, rec = &k, nrec
		}
		if err := store.InsertBackupRepository(ctx, tx, &r, sealed); err != nil {
			return err
		}
		out.Repository, out.Key = r, rec.State
		return nil
	})
	if err != nil {
		return CreatedRepository{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeBackupRepository, ID: r.ID})
	audit.SetDetail(ctx, "kind", r.Kind)
	audit.SetDetail(ctx, "location", destination(r).Base())
	audit.SetDetail(ctx, "keyGenerated", out.RecoveryKey != nil)
	if out.RecoveryKey != nil {
		audit.SetDetail(ctx, "pendingKeyFingerprint", out.RecoveryKey.Fingerprint())
	}
	s.notify()
	return out, nil
}

func (s *Service) sealCredentials(id, access, secret string) (store.BackupRepositorySealed, error) {
	a, err := s.opts.Keyring.Seal([]byte(access), accessContext(id))
	if err != nil {
		return store.BackupRepositorySealed{}, err
	}
	b, err := s.opts.Keyring.Seal([]byte(secret), secretContext(id))
	if err != nil {
		return store.BackupRepositorySealed{}, err
	}
	return store.BackupRepositorySealed{AccessKey: a, SecretKey: b}, nil
}

// validateDestination checks kind, executor and location.
func (s *Service) validateDestination(ctx context.Context, r *domain.BackupRepository) error {
	d := destination(*r)
	if err := d.Validate(); err != nil {
		field := "path"
		if r.Kind == backup.KindS3 {
			field = "endpoint"
		}
		if r.Kind != backup.KindLocal && r.Kind != backup.KindS3 {
			field = "kind"
		}
		return fieldErr(field, "%s", err.Error())
	}
	switch r.Kind {
	case backup.KindS3:
		if r.Executor != "" {
			return fieldErr("executor", "an S3 repository is used by every executor")
		}
	case backup.KindLocal:
		switch r.Executor {
		case "":
			return fieldErr("executor", "a local repository lives on the manager (\"manager\") or on one environment's agent (its ID)")
		case domain.BackupExecutorManager:
			if err := s.checkManagerLocalPath(r.Path); err != nil {
				return err
			}
		default:
			if s.opts.Environments != nil {
				if _, err := s.opts.Environments.GetEnvironment(ctx, r.Executor); err != nil {
					if errors.Is(err, domain.ErrEnvironmentNotFound) {
						return fieldErr("executor", "unknown environment")
					}
					return err
				}
			}
		}
	}
	return nil
}

// checkManagerLocalPath requires a manager-local repository below an
// allowlisted root and outside the manager's data directory (a repository
// must never sit inside its own backup source).
func (s *Service) checkManagerLocalPath(p string) error {
	ok := false
	for _, root := range s.opts.LocalRoots {
		if backup.Within(p, path.Clean(root)) {
			ok = true
		}
	}
	if !ok {
		return fieldErr("path", "must be below one of the manager's backup roots (DOCKER_MANAGER_BACKUP_LOCAL_ROOTS)")
	}
	if s.opts.DataDir != "" {
		data := filepath.ToSlash(filepath.Clean(s.opts.DataDir))
		if backup.Within(p, data) || backup.Within(data, p) {
			return fieldErr("path", "must not be inside (or contain) the manager's data directory, which it backs up")
		}
	}
	return nil
}

// UpdateRepository edits a repository (If-Match revision).
func (s *Service) UpdateRepository(ctx context.Context, id string, revision int64, p domain.BackupRepositoryPatch) (before, after domain.BackupRepository, err error) {
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		r, err := store.GetBackupRepository(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return domain.ErrRevisionMismatch
		}
		before = r
		if p.Name != nil {
			if r.Name, err = validName(*p.Name); err != nil {
				return err
			}
		}
		if p.Region != nil {
			if r.Kind != backup.KindS3 {
				return fieldErr("region", "only S3 repositories have a region")
			}
			r.Region = *p.Region
		}
		if p.PathStyle != nil {
			if r.Kind != backup.KindS3 {
				return fieldErr("pathStyle", "only S3 repositories have an addressing style")
			}
			r.PathStyle = *p.PathStyle
		}
		if err := destination(r).Validate(); err != nil {
			return fieldErr("region", "%s", err.Error())
		}
		if p.VerifyCron != nil {
			r.VerifyCron = *p.VerifyCron
		}
		if p.VerifyTimeZone != nil {
			r.VerifyTimeZone = *p.VerifyTimeZone
		}
		if err := scheduler.ValidateSpec(r.VerifyCron, r.VerifyTimeZone); err != nil {
			return fieldErr("verifySchedule", "%s", err.Error())
		}
		if p.VerifyReadData != nil {
			if !protocol.ValidReadDataSubset(*p.VerifyReadData) {
				return fieldErr("verifyReadData", "must be a percentage (5%%), a fraction (1/10) or a size (500M)")
			}
			r.VerifyReadData = *p.VerifyReadData
		}
		if p.VerifyEnabled != nil {
			if *p.VerifyEnabled && r.State != domain.BackupRepositoryReady {
				return domain.ErrRecoveryKeyNotConfirmed
			}
			r.VerifyEnabled = *p.VerifyEnabled
		}
		var sealed *store.BackupRepositorySealed
		if p.AccessKeyID != nil || p.SecretAccessKey != nil {
			if r.Kind != backup.KindS3 {
				return fieldErr("accessKeyId", "a local repository has no credentials")
			}
			if p.AccessKeyID == nil || p.SecretAccessKey == nil || *p.AccessKeyID == "" || *p.SecretAccessKey == "" {
				return fieldErr("secretAccessKey", "replace both the access key ID and the secret access key")
			}
			sc, err := s.sealCredentials(r.ID, *p.AccessKeyID, *p.SecretAccessKey)
			if err != nil {
				return err
			}
			sealed = &sc
			r.CredentialFingerprint = s.opts.Keyring.Fingerprint([]byte(*p.AccessKeyID+"\x00"+*p.SecretAccessKey), "backup_repositories/"+r.ID+"/credentials")
		}
		r.Revision, r.UpdatedAt = revision+1, s.now()
		if err := store.UpdateBackupRepository(ctx, tx, &r, revision, sealed); err != nil {
			return err
		}
		after = r
		return nil
	})
	if err != nil {
		return before, after, err
	}
	audit.SetDiff(ctx, repositoryAuditView(before), repositoryAuditView(after))
	s.notify()
	return before, after, nil
}

func repositoryAuditView(r domain.BackupRepository) map[string]any {
	return map[string]any{"name": r.Name, "region": r.Region, "pathStyle": r.PathStyle, "s3KeyPairFingerprint": r.CredentialFingerprint,
		"verifyCron": r.VerifyCron, "verifyTimeZone": r.VerifyTimeZone, "verifyEnabled": r.VerifyEnabled, "verifyReadData": r.VerifyReadData}
}

// DeleteRepository removes a repository that no policy uses and the
// backups indexed in it (they cannot be browsed or restored without it).
// The restic repositories at the destination are left untouched.
func (s *Service) DeleteRepository(ctx context.Context, id string, revision int64) error {
	var removed []string
	if err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		removed, err = store.DeleteBackupRepository(ctx, tx, id, revision)
		return err
	}); err != nil {
		return err
	}
	audit.SetDetail(ctx, "backupCount", len(removed))
	if s.opts.ForgetResource != nil {
		_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeBackupRepository, ID: id})
		for _, sn := range removed {
			_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeBackup, ID: sn})
		}
	}
	s.notify()
	return nil
}

// credentials returns a repository's S3 key pair ("" for local).
func (s *Service) credentials(ctx context.Context, db bun.IDB, id string) (backup.S3Credentials, error) {
	sealed, err := store.BackupRepositorySecrets(ctx, db, id)
	if err != nil {
		return backup.S3Credentials{}, err
	}
	if sealed.AccessKey == "" {
		return backup.S3Credentials{}, nil
	}
	a, err := s.opts.Keyring.Open(sealed.AccessKey, accessContext(id))
	if err != nil {
		return backup.S3Credentials{}, err
	}
	b, err := s.opts.Keyring.Open(sealed.SecretKey, secretContext(id))
	if err != nil {
		return backup.S3Credentials{}, err
	}
	return backup.S3Credentials{AccessKeyID: string(a), SecretAccessKey: string(b)}, nil
}

// repositoryRef is the wire reference of a repository's scope.
func repositoryRef(r domain.BackupRepository, scope string, k domain.BackupKeyState) protocol.BackupRepositoryRef {
	return protocol.BackupRepositoryRef{RepositoryID: r.ID, Destination: destination(r), Scope: scope, KeyGeneration: k.Generation,
		KeyFingerprint: k.Fingerprint}
}

// credentialFor builds the credential of a repository for one call.
func (s *Service) credentialFor(ctx context.Context, db bun.IDB, repositoryID string) (*protocol.RepositoryCredential, error) {
	cur, prev, _, _, err := s.currentKeys(ctx, db)
	if err != nil {
		return nil, err
	}
	creds, err := s.credentials(ctx, db, repositoryID)
	if err != nil {
		return nil, err
	}
	return &protocol.RepositoryCredential{RepositoryID: repositoryID, Password: cur, PreviousPassword: prev,
		AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey}, nil
}

// CommandSecrets resolves the repository credential of a backup job's
// agent command at dispatch (never stored with the job). An error fails
// the job with credential_unavailable.
func (s *Service) CommandSecrets(ctx context.Context, j *domain.Job) ([]protocol.RepositoryCredential, error) {
	if !isBackupKind(j.Kind) {
		return nil, nil
	}
	id := repositoryOf(*j)
	if id == "" {
		return nil, nil
	}
	repo, err := store.GetBackupRepository(ctx, s.db, id)
	if err != nil {
		return nil, errors.New("the backup repository no longer exists")
	}
	if repo.State != domain.BackupRepositoryReady {
		return nil, errors.New("the backup repository's Recovery Key has not been confirmed")
	}
	c, err := s.credentialFor(ctx, s.db, id)
	if err != nil {
		return nil, errors.New("the Recovery Key or the repository credentials are unavailable")
	}
	return []protocol.RepositoryCredential{*c}, nil
}

// --- connection tests and health ---

// TestRepository tests a repository's destination: S3 capabilities and
// Object Lock (from the manager), the local path on its executor, and
// which scopes already hold a restic repository the Recovery Key opens.
func (s *Service) TestRepository(ctx context.Context, id string) (domain.BackupConnectionTest, error) {
	r, err := store.GetBackupRepository(ctx, s.db, id)
	if err != nil {
		return domain.BackupConnectionTest{}, err
	}
	t := domain.BackupConnectionTest{At: s.now(), OK: true, Result: "ok"}
	fail := func(class, msg string) {
		if t.OK {
			t.OK, t.Result, t.Message = false, class, msg
		}
	}
	creds, err := s.credentials(ctx, s.db, id)
	if err != nil {
		return t, err
	}
	switch {
	case r.Kind == backup.KindS3:
		p := s3probe.Probe(ctx, s.opts.HTTPClient, s3probe.Target{Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix,
			Region: r.Region, PathStyle: r.PathStyle, AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey}, s.opts.Clock.Now)
		t.CanRead, t.CanWrite, t.CanDelete, t.ObjectLock = p.CanRead, p.CanWrite, p.CanDelete, p.ObjectLock
		if p.Class != "" {
			fail(p.Class, p.Message)
		}
		if p.CanDelete != nil && !*p.CanDelete {
			t.Warnings = append(t.Warnings, "The credentials cannot delete objects: retention (forget and prune) will fail.")
		}
		if p.ObjectLock != nil && *p.ObjectLock {
			t.Warnings = append(t.Warnings, "The bucket enforces Object Lock: restic prune cannot delete locked objects until their "+
				"retention expires, so retention may fail or reclaim no space.")
		}
	case r.Executor == domain.BackupExecutorManager:
		if err := s.checkManagerLocalPath(r.Path); err != nil {
			fail("path_not_allowed", err.Error())
		} else if err := probeWritable(filepath.FromSlash(r.Path)); err != nil {
			fail("path_not_writable", "the manager cannot write "+r.Path)
		}
	}
	if t.OK || t.Result == "access_denied" {
		t.Scopes = s.probeScopes(ctx, r, creds)
		for _, sc := range t.Scopes {
			if sc.ErrorClass != "" && sc.ErrorClass != restic.CodeRepositoryNotFound {
				fail(sc.ErrorClass, restic.RecoveryFor(sc.ErrorClass))
			}
			if sc.PreviousKey {
				t.Warnings = append(t.Warnings, "Location "+sc.Scope+" still uses the previous Recovery Key (the rotation has not reached it); "+
					"it moves to the current key the next time a job uses it.")
			}
		}
	}
	if err := store.RecordBackupRepositoryTest(ctx, s.db, id, t); err != nil {
		return t, err
	}
	audit.SetDetail(ctx, "result", t.Result)
	return t, nil
}

func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".docker-manager-probe-")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// probeScopes opens the scopes the repository can hold that Docker Manager knows
// (the manager scope, and every location recorded).
func (s *Service) probeScopes(ctx context.Context, r domain.BackupRepository, creds backup.S3Credentials) []domain.BackupScopeProbe {
	cur, prev, _, _, err := s.currentKeys(ctx, s.db)
	if err != nil {
		// Without a confirmed key only reachability was tested.
		return nil
	}
	scopes := []string{}
	if Serves(r, backup.ScopeManager) {
		scopes = append(scopes, backup.ScopeManager)
	}
	if locs, err := store.ListBackupLocations(ctx, s.db, r.ID); err == nil {
		for _, l := range locs {
			if !slices.Contains(scopes, l.Scope) {
				scopes = append(scopes, l.Scope)
			}
		}
	}
	if r.Kind == backup.KindLocal && r.Executor != domain.BackupExecutorManager && !slices.Contains(scopes, backup.EnvironmentScope(r.Executor)) {
		scopes = append(scopes, backup.EnvironmentScope(r.Executor))
	}
	var out []domain.BackupScopeProbe
	for _, scope := range scopes {
		p := domain.BackupScopeProbe{Scope: scope}
		if env, ok := backup.ScopeEnvironment(scope); ok && r.Kind == backup.KindLocal {
			p = s.probeAgentScope(ctx, r, env, scope)
		} else {
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			cfg, err := s.opts.Restic.Open(destination(r).Location(scope, creds), cur).Config(cctx)
			cancel()
			switch {
			case err == nil:
				p.Exists, p.KeyAccepted, p.ResticRepositoryID = true, true, cfg.ID
			case restic.IsCode(err, restic.CodeKeyRejected):
				p.Exists, p.ErrorClass = true, restic.CodeKeyRejected
				if prev != "" {
					// A rotation that has not reached this location yet.
					cctx, cancel := context.WithTimeout(ctx, time.Minute)
					cfg, perr := s.opts.Restic.Open(destination(r).Location(scope, creds), prev).Config(cctx)
					cancel()
					if perr == nil {
						p.KeyAccepted, p.PreviousKey, p.ErrorClass, p.ResticRepositoryID = true, true, "", cfg.ID
					}
				}
			default:
				p.ErrorClass = restic.CodeOf(err)
				if p.ErrorClass == "" {
					p.ErrorClass = restic.CodeFailed
				}
			}
		}
		out = append(out, p)
	}
	return out
}

func (s *Service) probeAgentScope(ctx context.Context, r domain.BackupRepository, env, scope string) domain.BackupScopeProbe {
	p := domain.BackupScopeProbe{Scope: scope}
	if s.opts.Agents == nil {
		p.ErrorClass = "agent_offline"
		return p
	}
	rec, _, _ := store.GetBackupKey(ctx, s.db)
	cred, err := s.credentialFor(ctx, s.db, r.ID)
	if err != nil {
		p.ErrorClass = "recovery_key_not_confirmed"
		return p
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqBackupSnapshots,
		protocol.BackupSnapshotsInput{Repository: repositoryRef(r, scope, rec.State), Credential: cred, Tags: []string{backup.TagManifest}}, time.Minute)
	if err != nil {
		p.ErrorClass = agentErrorClass(err)
		return p
	}
	var out protocol.BackupSnapshotsOutput
	if json.Unmarshal(raw, &out) == nil {
		p.Exists, p.KeyAccepted, p.ResticRepositoryID = true, true, out.ResticRepositoryID
	}
	return p
}

// agentErrorClass maps an agent request error to a class.
func agentErrorClass(err error) string {
	if errors.Is(err, jobs.ErrAgentOffline) {
		return "agent_offline"
	}
	type coded interface{ AgentCode() string }
	var c coded
	if errors.As(err, &c) {
		return c.AgentCode()
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout"
	}
	return "agent_error"
}

// RepositoryHealth summarizes a repository: its locations, last backups
// and verifications, key generation and the last connection test.
type RepositoryHealth struct {
	Repository domain.BackupRepository
	Locations  []domain.BackupLocation
	Key        domain.BackupKeyState
	// LastBackupAt / LastVerifiedAt are the newest over all locations.
	LastBackupAt   *time.Time
	LastVerifiedAt *time.Time
	// Problems lists what needs attention (plain language).
	Problems  []string
	Snapshots int
	SizeBytes int64
}

// Health reports a repository's health.
func (s *Service) Health(ctx context.Context, id string) (RepositoryHealth, error) {
	r, err := store.GetBackupRepository(ctx, s.db, id)
	if err != nil {
		return RepositoryHealth{}, err
	}
	h := RepositoryHealth{Repository: r}
	if h.Locations, err = store.ListBackupLocations(ctx, s.db, id); err != nil {
		return h, err
	}
	if h.Key, err = s.KeyState(ctx); err != nil {
		return h, err
	}
	if r.State != domain.BackupRepositoryReady {
		h.Problems = append(h.Problems, "The Recovery Key has not been confirmed for this repository; nothing is backed up to it yet.")
	}
	for _, l := range h.Locations {
		h.SizeBytes += l.SizeBytes
		if l.LastBackupAt != nil && (h.LastBackupAt == nil || l.LastBackupAt.After(*h.LastBackupAt)) {
			h.LastBackupAt = l.LastBackupAt
		}
		if l.LastVerifiedAt != nil && (h.LastVerifiedAt == nil || l.LastVerifiedAt.After(*h.LastVerifiedAt)) {
			h.LastVerifiedAt = l.LastVerifiedAt
		}
		if l.LastVerifyResult != "" && l.LastVerifyResult != "ok" {
			h.Problems = append(h.Problems, "The last verification of "+l.Scope+" failed ("+l.LastVerifyResult+").")
		}
		if h.Key.Generation > 0 && l.KeyGeneration < h.Key.Generation {
			h.Problems = append(h.Problems, "Location "+l.Scope+" still uses the previous Recovery Key (the rotation is not complete there).")
		}
	}
	if r.LastTest != nil && !r.LastTest.OK {
		h.Problems = append(h.Problems, "The last connection test failed ("+r.LastTest.Result+").")
	}
	snaps, err := store.ListBackupSnapshots(ctx, s.db, domain.BackupSnapshotFilter{RepositoryID: id})
	if err != nil {
		return h, err
	}
	h.Snapshots = len(snaps)
	return h, nil
}
