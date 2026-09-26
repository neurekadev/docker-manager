package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/backups"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/restic"
)

// Backup repositories and the Recovery Key (#10, #24). The flows live in
// internal/manager/backups; this file is the transport contract.

const tagBackups = "Backups"

// Backup capabilities (#17 catalog).
const (
	CapBackupRepositoryRead   Capability = "backup_repository.read"
	CapBackupRepositoryManage Capability = "backup_repository.manage"
	CapBackupPolicyRead       Capability = "backup_policy.read"
	CapBackupPolicyManage     Capability = "backup_policy.manage"
	CapBackupRead             Capability = "backup.read"
	CapBackupRun              Capability = "backup.run"
	CapBackupRestore          Capability = "backup.restore"
	CapBackupContentsRead     Capability = "backup.contents.read"
	CapBackupContentsDownload Capability = "backup.contents.download"
	CapBackupVerify           Capability = "backup.verify"
	CapBackupRetention        Capability = "backup.retention"
)

// Backup error codes.
const (
	CodeBackupRepositoryNameTaken = "backup_repository_name_taken"
	CodeBackupPolicyNameTaken     = "backup_policy_name_taken"
	CodeBackupRepositoryInUse     = "backup_repository_in_use"
	CodeRecoveryKeyNotConfirmed   = "recovery_key_not_confirmed"
	CodeRecoveryKeyMismatch       = "recovery_key_mismatch"
	CodeRecoveryKeyMalformed      = "recovery_key_malformed"
	CodeKeyRotationInProgress     = "key_rotation_in_progress"
	CodeNothingToRetry            = "nothing_to_retry"
	CodeBackupRepositoryError     = "backup_repository_error"
	CodeBackupNotAFile            = "backup_not_a_file"
	CodeBackupFileTooLarge        = "backup_file_too_large"
	CodeManagerRestoreRequired    = "manager_restore_required"
)

// BackupService is the backup service as seen by the API (implemented by
// *backups.Service).
type BackupService interface {
	ListRepositories(ctx context.Context, afterID string, limit int) ([]domain.BackupRepository, error)
	GetRepository(ctx context.Context, id string) (domain.BackupRepository, error)
	CreateRepository(ctx context.Context, in domain.BackupRepositoryInput) (backups.CreatedRepository, error)
	UpdateRepository(ctx context.Context, id string, revision int64, p domain.BackupRepositoryPatch) (before, after domain.BackupRepository, err error)
	DeleteRepository(ctx context.Context, id string, revision int64) error
	TestRepository(ctx context.Context, id string) (domain.BackupConnectionTest, error)
	Health(ctx context.Context, id string) (backups.RepositoryHealth, error)
	KeyState(ctx context.Context) (domain.BackupKeyState, error)
	LocationKeyStatus(ctx context.Context) ([]domain.BackupLocation, error)
	ConfirmKey(ctx context.Context, repositoryID, input string, backedUp bool) (backups.KeyConfirmation, error)
	RotateKey(ctx context.Context) (backups.KeyRotation, error)

	ListPolicies(ctx context.Context, afterID string, limit int) ([]domain.BackupPolicy, error)
	GetPolicy(ctx context.Context, id string) (domain.BackupPolicy, error)
	CreatePolicy(ctx context.Context, p domain.BackupPolicy) (domain.BackupPolicy, error)
	UpdatePolicy(ctx context.Context, id string, revision int64, p backups.PolicyPatch) (before, after domain.BackupPolicy, err error)
	DeletePolicy(ctx context.Context, id string, revision int64) error
	PreviewScope(ctx context.Context, id string, draft *domain.BackupPolicy) (backups.ScopePreview, error)
	PreviewRetention(ctx context.Context, id string, override *domain.BackupRetention) ([]backups.RetentionLocation, domain.BackupPolicy, error)
	RunPolicy(ctx context.Context, policyID string, o backups.RunOptions) (backups.RunResult, error)
	RetentionRun(ctx context.Context, policyID string, principal authz.Principal, idempotencyKey string) ([]domain.Job, error)
	ListSets(ctx context.Context, policyID string, limit int) ([]domain.BackupSet, error)
	GetSet(ctx context.Context, id string) (domain.BackupSet, error)
	NextRun(ctx context.Context, kind, policyID string) *time.Time

	ListSnapshots(ctx context.Context, f domain.BackupSnapshotFilter) ([]domain.BackupSnapshot, error)
	GetSnapshot(ctx context.Context, id string) (domain.BackupSnapshot, error)
	Contents(ctx context.Context, sn domain.BackupSnapshot, dir string, recursive bool, limit int) (restic.Listing, error)
	FileInfo(ctx context.Context, sn domain.BackupSnapshot, file string) (restic.Node, error)
	Download(ctx context.Context, sn domain.BackupSnapshot, node restic.Node, dst io.Writer) error
	VerifySnapshot(ctx context.Context, sn domain.BackupSnapshot, principal authz.Principal, subset, idempotencyKey string) (domain.Job, error)
	RestoreTargets(ctx context.Context, sn domain.BackupSnapshot, req backups.RestoreRequest) ([]domain.JobTarget, string, error)
	PreviewRestore(ctx context.Context, sn domain.BackupSnapshot, req backups.RestoreRequest) (protocol.RestorePreviewOutput, []domain.JobTarget, error)
	Restore(ctx context.Context, sn domain.BackupSnapshot, req backups.RestoreRequest, principal authz.Principal, idempotencyKey string) (domain.Job, error)

	TestImport(ctx context.Context, src backups.ImportSource) (backups.ImportConnection, error)
	PreviewImport(ctx context.Context, src backups.ImportSource, setID string) (backups.ImportPreview, error)
	StartImport(ctx context.Context, src backups.ImportSource, setID, idempotencyKey string) (domain.Job, error)
	LatestImport(ctx context.Context) (*backups.ImportStatus, error)
}

