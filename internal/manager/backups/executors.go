package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/templates"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// Manager-state snapshot layout. The staging directory lives in the data
// volume (it holds a database copy) and is removed after every run and at
// startup.
const (
	stagingDirName = "backup-staging"
	// StateDir is the directory name inside manager-state snapshots.
	StateDir      = "docker-manager-state"
	stateDBFile   = "docker-manager.db"
	stateMetrics  = "metrics.db"
	stateInfoFile = "state.json"
	// ManagerHost is the hostname of manager snapshots.
	ManagerHost = "docker-manager"
)

// StateInfo describes a manager-state snapshot (state.json).
type StateInfo struct {
	Format          string            `json:"format"`
	Version         int               `json:"version"`
	InstanceID      string            `json:"instanceId"`
	CreatedAt       time.Time         `json:"createdAt"`
	App             backup.AppInfo    `json:"app"`
	Schema          backup.SchemaInfo `json:"schema"`
	SecretKeyID     string            `json:"secretKeyId"`
	MetricsIncluded bool              `json:"metricsIncluded"`
	// TemplatesIncluded: the template drafts are in templates.tar.gz (the
	// published versions are in the database).
	TemplatesIncluded bool `json:"templatesIncluded,omitempty"`
}

// StateFormat identifies state.json.
const StateFormat = "docker-manager-state"

// managerBackupOutput is the result output of manager.backup.
type managerBackupOutput struct {
	ResticRepositoryID string         `json:"resticRepositoryId,omitempty"`
	KeyGeneration      int            `json:"keyGeneration,omitempty"`
	Member             *backup.Member `json:"member,omitempty"`
	ManifestSnapshotID string         `json:"manifestSnapshotId,omitempty"`
	// Stats is the location's size after the run (#10).
	Stats *protocol.RepositoryStats `json:"stats,omitempty"`
}

func (s *Service) executors() []jobexec.Executor {
	return []jobexec.Executor{
		{Kind: jobspec.ManagerBackup, Steps: map[string]jobexec.StepFunc{
			"snapshot_database": s.stepSnapshotDatabase,
			"backup":            s.stepManagerBackup,
			"write_manifest":    s.stepWriteManifest,
		}},
		{Kind: jobspec.ManagerRetention, Steps: map[string]jobexec.StepFunc{
			"forget":           s.stepManagerForget,
			"prune_repository": s.stepManagerPrune,
		}},
		{Kind: jobspec.ManagerVerify, Steps: map[string]jobexec.StepFunc{
			"check": s.stepManagerCheck,
		}},
		s.importExecutor(),
	}
}

func (s *Service) staging(jobID string) string {
	return filepath.Join(s.opts.DataDir, stagingDirName, jobID)
}

// managerLocation opens the manager scope of a repository for a job.
func (s *Service) managerLocation(ctx context.Context, repositoryID string, init bool) (backup.Opened, int, domain.BackupRepository, error) {
	repo, err := store.GetBackupRepository(ctx, s.db, repositoryID)
	if err != nil {
		return backup.Opened{}, 0, repo, err
	}
	if !Serves(repo, backup.ScopeManager) {
		return backup.Opened{}, 0, repo, backup.Refuse("repository_not_usable", "this repository cannot hold the manager state",
			"Use an S3 repository or a local repository on the manager.")
	}
	if repo.State != domain.BackupRepositoryReady {
		return backup.Opened{}, 0, repo, backup.Refuse("recovery_key_not_confirmed", "the Recovery Key of this repository is not confirmed",
			"Confirm the Recovery Key for the repository, then run the job again.")
	}
	cur, prev, gen, _, err := s.currentKeys(ctx, s.db)
	if err != nil {
		return backup.Opened{}, 0, repo, err
	}
	creds, err := s.credentials(ctx, s.db, repositoryID)
	if err != nil {
		return backup.Opened{}, 0, repo, err
	}
	o, err := backup.OpenLocation(ctx, s.opts.Restic, destination(repo).Location(backup.ScopeManager, creds), cur, prev, init)
	return o, gen, repo, err
}

// --- manager.backup ---

