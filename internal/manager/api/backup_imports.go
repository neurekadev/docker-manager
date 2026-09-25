package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/manager/backups"
)

// Fresh-manager import (#24): public routes of the protected first-run
// setup session. They are refused once an owner exists (409
// setup_complete), outside a secure origin (403 insecure_origin) and
// beyond the setup rate limit (429). The Recovery Key and S3 credentials
// are write-only: never returned, logged, audited or stored by the
// import itself.

// Import error codes (backups.Import*).
const (
	CodeBackupImportKeyRejected        = backups.ImportKeyRejected
	CodeBackupImportNotFound           = backups.ImportNotFound
	CodeBackupImportManifestCorrupt    = backups.ImportManifestCorrupt
	CodeBackupImportSchemaIncompatible = backups.ImportSchemaIncompatible
	CodeBackupImportKeyRotated         = backups.ImportKeyRotated
	CodeBackupImportStateMissing       = backups.ImportStateMissing
	CodeBackupImportUnreachable        = backups.ImportUnreachable
	CodeBackupImportInProgress         = backups.ImportInProgress
)

var importStatus = map[string]int{
	backups.ImportKeyRejected:        http.StatusUnprocessableEntity,
	backups.ImportNotFound:           http.StatusUnprocessableEntity,
	backups.ImportManifestCorrupt:    http.StatusUnprocessableEntity,
	backups.ImportKeyRotated:         http.StatusUnprocessableEntity,
	backups.ImportUnreachable:        http.StatusUnprocessableEntity,
	backups.ImportSchemaIncompatible: http.StatusConflict,
	backups.ImportStateMissing:       http.StatusConflict,
	backups.ImportInProgress:         http.StatusConflict,
}

// importError maps import refusals (with their recovery guidance) and
// other backup errors.
func importError(err error) error {
	var ref *backup.Refusal
	if errors.As(err, &ref) {
		if status, ok := importStatus[ref.Class]; ok {
			msg := ref.Message
			if ref.Guidance != "" {
				msg += ". " + ref.Guidance
			}
			return NewError(status, ref.Class, msg)
		}
	}
	return backupError(err)
}

// BackupImportSource is the destination and keys of an import.
type BackupImportSource struct {
	Kind                string `json:"kind" enum:"local,s3"`
	Path                string `json:"path,omitempty" maxLength:"1024" example:"/backups/dockyard" doc:"Local destinations: the directory on this manager (below DOCKYARD_BACKUP_LOCAL_ROOTS); it may be a new mount path."`
	Endpoint            string `json:"endpoint,omitempty" maxLength:"255" example:"https://s3.eu-central-1.amazonaws.com"`
	Bucket              string `json:"bucket,omitempty" maxLength:"63"`
	Prefix              string `json:"prefix,omitempty" maxLength:"512" example:"dockyard"`
	Region              string `json:"region,omitempty" maxLength:"64"`
	PathStyle           bool   `json:"pathStyle,omitempty" doc:"Path-style bucket addressing (MinIO and most self-hosted S3)."`
	AccessKeyID         string `json:"accessKeyId,omitempty" maxLength:"256" writeOnly:"true" doc:"S3: the key pair to use now (it may be newly issued); the restored repository keeps it."`
	SecretAccessKey     string `json:"secretAccessKey,omitempty" maxLength:"1024" writeOnly:"true" doc:"Write-only: never returned, logged or audited."`
	RecoveryKey         string `json:"recoveryKey" minLength:"1" maxLength:"256" writeOnly:"true" doc:"The saved Recovery Key (the newest one after a rotation). Never returned, logged, stored or audited."`
	PreviousRecoveryKey string `json:"previousRecoveryKey,omitempty" maxLength:"256" writeOnly:"true" doc:"Only after a rotation that has not reached every repository, or to import a set saved before it: the previous key."`
	SetID               string `json:"setId,omitempty" maxLength:"64" doc:"Previews: also open this set's secret-key bundle (the final check). Restores: the set to import (required)."`
	Confirm             bool   `json:"confirm,omitempty" doc:"Restores: must be true (this manager's empty state is replaced and the manager restarts)."`
}