func backupRepositoryResource(id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeBackupRepository, ID: id, Parents: []authz.ResourceRef{}}
}

// backupError maps backup service errors.
func backupError(err error) error {
	var re *restic.Error
	var ae *backups.AgentError
	var ref *backup.Refusal
	var classed jobexec.ClassedError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrBackupRepositoryNotFound):
		return NotFound("backup repository not found")
	case errors.Is(err, domain.ErrBackupPolicyNotFound):
		return NotFound("backup policy not found")
	case errors.Is(err, domain.ErrBackupNotFound):
		return NotFound("backup not found")
	case errors.Is(err, domain.ErrBackupSetNotFound):
		return NotFound("backup set not found")
	case errors.Is(err, domain.ErrBackupRepositoryNameUsed):
		return Conflict(CodeBackupRepositoryNameTaken, "another backup repository already uses this name")
	case errors.Is(err, domain.ErrBackupPolicyNameUsed):
		return Conflict(CodeBackupPolicyNameTaken, "another backup policy already uses this name")
	case errors.Is(err, domain.ErrBackupScopeOverlap):
		return Conflict("backup_scope_overlap", "a backup policy already covers this environment")
	case errors.Is(err, domain.ErrBackupRepositoryInUse):
		return Conflict(CodeBackupRepositoryInUse, "a backup policy uses this repository; change or delete the policy first")
	case errors.Is(err, domain.ErrRecoveryKeyNotConfirmed):
		return Conflict(CodeRecoveryKeyNotConfirmed, "the Recovery Key has not been confirmed for this repository; confirm it first (recovery-confirmations)")
	case errors.Is(err, domain.ErrRecoveryKeyMismatch):
		return NewError(http.StatusUnprocessableEntity, CodeRecoveryKeyMismatch, "the Recovery Key does not match the instance's key",
			Field("body.recoveryKey", "not the Recovery Key shown at creation (or rotation)"))
	case errors.Is(err, domain.ErrRecoveryKeyMalformed):
		return NewError(http.StatusUnprocessableEntity, CodeRecoveryKeyMalformed, "the Recovery Key is malformed; check it for typos",
			Field("body.recoveryKey", "checksum mismatch or wrong length"))
	case errors.Is(err, domain.ErrKeyRotationInProgress):
		return Conflict(CodeKeyRotationInProgress, "a Recovery Key rotation is still moving repositories to the new key; let it finish first")
	case errors.Is(err, backups.ErrNothingToRetry):
		return Conflict(CodeNothingToRetry, "every member of the backup set completed; nothing to retry")
	case errors.Is(err, backups.ErrNotAFile):
		return Conflict(CodeBackupNotAFile, "only regular files can be downloaded from a backup")
	case errors.Is(err, backups.ErrFileTooLarge):
		return NewError(http.StatusRequestEntityTooLarge, CodeBackupFileTooLarge, "the file is larger than the download limit")
	case errors.Is(err, backups.ErrManagerStateRestore):
		return Conflict(CodeManagerRestoreRequired, "manager-state backups are restored by importing them into a fresh manager (first-run setup, backup import)")
	case errors.Is(err, backups.ErrContentUnavailable):
		return Unavailable(CodeUnavailable, "the backup contents cannot be read right now")
	case errors.As(err, &re):
		if re.Code == restic.CodeSnapshotNotFound {
			return NotFound("no such snapshot or path in the backup")
		}
		return Conflict(CodeBackupRepositoryError, "the repository could not be read ("+re.Code+"): "+restic.RecoveryFor(re.Code))
	case errors.As(err, &ae):
		if ae.Class == "agent_offline" {
			return Unavailable(CodeUnavailable, "the environment's agent is offline")
		}
		if ae.Class == restic.CodeSnapshotNotFound {
			return NotFound("no such snapshot or path in the backup")
		}
		return Conflict(CodeBackupRepositoryError, "the repository could not be read ("+ae.Class+")")
	case errors.As(err, &ref):
		return Conflict(CodeBackupRepositoryError, ref.Message)
	case errors.As(err, &classed):
		return Conflict(CodeBackupRepositoryError, classed.Error())
	case errors.Is(err, domain.ErrStackNotFound), errors.Is(err, domain.ErrEnvironmentNotFound):
		return Invalid("the policy refers to a stack or environment that no longer exists")
	}
	if je := JobErrorFor(err); !isInternal(je) {
		return je
	}
	return identityError(err)
}

// --- DTOs ---

// BackupCredentialState describes stored S3 credentials without them.
type BackupCredentialState struct {
	Set         bool   `json:"set"`
	Fingerprint string `json:"fingerprint,omitempty" example:"fp_3f2a9c0d1e4b5a67" doc:"Keyed fingerprint of the access key pair; changes when it changes."`
}

