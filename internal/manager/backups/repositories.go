package backups

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/backups/s3probe"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
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
	// Primary: the setup had no Primary repository; this one became it.
	Primary bool
}

// CreateRepository stores a destination. While the instance has no
// Recovery Key, creating the first repository generates one (owner only);
// every repository starts awaiting confirmation of the key. While the setup
// has no Primary repository, the new one becomes it.
func (s *Service) CreateRepository(ctx context.Context, in domain.BackupRepositoryInput) (CreatedRepository, error) {
	name, err := validName(in.Name)
	if err != nil {
		return CreatedRepository{}, err
	}
	now := s.now()
	r := domain.BackupRepository{ID: ids.New(), Name: name, Endpoint: in.Endpoint,
		Bucket: in.Bucket, Prefix: in.Prefix, Region: in.Region, PathStyle: in.PathStyle, Compression: in.Compression,
		State: domain.BackupRepositoryAwaitingConfirmation, VerifyReadData: in.VerifyReadData, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if r.Compression == "" {
		r.Compression = domain.BackupCompressionAuto
	}
	if !validCompression(r.Compression) {
		return CreatedRepository{}, fieldErr("compression", "must be auto, max or off")
	}
	if err := destination(r).Validate(); err != nil {
		return CreatedRepository{}, fieldErr("endpoint", "%s", err.Error())
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
	if in.AccessKeyID == "" || in.SecretAccessKey == "" || len(in.AccessKeyID) > 256 || len(in.SecretAccessKey) > 1024 {
		return CreatedRepository{}, fieldErr("secretAccessKey", "an S3 repository needs an access key ID and a secret access key")
	}
	sealed, err := s.sealCredentials(r.ID, in.AccessKeyID, in.SecretAccessKey)
	if err != nil {
		return CreatedRepository{}, err
	}
	r.CredentialFingerprint = s.opts.Keyring.Fingerprint([]byte(in.AccessKeyID+"\x00"+in.SecretAccessKey), "backup_repositories/"+r.ID+"/credentials")
	var out CreatedRepository
	var setup domain.BackupSetup
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
		if setup, err = store.GetBackupSetup(ctx, tx); err != nil || setup.PrimaryRepositoryID != "" {
			return err
		}
		rev := setup.Revision
		setup.PrimaryRepositoryID, setup.Revision, setup.UpdatedAt = r.ID, rev+1, now
		out.Primary = true
		return store.UpdateBackupSetup(ctx, tx, setup, rev)
	})
	if err != nil {
		return CreatedRepository{}, err
	}
	if out.Primary {
		audit.SetDetail(ctx, "role", domain.BackupRolePrimary)
		s.primaryChanged(ctx, setup)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeBackupRepository, ID: r.ID})
	audit.SetDetail(ctx, "location", destination(r).Base())
	audit.SetDetail(ctx, "compression", r.Compression)
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
			r.Region = *p.Region
		}
		if p.PathStyle != nil {
			r.PathStyle = *p.PathStyle
		}
		if p.Compression != nil {
			// Only data written from now on (backups, and what prune
			// repacks) uses the new mode.
			if !validCompression(*p.Compression) {
				return fieldErr("compression", "must be auto, max or off")
			}
			r.Compression = *p.Compression
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
	return map[string]any{"name": r.Name, "region": r.Region, "pathStyle": r.PathStyle, "compression": r.Compression,
		"s3KeyPairFingerprint": r.CredentialFingerprint,
		"verifyCron":           r.VerifyCron, "verifyTimeZone": r.VerifyTimeZone, "verifyEnabled": r.VerifyEnabled, "verifyReadData": r.VerifyReadData}
}

// DeleteRepository removes a repository and the backups indexed in it
// (they cannot be browsed or restored without it). The setup stops using
// it: a removed Primary promotes the Secondary. The restic repositories at
// the destination are left untouched; the storage history stops counting
// it from now on.
func (s *Service) DeleteRepository(ctx context.Context, id string, revision int64) error {
	var removed []string
	var role string
	var st domain.BackupSetup
	if err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		before, err := store.GetBackupSetup(ctx, tx)
		if err != nil {
			return err
		}
		role = before.RoleOf(id)
		if removed, err = store.DeleteBackupRepository(ctx, tx, id, revision, s.now()); err != nil {
			return err
		}
		if st, err = store.GetBackupSetup(ctx, tx); err != nil {
			return err
		}
		return store.EndBackupStorage(ctx, tx, id, s.now())
	}); err != nil {
		return err
	}
	audit.SetDetail(ctx, "backupCount", len(removed))
	if role != "" {
		audit.SetDetail(ctx, "role", role)
		audit.SetDetail(ctx, "primaryRepositoryId", st.PrimaryRepositoryID)
		s.primaryChanged(ctx, st)
	}
	if s.opts.ForgetResource != nil {
		_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeBackupRepository, ID: id})
		for _, sn := range removed {
			_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeBackup, ID: sn})
		}
	}
	s.notify()
	return nil
}

