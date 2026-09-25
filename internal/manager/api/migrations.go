package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/migrations"
)

// Environment migration (#35): previews, migrations and source removals of
// stacks, previews and migrations of standalone volumes.

// Migration capabilities.
const (
	CapStackMigrate  Capability = "stack.migrate"
	CapVolumeMigrate Capability = "volume.migrate"
)

// Migration error codes.
const (
	CodeMigrationBlocked       = "migration_blocked"
	CodeMigrationNotCompleted  = "migration_not_completed"
	CodeMigrationSourceRemoved = "migration_source_removed"
	CodeMigrationSourceInUse   = "migration_source_in_use"
)

// MigrationService runs migrations (*migrations.Service).
type MigrationService interface {
	PreviewStack(ctx context.Context, p authz.Principal, owner bool, st domain.Stack, r migrations.StackRequest) (migrations.Plan, error)
	StartStack(ctx context.Context, p authz.Principal, st domain.Stack, r migrations.StackRequest) (domain.Job, domain.Migration, error)
	RemoveSource(ctx context.Context, p authz.Principal, stackID, migrationID, key string) (domain.Job, error)
	PreviewVolume(ctx context.Context, p authz.Principal, owner bool, env, volume string, r migrations.VolumeRequest) (migrations.Plan, error)
	StartVolume(ctx context.Context, p authz.Principal, env, volume string, r migrations.VolumeRequest) (domain.Job, domain.Migration, error)
}

// MigrationFinding is a preflight blocker or warning.
type MigrationFinding struct {
	Code     string `json:"code" doc:"Stable code, e.g. platform_mismatch, port_conflict, volume_name_conflict, external_network_missing, external_bind_path, plain_http_transport."`
	Message  string `json:"message"`
	Service  string `json:"service,omitempty"`
	Resource string `json:"resource,omitempty"`
}