// BackupVerification is a repository's verification schedule (#13).
type BackupVerification struct {
	Cron           string     `json:"cron" example:"0 5 * * 0"`
	TimeZone       string     `json:"timeZone" example:"Europe/Berlin"`
	Enabled        bool       `json:"enabled"`
	ReadDataSubset string     `json:"readDataSubset,omitempty" example:"5%" doc:"Part of the data scheduled checks read (restic --read-data-subset); empty checks the structure only."`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt,omitempty"`
}

// BackupConnectionTest is a connection test result.
type BackupConnectionTest struct {
	At         time.Time          `json:"at"`
	OK         bool               `json:"ok"`
	Result     string             `json:"result" example:"ok" doc:"ok, or an error class: access_denied, bucket_not_found, unreachable, path_not_allowed, path_not_writable, recovery_key_rejected, repository_locked, storage_access_denied, ..."`
	Message    string             `json:"message,omitempty"`
	CanRead    *bool              `json:"canRead,omitempty"`
	CanWrite   *bool              `json:"canWrite,omitempty"`
	CanDelete  *bool              `json:"canDelete,omitempty"`
	ObjectLock *bool              `json:"objectLock,omitempty" doc:"The bucket enforces Object Lock: pruning may be refused (see warnings)."`
	Scopes     []BackupScopeProbe `json:"scopes,omitempty" doc:"Restic repositories found below the destination."`
	Warnings   []string           `json:"warnings,omitempty"`
}

// BackupScopeProbe is one restic repository found by a connection test.
type BackupScopeProbe struct {
	Scope              string `json:"scope" example:"manager" doc:"manager or env:<environmentId>."`
	Exists             bool   `json:"exists"`
	KeyAccepted        bool   `json:"keyAccepted"`
	PreviousKey        bool   `json:"previousKey,omitempty" doc:"Only the previous Recovery Key opens it: the rotation has not reached it yet."`
	ResticRepositoryID string `json:"resticRepositoryId,omitempty"`
	ErrorClass         string `json:"errorClass,omitempty"`
}

// BackupRepository is a destination. Credentials and the Recovery Key are
// never returned.
//
// Shaping (#17): backup_repository.read shows it in full; any other
// capability on it only id, name, kind and state.
type BackupRepository struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name" example:"Offsite S3"`
	Kind       string                 `json:"kind" enum:"local,s3"`
	State      string                 `json:"state" enum:"awaiting_confirmation,ready" doc:"awaiting_confirmation until the owner re-enters the Recovery Key for it; nothing is initialized before."`
	View       string                 `json:"view" enum:"minimal,full"`
	Actions    []string               `json:"actions"`
	Executor   string                 `json:"executor,omitempty" doc:"Local repositories: manager or the environment ID whose agent owns the path."`
	Location   string                 `json:"location,omitempty" example:"https://s3.example.com/backups/dockyard" doc:"Where the restic repositories live (no credentials). Below it: dockyard-manager and dockyard-env-<environmentId>."`
	Path       string                 `json:"path,omitempty"`
	Endpoint   string                 `json:"endpoint,omitempty"`
	Bucket     string                 `json:"bucket,omitempty"`
	Prefix     string                 `json:"prefix,omitempty"`
	Region     string                 `json:"region,omitempty"`
	PathStyle  bool                   `json:"pathStyle,omitempty"`
	Credential *BackupCredentialState `json:"credential,omitempty"`
	// Recovery documents what a fresh restore needs (#10).
	RecoveryRequirements []string              `json:"recoveryRequirements,omitempty"`
	ConfirmedAt          *time.Time            `json:"confirmedAt,omitempty"`
	Verification         *BackupVerification   `json:"verification,omitempty"`
	LastTest             *BackupConnectionTest `json:"lastTest,omitempty"`
	Revision             int64                 `json:"revision,omitempty"`
	CreatedAt            time.Time             `json:"createdAt,omitzero"`
	UpdatedAt            time.Time             `json:"updatedAt,omitzero"`
}

func newBackupConnectionTest(t *domain.BackupConnectionTest) *BackupConnectionTest {
	if t == nil {
		return nil
	}
	out := &BackupConnectionTest{At: t.At, OK: t.OK, Result: t.Result, Message: t.Message, CanRead: t.CanRead, CanWrite: t.CanWrite,
		CanDelete: t.CanDelete, ObjectLock: t.ObjectLock, Warnings: t.Warnings}
	for _, s := range t.Scopes {
		out.Scopes = append(out.Scopes, BackupScopeProbe{Scope: s.Scope, Exists: s.Exists, KeyAccepted: s.KeyAccepted, PreviousKey: s.PreviousKey,
			ResticRepositoryID: s.ResticRepositoryID, ErrorClass: s.ErrorClass})
	}
	return out
}

func recoveryRequirements(r domain.BackupRepository) []string {
	if r.Kind == backup.KindS3 {
		return []string{"the Recovery Key", "the endpoint, bucket and prefix", "S3 credentials that can read the bucket (new credentials work)"}
	}
	if r.Executor == domain.BackupExecutorManager {
		return []string{"the Recovery Key", "the directory " + r.Path + " mounted into the new manager at the same path"}
	}
	return []string{"the Recovery Key", "the directory " + r.Path + " on the environment's host, mounted into its (re-enrolled) agent at the same path"}
}

