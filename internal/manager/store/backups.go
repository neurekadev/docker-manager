package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Backup persistence (#10, #24). Sealed values (the Recovery Key slots, S3
// credentials) never leave this package inside a domain value: they are
// read only through BackupKeySecrets and BackupRepositorySecrets.

// --- Recovery Key state ---

type backupKeyRow struct {
	bun.BaseModel `bun:"table:backup_key_state"`

	ID                  int        `bun:"id,pk"`
	Generation          int        `bun:"generation,notnull"`
	CurrentSealed       string     `bun:"current_sealed,notnull"`
	CurrentFingerprint  string     `bun:"current_fingerprint,notnull"`
	CreatedAt           *time.Time `bun:"created_at"`
	ConfirmedAt         *time.Time `bun:"confirmed_at"`
	PendingSealed       string     `bun:"pending_sealed,notnull"`
	PendingFingerprint  string     `bun:"pending_fingerprint,notnull"`
	PendingCreatedAt    *time.Time `bun:"pending_created_at"`
	PreviousSealed      string     `bun:"previous_sealed,notnull"`
	PreviousFingerprint string     `bun:"previous_fingerprint,notnull"`
	RotationStartedAt   *time.Time `bun:"rotation_started_at"`
	Revision            int64      `bun:"revision,notnull"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull"`
}

// BackupKeySealed are the sealed Recovery Key slots ("" = empty).
type BackupKeySealed struct {
	Current  string
	Pending  string
	Previous string
}

// BackupKeyRecord is the stored key state with its sealed slots.
type BackupKeyRecord struct {
	State  domain.BackupKeyState
	Sealed BackupKeySealed
}

func (r backupKeyRow) toRecord() BackupKeyRecord {
	return BackupKeyRecord{
		State: domain.BackupKeyState{Generation: r.Generation, Fingerprint: r.CurrentFingerprint, CreatedAt: utcPtr(r.CreatedAt),
			ConfirmedAt: utcPtr(r.ConfirmedAt), PendingFingerprint: r.PendingFingerprint, PendingCreatedAt: utcPtr(r.PendingCreatedAt),
			PreviousFingerprint: r.PreviousFingerprint, RotationStartedAt: utcPtr(r.RotationStartedAt), Revision: r.Revision},
		Sealed: BackupKeySealed{Current: r.CurrentSealed, Pending: r.PendingSealed, Previous: r.PreviousSealed},
	}
}

// GetBackupKey returns the key record (found false before any key).
func GetBackupKey(ctx context.Context, db bun.IDB) (BackupKeyRecord, bool, error) {
	var row backupKeyRow
	err := db.NewSelect().Model(&row).Where("id = 1").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupKeyRecord{}, false, nil
	}
	if err != nil {
		return BackupKeyRecord{}, false, fmt.Errorf("store: get backup key: %w", err)
	}
	return row.toRecord(), true, nil
}

// PutBackupKey writes the key record: an insert when expectRevision is 0,
// otherwise an update if the stored revision still matches. The record's
// revision is set to expectRevision+1.
func PutBackupKey(ctx context.Context, db bun.IDB, rec BackupKeyRecord, expectRevision int64, now time.Time) error {
	s := rec.State
	row := backupKeyRow{ID: 1, Generation: s.Generation, CurrentSealed: rec.Sealed.Current, CurrentFingerprint: s.Fingerprint,
		CreatedAt: utcPtr(s.CreatedAt), ConfirmedAt: utcPtr(s.ConfirmedAt), PendingSealed: rec.Sealed.Pending,
		PendingFingerprint: s.PendingFingerprint, PendingCreatedAt: utcPtr(s.PendingCreatedAt), PreviousSealed: rec.Sealed.Previous,
		PreviousFingerprint: s.PreviousFingerprint, RotationStartedAt: utcPtr(s.RotationStartedAt), Revision: expectRevision + 1,
		UpdatedAt: now.UTC()}
	if expectRevision == 0 {
		if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
			if uniqueViolation(err, "backup_key_state.id") {
				return domain.ErrRevisionMismatch
			}
			return fmt.Errorf("store: insert backup key: %w", err)
		}
		return nil
	}
	res, err := db.NewUpdate().Model(&row).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update backup key: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}

// --- repositories ---