// MigrationServicePlan is what happens to a service's image.
type MigrationServicePlan struct {
	Name                 string `json:"name"`
	Image                string `json:"image"`
	Action               string `json:"action" enum:"pull,rebuild,transfer,present" doc:"pull on the destination (through its registry connection), rebuild from the build section, transfer through the manager, or already present."`
	Platform             string `json:"platform,omitempty" doc:"The image's platform on the source."`
	RegistryConnectionID string `json:"registryConnectionId,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

// MigrationVolumePlan is what happens to a volume.
type MigrationVolumePlan struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Key       string `json:"key,omitempty" doc:"Compose volume key."`
	Action    string `json:"action" enum:"copy,skip,definition_only,external"`
	Anonymous bool   `json:"anonymous,omitempty"`
	Bytes     int64  `json:"bytes"`
	Entries   int64  `json:"entries"`
	Truncated bool   `json:"truncated,omitempty" doc:"The size is a lower bound (the scan hit its budget)."`
	Reason    string `json:"reason,omitempty"`
}

// MigrationData sizes the transfer.
type MigrationData struct {
	ProjectBytes           int64 `json:"projectBytes"`
	VolumeBytes            int64 `json:"volumeBytes"`
	ImageBytes             int64 `json:"imageBytes"`
	TotalBytes             int64 `json:"totalBytes" doc:"Estimated bytes to transfer (tar framing included)."`
	Truncated              bool  `json:"truncated,omitempty"`
	DestinationStacksFree  int64 `json:"destinationStacksFree" doc:"Free bytes of the destination's stacks volume (-1: unknown)."`
	DestinationVolumesFree int64 `json:"destinationVolumesFree" doc:"Free bytes of the destination's Docker data root (-1: unknown)."`
}

// MigrationDowntime is the expected downtime.
type MigrationDowntime struct {
	EstimatedSeconds int64  `json:"estimatedSeconds"`
	Basis            string `json:"basis"`
}

// MigrationTransport describes the relay.
type MigrationTransport struct {
	SourcePlainHTTP              bool  `json:"sourcePlainHttp"`
	DestinationPlainHTTP         bool  `json:"destinationPlainHttp"`
	BandwidthLimitBytesPerSecond int64 `json:"bandwidthLimitBytesPerSecond" doc:"DOCKYARD_MIGRATION_BANDWIDTH_LIMIT (0: unlimited)."`
}

// MigrationAccessChange is one user's access change.
type MigrationAccessChange struct {
	UserID   string   `json:"userId"`
	Username string   `json:"username"`
	Gained   []string `json:"gained"`
	Lost     []string `json:"lost"`
}

// MigrationAccess is how the migration changes who may act on the stack.
type MigrationAccess struct {
	Complete       bool                    `json:"complete" doc:"true: changes lists every affected user (the caller is the instance owner); otherwise the caller's own change only."`
	Changes        []MigrationAccessChange `json:"changes"`
	OthersAffected int                     `json:"othersAffected"`
	Unavailable    string                  `json:"unavailable,omitempty"`
}

// MigrationExclusion is a DockYard resource left out (#32).
type MigrationExclusion struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// MigrationPreview is a migration's preflight check, computed before
// anything stops.
type MigrationPreview struct {
	Kind                string                 `json:"kind" enum:"stack,volume"`
	StackID             string                 `json:"stackId,omitempty"`
	SourceEnvironmentID string                 `json:"sourceEnvironmentId"`
	TargetEnvironmentID string                 `json:"targetEnvironmentId"`
	ProjectName         string                 `json:"projectName,omitempty" example:"web"`
	TargetDirectory     string                 `json:"targetDirectory,omitempty" doc:"The new project directory in the destination's stacks volume."`
	Allowed             bool                   `json:"allowed" doc:"No blockers: the migration can start (warnings are accepted by starting it)."`
	Blockers            []MigrationFinding     `json:"blockers"`
	Warnings            []MigrationFinding     `json:"warnings"`
	Services            []MigrationServicePlan `json:"services"`
	Volumes             []MigrationVolumePlan  `json:"volumes"`
	Data                MigrationData          `json:"data"`
	Downtime            MigrationDowntime      `json:"downtime"`
	Transport           MigrationTransport     `json:"transport"`
	Access              MigrationAccess        `json:"access"`
	Leftovers           []string               `json:"leftovers" doc:"Earlier unsuccessful migrations whose partial data on the destination is removed first."`
	Excluded            []MigrationExclusion   `json:"excluded"`
}

func findings(in []migrations.Finding) []MigrationFinding {
	out := make([]MigrationFinding, 0, len(in))
	for _, f := range in {
		out = append(out, MigrationFinding(f))
	}
	return out
}

// newMigrationPreview converts a plan; bind sources (from the Compose
// definition) are shown only with stack.definition.read (#7).
func newMigrationPreview(p migrations.Plan, definition bool) MigrationPreview {
	if !definition {
		p.Warnings = slices.Clone(p.Warnings)
		for i := range p.Warnings {
			if p.Warnings[i].Code == migrations.FindingExternalBind {
				p.Warnings[i].Resource = ""
			}
		}
	}
	out := MigrationPreview{Kind: string(p.Kind), StackID: p.StackID, SourceEnvironmentID: p.SourceEnvironmentID,
		TargetEnvironmentID: p.TargetEnvironmentID, ProjectName: p.ProjectName, TargetDirectory: p.TargetDir, Allowed: p.Allowed(),
		Blockers: findings(p.Blockers), Warnings: findings(p.Warnings), Services: []MigrationServicePlan{}, Volumes: []MigrationVolumePlan{},
		Data: MigrationData{ProjectBytes: p.Data.ProjectBytes, VolumeBytes: p.Data.VolumeBytes, ImageBytes: p.Data.ImageBytes,
			TotalBytes: p.Data.TotalBytes, Truncated: p.Data.Truncated, DestinationStacksFree: p.Data.TargetStacksFree,
			DestinationVolumesFree: p.Data.TargetVolumesFree},
		Downtime: MigrationDowntime{EstimatedSeconds: p.Downtime.EstimatedSeconds, Basis: p.Downtime.Basis},
		Transport: MigrationTransport{SourcePlainHTTP: p.Transport.SourcePlainHTTP, DestinationPlainHTTP: p.Transport.DestinationPlainHTTP,
			BandwidthLimitBytesPerSecond: p.Transport.BandwidthLimit},
		Access:    MigrationAccess{Complete: p.Access.Complete, Changes: []MigrationAccessChange{}, OthersAffected: p.Access.OthersAffected, Unavailable: p.Access.Unavailable},
		Leftovers: append([]string{}, p.Leftovers...), Excluded: []MigrationExclusion{}}
	for _, s := range p.Services {
		out.Services = append(out.Services, MigrationServicePlan(s))
	}
	for _, v := range p.Volumes {
		out.Volumes = append(out.Volumes, MigrationVolumePlan{Source: v.Source, Target: v.Target, Key: v.Key, Action: v.Action, Anonymous: v.Anonymous,
			Bytes: v.Bytes, Entries: v.Entries, Truncated: v.Truncated, Reason: v.Reason})
	}
	for _, ch := range p.Access.Changes {
		out.Access.Changes = append(out.Access.Changes, MigrationAccessChange{UserID: ch.UserID, Username: ch.Username,
			Gained: append([]string{}, ch.Gained...), Lost: append([]string{}, ch.Lost...)})
	}
	for _, e := range p.Excluded {
		out.Excluded = append(out.Excluded, MigrationExclusion(e))
	}
	return out
}

// StackMigrationBody selects what a stack migration moves.
type StackMigrationBody struct {
	TargetEnvironmentID string   `json:"targetEnvironmentId,omitempty" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f" maxLength:"64" doc:"Required: the destination environment."`
	ExcludeVolumes      []string `json:"excludeVolumes,omitempty" maxItems:"64" doc:"Named volumes whose data is not copied (Compose recreates them empty on the destination). Default: every named local volume is copied."`
	AnonymousVolumes    []string `json:"anonymousVolumes,omitempty" maxItems:"64" doc:"Anonymous volumes to copy (skipped by default)."`
	TransferImages      []string `json:"transferImages,omitempty" maxItems:"64" doc:"Images to copy through the manager instead of pulling or rebuilding them on the destination (locally built images are copied anyway)."`
	TimeoutSeconds      int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period of the source's containers."`
}