func newBackupRepository(r domain.BackupRepository, v authz.View) BackupRepository {
	out := BackupRepository{ID: r.ID, Name: r.Name, Kind: r.Kind, State: r.State, View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	d := backup.Destination{Kind: r.Kind, Path: r.Path, Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix, Region: r.Region, PathStyle: r.PathStyle}
	out.Executor, out.Location, out.Path, out.Endpoint, out.Bucket, out.Prefix = r.Executor, d.Base(), r.Path, r.Endpoint, r.Bucket, r.Prefix
	out.Region, out.PathStyle, out.ConfirmedAt = r.Region, r.PathStyle, r.ConfirmedAt
	if r.Kind == backup.KindS3 {
		out.Credential = &BackupCredentialState{Set: r.CredentialFingerprint != "", Fingerprint: r.CredentialFingerprint}
	}
	out.RecoveryRequirements = recoveryRequirements(r)
	out.Verification = &BackupVerification{Cron: r.VerifyCron, TimeZone: r.VerifyTimeZone, Enabled: r.VerifyEnabled, ReadDataSubset: r.VerifyReadData}
	out.LastTest = newBackupConnectionTest(r.LastTest)
	out.Revision, out.CreatedAt, out.UpdatedAt = r.Revision, r.CreatedAt, r.UpdatedAt
	return out
}

// RecoveryKeyState is the instance Recovery Key state: fingerprints only,
// never the key.
type RecoveryKeyState struct {
	Generation          int        `json:"generation" doc:"Confirmed keys so far (0: none)."`
	Fingerprint         string     `json:"fingerprint,omitempty" example:"rk_9f3a61c2d4e5b708" doc:"Fingerprint of the current key; compare it with your saved copy."`
	ConfirmedAt         *time.Time `json:"confirmedAt,omitempty"`
	PendingFingerprint  string     `json:"pendingFingerprint,omitempty" doc:"A generated key awaiting confirmation (re-entry)."`
	PendingCreatedAt    *time.Time `json:"pendingCreatedAt,omitempty"`
	PreviousFingerprint string     `json:"previousFingerprint,omitempty" doc:"Set while a rotation still has repositories on the previous key: keep both keys until it is gone."`
	RotationInProgress  bool       `json:"rotationInProgress"`
	// PendingLocations lists repository scopes still on an older key.
	PendingLocations []string `json:"pendingLocations,omitempty" example:"0190a6e0-...:env:0190a6e1-..."`
	Scope            string   `json:"scope" enum:"instance" doc:"One Recovery Key opens every DockYard repository of this instance (manager and every environment, local and S3)."`
}

func newRecoveryKeyState(k domain.BackupKeyState, pending []domain.BackupLocation) RecoveryKeyState {
	out := RecoveryKeyState{Generation: k.Generation, Fingerprint: k.Fingerprint, ConfirmedAt: k.ConfirmedAt, PendingFingerprint: k.PendingFingerprint,
		PendingCreatedAt: k.PendingCreatedAt, PreviousFingerprint: k.PreviousFingerprint, RotationInProgress: k.RotationInProgress(), Scope: "instance"}
	for _, l := range pending {
		out.PendingLocations = append(out.PendingLocations, l.RepositoryID+":"+l.Scope)
	}
	return out
}

// RecoveryKeyReveal is a newly generated Recovery Key, returned exactly
// once.
type RecoveryKeyReveal struct {
	Key         string   `json:"key" example:"DYRK-ABCD-EFGH-..." doc:"The Recovery Key. Shown once: save it now (password manager, printed copy). It is never shown again."`
	Fingerprint string   `json:"fingerprint" example:"rk_9f3a61c2d4e5b708"`
	Notice      []string `json:"notice"`
}

var recoveryKeyNotice = []string{
	"This key opens every DockYard backup repository of this instance. A fresh manager can restore only with it.",
	"If this manager's key store and your saved copy are both lost, the backup data cannot be restored.",
	"Save it outside DockYard now; confirming below proves only that you typed it, not that it is stored safely.",
}

func newRecoveryKeyReveal(k backups.RecoveryKey) *RecoveryKeyReveal {
	return &RecoveryKeyReveal{Key: k.String(), Fingerprint: k.Fingerprint(), Notice: recoveryKeyNotice}
}

// --- handlers ---

type backupsAPI struct {
	svc   BackupService
	authz authz.Authorizer
	deps  Deps
}

func (h *backupsAPI) service() (BackupService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "backups are not available")
	}
	return h.svc, nil
}

func (h *backupsAPI) checker(ctx context.Context) (BackupService, authz.Checker, authz.Principal, error) {
	svc, err := h.service()
	if err != nil {
		return nil, nil, authz.Principal{}, err
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, err
	}
	return svc, c, p, nil
}

type backupRepositoryIDInput struct {
	RepositoryID string `path:"repositoryId" maxLength:"64" doc:"Backup repository ID."`
}

type backupRepositoryOutput struct {
	ETagHeader
	Body BackupRepository
}

type backupRepositoryListOutput struct{ Body Page[BackupRepository] }

func repositoryETag(r BackupRepository) ETagHeader {
	if r.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(r.Revision)}
}