type backupRepositoryRow struct {
	bun.BaseModel `bun:"table:backup_repositories"`

	ID                    string     `bun:"id,pk"`
	Name                  string     `bun:"name,notnull"`
	NameKey               string     `bun:"name_key,notnull"`
	Kind                  string     `bun:"kind,notnull"`
	Executor              string     `bun:"executor,notnull"`
	Path                  string     `bun:"path,notnull"`
	Endpoint              string     `bun:"endpoint,notnull"`
	Bucket                string     `bun:"bucket,notnull"`
	Prefix                string     `bun:"prefix,notnull"`
	Region                string     `bun:"region,notnull"`
	PathStyle             int        `bun:"path_style,notnull"`
	AccessKeySealed       string     `bun:"access_key_sealed,notnull"`
	SecretKeySealed       string     `bun:"secret_key_sealed,notnull"`
	CredentialFingerprint string     `bun:"credential_fingerprint,notnull"`
	State                 string     `bun:"state,notnull"`
	ConfirmedAt           *time.Time `bun:"confirmed_at"`
	VerifyCron            string     `bun:"verify_cron,notnull"`
	VerifyTimeZone        string     `bun:"verify_time_zone,notnull"`
	VerifyEnabled         int        `bun:"verify_enabled,notnull"`
	VerifyReadData        string     `bun:"verify_read_data,notnull"`
	LastTestAt            *time.Time `bun:"last_test_at"`
	LastTestResult        string     `bun:"last_test_result,notnull"`
	LastTest              string     `bun:"last_test,notnull"`
	Revision              int64      `bun:"revision,notnull"`
	CreatedAt             time.Time  `bun:"created_at,notnull"`
	UpdatedAt             time.Time  `bun:"updated_at,notnull"`
}

// BackupRepositorySealed are a repository's sealed S3 credentials.
type BackupRepositorySealed struct {
	AccessKey string
	SecretKey string
}

func fromBackupRepository(r *domain.BackupRepository, sealed BackupRepositorySealed) backupRepositoryRow {
	var test string
	if r.LastTest != nil {
		b, _ := json.Marshal(r.LastTest)
		test = string(b)
	}
	return backupRepositoryRow{ID: r.ID, Name: r.Name, NameKey: NameKey(r.Name), Kind: r.Kind, Executor: r.Executor, Path: r.Path,
		Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix, Region: r.Region, PathStyle: b2i(r.PathStyle),
		AccessKeySealed: sealed.AccessKey, SecretKeySealed: sealed.SecretKey, CredentialFingerprint: r.CredentialFingerprint,
		State: r.State, ConfirmedAt: utcPtr(r.ConfirmedAt), VerifyCron: r.VerifyCron, VerifyTimeZone: r.VerifyTimeZone,
		VerifyEnabled: b2i(r.VerifyEnabled), VerifyReadData: r.VerifyReadData, LastTestAt: utcPtr(r.LastTestAt),
		LastTestResult: r.LastTestResult, LastTest: test, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func (r backupRepositoryRow) toDomain() domain.BackupRepository {
	out := domain.BackupRepository{ID: r.ID, Name: r.Name, Kind: r.Kind, Executor: r.Executor, Path: r.Path, Endpoint: r.Endpoint,
		Bucket: r.Bucket, Prefix: r.Prefix, Region: r.Region, PathStyle: r.PathStyle == 1, CredentialFingerprint: r.CredentialFingerprint,
		State: r.State, ConfirmedAt: utcPtr(r.ConfirmedAt), VerifyCron: r.VerifyCron, VerifyTimeZone: r.VerifyTimeZone,
		VerifyEnabled: r.VerifyEnabled == 1, VerifyReadData: r.VerifyReadData, LastTestAt: utcPtr(r.LastTestAt),
		LastTestResult: r.LastTestResult, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	if r.LastTest != "" {
		var t domain.BackupConnectionTest
		if json.Unmarshal([]byte(r.LastTest), &t) == nil {
			out.LastTest = &t
		}
	}
	return out
}

// InsertBackupRepository stores a new repository.
func InsertBackupRepository(ctx context.Context, db bun.IDB, r *domain.BackupRepository, sealed BackupRepositorySealed) error {
	row := fromBackupRepository(r, sealed)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "backup_policy_scope") || uniqueViolation(err, "backup_policies.environment_id") {
			return domain.ErrBackupScopeOverlap
		}
		if uniqueViolation(err, "backup_policies.environment_id") || uniqueViolation(err, "backup_policy_scope") {
			return domain.ErrBackupScopeOverlap
		}
		if uniqueViolation(err, "backup_repositories.name_key") {
			return domain.ErrBackupRepositoryNameUsed
		}
		return fmt.Errorf("store: insert backup repository: %w", err)
	}
	return nil
}

// UpdateBackupRepository writes r's configuration when the revision still
// matches; sealed credentials are written when replace is true.
func UpdateBackupRepository(ctx context.Context, db bun.IDB, r *domain.BackupRepository, expectRevision int64,
	sealed *BackupRepositorySealed) error {
	var s BackupRepositorySealed
	cols := []string{"name", "name_key", "region", "path_style", "state", "confirmed_at", "verify_cron", "verify_time_zone",
		"verify_enabled", "verify_read_data", "revision", "updated_at"}
	if sealed != nil {
		s = *sealed
		cols = append(cols, "access_key_sealed", "secret_key_sealed", "credential_fingerprint")
	}
	row := fromBackupRepository(r, s)
	res, err := db.NewUpdate().Model(&row).Column(cols...).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "backup_repositories.name_key") {
			return domain.ErrBackupRepositoryNameUsed
		}
		return fmt.Errorf("store: update backup repository: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBackupRepository(ctx, db, r.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// RelocateBackupRepository points a repository at a new destination of the
// same kind (a restored manager whose repository was re-mounted or moved,
// #24) and, when sealed is set, replaces its credentials. The revision
// increases.
func RelocateBackupRepository(ctx context.Context, db bun.IDB, r *domain.BackupRepository, sealed *BackupRepositorySealed) error {
	var s BackupRepositorySealed
	cols := []string{"path", "endpoint", "bucket", "prefix", "region", "path_style", "revision", "updated_at"}
	if sealed != nil {
		s = *sealed
		cols = append(cols, "access_key_sealed", "secret_key_sealed", "credential_fingerprint")
	}
	row := fromBackupRepository(r, s)
	res, err := db.NewUpdate().Model(&row).Column(cols...).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: relocate backup repository: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrBackupRepositoryNotFound
	}
	return nil
}

// RecordBackupRepositoryTest stores a connection test (status, not
// configuration: the revision does not change).
func RecordBackupRepositoryTest(ctx context.Context, db bun.IDB, id string, t domain.BackupConnectionTest) error {
	b, _ := json.Marshal(t)
	if _, err := db.NewUpdate().Model((*backupRepositoryRow)(nil)).Where("id = ?", id).
		Set("last_test_at = ?", t.At.UTC()).Set("last_test_result = ?", t.Result).Set("last_test = ?", string(b)).Exec(ctx); err != nil {
		return fmt.Errorf("store: record backup repository test: %w", err)
	}
	return nil
}

// GetBackupRepository returns one repository (without credentials).
func GetBackupRepository(ctx context.Context, db bun.IDB, id string) (domain.BackupRepository, error) {
	var row backupRepositoryRow
	err := db.NewSelect().Model(&row).ExcludeColumn("access_key_sealed", "secret_key_sealed").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupRepository{}, domain.ErrBackupRepositoryNotFound
	}
	if err != nil {
		return domain.BackupRepository{}, fmt.Errorf("store: get backup repository: %w", err)
	}
	return row.toDomain(), nil
}