func (b StackMigrationBody) request(key string) migrations.StackRequest {
	return migrations.StackRequest{TargetEnvironmentID: b.TargetEnvironmentID, TimeoutSeconds: b.TimeoutSeconds, IdempotencyKey: key,
		Selection: migrations.Selection{ExcludeVolumes: b.ExcludeVolumes, AnonymousVolumes: b.AnonymousVolumes, TransferImages: b.TransferImages}}
}

type stackMigrationPreviewInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	Body    StackMigrationBody
}

type stackMigrationInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	Body StackMigrationBody
}

type sourceRemovalInput struct {
	StackID     string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	MigrationID string `path:"migrationId" maxLength:"64" doc:"Migration ID (the stack.migrate job's ID)."`
	IdempotencyKeyParam
}

// VolumeMigrationBody selects a volume migration's destination.
type VolumeMigrationBody struct {
	TargetEnvironmentID         string `json:"targetEnvironmentId,omitempty" maxLength:"64" doc:"Required: the destination environment."`
	TargetName                  string `json:"targetName,omitempty" example:"web_data" maxLength:"128" pattern:"^[a-zA-Z0-9][a-zA-Z0-9_.-]*$" doc:"A new name on the destination (default: the same name)."`
	AcknowledgeCrashConsistency bool   `json:"acknowledgeCrashConsistency,omitempty" doc:"Copy the volume although running containers use it (a crash-consistent copy). Otherwise such containers block the migration."`
}

func (b VolumeMigrationBody) request(key string) migrations.VolumeRequest {
	return migrations.VolumeRequest{TargetEnvironmentID: b.TargetEnvironmentID, TargetName: b.TargetName,
		AcknowledgeCrashConsistency: b.AcknowledgeCrashConsistency, IdempotencyKey: key}
}