func (s *Service) stepSnapshotDatabase(ctx context.Context, sc *jobexec.StepContext) error {
	var in managerBackupInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	dir := filepath.Join(s.staging(sc.JobID), StateDir)
	if err := os.RemoveAll(s.staging(sc.JobID)); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sc.Progress(ctx, 5, "taking a consistent database snapshot")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(dir, stateDBFile)); err != nil {
		return fmt.Errorf("database snapshot: %w", err)
	}
	if in.IncludeMetrics && s.opts.MetricsSnapshot != nil {
		if err := s.opts.MetricsSnapshot(ctx, filepath.Join(dir, stateMetrics)); err != nil {
			return fmt.Errorf("metrics snapshot: %w", err)
		}
	}
	templatesIncluded := false
	if s.opts.DataDir != "" {
		sc.Progress(ctx, 10, "copying the template drafts")
		if err := writeDrafts(filepath.Join(dir, templates.DraftsArchiveName), templates.DraftsDir(s.opts.DataDir)); err != nil {
			return fmt.Errorf("template drafts: %w", err)
		}
		templatesIncluded = true
	}
	cur, _, _, _, err := s.currentKeys(ctx, s.db)
	if err != nil {
		return err
	}
	rk, err := ParseRecoveryKey(cur)
	if err != nil {
		return err
	}
	bundle, err := SealKeyBundle(s.opts.Keyring.Primary(), rk, s.opts.InstanceID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, BundleFile), bundle, 0o600); err != nil {
		return err
	}
	var migrations []string
	if s.opts.Migrations != nil {
		if migrations, err = s.opts.Migrations(ctx); err != nil {
			return err
		}
	}
	info := StateInfo{Format: StateFormat, Version: 1, InstanceID: s.opts.InstanceID, CreatedAt: s.now(),
		App: backup.AppInfo{Version: s.opts.Build.Version, Commit: s.opts.Build.Commit}, Schema: backup.SchemaInfo{Migrations: migrations},
		SecretKeyID: s.opts.Keyring.Primary().ID(), MetricsIncluded: in.IncludeMetrics, TemplatesIncluded: templatesIncluded}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateInfoFile), b, 0o600)
}

// writeDrafts writes the template drafts archive of a snapshot.
func writeDrafts(file, templatesDir string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // staging path below the data directory
	if err != nil {
		return err
	}
	werr := templates.WriteDrafts(f, templatesDir)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func (s *Service) stepManagerBackup(ctx context.Context, sc *jobexec.StepContext) error {
	var in managerBackupInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	dir := filepath.Join(s.staging(sc.JobID), StateDir)
	if _, err := os.Stat(filepath.Join(dir, stateDBFile)); err != nil {
		return errors.New("the database snapshot is missing; run the manager backup again")
	}
	sc.Progress(ctx, 20, "opening the manager-state repository")
	o, gen, _, err := s.managerLocation(ctx, in.RepositoryID, true)
	if err != nil {
		return err
	}
	out := managerBackupOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: gen}
	tags := []string{backup.TagManagerState, backup.SetTag(in.SetID), backup.ItemTag(backup.ItemManagerState)}
	if in.PolicyID != "" {
		tags = append(tags, backup.PolicyTag(in.PolicyID))
	}
	sum, err := o.Repo.Backup(ctx, restic.BackupRequest{Paths: []string{dir}, Tags: tags, Host: ManagerHost,
		Progress: func(p restic.Progress) {
			sc.Progress(ctx, 20+int(p.Percent*0.7), "backing up the manager state")
			a := protocol.ActivityPayload{Item: backup.ItemManagerState, ItemCount: 1, Percent: min(100, max(0, int(p.Percent))),
				FilesDone: max(0, p.FilesDone), FilesTotal: max(0, p.FilesTotal), BytesDone: max(0, p.BytesDone),
				BytesTotal: max(0, p.BytesTotal), SecondsRemaining: max(0, p.SecondsRemaining)}
			if p.CurrentFile != "" {
				if rel, err := filepath.Rel(dir, p.CurrentFile); err == nil && !strings.HasPrefix(rel, "..") {
					a.CurrentFile = filepath.ToSlash(rel)
				}
			}
			sc.Activity(ctx, a)
		}})
	if err != nil {
		_ = sc.SetOutput(ctx, out)
		return err
	}
	snaps, err := o.Repo.Snapshots(ctx, restic.SnapshotFilter{Tags: []string{backup.SetTag(in.SetID), backup.TagManagerState}})
	at := s.now()
	if err == nil {
		for _, sn := range snaps {
			if sn.ID == sum.SnapshotID {
				at = sn.Time.UTC()
			}
		}
	}
	state := backup.StateComplete
	if sum.Incomplete {
		state = backup.StatePartial
	}
	out.Member = &backup.Member{Item: backup.ItemManagerState, Kind: backup.MemberManagerState, Scope: backup.ScopeManager,
		RepositoryID: in.RepositoryID, SnapshotID: sum.SnapshotID, SnapshotTime: at, Paths: []string{filepath.ToSlash(dir)},
		Consistency: backup.ConsistencySnapshot, State: state, Bytes: sum.TotalBytesProcessed}
	return sc.SetOutput(ctx, out)
}