// ListBackupRepositories returns repositories in ID order after afterID
// (limit 0 = all).
func ListBackupRepositories(ctx context.Context, db bun.IDB, afterID string, limit int) ([]domain.BackupRepository, error) {
	var rows []backupRepositoryRow
	q := db.NewSelect().Model(&rows).ExcludeColumn("access_key_sealed", "secret_key_sealed").Order("id ASC")
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup repositories: %w", err)
	}
	out := make([]domain.BackupRepository, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// BackupRepositorySecrets returns a repository's sealed credentials.
func BackupRepositorySecrets(ctx context.Context, db bun.IDB, id string) (BackupRepositorySealed, error) {
	var row backupRepositoryRow
	err := db.NewSelect().Model(&row).Column("access_key_sealed", "secret_key_sealed").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRepositorySealed{}, domain.ErrBackupRepositoryNotFound
	}
	if err != nil {
		return BackupRepositorySealed{}, fmt.Errorf("store: read backup repository credentials: %w", err)
	}
	return BackupRepositorySealed{AccessKey: row.AccessKeySealed, SecretKey: row.SecretKeySealed}, nil
}

// DeleteBackupRepository removes a repository (its locations cascade)
// when the revision matches and no policy uses it, together with its part
// of the backup index: its snapshots and the sets whose members were all
// in it (sets spanning other repositories stay). Nothing at the
// destination changes. It returns the removed snapshot IDs; call it in a
// transaction.
func DeleteBackupRepository(ctx context.Context, db bun.IDB, id string, expectRevision int64) (snapshotIDs []string, err error) {
	var n int
	if n, err = db.NewSelect().Model((*backupPolicyRow)(nil)).
		Where("repository_id = ? OR environment_repos LIKE ?", id, "%\""+id+"\"%").Count(ctx); err != nil {
		return nil, fmt.Errorf("store: count policies: %w", err)
	}
	if n > 0 {
		return nil, domain.ErrBackupRepositoryInUse
	}
	res, err := db.NewDelete().Model((*backupRepositoryRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: delete backup repository: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBackupRepository(ctx, db, id); gerr != nil {
			return nil, gerr
		}
		return nil, domain.ErrRevisionMismatch
	}
	if err := db.NewSelect().Model((*backupSnapshotRow)(nil)).Column("id").Where("repository_id = ?", id).Order("id ASC").
		Scan(ctx, &snapshotIDs); err != nil {
		return nil, fmt.Errorf("store: list repository snapshots: %w", err)
	}
	if _, err := db.NewDelete().Model((*backupSnapshotRow)(nil)).Where("repository_id = ?", id).Exec(ctx); err != nil {
		return nil, fmt.Errorf("store: delete repository snapshots: %w", err)
	}
	// Members are stored as domain.BackupSetMember JSON (untagged fields).
	if _, err := db.NewDelete().Model((*backupSetRow)(nil)).
		Where("EXISTS (SELECT 1 FROM json_each(members))").
		Where("NOT EXISTS (SELECT 1 FROM json_each(members) WHERE json_extract(json_each.value, '$.RepositoryID') IS NOT ?)", id).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("store: delete repository sets: %w", err)
	}
	return snapshotIDs, nil
}