type volumeMigrationPreviewInput struct {
	VolumePath
	Body VolumeMigrationBody
}

type volumeMigrationInput struct {
	VolumePath
	IdempotencyKeyParam
	Body VolumeMigrationBody
}

type migrationPreviewOutput struct{ Body MigrationPreview }

type migrationsAPI struct {
	svc    MigrationService
	stacks *stacksAPI
	docker *dockerAPI
	authz  authz.Authorizer
}

func (h *migrationsAPI) available() error {
	if h.svc == nil {
		return Unavailable(CodeUnavailable, "the migration service is not available")
	}
	return nil
}

// destination requires the destination-side capabilities (404 when the
// destination is invisible, 403 when visible but not granted).
func destination(c authz.Checker, env string, checks []migrations.Check) error {
	for _, ch := range checks {
		if !c.Can(ch.Capability, ch.Resource).Allowed {
			if authz.ViewOf(c, authz.EnvironmentResource(env)).Visible() {
				return Forbidden("not permitted on the destination environment: " + ch.Capability)
			}
			return NotFound("environment not found")
		}
	}
	return nil
}

func owner(c authz.Checker) bool { return c.Can("groups.manage", authz.Instance()).Allowed }

// requireTarget validates the destination after authorization (the answer
// must not reveal more to callers without the capability).
func requireTarget(env string) error {
	if env == "" {
		return Invalid("targetEnvironmentId is required", Field("body.targetEnvironmentId", "the destination environment"))
	}
	return nil
}

func migrationErr(err error) error {
	var be *migrations.BlockedError
	var ae *migrations.AgentError
	switch {
	case errors.As(err, &be):
		var details []ErrorDetail
		for _, b := range be.Plan.Blockers {
			details = append(details, ErrorDetail{Field: "preflight." + b.Code, Message: b.Message})
		}
		return NewError(http.StatusConflict, CodeMigrationBlocked, "the preflight check has blockers; preview the migration for details", details...)
	case errors.Is(err, migrations.ErrNotCompleted):
		return Conflict(CodeMigrationNotCompleted, "only a completed stack migration's source can be removed")
	case errors.Is(err, migrations.ErrSourceRemoved):
		return Conflict(CodeMigrationSourceRemoved, "the migration's source was already removed")
	case errors.Is(err, migrations.ErrSourceInUse):
		return Conflict(CodeMigrationSourceInUse, "a DockYard stack on the source environment manages the source project again; its files are not removed")
	case errors.Is(err, domain.ErrMigrationNotFound):
		return NotFound("migration not found")
	case errors.As(err, &ae):
		switch {
		case ae.Offline:
			return NewError(http.StatusServiceUnavailable, CodeEnvironmentOffline, "the "+ae.Side+" environment's agent is offline").WithRetryable(true)
		case ae.Timeout:
			return NewError(http.StatusGatewayTimeout, CodeTimeout, "the "+ae.Side+" environment's agent did not answer in time")
		}
		return NewError(http.StatusBadGateway, CodeEngineError, ae.Error())
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	}
	return stackErr(err)
}

func (h *migrationsAPI) previewStack(ctx context.Context, in *stackMigrationPreviewInput) (*migrationPreviewOutput, error) {
	c, p, st, v, err := h.stacks.requireStack(ctx, in.StackID, CapStackMigrate)
	if err != nil {
		return nil, err
	}
	if err := requireTarget(in.Body.TargetEnvironmentID); err != nil {
		return nil, err
	}
	if err := destination(c, in.Body.TargetEnvironmentID, migrations.DestinationStackCapabilities(st.ID, in.Body.TargetEnvironmentID)); err != nil {
		return nil, err
	}
	if err := h.available(); err != nil {
		return nil, err
	}
	plan, err := h.svc.PreviewStack(ctx, p, owner(c), st, in.Body.request(""))
	if err != nil {
		return nil, migrationErr(err)
	}
	return &migrationPreviewOutput{Body: newMigrationPreview(plan, v.Has(string(CapStackDefinitionRead)))}, nil
}