func (s *Service) stepWriteManifest(ctx context.Context, sc *jobexec.StepContext) error {
	defer func() { _ = os.RemoveAll(s.staging(sc.JobID)) }()
	var in managerBackupInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	var out managerBackupOutput
	if err := json.Unmarshal(sc.Output(), &out); err != nil || out.Member == nil {
		return errors.New("the manager snapshot result is missing")
	}
	m, err := s.buildSetManifest(ctx, in.SetID, out.Member)
	if err != nil {
		return err
	}
	b, err := backup.EncodeManifest(m)
	if err != nil {
		return err
	}
	o, _, _, err := s.managerLocation(ctx, in.RepositoryID, false)
	if err != nil {
		return err
	}
	sum, err := o.Repo.Backup(ctx, restic.BackupRequest{Stdin: bytes.NewReader(b), StdinFilename: backup.ManifestFile,
		Tags: []string{backup.TagManifest, backup.SetTag(in.SetID)}, Host: ManagerHost})
	if err != nil {
		return err
	}
	out.ManifestSnapshotID = sum.SnapshotID
	out.Stats = protocol.StatsOf(backup.MeasureStats(ctx, o.Repo))
	return sc.SetOutput(ctx, out)
}

// buildSetManifest describes a set from the index (#24): repositories and
// locations without credentials, every planned member with its result so
// far, and this manager's schema.
func (s *Service) buildSetManifest(ctx context.Context, setID string, manager *backup.Member) (backup.Manifest, error) {
	set, err := store.GetBackupSet(ctx, s.db, setID)
	if err != nil {
		return backup.Manifest{}, err
	}
	key, err := s.KeyState(ctx)
	if err != nil {
		return backup.Manifest{}, err
	}
	m := backup.Manifest{Kind: backup.ManifestSet, SetID: set.ID, InstanceID: s.opts.InstanceID, PolicyID: set.PolicyID,
		PolicyName: set.PolicyName, StartedAt: set.StartedAt, CreatedAt: s.now(),
		App: backup.AppInfo{Version: s.opts.Build.Version, Commit: s.opts.Build.Commit}}
	if s.opts.Migrations != nil {
		migs, err := s.opts.Migrations(ctx)
		if err != nil {
			return m, err
		}
		m.Schema = &backup.SchemaInfo{Migrations: migs}
	}
	repos := map[string]bool{}
	scopes := map[[2]string]bool{}
	for _, mem := range set.Members {
		bm := backup.Member{Item: mem.Item, Kind: mem.Kind, Scope: mem.Scope, RepositoryID: mem.RepositoryID, EnvironmentID: mem.EnvironmentID,
			StackID: mem.StackID, StackName: mem.StackName, Volume: mem.Volume, SnapshotID: mem.SnapshotID, State: mem.State,
			ErrorClass: mem.ErrorClass}
		if mem.SnapshotTime != nil {
			bm.SnapshotTime = *mem.SnapshotTime
		}
		switch mem.Kind {
		case backup.MemberStack:
			bm.RequiredCapabilities = []string{"compose", "files"}
		case backup.MemberVolume:
			bm.RequiredCapabilities = []string{"files"}
		}
		if mem.Kind == backup.MemberManagerState && manager != nil {
			bm = *manager
		}
		m.Members = append(m.Members, bm)
		repos[mem.RepositoryID] = true
		scopes[[2]string{mem.RepositoryID, mem.Scope}] = true
	}
	locs, err := store.ListBackupLocations(ctx, s.db, "")
	if err != nil {
		return m, err
	}
	for id := range repos {
		r, err := store.GetBackupRepository(ctx, s.db, id)
		if err != nil {
			continue
		}
		ref := backup.RepositoryRef{ID: r.ID, Name: r.Name, Destination: destination(r), KeyFingerprint: key.Fingerprint,
			KeyGeneration: key.Generation}
		if r.Kind == backup.KindLocal {
			ref.Executor = r.Executor
		}
		m.Repositories = append(m.Repositories, ref)
		for k := range scopes {
			if k[0] != id {
				continue
			}
			lr := backup.LocationRef{RepositoryID: id, Scope: k[1], Repository: destination(r).Repository(k[1]), KeyFingerprint: key.Fingerprint}
			for _, l := range locs {
				if l.RepositoryID == id && l.Scope == k[1] {
					lr.ResticRepositoryID = l.ResticRepositoryID
				}
			}
			if env, ok := backup.ScopeEnvironment(k[1]); ok {
				lr.EnvironmentID = env
				if e, err := s.environment(ctx, env); err == nil {
					lr.EnvironmentName, lr.EngineID = e.Name, e.EngineID
				}
			}
			m.Locations = append(m.Locations, lr)
		}
	}
	m.Completeness = backup.Completeness(m.Members)
	return m, nil
}