// --- locations ---

type backupLocationRow struct {
	bun.BaseModel `bun:"table:backup_locations"`

	RepositoryID       string     `bun:"repository_id,pk"`
	Scope              string     `bun:"scope,pk"`
	ResticRepositoryID string     `bun:"restic_repository_id,notnull"`
	KeyGeneration      int        `bun:"key_generation,notnull"`
	InitializedAt      *time.Time `bun:"initialized_at"`
	LastBackupAt       *time.Time `bun:"last_backup_at"`
	LastVerifiedAt     *time.Time `bun:"last_verified_at"`
	LastVerifyResult   string     `bun:"last_verify_result,notnull"`
	LastVerifyJobID    string     `bun:"last_verify_job_id,notnull"`
	SizeBytes          int64      `bun:"size_bytes,notnull"`
	UncompressedBytes  int64      `bun:"uncompressed_bytes,notnull"`
	CompressionRatio   float64    `bun:"compression_ratio,notnull"`
	CompressionProg    float64    `bun:"compression_progress,notnull"`
	StatsSnapshots     int64      `bun:"stats_snapshots,notnull"`
	StatsAt            *time.Time `bun:"stats_at"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull"`
}

func (r backupLocationRow) toDomain() domain.BackupLocation {
	return domain.BackupLocation{RepositoryID: r.RepositoryID, Scope: r.Scope, ResticRepositoryID: r.ResticRepositoryID,
		KeyGeneration: r.KeyGeneration, InitializedAt: utcPtr(r.InitializedAt), LastBackupAt: utcPtr(r.LastBackupAt),
		LastVerifiedAt: utcPtr(r.LastVerifiedAt), LastVerifyResult: r.LastVerifyResult, LastVerifyJobID: r.LastVerifyJobID,
		SizeBytes: r.SizeBytes, UncompressedBytes: r.UncompressedBytes, CompressionRatio: r.CompressionRatio,
		CompressionProgress: r.CompressionProg, StatsSnapshots: r.StatsSnapshots, StatsAt: utcPtr(r.StatsAt), UpdatedAt: r.UpdatedAt.UTC()}
}

// LocationUpdate is what a finished job learned about a location. Zero
// fields leave the stored value unchanged.
type LocationUpdate struct {
	ResticRepositoryID string
	KeyGeneration      int
	Initialized        bool
	BackupAt           *time.Time
	VerifiedAt         *time.Time
	VerifyResult       string
	VerifyJobID        string
	// Stats replaces the measured size (all of its fields, zeros included).
	Stats *domain.LocationStats
}

// UpsertBackupLocation records what a job learned about a location.
func UpsertBackupLocation(ctx context.Context, db bun.IDB, repositoryID, scope string, u LocationUpdate, now time.Time) error {
	var row backupLocationRow
	err := db.NewSelect().Model(&row).Where("repository_id = ? AND scope = ?", repositoryID, scope).Scan(ctx)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: get backup location: %w", err)
	}
	if !exists {
		row = backupLocationRow{RepositoryID: repositoryID, Scope: scope}
	}
	if u.ResticRepositoryID != "" {
		row.ResticRepositoryID = u.ResticRepositoryID
	}
	if u.KeyGeneration > 0 {
		row.KeyGeneration = u.KeyGeneration
	}
	if u.Initialized && row.InitializedAt == nil {
		t := now.UTC()
		row.InitializedAt = &t
	}
	if u.BackupAt != nil {
		row.LastBackupAt = utcPtr(u.BackupAt)
	}
	if u.VerifyResult != "" {
		row.LastVerifyResult, row.LastVerifyJobID = u.VerifyResult, u.VerifyJobID
		if u.VerifiedAt != nil {
			row.LastVerifiedAt = utcPtr(u.VerifiedAt)
		}
	}
	if st := u.Stats; st != nil {
		row.SizeBytes, row.UncompressedBytes, row.StatsSnapshots = st.SizeBytes, st.UncompressedBytes, st.Snapshots
		row.CompressionRatio, row.CompressionProg = st.CompressionRatio, st.CompressionProgress
		t := now.UTC()
		row.StatsAt = &t
	}
	row.UpdatedAt = now.UTC()
	if exists {
		_, err = db.NewUpdate().Model(&row).WherePK().Exec(ctx)
	} else {
		_, err = db.NewInsert().Model(&row).Exec(ctx)
	}
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return domain.ErrBackupRepositoryNotFound
		}
		return fmt.Errorf("store: write backup location: %w", err)
	}
	return nil
}