func (b BackupImportSource) source() backups.ImportSource {
	return backups.ImportSource{
		Destination: backup.Destination{Kind: b.Kind, Path: b.Path, Endpoint: b.Endpoint, Bucket: b.Bucket, Prefix: b.Prefix, Region: b.Region,
			PathStyle: b.PathStyle},
		Credentials: backup.S3Credentials{AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey},
		RecoveryKey: b.RecoveryKey, PreviousRecoveryKey: b.PreviousRecoveryKey,
	}
}

// BackupImportLocation is one restic repository as seen by an import.
type BackupImportLocation struct {
	Scope              string `json:"scope" example:"manager" doc:"manager or env:<environmentId>."`
	EnvironmentID      string `json:"environmentId,omitempty"`
	EnvironmentName    string `json:"environmentName,omitempty"`
	EngineID           string `json:"engineId,omitempty"`
	RepositoryID       string `json:"repositoryId,omitempty"`
	RepositoryName     string `json:"repositoryName,omitempty"`
	Repository         string `json:"repository" doc:"The restic repository (no credentials)."`
	Reachable          bool   `json:"reachable" doc:"This manager can open it with the supplied destination and credentials."`
	Found              bool   `json:"found"`
	Key                string `json:"key,omitempty" enum:"current,previous" doc:"Which supplied Recovery Key opened it; previous means the rotation has not reached it (partially rotated keys)."`
	ResticRepositoryID string `json:"resticRepositoryId,omitempty"`
	ErrorClass         string `json:"errorClass,omitempty" example:"repository_not_found"`
	Note               string `json:"note,omitempty" doc:"Why it is not reachable yet and what makes it available."`
}

func newImportLocation(l backups.ImportLocation) BackupImportLocation {
	return BackupImportLocation{Scope: l.Scope, EnvironmentID: l.EnvironmentID, EnvironmentName: l.EnvironmentName, EngineID: l.EngineID,
		RepositoryID: l.RepositoryID, RepositoryName: l.RepositoryName, Repository: l.Repository, Reachable: l.Reachable, Found: l.Found,
		Key: l.Key, ResticRepositoryID: l.ResticRepositoryID, ErrorClass: l.ErrorClass, Note: l.Note}
}

func newImportLocations(ls []backups.ImportLocation) []BackupImportLocation {
	out := make([]BackupImportLocation, 0, len(ls))
	for _, l := range ls {
		out = append(out, newImportLocation(l))
	}
	return out
}

// BackupImportConnectionTest is the result of an import connection test.
type BackupImportConnectionTest struct {
	OK             bool                   `json:"ok"`
	KeyFingerprint string                 `json:"keyFingerprint" example:"rk_3f2a9c1d0b7e4a55" doc:"Fingerprint of the supplied Recovery Key (compare with the one you saved)."`
	Manager        BackupImportLocation   `json:"manager"`
	Locations      []BackupImportLocation `json:"locations" doc:"Host repositories named by the manifests or found at the destination."`
	CanRead        *bool                  `json:"canRead,omitempty"`
	CanWrite       *bool                  `json:"canWrite,omitempty"`
	CanDelete      *bool                  `json:"canDelete,omitempty"`
	ObjectLock     *bool                  `json:"objectLock,omitempty"`
	Sets           int                    `json:"sets" doc:"Backup sets found in the manager repository (newest 20)."`
	Problems       []string               `json:"problems" doc:"What prevents or limits the import, with what to do."`
}

// BackupImportMember is one planned snapshot of a set.
type BackupImportMember struct {
	Item            string    `json:"item" example:"stack/0190a6e0-0000-7000-8000-000000000001"`
	Kind            string    `json:"kind" enum:"manager_state,stack,volume"`
	Scope           string    `json:"scope"`
	EnvironmentID   string    `json:"environmentId,omitempty"`
	EnvironmentName string    `json:"environmentName,omitempty"`
	StackID         string    `json:"stackId,omitempty"`
	StackName       string    `json:"stackName,omitempty"`
	Volume          string    `json:"volume,omitempty"`
	SnapshotID      string    `json:"snapshotId,omitempty"`
	SnapshotTime    time.Time `json:"snapshotTime,omitzero"`
	State           string    `json:"state" enum:"complete,partial,failed,pending,missing"`
	ErrorClass      string    `json:"errorClass,omitempty"`
	Located         string    `json:"located" enum:"found,missing,unverified,not_backed_up" doc:"found: listed in its repository; missing: its repository was read and the snapshot is not there; unverified: its repository is not reachable from this manager yet; not_backed_up: no snapshot was written."`
}

