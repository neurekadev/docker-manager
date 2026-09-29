package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Environment removal preview (#34). Removing an environment archives it
// (DELETE /environments/{environmentId}); this preview lists everything
// that depends on it first and offers migrating its stacks and volumes
// (#35).

// RemovalService computes removal previews (*removal.Service).
type RemovalService interface {
	Preview(ctx context.Context, environmentID string) (domain.EnvironmentRemovalPreview, error)
}

// RemovalDependent is one dependent record.
type RemovalDependent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// RemovalDependentKind groups the dependent records of one kind.
type RemovalDependentKind struct {
	Kind string `json:"kind" enum:"stack,managed_container,update_policy,backup_policy,maintenance_policy,backup_repository,backup_set,registry_connection,build_definition,permission_rule,schedule,job"`
	// OnArchive is what archiving does to records of this kind.
	OnArchive string             `json:"onArchive" enum:"kept,paused,removed,interrupted" doc:"kept: kept and hidden with the environment, working again after a re-attach; paused: kept, scheduled runs are refused until a re-attach; removed: deleted with an audit record (permission rules scoped to the environment); interrupted: unfinished jobs end by the offline rules once the agent is disconnected."`
	Count     int                `json:"count" doc:"Records of this kind the caller can see (#17: records hidden from the caller are not counted)."`
	Items     []RemovalDependent `json:"items"`
}

// RemovalMigrationOffer points at moving the environment's stacks and
// volumes before removing it (#35).
type RemovalMigrationOffer struct {
	Stacks      int    `json:"stacks" doc:"Visible managed stacks that could be migrated first."`
	Description string `json:"description"`
}

// EnvironmentRemovalPreview is the response of the removal preview.
type EnvironmentRemovalPreview struct {
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName" example:"nas"`
	Status          string `json:"status" enum:"active,archived"`
	Revision        int64  `json:"revision" doc:"Send it as If-Match to DELETE /environments/{environmentId}."`
	Action          string `json:"action" enum:"archive" doc:"What removal does: archive (the only v1 action)."`
	Description     string `json:"description"`
	// HostUntouched is always true: removal never stops, removes or edits
	// anything on the host.
	HostUntouched   bool                   `json:"hostUntouched"`
	Dependents      []RemovalDependentKind `json:"dependents" doc:"Every dependent record kind, in a fixed order (count 0 when none)."`
	BackupSnapshots int                    `json:"backupSnapshots" doc:"Snapshot index entries of this host (kept; owner and backup readers only)."`
	Migration       RemovalMigrationOffer  `json:"migration"`
	Reattach        string                 `json:"reattach" doc:"How to bring the archived environment back."`
}

type removalPreviewOutput struct{ Body EnvironmentRemovalPreview }

// removalOnArchive is the fixed effect of archiving per kind.
var removalOnArchive = map[string]string{
	domain.DependentStack: domain.OnArchiveKept, domain.DependentManagedContainer: domain.OnArchiveKept,
	domain.DependentUpdatePolicy: domain.OnArchivePaused, domain.DependentBackupPolicy: domain.OnArchivePaused,
	domain.DependentMaintenancePolicy: domain.OnArchivePaused, domain.DependentBackupRepository: domain.OnArchiveKept,
	domain.DependentBackupSet: domain.OnArchiveKept, domain.DependentRegistryConnection: domain.OnArchiveKept,
	domain.DependentBuildDefinition: domain.OnArchiveKept, domain.DependentPermissionRule: domain.OnArchiveRemoved,
	domain.DependentSchedule: domain.OnArchivePaused, domain.DependentJob: domain.OnArchiveInterrupted,
}

// dependentVisible applies #17 shaping to one dependent record: records
// with a resource need at least a minimal view of it, jobs need job.read
// on the job, and permission rules and records without a resource are
// owner-only (the owner can read everything; rules are owner-only data).
func dependentVisible(c authz.Checker, envID string, d domain.EnvironmentDependent) bool {
	switch {
	case d.Job != nil:
		return c.Can("job.read", authz.JobResource(*d.Job)).Allowed
	case d.ResourceType == "" || d.ResourceID == "":
		return c.Can("groups.manage", authz.Instance()).Allowed
	}
	res := authz.Resource{Type: d.ResourceType, ID: d.ResourceID}
	if rt, ok := catalog.Default().Type(d.ResourceType); ok && rt.EnvironmentBound {
		res.EnvironmentID = envID
	}
	return authz.ViewOf(c, res).Visible()
}

func (h *agentsAPI) previewRemoval(ctx context.Context, in *environmentIDInput) (*removalPreviewOutput, error) {
	c, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapEnvironmentRemove)) {
		return nil, Forbidden("not permitted to remove this environment")
	}
	if h.deps.Removal == nil {
		return nil, Unavailable(CodeUnavailable, "environment removal previews are not available")
	}
	p, err := h.deps.Removal.Preview(ctx, env.ID)
	if err != nil {
		return nil, agentErr(err)
	}
	kinds := map[string]*RemovalDependentKind{}
	out := EnvironmentRemovalPreview{EnvironmentID: env.ID, EnvironmentName: env.Name, Status: string(env.Status), Revision: env.Revision,
		Action: "archive", HostUntouched: true,
		Description: "Removing archives the environment: it is hidden from operations and its agent's credential is revoked; " +
			"stacks, policies, history and backups are kept, permission rules scoped to it are removed (audited). Nothing on the " +
			"host is stopped, removed or edited.",
		Reattach: "Enroll an agent for the same Docker Engine with an enrollment of intent reattach:" + env.ID +
			" (POST /api/v1/agent-enrollments); stacks and policies resume once it is online."}
	all := domain.DependentKinds()
	out.Dependents = make([]RemovalDependentKind, len(all)) // fixed length: kinds points into it
	for i, k := range all {
		out.Dependents[i] = RemovalDependentKind{Kind: k, OnArchive: removalOnArchive[k], Items: []RemovalDependent{}}
		kinds[k] = &out.Dependents[i]
	}
	for _, d := range p.Dependents {
		k, ok := kinds[d.Kind]
		if !ok || !dependentVisible(c, env.ID, d) {
			continue
		}
		k.Items = append(k.Items, RemovalDependent{ID: d.ID, Name: d.Name, Detail: d.Detail})
		k.Count++
	}
	if kinds[domain.DependentBackupSet].Count > 0 || c.Can("groups.manage", authz.Instance()).Allowed {
		out.BackupSnapshots = p.BackupSnapshots
	}
	out.Migration = RemovalMigrationOffer{Stacks: kinds[domain.DependentStack].Count,
		Description: "Move stacks and volumes to another environment first to keep operating them: every stack at once with POST " +
			"/api/v1/environments/{environmentId}/migration-previews then /migrations, one stack with POST /api/v1/stacks/{stackId}/migration-previews " +
			"then /migrations, and POST /api/v1/environments/{environmentId}/volumes/{volumeId}/migration-previews then /migrations (#35)."}
	return &removalPreviewOutput{Body: out}, nil
}

func registerRemoval(a huma.API, deps Deps) {
	h := &agentsAPI{svc: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-environment-removal-preview", Method: http.MethodPost,
			Path:    BasePath + "/environments/{environmentId}/removal-previews",
			Summary: "Preview removing an environment",
			Description: "Lists every record that depends on the environment before it is removed (archived with DELETE " +
				"/environments/{environmentId}): managed stacks and standalone container specifications, update, backup and " +
				"maintenance policies, backup repositories and sets holding its data, registry and Git bindings, permission rules " +
				"scoped to it, schedules and unfinished jobs, each with what archiving does to it, and offers migrating its stacks and " +
				"volumes first (#35). Items the caller cannot see are left out (#17); permission rules are listed to the owner only. " +
				"Changes nothing.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
		},
		Capability: CapEnvironmentRemove, Scope: ScopeEnvironment, AuditAction: "environment.remove.preview",
	}, h.previewRemoval)
}