// ListBackupLocations returns the locations of a repository ("" = all).
func ListBackupLocations(ctx context.Context, db bun.IDB, repositoryID string) ([]domain.BackupLocation, error) {
	var rows []backupLocationRow
	q := db.NewSelect().Model(&rows).Order("repository_id ASC", "scope ASC")
	if repositoryID != "" {
		q = q.Where("repository_id = ?", repositoryID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup locations: %w", err)
	}
	out := make([]domain.BackupLocation, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// --- policies ---

type backupPolicyRow struct {
	bun.BaseModel `bun:"table:backup_policies"`

	ID               string    `bun:"id,pk"`
	Name             string    `bun:"name,notnull"`
	NameKey          string    `bun:"name_key,notnull"`
	EnvironmentID    string    `bun:"environment_id,notnull"`
	ExcludeStacks    string    `bun:"exclude_stacks,notnull"`
	ExcludeVolumes   string    `bun:"exclude_volumes,notnull"`
	AnonymousVolumes int       `bun:"anonymous_volumes,notnull"`
	RepositoryID     string    `bun:"repository_id,notnull"`
	EnvironmentRepos string    `bun:"environment_repos,notnull"`
	IncludeManager   int       `bun:"include_manager,notnull"`
	IncludeMetrics   int       `bun:"include_metrics,notnull"`
	Stacks           string    `bun:"stacks,notnull"`
	Volumes          string    `bun:"volumes,notnull"`
	Shutdown         int       `bun:"shutdown,notnull"`
	Cron             string    `bun:"cron,notnull"`
	TimeZone         string    `bun:"time_zone,notnull"`
	Enabled          int       `bun:"enabled,notnull"`
	Retention        string    `bun:"retention,notnull"`
	Revision         int64     `bun:"revision,notnull"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	UpdatedAt        time.Time `bun:"updated_at,notnull"`
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func fromBackupPolicy(p *domain.BackupPolicy) backupPolicyRow {
	repos := p.EnvironmentRepos
	if repos == nil {
		repos = map[string]string{}
	}
	stacks, volumes := p.Stacks, p.Volumes
	if stacks == nil {
		stacks = []domain.BackupStackSelection{}
	}
	if volumes == nil {
		volumes = []domain.BackupVolumeSelection{}
	}
	return backupPolicyRow{ID: p.ID, Name: p.Name, NameKey: NameKey(p.Name), EnvironmentID: p.EnvironmentID,
		ExcludeStacks: jsonText(p.ExcludeStacks), ExcludeVolumes: jsonText(p.ExcludeVolumes), AnonymousVolumes: b2i(p.AnonymousVolumes), RepositoryID: p.RepositoryID,
		EnvironmentRepos: jsonText(repos), IncludeManager: b2i(p.IncludeManager), IncludeMetrics: b2i(p.IncludeMetrics),
		Stacks: jsonText(stacks), Volumes: jsonText(volumes), Shutdown: b2i(p.Shutdown), Cron: p.Cron, TimeZone: p.TimeZone,
		Enabled: b2i(p.Enabled), Retention: jsonText(p.Retention), Revision: p.Revision, CreatedAt: p.CreatedAt.UTC(),
		UpdatedAt: p.UpdatedAt.UTC()}
}

func (r backupPolicyRow) toDomain() domain.BackupPolicy {
	p := domain.BackupPolicy{ID: r.ID, Name: r.Name, EnvironmentID: r.EnvironmentID, AnonymousVolumes: r.AnonymousVolumes == 1,
		RepositoryID: r.RepositoryID, IncludeManager: r.IncludeManager == 1,
		IncludeMetrics: r.IncludeMetrics == 1, Shutdown: r.Shutdown == 1, Cron: r.Cron, TimeZone: r.TimeZone, Enabled: r.Enabled == 1,
		Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.EnvironmentRepos), &p.EnvironmentRepos)
	_ = json.Unmarshal([]byte(r.ExcludeStacks), &p.ExcludeStacks)
	_ = json.Unmarshal([]byte(r.ExcludeVolumes), &p.ExcludeVolumes)
	_ = json.Unmarshal([]byte(r.Stacks), &p.Stacks)
	_ = json.Unmarshal([]byte(r.Volumes), &p.Volumes)
	_ = json.Unmarshal([]byte(r.Retention), &p.Retention)
	return p
}

// InsertBackupPolicy stores a new policy.
func InsertBackupPolicy(ctx context.Context, db bun.IDB, p *domain.BackupPolicy) error {
	row := fromBackupPolicy(p)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "backup_policies.name_key") {
			return domain.ErrBackupPolicyNameUsed
		}
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return domain.ErrBackupRepositoryNotFound
		}
		return fmt.Errorf("store: insert backup policy: %w", err)
	}
	return nil
}

// UpdateBackupPolicy replaces a policy when the revision matches.
func UpdateBackupPolicy(ctx context.Context, db bun.IDB, p *domain.BackupPolicy, expectRevision int64) error {
	row := fromBackupPolicy(p)
	res, err := db.NewUpdate().Model(&row).ExcludeColumn("id", "created_at").WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "backup_policies.name_key") {
			return domain.ErrBackupPolicyNameUsed
		}
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return domain.ErrBackupRepositoryNotFound
		}
		return fmt.Errorf("store: update backup policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBackupPolicy(ctx, db, p.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetBackupPolicy returns one policy.
func GetBackupPolicy(ctx context.Context, db bun.IDB, id string) (domain.BackupPolicy, error) {
	var row backupPolicyRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupPolicy{}, domain.ErrBackupPolicyNotFound
	}
	if err != nil {
		return domain.BackupPolicy{}, fmt.Errorf("store: get backup policy: %w", err)
	}
	return row.toDomain(), nil
}

// ListBackupPolicies returns policies in ID order after afterID (limit 0 =
// all).
func ListBackupPolicies(ctx context.Context, db bun.IDB, afterID string, limit int) ([]domain.BackupPolicy, error) {
	var rows []backupPolicyRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup policies: %w", err)
	}
	out := make([]domain.BackupPolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// DeleteBackupPolicy removes a policy when the revision matches (its sets
// and snapshots stay as history).
func DeleteBackupPolicy(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*backupPolicyRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete backup policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBackupPolicy(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// --- sets ---

type backupSetRow struct {
	bun.BaseModel `bun:"table:backup_sets"`

	ID                 string     `bun:"id,pk"`
	PolicyID           string     `bun:"policy_id,notnull"`
	PolicyName         string     `bun:"policy_name,notnull"`
	Origin             string     `bun:"origin,notnull"`
	State              string     `bun:"state,notnull"`
	StartedAt          time.Time  `bun:"started_at,notnull"`
	FinishedAt         *time.Time `bun:"finished_at"`
	Members            string     `bun:"members,notnull"`
	ManifestSnapshotID string     `bun:"manifest_snapshot_id,notnull"`
	FollowUp           string     `bun:"follow_up,notnull"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull"`
}

func fromBackupSet(s *domain.BackupSet) backupSetRow {
	members := s.Members
	if members == nil {
		members = []domain.BackupSetMember{}
	}
	return backupSetRow{ID: s.ID, PolicyID: s.PolicyID, PolicyName: s.PolicyName, Origin: string(s.Origin), State: s.State,
		StartedAt: s.StartedAt.UTC(), FinishedAt: utcPtr(s.FinishedAt), Members: jsonText(members),
		ManifestSnapshotID: s.ManifestSnapshotID, FollowUp: s.FollowUp, UpdatedAt: s.UpdatedAt.UTC()}
}

func (r backupSetRow) toDomain() domain.BackupSet {
	s := domain.BackupSet{ID: r.ID, PolicyID: r.PolicyID, PolicyName: r.PolicyName, Origin: domain.JobOrigin(r.Origin), State: r.State,
		StartedAt: r.StartedAt.UTC(), FinishedAt: utcPtr(r.FinishedAt), ManifestSnapshotID: r.ManifestSnapshotID, FollowUp: r.FollowUp,
		UpdatedAt: r.UpdatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.Members), &s.Members)
	return s
}

// InsertBackupSet stores a new set; an existing set with the same ID is
// left unchanged (inserted false), which makes scheduled runs idempotent.
func InsertBackupSet(ctx context.Context, db bun.IDB, s *domain.BackupSet) (inserted bool, err error) {
	row := fromBackupSet(s)
	res, err := db.NewInsert().Model(&row).On("CONFLICT (id) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: insert backup set: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListBackupSetsWithFollowUp returns sets whose follow-up is pending.
func ListBackupSetsWithFollowUp(ctx context.Context, db bun.IDB, followUp string, limit int) ([]domain.BackupSet, error) {
	var rows []backupSetRow
	if err := db.NewSelect().Model(&rows).Where("follow_up = ?", followUp).Order("started_at ASC").Limit(limit).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup sets: %w", err)
	}
	out := make([]domain.BackupSet, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// UpdateBackupSet writes a set's state, members and manifest.
func UpdateBackupSet(ctx context.Context, db bun.IDB, s *domain.BackupSet) error {
	row := fromBackupSet(s)
	if _, err := db.NewUpdate().Model(&row).Column("state", "finished_at", "members", "manifest_snapshot_id", "follow_up", "updated_at").
		WherePK().Exec(ctx); err != nil {
		return fmt.Errorf("store: update backup set: %w", err)
	}
	return nil
}

// GetBackupSet returns one set.
func GetBackupSet(ctx context.Context, db bun.IDB, id string) (domain.BackupSet, error) {
	var row backupSetRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupSet{}, domain.ErrBackupSetNotFound
	}
	if err != nil {
		return domain.BackupSet{}, fmt.Errorf("store: get backup set: %w", err)
	}
	return row.toDomain(), nil
}

// ListBackupSets returns a policy's sets, newest first (limit 0 = all).
func ListBackupSets(ctx context.Context, db bun.IDB, policyID string, limit int) ([]domain.BackupSet, error) {
	var rows []backupSetRow
	q := db.NewSelect().Model(&rows).Order("started_at DESC", "id DESC")
	if policyID != "" {
		q = q.Where("policy_id = ?", policyID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup sets: %w", err)
	}
	out := make([]domain.BackupSet, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// --- snapshots ---

type backupSnapshotRow struct {
	bun.BaseModel `bun:"table:backup_snapshots"`

	ID               string     `bun:"id,pk"`
	SetID            string     `bun:"set_id,notnull"`
	PolicyID         string     `bun:"policy_id,notnull"`
	RepositoryID     string     `bun:"repository_id,notnull"`
	Scope            string     `bun:"scope,notnull"`
	EnvironmentID    string     `bun:"environment_id,notnull"`
	Kind             string     `bun:"kind,notnull"`
	Item             string     `bun:"item,notnull"`
	StackID          string     `bun:"stack_id,notnull"`
	StackName        string     `bun:"stack_name,notnull"`
	Volume           string     `bun:"volume,notnull"`
	ResticSnapshotID string     `bun:"restic_snapshot_id,notnull"`
	SnapshotTime     time.Time  `bun:"snapshot_time,notnull"`
	Paths            string     `bun:"paths,notnull"`
	Volumes          string     `bun:"volumes,notnull"`
	ProjectPath      string     `bun:"project_path,notnull"`
	VolumePaths      string     `bun:"volume_paths,notnull"`
	Consistency      string     `bun:"consistency,notnull"`
	State            string     `bun:"state,notnull"`
	ErrorClass       string     `bun:"error_class,notnull"`
	BytesAdded       int64      `bun:"bytes_added,notnull"`
	BytesTotal       int64      `bun:"bytes_total,notnull"`
	Files            int64      `bun:"files,notnull"`
	JobID            string     `bun:"job_id,notnull"`
	VerifiedAt       *time.Time `bun:"verified_at"`
	ForgottenAt      *time.Time `bun:"forgotten_at"`
	CreatedAt        time.Time  `bun:"created_at,notnull"`
}

func (r backupSnapshotRow) toDomain() domain.BackupSnapshot {
	s := domain.BackupSnapshot{ID: r.ID, SetID: r.SetID, PolicyID: r.PolicyID, RepositoryID: r.RepositoryID, Scope: r.Scope,
		EnvironmentID: r.EnvironmentID, Kind: r.Kind, Item: r.Item, StackID: r.StackID, StackName: r.StackName, Volume: r.Volume,
		ResticSnapshotID: r.ResticSnapshotID, SnapshotTime: r.SnapshotTime.UTC(), Consistency: r.Consistency, State: r.State,
		ErrorClass: r.ErrorClass, BytesAdded: r.BytesAdded, BytesTotal: r.BytesTotal, Files: r.Files, JobID: r.JobID,
		VerifiedAt: utcPtr(r.VerifiedAt), ForgottenAt: utcPtr(r.ForgottenAt), CreatedAt: r.CreatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.Paths), &s.Paths)
	_ = json.Unmarshal([]byte(r.Volumes), &s.Volumes)
	s.ProjectPath = r.ProjectPath
	if r.VolumePaths != "" && r.VolumePaths != "{}" {
		_ = json.Unmarshal([]byte(r.VolumePaths), &s.VolumePaths)
	}
	return s
}

// InsertBackupSnapshot indexes a snapshot; a snapshot already indexed
// (same repository, scope and restic ID) is left unchanged and reported
// with inserted false.
func InsertBackupSnapshot(ctx context.Context, db bun.IDB, s *domain.BackupSnapshot) (inserted bool, err error) {
	paths, volumes := s.Paths, s.Volumes
	if paths == nil {
		paths = []string{}
	}
	if volumes == nil {
		volumes = []string{}
	}
	volumePaths := s.VolumePaths
	if volumePaths == nil {
		volumePaths = map[string]string{}
	}
	row := backupSnapshotRow{ID: s.ID, SetID: s.SetID, PolicyID: s.PolicyID, RepositoryID: s.RepositoryID, Scope: s.Scope,
		EnvironmentID: s.EnvironmentID, Kind: s.Kind, Item: s.Item, StackID: s.StackID, StackName: s.StackName, Volume: s.Volume,
		ResticSnapshotID: s.ResticSnapshotID, SnapshotTime: s.SnapshotTime.UTC(), Paths: jsonText(paths), Volumes: jsonText(volumes),
		ProjectPath: s.ProjectPath, VolumePaths: jsonText(volumePaths),
		Consistency: s.Consistency, State: s.State, ErrorClass: s.ErrorClass, BytesAdded: s.BytesAdded, BytesTotal: s.BytesTotal,
		Files: s.Files, JobID: s.JobID, VerifiedAt: utcPtr(s.VerifiedAt), ForgottenAt: utcPtr(s.ForgottenAt), CreatedAt: s.CreatedAt.UTC()}
	res, err := db.NewInsert().Model(&row).On("CONFLICT (repository_id, scope, restic_snapshot_id) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: insert backup snapshot: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetBackupSnapshot returns one snapshot.
func GetBackupSnapshot(ctx context.Context, db bun.IDB, id string) (domain.BackupSnapshot, error) {
	var row backupSnapshotRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupSnapshot{}, domain.ErrBackupNotFound
	}
	if err != nil {
		return domain.BackupSnapshot{}, fmt.Errorf("store: get backup snapshot: %w", err)
	}
	return row.toDomain(), nil
}

// ListBackupSnapshots returns snapshots in ID order (creation order).
func ListBackupSnapshots(ctx context.Context, db bun.IDB, f domain.BackupSnapshotFilter) ([]domain.BackupSnapshot, error) {
	var rows []backupSnapshotRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	for col, v := range map[string]string{"repository_id": f.RepositoryID, "policy_id": f.PolicyID, "set_id": f.SetID,
		"environment_id": f.EnvironmentID, "stack_id": f.StackID, "kind": f.Kind} {
		if v != "" {
			q = q.Where("? = ?", bun.Ident(col), v)
		}
	}
	if f.Volume != "" {
		q = q.Where("(volume = ? OR EXISTS (SELECT 1 FROM json_each(volumes) WHERE json_each.value = ?))", f.Volume, f.Volume)
	}
	if !f.IncludeForgotten {
		q = q.Where("forgotten_at IS NULL")
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list backup snapshots: %w", err)
	}
	out := make([]domain.BackupSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// MarkBackupSnapshotsForgotten stamps the snapshots of a location that
// retention removed.
func MarkBackupSnapshotsForgotten(ctx context.Context, db bun.IDB, repositoryID, scope string, resticIDs []string, at time.Time) error {
	if len(resticIDs) == 0 {
		return nil
	}
	if _, err := db.NewUpdate().Model((*backupSnapshotRow)(nil)).Set("forgotten_at = ?", at.UTC()).
		Where("repository_id = ? AND scope = ?", repositoryID, scope).Where("restic_snapshot_id IN (?)", bun.List(resticIDs)).
		Where("forgotten_at IS NULL").Exec(ctx); err != nil {
		return fmt.Errorf("store: mark snapshots forgotten: %w", err)
	}
	return nil
}

// MarkBackupSnapshotsVerified stamps the snapshots of a location a
// successful check covered.
func MarkBackupSnapshotsVerified(ctx context.Context, db bun.IDB, repositoryID, scope string, at time.Time) error {
	if _, err := db.NewUpdate().Model((*backupSnapshotRow)(nil)).Set("verified_at = ?", at.UTC()).
		Where("repository_id = ? AND scope = ?", repositoryID, scope).Where("forgotten_at IS NULL").
		Where("snapshot_time <= ?", at.UTC()).Exec(ctx); err != nil {
		return fmt.Errorf("store: mark snapshots verified: %w", err)
	}
	return nil
}