// BackupImportSet is one backup set at the source.
type BackupImportSet struct {
	SetID               string               `json:"setId"`
	PolicyName          string               `json:"policyName,omitempty"`
	InstanceID          string               `json:"instanceId,omitempty"`
	CreatedAt           time.Time            `json:"createdAt,omitzero"`
	AppVersion          string               `json:"appVersion,omitempty"`
	Completeness        string               `json:"completeness,omitempty" enum:"complete,partial,failed,pending"`
	SchemaLatest        string               `json:"schemaLatest,omitempty" doc:"Newest migration of the set's manager database."`
	SchemaCompatible    bool                 `json:"schemaCompatible"`
	ManagerSnapshotID   string               `json:"managerSnapshotId,omitempty"`
	ManagerSnapshotTime time.Time            `json:"managerSnapshotTime,omitzero"`
	KeyBundle           string               `json:"keyBundle,omitempty" enum:"ok,previous_key,mismatch,corrupt,missing" doc:"The selected set only: whether a supplied Recovery Key opens its secret-key bundle."`
	HostOnly            bool                 `json:"hostOnly" doc:"Known only from host repositories (no manager-state manifest)."`
	Importable          bool                 `json:"importable"`
	BlockerCode         string               `json:"blockerCode,omitempty" doc:"The error code an import of this set answers."`
	Problems            []string             `json:"problems"`
	Members             []BackupImportMember `json:"members"`
}

// BackupImportPreview lists the importable sets.
type BackupImportPreview struct {
	KeyFingerprint string                 `json:"keyFingerprint"`
	Manager        BackupImportLocation   `json:"manager"`
	Locations      []BackupImportLocation `json:"locations"`
	Sets           []BackupImportSet      `json:"sets" doc:"Newest first (at most 20)."`
	Problems       []string               `json:"problems"`
}

func newImportPreview(p backups.ImportPreview) BackupImportPreview {
	out := BackupImportPreview{KeyFingerprint: p.KeyFingerprint, Manager: newImportLocation(p.Manager), Locations: newImportLocations(p.Locations),
		Sets: []BackupImportSet{}, Problems: nonNil(p.Problems)}
	for _, s := range p.Sets {
		set := BackupImportSet{SetID: s.SetID, PolicyName: s.PolicyName, InstanceID: s.InstanceID, CreatedAt: s.CreatedAt, AppVersion: s.AppVersion,
			Completeness: s.Completeness, SchemaLatest: s.SchemaLatest, SchemaCompatible: s.SchemaCompatible, ManagerSnapshotID: s.ManagerSnapshotID,
			ManagerSnapshotTime: s.ManagerSnapshotTime, KeyBundle: s.KeyBundle, HostOnly: s.HostOnly, Importable: s.Importable,
			BlockerCode: s.BlockerClass, Problems: nonNil(s.Problems), Members: []BackupImportMember{}}
		for _, m := range s.Members {
			set.Members = append(set.Members, BackupImportMember{Item: m.Item, Kind: m.Kind, Scope: m.Scope, EnvironmentID: m.EnvironmentID,
				EnvironmentName: m.EnvironmentName, StackID: m.StackID, StackName: m.StackName, Volume: m.Volume, SnapshotID: m.SnapshotID,
				SnapshotTime: m.SnapshotTime, State: m.State, ErrorClass: m.ErrorClass, Located: m.Located})
		}
		out.Sets = append(out.Sets, set)
	}
	return out
}

type importInput struct {
	Body BackupImportSource
}

type importTestOutput struct {
	Body BackupImportConnectionTest
}

type importPreviewOutput struct {
	Body BackupImportPreview
}

type backupImportsAPI struct {
	identity IdentityService
	backups  BackupService
}

func (h *backupImportsAPI) open(ctx context.Context) (BackupService, error) {
	if h.identity == nil || h.backups == nil {
		return nil, Unavailable(CodeUnavailable, "backup import is not available")
	}
	if err := h.identity.SetupOpen(ctx); err != nil {
		return nil, identityError(err)
	}
	return h.backups, nil
}