// credentials returns a repository's S3 key pair.
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

// repositoryRef is the wire reference of a repository's scope. It never
// carries the compression mode: requests and most commands do not write
// data, and older agents do not know the field. CommandInput adds it at
// dispatch to the commands that write, for agents announcing
// protocol.FeatureBackupCompression.
func repositoryRef(r domain.BackupRepository, scope string, k domain.BackupKeyState) protocol.BackupRepositoryRef {
	d := destination(r)
	d.Compression = ""
	return protocol.BackupRepositoryRef{RepositoryID: r.ID, Destination: d, Scope: scope, KeyGeneration: k.Generation,
		KeyFingerprint: k.Fingerprint}
}

// compressionKinds are the agent commands that write pack files (backup,
// and retention's prune), so they carry the compression mode.
var compressionKinds = []domain.JobKind{jobspec.BackupRun, jobspec.BackupRetention}

// CommandInput sets the repository's current compression mode in the
// destination of a backup.run or backup.retention command at dispatch,
// when the receiving agent announces protocol.FeatureBackupCompression and
// the mode is not auto (#10). Otherwise, or when the input cannot be
// adapted, it returns nil: the command carries the stored input and the
// agent writes with restic's default. The stored job never changes.
func (s *Service) CommandInput(ctx context.Context, j *domain.Job) json.RawMessage {
	if !slices.Contains(compressionKinds, j.Kind) {
		return nil
	}
	id := repositoryOf(*j)
	if id == "" {
		return nil
	}
	fh, ok := s.opts.Agents.(FeatureHub)
	if !ok || !fh.EnvironmentHasFeature(j.EnvironmentID, protocol.FeatureBackupCompression) {
		return nil
	}
	repo, err := store.GetBackupRepository(ctx, s.db, id)
	if err != nil {
		return nil // CommandSecrets fails the job
	}
	mode := DestinationCompression(repo.Compression)
	if mode == "" {
		return nil
	}
	out, err := withCompression(j.Input, mode)
	if err != nil {
		s.log.Warn("could not add the compression mode to a backup command; it writes with restic's default",
			"job_id", j.ID, "error", err)
		return nil
	}
	return out
}

// withCompression sets repository.destination.compression in a job input.
func withCompression(input json.RawMessage, mode string) (json.RawMessage, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	var ref map[string]json.RawMessage
	if err := json.Unmarshal(in["repository"], &ref); err != nil || ref == nil {
		return nil, errors.New("the input has no repository")
	}
	var dest map[string]json.RawMessage
	if err := json.Unmarshal(ref["destination"], &dest); err != nil || dest == nil {
		return nil, errors.New("the repository has no destination")
	}
	var err error
	if dest["compression"], err = json.Marshal(mode); err != nil {
		return nil, err
	}
	if ref["destination"], err = json.Marshal(dest); err != nil {
		return nil, err
	}
	if in["repository"], err = json.Marshal(ref); err != nil {
		return nil, err
	}
	return json.Marshal(in)
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
// Object Lock (from the manager), and which scopes already hold a restic
// repository the Recovery Key opens.
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

// probeScopes opens the scopes of the repository that Docker Manager knows
// (the manager scope, and every location recorded).
func (s *Service) probeScopes(ctx context.Context, r domain.BackupRepository, creds backup.S3Credentials) []domain.BackupScopeProbe {
	cur, prev, _, _, err := s.currentKeys(ctx, s.db)
	if err != nil {
		// Without a confirmed key only reachability was tested.
		return nil
	}
	var out []domain.BackupScopeProbe
	for _, scope := range s.knownScopes(ctx, r) {
		p := domain.BackupScopeProbe{Scope: scope}
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
		out = append(out, p)
	}
	return out
}

// knownScopes are the scopes of a repository Docker Manager knows: the
// manager scope and every recorded location.
func (s *Service) knownScopes(ctx context.Context, r domain.BackupRepository) []string {
	scopes := []string{backup.ScopeManager}
	if locs, err := store.ListBackupLocations(ctx, s.db, r.ID); err == nil {
		for _, l := range locs {
			if !slices.Contains(scopes, l.Scope) {
				scopes = append(scopes, l.Scope)
			}
		}
	}
	return scopes
}

// agentErrorClass maps an agent request error to a class.
func agentErrorClass(err error) string {
	if errors.Is(err, jobs.ErrAgentOffline) {
		return "agent_offline"
	}
	if errors.Is(err, protocol.ErrRequestTimeout) {
		return "timeout"
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