func (h *backupsAPI) listRepositories(ctx context.Context, in *struct{ PageParams }) (*backupRepositoryListOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("backup-repositories")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.BackupRepository]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.BackupRepository, error) {
			return svc.ListRepositories(ctx, afterID, n)
		},
		Position: func(r domain.BackupRepository) string { return r.ID },
		Visible:  func(r domain.BackupRepository) bool { return authz.ViewOf(c, backupRepositoryResource(r.ID)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]BackupRepository, 0, len(items))
	for _, r := range items {
		out = append(out, newBackupRepository(r, authz.ViewOf(c, backupRepositoryResource(r.ID))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &backupRepositoryListOutput{Body: NewPage(out, cursor, nil)}, nil
}

// visibleRepository loads a repository the caller may see.
func (h *backupsAPI) visibleRepository(ctx context.Context, id string) (BackupService, authz.Checker, authz.Principal, domain.BackupRepository, authz.View, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, nil, p, domain.BackupRepository{}, authz.View{}, err
	}
	r, err := svc.GetRepository(ctx, id)
	if err != nil {
		return nil, nil, p, r, authz.View{}, backupError(err)
	}
	v := authz.ViewOf(c, backupRepositoryResource(r.ID))
	if !v.Visible() {
		return nil, nil, p, r, v, NotFound("backup repository not found")
	}
	return svc, c, p, r, v, nil
}

func (h *backupsAPI) requireRepository(ctx context.Context, id string, cp Capability) (BackupService, authz.Checker, authz.Principal, domain.BackupRepository, authz.View, error) {
	svc, c, p, r, v, err := h.visibleRepository(ctx, id)
	if err != nil {
		return svc, c, p, r, v, err
	}
	if !v.Has(string(cp)) {
		return svc, c, p, r, v, Forbidden("not permitted: " + string(cp))
	}
	return svc, c, p, r, v, nil
}

func (h *backupsAPI) getRepository(ctx context.Context, in *backupRepositoryIDInput) (*backupRepositoryOutput, error) {
	_, _, _, r, v, err := h.visibleRepository(ctx, in.RepositoryID)
	if err != nil {
		return nil, err
	}
	body := newBackupRepository(r, v)
	return &backupRepositoryOutput{ETagHeader: repositoryETag(body), Body: body}, nil
}

type createBackupRepositoryInput struct {
	Body struct {
		Name            string `json:"name" minLength:"1" maxLength:"100" example:"Offsite S3"`
		Kind            string `json:"kind" enum:"local,s3"`
		Executor        string `json:"executor,omitempty" maxLength:"64" doc:"Local repositories: manager, or the environment ID whose agent owns the path."`
		Path            string `json:"path,omitempty" maxLength:"1024" example:"/backups/dockyard" doc:"Local repositories: an absolute directory below the executor's DOCKYARD_BACKUP_LOCAL_ROOTS, outside every backup source."`
		Endpoint        string `json:"endpoint,omitempty" maxLength:"255" example:"https://s3.eu-central-1.amazonaws.com"`
		Bucket          string `json:"bucket,omitempty" maxLength:"63"`
		Prefix          string `json:"prefix,omitempty" maxLength:"512" example:"dockyard"`
		Region          string `json:"region,omitempty" maxLength:"64"`
		PathStyle       bool   `json:"pathStyle,omitempty" doc:"Path-style bucket addressing (MinIO and most self-hosted S3)."`
		AccessKeyID     string `json:"accessKeyId,omitempty" maxLength:"256" writeOnly:"true"`
		SecretAccessKey string `json:"secretAccessKey,omitempty" maxLength:"1024" writeOnly:"true" doc:"Write-only: never returned, logged or audited."`
		VerifyCron      string `json:"verifyCron,omitempty" maxLength:"128" doc:"Verification schedule (default: the instance default of backup_verification)."`
		VerifyTimeZone  string `json:"verifyTimeZone,omitempty" maxLength:"64"`
		VerifyReadData  string `json:"verifyReadData,omitempty" maxLength:"16" example:"5%"`
	}
}

// CreatedBackupRepository is the result of creating a repository.
type CreatedBackupRepository struct {
	Repository  BackupRepository   `json:"repository"`
	RecoveryKey *RecoveryKeyReveal `json:"recoveryKey,omitempty" doc:"Present only when this creation generated the instance Recovery Key: shown exactly once."`
	KeyState    RecoveryKeyState   `json:"keyState"`
	NextStep    string             `json:"nextStep"`
}

type createdBackupRepositoryOutput struct {
	ETagHeader
	Body CreatedBackupRepository
}

func (h *backupsAPI) createRepository(ctx context.Context, in *createBackupRepositoryInput) (*createdBackupRepositoryOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapBackupRepositoryManage), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapBackupRepositoryManage))
	}
	b := in.Body
	if b.VerifyCron != "" || b.VerifyTimeZone != "" {
		if err := ValidateSchedule(b.VerifyCron, b.VerifyTimeZone, "body.verifyCron"); err != nil {
			return nil, err
		}
	}
	res, err := svc.CreateRepository(ctx, domain.BackupRepositoryInput{Name: b.Name, Kind: b.Kind, Executor: b.Executor, Path: b.Path,
		Endpoint: b.Endpoint, Bucket: b.Bucket, Prefix: b.Prefix, Region: b.Region, PathStyle: b.PathStyle, AccessKeyID: b.AccessKeyID,
		SecretAccessKey: b.SecretAccessKey, VerifyCron: b.VerifyCron, VerifyTimeZone: b.VerifyTimeZone, VerifyReadData: b.VerifyReadData})
	if err != nil {
		return nil, backupError(err)
	}
	repo := newBackupRepository(res.Repository, authz.ViewOf(c, backupRepositoryResource(res.Repository.ID)))
	if !repo.ViewFull() {
		repo = newBackupRepository(res.Repository, authz.View{Level: authz.Full, Actions: []string{string(CapBackupRepositoryRead)}})
	}
	out := CreatedBackupRepository{Repository: repo, KeyState: newRecoveryKeyState(res.Key, nil),
		NextStep: "Confirm the Recovery Key for this repository (POST .../recovery-confirmations) before policies can use it."}
	if res.RecoveryKey != nil {
		out.RecoveryKey = newRecoveryKeyReveal(*res.RecoveryKey)
	}
	return &createdBackupRepositoryOutput{ETagHeader: repositoryETag(out.Repository), Body: out}, nil
}

