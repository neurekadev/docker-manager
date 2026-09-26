package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// RestoreMarker describes a staged (restore.json in PendingRestoreDir) or
// applied (AppliedRestoreFile) manager-state restore. It never holds
// plaintext secrets: the credentials and the adopted Recovery Key are
// sealed with the restored secret key.
type RestoreMarker struct {
	Format       string             `json:"format"`
	Version      int                `json:"version"`
	JobID        string             `json:"jobId"`
	SetID        string             `json:"setId"`
	InstanceID   string             `json:"instanceId"`
	RepositoryID string             `json:"repositoryId"`
	Destination  backup.Destination `json:"destination"`
	App          backup.AppInfo     `json:"app"`
	SchemaLatest string             `json:"schemaLatest"`
	// SealedCredentials is the S3 key pair entered for the import.
	SealedCredentials string `json:"sealedCredentials,omitempty"`
	// SealedKey is the entered Recovery Key when it is newer than the
	// restored database's current key (the snapshot predates a rotation).
	SealedKey string `json:"sealedKey,omitempty"`
	// Manifests are every manifest read at the source (reconciliation).
	Manifests []backup.Manifest `json:"manifests"`
	StagedAt  time.Time         `json:"stagedAt"`
	AppliedAt *time.Time        `json:"appliedAt,omitempty"`
	// PreRestoreDir keeps the replaced database and key file.
	PreRestoreDir string `json:"preRestoreDir,omitempty"`
}

// RestoreMarkerFormat identifies RestoreMarker files.
const RestoreMarkerFormat = "docker-manager-restore"

// importScanOutput is the output of backup.import's scan step.
type importScanOutput struct {
	InstanceID     string `json:"instanceId"`
	AppVersion     string `json:"appVersion"`
	SchemaLatest   string `json:"schemaLatest"`
	SecretKeyID    string `json:"secretKeyId"`
	KeyFingerprint string `json:"keyFingerprint"`
	// ManagerKey is which entered key opened the manager repository.
	ManagerKey string `json:"managerKey"`
	// AdoptKey: the entered key becomes the restored manager's current
	// key (the snapshot predates a rotation).
	AdoptKey bool `json:"adoptKey"`
	// KeyNotAdopted: the entered key differs from the snapshot's but
	// opened nothing, so it was not adopted.
	KeyNotAdopted  bool  `json:"keyNotAdopted,omitempty"`
	DatabaseBytes  int64 `json:"databaseBytes"`
	HostManifests  int   `json:"hostManifests,omitempty"`
	SetManifests   int   `json:"setManifests,omitempty"`
	RestartPending bool  `json:"restartPending,omitempty"`
}

func (s *Service) importExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.BackupImport, Steps: map[string]jobexec.StepFunc{
		"scan":         s.stepImportScan,
		"import_index": s.stepImportIndex,
	}}
}

var errSecretsLost = backup.Refuse("backup_import_secrets_lost", "the import lost the Recovery Key and credentials (the manager restarted)",
	"Start the import again: Docker Manager keeps them in memory only.")

func (s *Service) importJob(sc *jobexec.StepContext) (importInput, importSecrets, error) {
	var in importInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, importSecrets{}, err
	}
	sec, ok := s.importSecretsFor(sc.JobID)
	if !ok {
		return in, sec, errSecretsLost
	}
	return in, sec, nil
}

func dumpSmall(ctx context.Context, repo restic.Repo, snapshotID, file string, limit int) ([]byte, error) {
	var buf limitedBuffer
	buf.max = limit
	err := repo.Dump(ctx, snapshotID, file, &buf)
	return buf.b, err
}