func registerBackupImports(a huma.API, deps Deps) {
	h := &backupImportsAPI{identity: deps.Identity, backups: deps.Backups}
	errs := []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-setup-backup-import-connection-test", Method: http.MethodPost,
			Path: BasePath + "/setup/backup-imports/connection-tests", Summary: "Test a backup import source (first-run setup)",
			Description: "Before an owner exists only (then 409 setup_complete). Checks S3 access, whether the Recovery Key opens the manager " +
				"repository (dockyard-manager) and which host repositories the manifests name or the destination holds. Problems explain " +
				"key loss, missing repositories, damaged manifests and partially rotated keys. Nothing is stored.",
			Tags: []string{tagSetup}, Errors: errs,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *importInput) (*importTestOutput, error) {
		svc, err := h.open(ctx)
		if err != nil {
			return nil, err
		}
		t, err := svc.TestImport(ctx, in.Body.source())
		if err != nil {
			return nil, importError(err)
		}
		return &importTestOutput{Body: BackupImportConnectionTest{OK: t.OK, KeyFingerprint: t.KeyFingerprint, Manager: newImportLocation(t.Manager),
			Locations: newImportLocations(t.Locations), CanRead: t.CanRead, CanWrite: t.CanWrite, CanDelete: t.CanDelete, ObjectLock: t.ObjectLock,
			Sets: t.Sets, Problems: nonNil(t.Problems)}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-setup-backup-import-preview", Method: http.MethodPost,
			Path: BasePath + "/setup/backup-imports/previews", Summary: "Preview the backup sets of an import source (first-run setup)",
			Description: "Lists the newest backup sets from the portable manifests in the repositories (not from any database): completeness, " +
				"where each member's snapshot is (found, missing, unverified, not_backed_up), the DockYard version and whether this build can run " +
				"the set's database. With setId the set's secret-key bundle is opened too. Sets known only from host repositories are listed " +
				"but cannot be imported.",
			Tags: []string{tagSetup}, Errors: errs,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *importInput) (*importPreviewOutput, error) {
		svc, err := h.open(ctx)
		if err != nil {
			return nil, err
		}
		p, err := svc.PreviewImport(ctx, in.Body.source(), in.Body.SetID)
		if err != nil {
			return nil, importError(err)
		}
		return &importPreviewOutput{Body: newImportPreview(p)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-setup-backup-import-restore", Method: http.MethodPost,
			Path: BasePath + "/setup/backup-imports/restores", Summary: "Import a backup set's manager state (first-run setup)",
			Description: "Queues backup.import (202 + job): the set's manager-state snapshot is restored into a staging directory, its " +
				"secret-key bundle is opened with the Recovery Key and the database is checked; then the manager restarts and applies it. " +
				"Follow the progress with GET /api/v1/setup/status (backupImport). After the restart the owner signs in with the restored " +
				"account: no session is revived, every API token is revoked, every agent must re-attach (enrollment intent " +
				"reattach:<environmentId>), and the repository uses the destination and S3 credentials supplied here.",
			Tags: []string{tagSetup}, Errors: errs, DefaultStatus: http.StatusAccepted,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *importInput) (*JobAccepted, error) {
		svc, err := h.open(ctx)
		if err != nil {
			return nil, err
		}
		if !in.Body.Confirm {
			return nil, Invalid("an import replaces this manager's state and restarts it: confirm it", Field("body.confirm", "must be true"))
		}
		j, err := svc.StartImport(ctx, in.Body.source(), in.Body.SetID, "")
		if err != nil {
			return nil, importError(err)
		}
		return Accepted(j), nil
	})
}

// SetupBackupImport is the newest import's progress (setup status).
type SetupBackupImport struct {
	JobID          string `json:"jobId"`
	State          string `json:"state" enum:"queued,waiting,running,succeeded,failed,cancelled,interrupted"`
	ErrorCode      string `json:"errorCode,omitempty"`
	Recovery       string `json:"recovery,omitempty" doc:"What to do after a failure."`
	RestartPending bool   `json:"restartPending" doc:"The manager state is staged; the manager restarts to apply it. Sign in afterwards with the restored owner account."`
}