func (h *migrationsAPI) migrateStack(ctx context.Context, in *stackMigrationInput) (*JobAccepted, error) {
	c, p, st, _, err := h.stacks.requireStack(ctx, in.StackID, CapStackMigrate)
	if err != nil {
		return nil, err
	}
	if err := requireTarget(in.Body.TargetEnvironmentID); err != nil {
		return nil, err
	}
	if err := destination(c, in.Body.TargetEnvironmentID, migrations.DestinationStackCapabilities(st.ID, in.Body.TargetEnvironmentID)); err != nil {
		return nil, err
	}
	if err := h.available(); err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "sourceEnvironmentId", st.EnvironmentID)
	audit.SetDetail(ctx, "targetEnvironmentId", in.Body.TargetEnvironmentID)
	j, m, err := h.svc.StartStack(ctx, p, st, in.Body.request(in.IdempotencyKey))
	if err != nil {
		return nil, migrationErr(err)
	}
	audit.SetDetail(ctx, "migrationId", m.ID)
	audit.SetDetail(ctx, "volumes", len(m.Volumes))
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeEnvironment, ID: in.Body.TargetEnvironmentID})
	return Accepted(j), nil
}

func (h *migrationsAPI) removeSource(ctx context.Context, in *sourceRemovalInput) (*JobAccepted, error) {
	_, p, st, _, err := h.stacks.requireStack(ctx, in.StackID, CapStackMigrate)
	if err != nil {
		return nil, err
	}
	if err := h.available(); err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "migrationId", in.MigrationID)
	j, err := h.svc.RemoveSource(ctx, p, st.ID, in.MigrationID, in.IdempotencyKey)
	if err != nil {
		return nil, migrationErr(err)
	}
	return Accepted(j), nil
}

// volume requires volume.migrate on a visible volume and the destination's
// volume.create.
func (h *migrationsAPI) volume(ctx context.Context, path VolumePath, target string) (*scope, string, error) {
	sc, err := h.docker.environment(ctx, path.EnvironmentID, true)
	if err != nil {
		return nil, "", err
	}
	vol, v, err := h.docker.visibleVolume(ctx, sc, path.VolumeID)
	if err != nil {
		return nil, "", err
	}
	if !v.Has(string(CapVolumeMigrate)) {
		return nil, "", Forbidden("not permitted: " + string(CapVolumeMigrate))
	}
	if err := requireTarget(target); err != nil {
		return nil, "", err
	}
	if err := destination(sc.c, target, migrations.DestinationVolumeCapabilities(target)); err != nil {
		return nil, "", err
	}
	if err := h.available(); err != nil {
		return nil, "", err
	}
	return sc, vol.Name, nil
}

func (h *migrationsAPI) previewVolume(ctx context.Context, in *volumeMigrationPreviewInput) (*migrationPreviewOutput, error) {
	sc, name, err := h.volume(ctx, in.VolumePath, in.Body.TargetEnvironmentID)
	if err != nil {
		return nil, err
	}
	plan, err := h.svc.PreviewVolume(ctx, sc.p, owner(sc.c), sc.env.ID, name, in.Body.request(""))
	if err != nil {
		return nil, migrationErr(err)
	}
	return &migrationPreviewOutput{Body: newMigrationPreview(plan, false)}, nil
}

func (h *migrationsAPI) migrateVolume(ctx context.Context, in *volumeMigrationInput) (*JobAccepted, error) {
	sc, name, err := h.volume(ctx, in.VolumePath, in.Body.TargetEnvironmentID)
	if err != nil {
		return nil, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeVolume, ID: name, EnvironmentID: sc.env.ID})
	audit.SetDetail(ctx, "targetEnvironmentId", in.Body.TargetEnvironmentID)
	j, m, err := h.svc.StartVolume(ctx, sc.p, sc.env.ID, name, in.Body.request(in.IdempotencyKey))
	if err != nil {
		return nil, migrationErr(err)
	}
	audit.SetDetail(ctx, "migrationId", m.ID)
	return Accepted(j), nil
}