// --- manager.retention ---

func (s *Service) stepManagerForget(ctx context.Context, sc *jobexec.StepContext) error {
	var in managerRetentionInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	o, gen, _, err := s.managerLocation(ctx, in.RepositoryID, false)
	if err != nil {
		return err
	}
	res, err := backup.ApplyRetention(ctx, o.Repo, in.PolicyID, in.Rules, nil, in.TimeZone) // manager state is never a deleted item
	out := protocol.RetentionOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: gen, Forgotten: res.Forgotten, Kept: res.Kept}
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	return err
}

func (s *Service) stepManagerPrune(ctx context.Context, sc *jobexec.StepContext) error {
	var in managerRetentionInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	var out protocol.RetentionOutput
	_ = json.Unmarshal(sc.Output(), &out)
	if len(out.Forgotten) == 0 {
		return nil // nothing forgotten: no prune (it downloads and rewrites pack data)
	}
	o, _, _, err := s.managerLocation(ctx, in.RepositoryID, false)
	if err != nil {
		return err
	}
	var after *restic.Stats
	out.ReclaimedBytes, after, err = backup.Prune(ctx, o.Repo)
	if err != nil {
		out.PruneError = restic.CodeOf(err)
		after = backup.MeasureStats(ctx, o.Repo) // forget still changed it
	}
	out.Stats = protocol.StatsOf(after)
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	return err
}

// --- manager.verify ---

func (s *Service) stepManagerCheck(ctx context.Context, sc *jobexec.StepContext) error {
	var in managerVerifyInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	o, gen, _, err := s.managerLocation(ctx, in.RepositoryID, false)
	if err != nil {
		return err
	}
	res, err := backup.Verify(ctx, o.Repo, in.ReadDataSubset)
	out := protocol.VerifyOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: gen, ReadData: res.ReadData,
		Damaged: res.Damaged, Snapshots: res.Snapshots}
	for {
		serr := sc.SetOutput(ctx, out)
		if !errors.Is(serr, jobexec.ErrOutputTooLarge) || len(out.Snapshots) == 0 {
			if serr != nil {
				return serr
			}
			break
		}
		out.Snapshots = out.Snapshots[len(out.Snapshots)/2:]
	}
	return err
}