// ViewFull reports whether the DTO is the full view.
func (r BackupRepository) ViewFull() bool { return r.View == authz.Full.String() }

type updateBackupRepositoryInput struct {
	RepositoryID string `path:"repositoryId" maxLength:"64" doc:"Backup repository ID."`
	IfMatchParam
	Body struct {
		Name            *string `json:"name,omitempty" example:"Offsite S3" minLength:"1" maxLength:"100"`
		Region          *string `json:"region,omitempty" maxLength:"64"`
		PathStyle       *bool   `json:"pathStyle,omitempty"`
		AccessKeyID     *string `json:"accessKeyId,omitempty" maxLength:"256" writeOnly:"true" doc:"Replace the S3 credentials (both fields)."`
		SecretAccessKey *string `json:"secretAccessKey,omitempty" maxLength:"1024" writeOnly:"true"`
		VerifyCron      *string `json:"verifyCron,omitempty" example:"0 5 * * 0" maxLength:"128"`
		VerifyTimeZone  *string `json:"verifyTimeZone,omitempty" maxLength:"64"`
		VerifyEnabled   *bool   `json:"verifyEnabled,omitempty" doc:"Enable scheduled verification (the key must be confirmed first)."`
		VerifyReadData  *string `json:"verifyReadData,omitempty" maxLength:"16"`
	}
}

func (h *backupsAPI) updateRepository(ctx context.Context, in *updateBackupRepositoryInput) (*backupRepositoryOutput, error) {
	svc, _, _, r, v, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(r.Revision)); err != nil {
		return nil, err
	}
	b := in.Body
	_, after, err := svc.UpdateRepository(ctx, r.ID, r.Revision, domain.BackupRepositoryPatch{Name: b.Name, Region: b.Region,
		PathStyle: b.PathStyle, AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey, VerifyCron: b.VerifyCron,
		VerifyTimeZone: b.VerifyTimeZone, VerifyEnabled: b.VerifyEnabled, VerifyReadData: b.VerifyReadData})
	if errors.Is(err, domain.ErrRevisionMismatch) {
		cur, gerr := svc.GetRepository(ctx, r.ID)
		if gerr != nil {
			return nil, backupError(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, backupError(err)
	}
	body := newBackupRepository(after, v)
	return &backupRepositoryOutput{ETagHeader: repositoryETag(body), Body: body}, nil
}

type deleteBackupRepositoryInput struct {
	RepositoryID string `path:"repositoryId" maxLength:"64" doc:"Backup repository ID."`
	IfMatchParam
}

func (h *backupsAPI) deleteRepository(ctx context.Context, in *deleteBackupRepositoryInput) (*struct{}, error) {
	svc, _, _, r, _, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(r.Revision)); err != nil {
		return nil, err
	}
	if err := svc.DeleteRepository(ctx, r.ID, r.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			return nil, stale(r.Revision)
		}
		return nil, backupError(err)
	}
	audit.SetDetail(ctx, "location", backup.Destination{Kind: r.Kind, Path: r.Path, Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix}.Base())
	return &struct{}{}, nil
}

type backupConnectionTestOutput struct{ Body BackupConnectionTest }

func (h *backupsAPI) testRepository(ctx context.Context, in *backupRepositoryIDInput) (*backupConnectionTestOutput, error) {
	svc, _, _, r, _, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryManage)
	if err != nil {
		return nil, err
	}
	t, err := svc.TestRepository(ctx, r.ID)
	if err != nil {
		return nil, backupError(err)
	}
	return &backupConnectionTestOutput{Body: *newBackupConnectionTest(&t)}, nil
}

type recoveryConfirmationInput struct {
	RepositoryID string `path:"repositoryId" maxLength:"64" doc:"Backup repository ID."`
	Body         struct {
		RecoveryKey string `json:"recoveryKey" example:"dyrk-4V7Q-2M9X-KP3T-8WRH-6JDN-CF5B-ZL2A" minLength:"1" maxLength:"256" writeOnly:"true" doc:"Type (or paste) the Recovery Key. Never logged, stored or audited."`
		BackedUp    bool   `json:"backedUp" doc:"Must be true: you saved the key outside DockYard."`
	}
}

// RecoveryConfirmation is the result of a confirmation.
type RecoveryConfirmation struct {
	Repository BackupRepository `json:"repository"`
	KeyState   RecoveryKeyState `json:"keyState"`
	Activated  bool             `json:"activated" doc:"The confirmed key was newly generated (first key or rotation) and is now current."`
	// Jobs moving existing repositories to a rotated key.
	Jobs []Job `json:"jobs,omitempty"`
}