func (s *Service) stepImportScan(ctx context.Context, sc *jobexec.StepContext) error {
	in, sec, err := s.importJob(sc)
	if err != nil {
		return err
	}
	src := ImportSource{Destination: in.Destination}
	repo, which, _, err := s.openImport(ctx, src, sec, backup.ScopeManager)
	if err != nil {
		return importFailure(err, in.Destination.Repository(backup.ScopeManager))
	}
	dir := s.importStaging(sc.JobID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sc.Progress(ctx, 5, "reading the manager-state snapshot")
	file := func(name string) string { return stateFile(in.StatePaths, name) }
	raw, err := dumpSmall(ctx, repo, in.ManagerSnapshotID, file(stateInfoFile), 1<<20)
	if err != nil {
		return importRefusal(ImportStateMissing, "the manager-state snapshot has no readable state.json", guideStateMissing)
	}
	var info StateInfo
	if json.Unmarshal(raw, &info) != nil || info.Format != StateFormat {
		return importRefusal(ImportStateMissing, "the manager-state snapshot's state.json is unreadable", guideStateMissing)
	}
	if info.Version > 1 || !schemaKnown(info.Schema.Migrations, s.knownMigrations()) {
		return importRefusal(ImportSchemaIncompatible, "the manager state was written by a newer Docker Manager ("+info.App.Version+")", guideSchema)
	}
	raw, err = dumpSmall(ctx, repo, in.ManagerSnapshotID, file(BundleFile), 64<<10)
	if err != nil {
		return importRefusal(ImportStateMissing, "the manager-state snapshot has no secret-key bundle", guideStateMissing)
	}
	var key secrets.Key
	opened := false
	for _, k := range sec.keys() {
		got, _, err := OpenKeyBundle(raw, k)
		if err == nil {
			key, opened = got, true
			break
		}
		if errors.Is(err, ErrBundleCorrupt) {
			return importRefusal(ImportStateMissing, "the secret-key bundle of this snapshot is damaged", guideStateMissing)
		}
	}
	if !opened {
		return importRefusal(ImportKeyRotated, "the manager secret key in this snapshot is sealed under another Recovery Key", guideRotated)
	}
	if s.opts.SecretKeyFile != "" {
		if err := probeWritable(filepath.Dir(s.opts.SecretKeyFile)); err != nil {
			return backup.Refuse("backup_import_key_file_read_only", "the secret key file's directory is not writable",
				"The restore replaces DOCKER_MANAGER_SECRET_KEY_FILE with the recovered key: mount it writable for the import, then import again.")
		}
	}
	sc.Progress(ctx, 15, "restoring the manager database")
	dbPath := filepath.Join(dir, restoredDBFile)
	f, err := os.OpenFile(dbPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // staging path below the data directory
	if err != nil {
		return err
	}
	derr := repo.Dump(ctx, in.ManagerSnapshotID, file(stateDBFile), f)
	cerr := f.Close()
	if derr != nil {
		return importFailure(derr, in.Destination.Repository(backup.ScopeManager))
	}
	if cerr != nil {
		return cerr
	}
	st, err := os.Stat(dbPath)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 70, "checking the restored database")
	adopt, notAdopted, err := s.checkRestoredDatabase(ctx, dbPath, info, key, sec, which)
	if err != nil {
		return err
	}
	if err := secrets.ReplaceKeyFile(filepath.Join(dir, restoredKeyFile), key); err != nil {
		return err
	}
	out := importScanOutput{InstanceID: info.InstanceID, AppVersion: info.App.Version, SchemaLatest: info.Schema.Latest(), SecretKeyID: key.ID(),
		KeyFingerprint: sec.current.Fingerprint(), ManagerKey: which, AdoptKey: adopt, KeyNotAdopted: notAdopted, DatabaseBytes: st.Size()}
	return sc.SetOutput(ctx, out)
}

// checkRestoredDatabase opens the staged database: integrity, instance,
// schema, and the Recovery Key record (sealed with the recovered secret
// key: proves the key decrypts the settings). It reports whether the
// entered key must be adopted as the current key.
func (s *Service) checkRestoredDatabase(ctx context.Context, path string, info StateInfo, key secrets.Key, sec importSecrets, which string) (adopt, notAdopted bool, err error) {
	db, err := store.Open(ctx, path)
	if err != nil {
		return false, false, importRefusal(ImportStateMissing, "the restored database cannot be opened", guideStateMissing)
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return false, false, importRefusal(ImportStateMissing, "the restored database fails its integrity check", guideStateMissing)
	}
	inst, found, err := store.GetInstance(ctx, db)
	if err != nil || !found || (info.InstanceID != "" && inst.ID != info.InstanceID) {
		return false, false, importRefusal(ImportStateMissing, "the restored database does not match its state.json", guideStateMissing)
	}
	if s.opts.CheckSchema != nil {
		if err := s.opts.CheckSchema(ctx, db); err != nil {
			if errors.Is(err, store.ErrUnknownMigrations) {
				return false, false, importRefusal(ImportSchemaIncompatible, "the restored database has migrations this Docker Manager does not know", guideSchema)
			}
			return false, false, err
		}
	}
	rec, found, err := store.GetBackupKey(ctx, db)
	if err != nil {
		return false, false, err
	}
	if !found || rec.State.Generation == 0 {
		return false, false, nil
	}
	kr := secrets.NewKeyring(key)
	b, err := kr.Open(rec.Sealed.Current, sealCurrent)
	if err != nil {
		return false, false, importRefusal(ImportStateMissing, "the recovered secret key does not decrypt the restored settings", guideStateMissing)
	}
	cur, err := ParseRecoveryKey(string(b))
	if err != nil {
		return false, false, importRefusal(ImportStateMissing, "the restored Recovery Key record is unreadable", guideStateMissing)
	}
	if cur.Equal(sec.current) {
		return false, false, nil
	}
	// The snapshot predates a rotation: adopt the entered key only when it
	// opened the manager repository or was the pending key of the
	// snapshot (never a key that opened nothing).
	if which == "current" || rec.State.PendingFingerprint == sec.current.Fingerprint() {
		return true, false, nil
	}
	return false, true, nil
}

func (s *Service) stepImportIndex(ctx context.Context, sc *jobexec.StepContext) error {
	in, sec, err := s.importJob(sc)
	if err != nil {
		return err
	}
	var out importScanOutput
	if err := json.Unmarshal(sc.Output(), &out); err != nil || out.SecretKeyID == "" {
		return errors.New("the scan result is missing; run the import again")
	}
	dir := s.importStaging(sc.JobID)
	key, err := secrets.LoadKeyFile(filepath.Join(dir, restoredKeyFile))
	if err != nil {
		return errors.New("the staged manager state is missing; run the import again")
	}
	if _, err := os.Stat(filepath.Join(dir, restoredDBFile)); err != nil {
		return errors.New("the staged manager state is missing; run the import again")
	}
	src := ImportSource{Destination: in.Destination}
	repo, _, _, err := s.openImport(ctx, src, sec, backup.ScopeManager)
	if err != nil {
		return importFailure(err, in.Destination.Repository(backup.ScopeManager))
	}
	sc.Progress(ctx, 80, "reading the manifests")
	mscan, err := scanManifests(ctx, repo, importMaxManifests, isSetManifest)
	if err != nil {
		return importFailure(err, in.Destination.Repository(backup.ScopeManager))
	}
	manifests := mscan.manifests
	out.SetManifests = len(manifests)
	for _, l := range s.importHosts(ctx, src, sec, mscan.manifests) {
		if l.repo == nil {
			continue
		}
		hs, err := scanManifests(ctx, l.repo, importMaxManifests, nil)
		if err != nil {
			continue
		}
		manifests = append(manifests, hs.manifests...)
		out.HostManifests += len(hs.manifests)
	}
	kr := secrets.NewKeyring(key)
	mk := RestoreMarker{Format: RestoreMarkerFormat, Version: 1, JobID: sc.JobID, SetID: in.SetID, InstanceID: out.InstanceID,
		RepositoryID: in.RepositoryID, Destination: in.Destination, App: backup.AppInfo{Version: out.AppVersion}, SchemaLatest: out.SchemaLatest,
		Manifests: manifests, StagedAt: s.now()}
	if in.Destination.Kind == backup.KindS3 {
		if mk.SealedCredentials, err = kr.Seal(jsonBytes(credentialsJSON{AccessKeyID: sec.creds.AccessKeyID, SecretAccessKey: sec.creds.SecretAccessKey}),
			sealImportCredentials); err != nil {
			return err
		}
	}
	if out.AdoptKey {
		if mk.SealedKey, err = kr.Seal([]byte(sec.current.String()), sealImportCurrent); err != nil {
			return err
		}
	}
	if err := writeJSONFile(filepath.Join(dir, restoreMarkerFile), mk); err != nil {
		return err
	}
	pending := filepath.Join(s.opts.DataDir, PendingRestoreDir)
	if err := os.RemoveAll(pending); err != nil {
		return err
	}
	if err := os.Rename(dir, pending); err != nil {
		return fmt.Errorf("stage the restore: %w", err)
	}
	out.RestartPending = true
	return sc.SetOutput(ctx, out)
}

type credentialsJSON struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// onImport forgets the import's secrets and, after success, asks for the
// controlled restart that applies the staged restore.
func (s *Service) onImport(_ context.Context, _ bun.IDB, j domain.Job) error {
	s.importMu.Lock()
	delete(s.imports, j.ID)
	s.importMu.Unlock()
	if j.State != domain.JobSucceeded {
		_ = os.RemoveAll(s.importStaging(j.ID))
		return nil
	}
	s.log.Info("manager state staged for restore; restarting to apply it", "job_id", j.ID)
	if s.opts.RequestRestart != nil {
		s.opts.RequestRestart()
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readMarker(path string) (*RestoreMarker, error) {
	b, err := os.ReadFile(path) //nolint:gosec // below the data directory
	if err != nil {
		return nil, err
	}
	var mk RestoreMarker
	if err := json.Unmarshal(b, &mk); err != nil || mk.Format != RestoreMarkerFormat {
		return nil, fmt.Errorf("unreadable restore marker %s", path)
	}
	return &mk, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ApplyPendingRestore applies a staged manager-state restore before the
// database is opened: the current database (with its -wal/-shm) and key
// file move to a pre-restore directory in the data directory, the restored
// database and the recovered key take their places, and the marker becomes
// AppliedRestoreFile (CompleteRestore finishes the restore once the
// manager runs). Every step can be repeated after a crash. It reports
// whether a restore was applied.
func ApplyPendingRestore(dataDir, dbPath, keyFile string, now time.Time) (*RestoreMarker, error) {
	pending := filepath.Join(dataDir, PendingRestoreDir)
	markerPath := filepath.Join(pending, restoreMarkerFile)
	if !exists(markerPath) {
		return nil, nil
	}
	mk, err := readMarker(markerPath)
	if err != nil {
		return nil, fmt.Errorf("%w: remove %s to start without the staged restore", err, pending)
	}
	if mk.PreRestoreDir == "" {
		mk.PreRestoreDir = filepath.Join(dataDir, "pre-restore-"+now.UTC().Format("20060102T150405Z"))
		if err := writeJSONFile(markerPath, mk); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(mk.PreRestoreDir, 0o700); err != nil {
		return nil, err
	}
	staged := filepath.Join(pending, restoredDBFile)
	if exists(staged) {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if exists(dbPath + suffix) {
				if err := os.Rename(dbPath+suffix, filepath.Join(mk.PreRestoreDir, filepath.Base(dbPath)+suffix)); err != nil {
					return nil, fmt.Errorf("keep the replaced database: %w", err)
				}
			}
		}
		if err := os.Rename(staged, dbPath); err != nil {
			return nil, fmt.Errorf("move the restored database into place: %w", err)
		}
	}
	stagedKey := filepath.Join(pending, restoredKeyFile)
	if exists(stagedKey) {
		k, err := secrets.LoadKeyFile(stagedKey)
		if err != nil {
			return nil, err
		}
		keep := filepath.Join(mk.PreRestoreDir, "secret.key")
		if exists(keyFile) && !exists(keep) {
			if err := copyFile(keyFile, keep); err != nil {
				return nil, fmt.Errorf("keep the replaced secret key: %w", err)
			}
		}
		if err := secrets.ReplaceKeyFile(keyFile, k); err != nil {
			return nil, err
		}
		if err := os.Remove(stagedKey); err != nil {
			return nil, err
		}
	}
	at := now.UTC()
	mk.AppliedAt = &at
	if err := writeJSONFile(filepath.Join(dataDir, AppliedRestoreFile), mk); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(pending); err != nil {
		return nil, err
	}
	return mk, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // operator-configured key file
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // below the data directory
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	return errors.Join(cerr, out.Sync(), out.Close())
}

// AppliedRestore returns the marker of an applied restore whose
// completion has not finished (nil when none).
func AppliedRestore(dataDir string) (*RestoreMarker, error) {
	p := filepath.Join(dataDir, AppliedRestoreFile)
	if !exists(p) {
		return nil, nil
	}
	return readMarker(p)
}

// FinishedRestore removes the applied-restore marker.
func FinishedRestore(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, AppliedRestoreFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RestoreCompletion is what CompleteRestore changed.
type RestoreCompletion struct {
	Relocated           bool
	CredentialsReplaced bool
	KeyAdopted          bool
	SnapshotsAdded      int
	SetsUpdated         int
	SetsAdded           int
}

// CompleteRestore finishes a restored manager's backup state (#24): the
// imported repository points at the destination and credentials entered
// for the import, a newer entered Recovery Key becomes current (the old
// one previous, so locations move as they are used), and the snapshot
// index is reconciled with every manifest read at the source (members
// that were still pending in the snapshot, and newer sets). Repeatable.
func (s *Service) CompleteRestore(ctx context.Context, mk *RestoreMarker) (RestoreCompletion, error) {
	var out RestoreCompletion
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := s.relocate(ctx, tx, mk, &out); err != nil {
			return err
		}
		if err := s.adoptKey(ctx, tx, mk, &out); err != nil {
			return err
		}
		return s.reconcile(ctx, tx, mk.Manifests, &out)
	})
	if err == nil {
		s.notify()
	}
	return out, err
}

func (s *Service) relocate(ctx context.Context, tx bun.IDB, mk *RestoreMarker, out *RestoreCompletion) error {
	r, err := store.GetBackupRepository(ctx, tx, mk.RepositoryID)
	if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	d := mk.Destination
	if d.Kind != r.Kind {
		s.log.Warn("the imported destination's kind differs from the restored repository; not relocated", "repository_id", r.ID)
		return nil
	}
	before := destination(r)
	r.Path, r.Endpoint, r.Bucket, r.Prefix, r.Region, r.PathStyle = d.Path, d.Endpoint, d.Bucket, d.Prefix, d.Region, d.PathStyle
	var sealed *store.BackupRepositorySealed
	if mk.SealedCredentials != "" {
		b, err := s.opts.Keyring.Open(mk.SealedCredentials, sealImportCredentials)
		if err != nil {
			return fmt.Errorf("open the imported credentials: %w", err)
		}
		var c credentialsJSON
		if err := json.Unmarshal(b, &c); err != nil {
			return err
		}
		sc, err := s.sealCredentials(r.ID, c.AccessKeyID, c.SecretAccessKey)
		if err != nil {
			return err
		}
		sealed = &sc
		fp := s.opts.Keyring.Fingerprint([]byte(c.AccessKeyID+"\x00"+c.SecretAccessKey), "backup_repositories/"+r.ID+"/credentials")
		out.CredentialsReplaced = fp != r.CredentialFingerprint
		r.CredentialFingerprint = fp
	}
	out.Relocated = before != destination(r)
	if !out.Relocated && sealed == nil {
		return nil
	}
	r.Revision, r.UpdatedAt = r.Revision+1, s.now()
	return store.RelocateBackupRepository(ctx, tx, &r, sealed)
}

func (s *Service) adoptKey(ctx context.Context, tx bun.IDB, mk *RestoreMarker, out *RestoreCompletion) error {
	if mk.SealedKey == "" {
		return nil
	}
	b, err := s.opts.Keyring.Open(mk.SealedKey, sealImportCurrent)
	if err != nil {
		return fmt.Errorf("open the imported Recovery Key: %w", err)
	}
	k, err := ParseRecoveryKey(string(b))
	if err != nil {
		return err
	}
	rec, found, err := store.GetBackupKey(ctx, tx)
	if err != nil || !found || rec.State.Generation == 0 {
		return err
	}
	cur, err := s.open(rec.Sealed.Current, sealCurrent)
	if err != nil {
		return err
	}
	if cur.Equal(k) {
		return nil
	}
	now := s.now()
	if rec.Sealed.Previous, err = s.seal(cur, sealPrevious); err != nil {
		return err
	}
	if rec.Sealed.Current, err = s.seal(k, sealCurrent); err != nil {
		return err
	}
	rec.State.PreviousFingerprint, rec.State.RotationStartedAt = rec.State.Fingerprint, &now
	rec.State.Fingerprint, rec.State.ConfirmedAt = k.Fingerprint(), &now
	rec.State.Generation++
	if rec.State.PendingFingerprint == k.Fingerprint() {
		rec.Sealed.Pending, rec.State.PendingFingerprint, rec.State.PendingCreatedAt = "", "", nil
	}
	if err := store.PutBackupKey(ctx, tx, rec, rec.State.Revision, now); err != nil {
		return err
	}
	out.KeyAdopted = true
	return nil
}

// reconcile brings the snapshot index up to date with manifests.
func (s *Service) reconcile(ctx context.Context, tx bun.IDB, manifests []backup.Manifest, out *RestoreCompletion) error {
	now := s.now()
	bySet := map[string][]backup.Manifest{}
	var order []string
	for _, m := range manifests {
		if _, ok := bySet[m.SetID]; !ok {
			order = append(order, m.SetID)
		}
		bySet[m.SetID] = append(bySet[m.SetID], m)
	}
	for _, setID := range order {
		ms := bySet[setID]
		var members []backup.Member
		var base *backup.Manifest
		for i := range ms {
			if ms[i].Kind == backup.ManifestSet {
				base = &ms[i]
			}
		}
		if base != nil {
			members, _ = backup.Merge(*base, ms)
		} else {
			base = &ms[0]
			for _, m := range ms {
				members = append(members, m.Members...)
			}
		}
		for _, m := range members {
			if m.SnapshotID == "" || !backup.ValidScope(m.Scope) {
				continue
			}
			state := backup.StateComplete
			if m.State == backup.StatePartial {
				state = backup.StatePartial
			}
			env, _ := backup.ScopeEnvironment(m.Scope)
			sn := domain.BackupSnapshot{ID: ids.New(), SetID: setID, PolicyID: base.PolicyID, RepositoryID: m.RepositoryID, Scope: m.Scope,
				EnvironmentID: env, Kind: m.Kind, Item: m.Item, StackID: m.StackID, StackName: m.StackName, Volume: m.Volume,
				ResticSnapshotID: m.SnapshotID, SnapshotTime: m.SnapshotTime, Paths: m.Paths, Volumes: m.Volumes, ProjectPath: m.ProjectPath,
				VolumePaths: m.VolumePaths, Consistency: m.Consistency, State: state, BytesTotal: m.Bytes, CreatedAt: now}
			if _, err := store.GetBackupRepository(ctx, tx, m.RepositoryID); err != nil {
				continue // a repository the restored manager does not know
			}
			inserted, err := store.InsertBackupSnapshot(ctx, tx, &sn)
			if err != nil {
				return err
			}
			if inserted {
				out.SnapshotsAdded++
			}
		}
		set, err := store.GetBackupSet(ctx, tx, setID)
		switch {
		case errors.Is(err, domain.ErrBackupSetNotFound):
			set = domain.BackupSet{ID: setID, PolicyID: base.PolicyID, PolicyName: base.PolicyName, Origin: domain.OriginManual,
				StartedAt: base.StartedAt, FollowUp: "done"}
			if base.PolicyID != "" {
				set.Origin = domain.OriginScheduled
			}
			if set.StartedAt.IsZero() {
				set.StartedAt = base.CreatedAt
			}
			for _, m := range members {
				set.Members = append(set.Members, setMember(m))
			}
			s.settle(&set)
			if _, err := store.InsertBackupSet(ctx, tx, &set); err != nil {
				return err
			}
			out.SetsAdded++
		case err != nil:
			return err
		default:
			changed := false
			for i := range set.Members {
				cur := &set.Members[i]
				if cur.SnapshotID != "" && cur.State == backup.StateComplete {
					continue
				}
				for _, m := range members {
					if m.Scope == cur.Scope && m.Item == cur.Item && m.SnapshotID != "" {
						jobID := cur.JobID
						*cur = setMember(m)
						cur.JobID = jobID
						changed = true
					}
				}
			}
			if changed {
				s.settle(&set)
				if err := store.UpdateBackupSet(ctx, tx, &set); err != nil {
					return err
				}
				out.SetsUpdated++
			}
		}
	}
	return nil
}

func setMember(m backup.Member) domain.BackupSetMember {
	out := domain.BackupSetMember{Item: m.Item, Kind: m.Kind, Scope: m.Scope, RepositoryID: m.RepositoryID, EnvironmentID: m.EnvironmentID,
		StackID: m.StackID, StackName: m.StackName, Volume: m.Volume, State: m.State, ErrorClass: m.ErrorClass, SnapshotID: m.SnapshotID}
	if m.SnapshotID != "" {
		t := m.SnapshotTime
		out.SnapshotTime = &t
		if out.State != backup.StatePartial {
			out.State = backup.StateComplete
		}
	}
	if out.State == backup.StatePending {
		out.State, out.ErrorClass = backup.StateFailed, "not_run"
	}
	return out
}