func registerMigrations(a huma.API, deps Deps) {
	h := &migrationsAPI{svc: deps.Migrations, authz: authz.OrDenyAll(deps.Authorizer), docker: newDockerAPI(deps),
		stacks: &stacksAPI{svc: deps.Stacks, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}}
	previewErrs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	one := BasePath + "/stacks/{stackId}"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-migration-preview", Method: http.MethodPost, Path: one + "/migration-previews",
		Summary: "Preview a stack migration",
		Description: "Computes the preflight check of moving the stack to another environment before anything stops: platform and " +
			"pullability of each image on the destination (registry connections, rebuilds, locally built images copied through the " +
			"manager), name conflicts (stack, Compose project, containers, volumes, networks, project directory), host port conflicts, " +
			"missing external networks and volumes, device mappings and bind paths outside the project directory (not migrated), " +
			"non-local volume drivers (definition only), data size against the destination's free space, expected downtime, plain-HTTP " +
			"transport and the effective-permission changes (stack-scoped rules follow the stack; environment rules do not). Needs " +
			"stack.migrate on the stack plus stack.create and stack.deploy on the destination. Changes nothing.",
		Tags: []string{tagStacks}, Errors: previewErrs,
	}, Capability: CapStackMigrate, Scope: ScopeResource, AuditAction: "stack.migrate.preview"}, h.previewStack)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-migration", Method: http.MethodPost, Path: one + "/migrations", Summary: "Migrate a stack",
		Description: "Re-runs the preview (409 migration_blocked with the blockers in details) and starts a stack.migrate job (202; " +
			"the migration ID is the job ID). Cold migration: the source stops in reverse dependency order, the project directory " +
			"(with its relative bind directories) and the selected named volumes are copied through the manager (checksummed per chunk " +
			"and as a whole), the stack moves to the destination (same ID, revisions and stack-scoped rules) and is deployed there in " +
			"dependency order. The source stays stopped and untouched until its removal is confirmed; a migration that stops before " +
			"completing puts the stack back and restarts the source's services.",
		Tags: []string{tagStacks}, Errors: previewErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackMigrate, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.migrateStack)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-migration-source-removal", Method: http.MethodPost, Path: one + "/migrations/{migrationId}/source-removals",
		Summary: "Remove a migrated stack's source",
		Description: "Confirms a completed migration: starts a stack.remove_source job (202) on the source environment that removes the " +
			"source project's containers and networks, the migrated volumes and the project directory there. 409 " +
			"migration_not_completed before the migration completed, migration_source_removed after a removal, " +
			"migration_source_in_use when a DockYard stack manages the source project again. Backup snapshots of the source " +
			"stay in their repository.",
		Tags: []string{tagStacks}, Errors: previewErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackMigrate, Scope: ScopeResource, Idempotency: IdempotencyJob, AuditAction: "stack.migrate.remove_source"}, h.removeSource)

	vol := BasePath + "/environments/{environmentId}/volumes/{volumeId}"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-volume-migration-preview", Method: http.MethodPost, Path: vol + "/migration-previews",
		Summary: "Preview a volume migration",
		Description: "The preflight check of copying the volume to another environment (optionally under a new name): support " +
			"(local volumes only; DockYard's own volumes are refused), running containers using it (blocking unless a " +
			"crash-consistent copy is acknowledged), name conflicts, size against free space, transport and access changes. Needs " +
			"volume.migrate on the volume and volume.create on the destination. Changes nothing.",
		Tags: []string{tagVolumes}, Errors: previewErrs,
	}, Capability: CapVolumeMigrate, Scope: ScopeResource, AuditAction: "volume.migrate.preview"}, h.previewVolume)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-volume-migration", Method: http.MethodPost, Path: vol + "/migrations", Summary: "Migrate a volume",
		Description: "Re-runs the preview (409 migration_blocked) and starts a volume.migrate job (202) that copies the volume's data " +
			"with owners, modes, times, symlinks and hard links preserved into a new volume on the destination. The source volume " +
			"is kept.",
		Tags: []string{tagVolumes}, Errors: previewErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapVolumeMigrate, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.migrateVolume)
}