type recoveryConfirmationOutput struct{ Body RecoveryConfirmation }

func (h *backupsAPI) confirmRecoveryKey(ctx context.Context, in *recoveryConfirmationInput) (*recoveryConfirmationOutput, error) {
	svc, c, _, r, _, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryManage)
	if err != nil {
		return nil, err
	}
	res, err := svc.ConfirmKey(ctx, r.ID, in.Body.RecoveryKey, in.Body.BackedUp)
	if err != nil {
		return nil, backupError(err)
	}
	pending, _ := svc.LocationKeyStatus(ctx)
	out := RecoveryConfirmation{Repository: newBackupRepository(res.Repository, authz.ViewOf(c, backupRepositoryResource(r.ID))),
		KeyState: newRecoveryKeyState(res.Key, pending), Activated: res.Activated}
	for _, j := range res.RotationJobs {
		out.Jobs = append(out.Jobs, NewJob(j))
	}
	return &recoveryConfirmationOutput{Body: out}, nil
}

// KeyRotationStarted is a started rotation.
type KeyRotationStarted struct {
	RecoveryKey RecoveryKeyReveal `json:"recoveryKey"`
	KeyState    RecoveryKeyState  `json:"keyState"`
	NextStep    string            `json:"nextStep"`
}

type keyRotationOutput struct{ Body KeyRotationStarted }

func (h *backupsAPI) rotateRecoveryKey(ctx context.Context, in *backupRepositoryIDInput) (*keyRotationOutput, error) {
	svc, _, _, _, _, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryManage)
	if err != nil {
		return nil, err
	}
	res, err := svc.RotateKey(ctx)
	if err != nil {
		return nil, backupError(err)
	}
	return &keyRotationOutput{Body: KeyRotationStarted{RecoveryKey: *newRecoveryKeyReveal(res.Key), KeyState: newRecoveryKeyState(res.State, nil),
		NextStep: "Save the new key, then confirm it (POST .../recovery-confirmations). Until then the current key stays in use; " +
			"afterwards every repository moves to the new key the next time it is used (keep the old key until keyState shows no pending locations)."}}, nil
}

// BackupLocationHealth is one physical repository's state.
type BackupLocationHealth struct {
	Scope              string     `json:"scope"`
	Repository         string     `json:"repository" doc:"restic repository location (no credentials)."`
	ResticRepositoryID string     `json:"resticRepositoryId,omitempty"`
	KeyGeneration      int        `json:"keyGeneration"`
	LastBackupAt       *time.Time `json:"lastBackupAt,omitempty"`
	LastVerifiedAt     *time.Time `json:"lastVerifiedAt,omitempty"`
	LastVerifyResult   string     `json:"lastVerifyResult,omitempty"`
	SizeBytes          int64      `json:"sizeBytes,omitempty"`
}

// BackupRepositoryHealth summarizes a repository.
type BackupRepositoryHealth struct {
	RepositoryID   string                 `json:"repositoryId"`
	State          string                 `json:"state"`
	Healthy        bool                   `json:"healthy"`
	Problems       []string               `json:"problems"`
	LastBackupAt   *time.Time             `json:"lastBackupAt,omitempty"`
	LastVerifiedAt *time.Time             `json:"lastVerifiedAt,omitempty"`
	BackupAge      string                 `json:"backupAge,omitempty" example:"26h0m0s"`
	Snapshots      int                    `json:"snapshots"`
	SizeBytes      int64                  `json:"sizeBytes"`
	Locations      []BackupLocationHealth `json:"locations"`
	KeyState       RecoveryKeyState       `json:"keyState"`
	LastTest       *BackupConnectionTest  `json:"lastTest,omitempty"`
}

type backupRepositoryHealthOutput struct{ Body BackupRepositoryHealth }

func (h *backupsAPI) repositoryHealth(ctx context.Context, in *backupRepositoryIDInput) (*backupRepositoryHealthOutput, error) {
	svc, _, _, r, _, err := h.requireRepository(ctx, in.RepositoryID, CapBackupRepositoryRead)
	if err != nil {
		return nil, err
	}
	hl, err := svc.Health(ctx, r.ID)
	if err != nil {
		return nil, backupError(err)
	}
	pending, _ := svc.LocationKeyStatus(ctx)
	d := backup.Destination{Kind: r.Kind, Path: r.Path, Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix, Region: r.Region, PathStyle: r.PathStyle}
	out := BackupRepositoryHealth{RepositoryID: r.ID, State: r.State, Healthy: len(hl.Problems) == 0, Problems: hl.Problems,
		LastBackupAt: hl.LastBackupAt, LastVerifiedAt: hl.LastVerifiedAt, Snapshots: hl.Snapshots, SizeBytes: hl.SizeBytes,
		Locations: []BackupLocationHealth{}, KeyState: newRecoveryKeyState(hl.Key, pending), LastTest: newBackupConnectionTest(r.LastTest)}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	if hl.LastBackupAt != nil {
		out.BackupAge = h.deps.clock().Now().Sub(*hl.LastBackupAt).Round(time.Minute).String()
	}
	for _, l := range hl.Locations {
		out.Locations = append(out.Locations, BackupLocationHealth{Scope: l.Scope, Repository: d.Repository(l.Scope),
			ResticRepositoryID: l.ResticRepositoryID, KeyGeneration: l.KeyGeneration, LastBackupAt: l.LastBackupAt,
			LastVerifiedAt: l.LastVerifiedAt, LastVerifyResult: l.LastVerifyResult, SizeBytes: l.SizeBytes})
	}
	return &backupRepositoryHealthOutput{Body: out}, nil
}

const recoveryKeyNote = " Recovery Key administration: instance owner only, in a signed-in browser session (never with an API token, 403 api_token_not_allowed)."

func registerBackupRepositories(a huma.API, h *backupsAPI) {
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-backup-repositories", Method: http.MethodGet, Path: BasePath + "/backup-repositories",
			Summary: "List backup repositories",
			Description: "Destinations in creation order, filtered per item (#17): backup_repository.read shows one in full, any other " +
				"capability on it only id, name, kind and state. Credentials and the Recovery Key are never returned.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRepositoryRead, Scope: ScopeResource,
	}, h.listRepositories)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-repository", Method: http.MethodPost, Path: BasePath + "/backup-repositories",
			Summary: "Add a backup repository", DefaultStatus: http.StatusCreated,
			Description: "Stores a destination: a local directory on the manager or on one environment's agent, or an S3 bucket/prefix " +
				"(credentials sealed, write-only). Below it DockYard keeps one restic repository per scope (dockyard-manager, " +
				"dockyard-env-<environmentId>). The first repository of an instance generates the Recovery Key (returned once in " +
				"recoveryKey; owner only). Every repository starts awaiting_confirmation: nothing is initialized and no policy can use it " +
				"until the owner re-enters the key." + recoveryKeyNote,
			Tags: []string{tagBackups}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeInstance, SessionOnly: true,
	}, h.createRepository)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup-repository", Method: http.MethodGet, Path: BasePath + "/backup-repositories/{repositoryId}",
			Summary: "Get a backup repository", Tags: []string{tagBackups}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapBackupRepositoryRead, Scope: ScopeResource,
	}, h.getRepository)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-backup-repository", Method: http.MethodPatch, Path: BasePath + "/backup-repositories/{repositoryId}",
			Summary: "Update a backup repository",
			Description: "Edits the name, S3 region/addressing, the verification schedule, or replaces the S3 credentials (write-only). " +
				"The destination itself cannot move. Requires If-Match.",
			Tags: []string{tagBackups}, Errors: editErrs,
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeResource,
	}, h.updateRepository)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-backup-repository", Method: http.MethodDelete, Path: BasePath + "/backup-repositories/{repositoryId}",
			Summary: "Remove a backup repository", DefaultStatus: http.StatusNoContent,
			Description: "Removes the repository from DockYard (409 backup_repository_in_use while a policy uses it). The restic " +
				"repositories at the destination are left untouched. Requires If-Match.",
			Tags: []string{tagBackups}, Errors: editErrs,
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeResource,
	}, h.deleteRepository)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-repository-connection-test", Method: http.MethodPost,
			Path: BasePath + "/backup-repositories/{repositoryId}/connection-tests", Summary: "Test a backup repository",
			Description: "S3: writes, reads and deletes a probe object below the prefix and reads the bucket's Object Lock configuration " +
				"(warns when pruning may be refused). Local: checks the path on its executor. With a confirmed key it also opens the " +
				"restic repositories that already exist below the destination. A failing test is a 200 with ok false.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeResource,
	}, h.testRepository)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-repository-recovery-confirmation", Method: http.MethodPost,
			Path: BasePath + "/backup-repositories/{repositoryId}/recovery-confirmations", Summary: "Confirm the Recovery Key",
			Description: "The re-entry challenge: the owner types the Recovery Key and confirms it is saved (backedUp: true). A newly " +
				"generated key (first key or rotation) becomes current; otherwise the input must be the current key. The repository " +
				"becomes ready. 422 recovery_key_mismatch / recovery_key_malformed. The key is never stored from this input, logged " +
				"or audited (only its fingerprint)." + recoveryKeyNote,
			Tags: []string{tagBackups}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeResource, SessionOnly: true,
	}, h.confirmRecoveryKey)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-repository-key-rotation", Method: http.MethodPost,
			Path: BasePath + "/backup-repositories/{repositoryId}/key-rotations", Summary: "Rotate the Recovery Key",
			Description: "Generates a new Recovery Key for the whole instance (every repository shares it), returned once. It becomes " +
				"current when confirmed; then each repository location moves to it the next time it is used (verification jobs are " +
				"queued at once) and the previous key is dropped when none is left on it. 409 key_rotation_in_progress while a " +
				"previous rotation is unfinished. Requires a recent step-up (403 step_up_required)." + recoveryKeyNote,
			Tags: []string{tagBackups}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapBackupRepositoryManage, Scope: ScopeResource, SessionOnly: true,
	}, h.rotateRecoveryKey)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup-repository-health", Method: http.MethodGet, Path: BasePath + "/backup-repositories/{repositoryId}/health",
			Summary: "Backup repository health",
			Description: "Last backup and verification per location, backup age, size, key rotation progress, the last connection test " +
				"and the problems that need attention.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusNotFound, http.StatusForbidden},
		},
		Capability: CapBackupRepositoryRead, Scope: ScopeResource,
	}, h.repositoryHealth)
}
