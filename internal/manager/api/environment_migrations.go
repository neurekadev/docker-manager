package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
)

// Environment migrations (#35): every stack of an environment moves to
// another environment, group by group, each as its own stack migration.

// EnvironmentMigrationService previews, starts and lists environment
// migrations (*migrations.Service).
type EnvironmentMigrationService interface {
	PreviewEnvironment(ctx context.Context, p authz.Principal, owner bool, source string, r migrations.EnvironmentRequest) (migrations.EnvironmentPlan, error)
	StartEnvironment(ctx context.Context, p authz.Principal, source string, r migrations.EnvironmentRequest) (domain.Job, domain.EnvironmentMigration, error)
	GetEnvironmentMigration(ctx context.Context, id string) (domain.EnvironmentMigration, error)
	EnvironmentMigrations(ctx context.Context, source string, limit int) ([]domain.EnvironmentMigration, error)
}

// EnvironmentMigrationBody selects an environment migration's destination
// and stacks.
type EnvironmentMigrationBody struct {
	TargetEnvironmentID string   `json:"targetEnvironmentId,omitempty" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f" maxLength:"64" doc:"Required: the destination environment."`
	Stacks              []string `json:"stacks,omitempty" doc:"Only these stacks (IDs; any number). Default: every stack of the environment the caller may migrate."`
	TimeoutSeconds      int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period of the source's containers."`
}

func (b EnvironmentMigrationBody) request(key string) migrations.EnvironmentRequest {
	return migrations.EnvironmentRequest{TargetEnvironmentID: b.TargetEnvironmentID, Stacks: b.Stacks, TimeoutSeconds: b.TimeoutSeconds,
		IdempotencyKey: key}
}

// EnvironmentMigrationStackPreview is one stack's place and preview.
type EnvironmentMigrationStackPreview struct {
	StackID   string           `json:"stackId"`
	Name      string           `json:"name"`
	Group     int              `json:"group" doc:"Index of its group in groups."`
	DependsOn []string         `json:"dependsOn" doc:"Stacks of its group that move before it: it joins a network or volume they create."`
	Preview   MigrationPreview `json:"preview" doc:"The stack's own preview; external networks and volumes that move before it are no longer missing."`
}

// EnvironmentMigrationNetwork is a network created on the destination
// before any stack moves.
type EnvironmentMigrationNetwork struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver,omitempty"`
	Internal   bool     `json:"internal,omitempty"`
	Attachable bool     `json:"attachable,omitempty"`
	UsedBy     []string `json:"usedBy" doc:"The stacks that join it."`
}

// EnvironmentMigrationSkipped is a stack of the environment that does not
// move.
type EnvironmentMigrationSkipped struct {
	StackID string `json:"stackId"`
	Name    string `json:"name"`
	Reason  string `json:"reason" enum:"docker_manager,not_permitted,not_selected" doc:"docker_manager: Docker Manager's own stack (it moves with the manager); not_permitted: the caller may not migrate it to the destination; not_selected: the request names other stacks."`
}

// EnvironmentMigrationPreview is an environment migration's preflight
// check, computed before anything stops.
type EnvironmentMigrationPreview struct {
	SourceEnvironmentID string                             `json:"sourceEnvironmentId"`
	TargetEnvironmentID string                             `json:"targetEnvironmentId"`
	Allowed             bool                               `json:"allowed" doc:"Neither the migration nor any stack has blockers: it can start (warnings are accepted by starting it)."`
	Blockers            []MigrationFinding                 `json:"blockers" doc:"Blockers of the whole migration (each stack's preview has its own)."`
	Warnings            []MigrationFinding                 `json:"warnings"`
	Stacks              []EnvironmentMigrationStackPreview `json:"stacks" doc:"The stacks that move, in the order they move."`
	Groups              [][]string                         `json:"groups" doc:"Stack IDs per group, in order: a group stops together, then its stacks move one after the other."`
	Networks            []EnvironmentMigrationNetwork      `json:"networks"`
	Skipped             []EnvironmentMigrationSkipped      `json:"skipped"`
	Data                MigrationData                      `json:"data" doc:"The stacks' transfers together."`
	Downtime            MigrationDowntime                  `json:"downtime" doc:"estimatedSeconds: the longest a group is down."`
}

func newEnvironmentMigrationPreview(p migrations.EnvironmentPlan, definition func(stackID string) bool) EnvironmentMigrationPreview {
	out := EnvironmentMigrationPreview{SourceEnvironmentID: p.SourceEnvironmentID, TargetEnvironmentID: p.TargetEnvironmentID,
		Allowed: p.Allowed(), Blockers: findings(p.Blockers), Warnings: findings(p.Warnings), Stacks: []EnvironmentMigrationStackPreview{},
		Groups: [][]string{}, Networks: []EnvironmentMigrationNetwork{}, Skipped: []EnvironmentMigrationSkipped{},
		Data: MigrationData{ProjectBytes: p.Data.ProjectBytes, VolumeBytes: p.Data.VolumeBytes, ImageBytes: p.Data.ImageBytes,
			TotalBytes: p.Data.TotalBytes, Truncated: p.Data.Truncated, DestinationStacksFree: p.Data.TargetStacksFree,
			DestinationVolumesFree: p.Data.TargetVolumesFree},
		Downtime: MigrationDowntime{EstimatedSeconds: p.Downtime.EstimatedSeconds, Basis: p.Downtime.Basis}}
	for _, s := range p.Stacks {
		out.Stacks = append(out.Stacks, EnvironmentMigrationStackPreview{StackID: s.StackID, Name: s.Name, Group: s.Group,
			DependsOn: append([]string{}, s.DependsOn...), Preview: newMigrationPreview(s.Plan, definition(s.StackID))})
	}
	for _, g := range p.Groups {
		out.Groups = append(out.Groups, append([]string{}, g...))
	}
	for _, n := range p.Networks {
		out.Networks = append(out.Networks, EnvironmentMigrationNetwork{Name: n.Name, Driver: n.Driver, Internal: n.Internal,
			Attachable: n.Attachable, UsedBy: append([]string{}, n.UsedBy...)})
	}
	for _, s := range p.Skipped {
		out.Skipped = append(out.Skipped, EnvironmentMigrationSkipped(s))
	}
	return out
}

// EnvironmentMigrationStack is one stack of an environment migration.
type EnvironmentMigrationStack struct {
	StackID     string `json:"stackId"`
	Name        string `json:"name"`
	MigrationID string `json:"migrationId,omitempty" doc:"Its stack migration (the stack.migrate job), once started."`
	State       string `json:"state" enum:"pending,moving,moved,failed" doc:"moved: its migration completed (its source stays stopped until its removal is confirmed); failed: it did not, the stack is back on the source."`
	// SourceRemoved: the stopped copy on the source was removed.
	SourceRemoved bool `json:"sourceRemoved,omitempty" doc:"The moved stack's stopped copy on the source was removed."`
}

// EnvironmentMigration is an environment migration's record.
type EnvironmentMigration struct {
	ID                  string                      `json:"id" example:"0192f5e4-9c1d-7a2b-8e3f-4a5b6c7d8e9f" doc:"The environment.migrate job's ID."`
	SourceEnvironmentID string                      `json:"sourceEnvironmentId"`
	TargetEnvironmentID string                      `json:"targetEnvironmentId"`
	State               string                      `json:"state" enum:"running,completed,failed,cancelled,interrupted"`
	Groups              [][]string                  `json:"groups"`
	Stacks              []EnvironmentMigrationStack `json:"stacks" doc:"The stacks the caller can see."`
	Networks            []string                    `json:"networks" doc:"Networks created on the destination first."`
	CreatedAt           time.Time                   `json:"createdAt"`
	UpdatedAt           time.Time                   `json:"updatedAt"`
	FinishedAt          *time.Time                  `json:"finishedAt,omitempty"`
}

func newEnvironmentMigration(m domain.EnvironmentMigration, visible func(stackID string) bool) EnvironmentMigration {
	out := EnvironmentMigration{ID: m.ID, SourceEnvironmentID: m.SourceEnvironmentID, TargetEnvironmentID: m.TargetEnvironmentID,
		State: string(m.State), Groups: [][]string{}, Stacks: []EnvironmentMigrationStack{}, Networks: []string{},
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, FinishedAt: m.FinishedAt}
	for _, n := range m.Networks {
		out.Networks = append(out.Networks, n.Name)
	}
	for _, g := range m.Groups {
		var ids []string
		for _, id := range g {
			if visible(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			out.Groups = append(out.Groups, ids)
		}
	}
	for _, s := range m.Stacks {
		if visible(s.StackID) {
			out.Stacks = append(out.Stacks, EnvironmentMigrationStack{StackID: s.StackID, Name: s.Name, MigrationID: s.MigrationID, State: string(s.State),
				SourceRemoved: s.SourceRemoved})
		}
	}
	return out
}

type environmentMigrationPreviewInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"The source environment."`
	Body          EnvironmentMigrationBody
}

type environmentMigrationInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"The source environment."`
	IdempotencyKeyParam
	Body EnvironmentMigrationBody
}

type environmentMigrationsInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"The source environment."`
}

type environmentMigrationGetInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"The source environment."`
	MigrationID   string `path:"migrationId" maxLength:"64" doc:"Environment migration ID (the environment.migrate job's ID)."`
}

type environmentMigrationPreviewOutput struct{ Body EnvironmentMigrationPreview }

type environmentMigrationOutput struct{ Body EnvironmentMigration }

type environmentMigrationListOutput struct {
	Body struct {
		Items []EnvironmentMigration `json:"items" doc:"The latest migrations away from the environment, newest first (at most 20)."`
	}
}

type environmentMigrationsAPI struct {
	svc    EnvironmentMigrationService
	agents AgentService
	authz  authz.Authorizer
	stacks StackService
}

func (h *environmentMigrationsAPI) available() error {
	if h.svc == nil {
		return Unavailable(CodeUnavailable, "the migration service is not available")
	}
	return nil
}

// source requires a visible source environment and stack.migrate on one
// of its stacks (the answer never depends on whether the service runs),
// and, for previews and starts, a visible destination where the caller
// may create stacks. Reads (mutation false) skip the stack.migrate check:
// once every stack moved away the source has none left, so list and get
// decide per record (readable).
func (h *environmentMigrationsAPI) source(ctx context.Context, envID, target string, mutation bool) (*scope, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if h.agents == nil {
		return nil, Unavailable(CodeUnavailable, "the environment service is not available")
	}
	env, err := h.agents.GetEnvironment(ctx, envID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) || (err == nil && !authz.ViewOf(c, authz.EnvironmentResource(env.ID)).Visible()) {
		return nil, NotFound("environment not found")
	}
	if err != nil {
		return nil, Internal(err)
	}
	sc := &scope{c: c, p: p, env: env}
	if !mutation {
		return sc, nil
	}
	if !h.migratesAny(ctx, c, env.ID) {
		return nil, Forbidden("not permitted: " + string(CapStackMigrate))
	}
	if env.Status == domain.EnvironmentArchived {
		return nil, Conflict(CodeEnvironmentArchived, "the environment is archived")
	}
	if err := requireTarget(target); err != nil {
		return nil, err
	}
	if err := destination(sc.c, target, []migrations.Check{{Capability: "stack.create", Resource: authz.InEnvironment(catalog.TypeStack, target)}}); err != nil {
		return nil, err
	}
	return sc, h.available()
}

// readable reports whether the caller may read a record of the
// environment: stack.migrate on one of the environment's stacks
// (sourceAllowed), or on one of the record's stacks where it is now (after
// a migration that moved every stack the source has none left, while the
// moved stacks' old copies still wait there for their removal).
func (h *environmentMigrationsAPI) readable(ctx context.Context, c authz.Checker, sourceAllowed bool, m domain.EnvironmentMigration) bool {
	if sourceAllowed {
		return true
	}
	if h.stacks == nil {
		return false
	}
	for _, s := range m.Stacks {
		if st, err := h.stacks.Get(ctx, s.StackID); err == nil && c.Can(string(CapStackMigrate), stackResource(st)).Allowed {
			return true
		}
	}
	return false
}

// migratesAny reports whether the caller may migrate a stack of the
// environment.
func (h *environmentMigrationsAPI) migratesAny(ctx context.Context, c authz.Checker, env string) bool {
	if h.stacks == nil {
		return false
	}
	all, err := h.stacks.List(ctx, domain.StackFilter{EnvironmentID: env})
	if err != nil {
		return false
	}
	for _, st := range all {
		if c.Can(string(CapStackMigrate), stackResource(st)).Allowed {
			return true
		}
	}
	return false
}

// stackVisible reports whether the caller sees a stack where it is now.
func (h *environmentMigrationsAPI) stackVisible(ctx context.Context, c authz.Checker) func(string) bool {
	return func(id string) bool {
		if h.stacks == nil {
			return false
		}
		st, err := h.stacks.Get(ctx, id)
		return err == nil && authz.ViewOf(c, stackResource(st)).Visible()
	}
}

func (h *environmentMigrationsAPI) preview(ctx context.Context, in *environmentMigrationPreviewInput) (*environmentMigrationPreviewOutput, error) {
	sc, err := h.source(ctx, in.EnvironmentID, in.Body.TargetEnvironmentID, true)
	if err != nil {
		return nil, err
	}
	plan, err := h.svc.PreviewEnvironment(ctx, sc.p, owner(sc.c), sc.env.ID, in.Body.request(""))
	if err != nil {
		return nil, migrationErr(err)
	}
	definition := func(id string) bool {
		return authz.ViewOf(sc.c, authz.Resource{Type: catalog.TypeStack, ID: id, EnvironmentID: sc.env.ID, Parents: []authz.ResourceRef{}}).
			Has(string(CapStackDefinitionRead))
	}
	return &environmentMigrationPreviewOutput{Body: newEnvironmentMigrationPreview(plan, definition)}, nil
}

func (h *environmentMigrationsAPI) start(ctx context.Context, in *environmentMigrationInput) (*JobAccepted, error) {
	sc, err := h.source(ctx, in.EnvironmentID, in.Body.TargetEnvironmentID, true)
	if err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "sourceEnvironmentId", sc.env.ID)
	audit.SetDetail(ctx, "targetEnvironmentId", in.Body.TargetEnvironmentID)
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeEnvironment, ID: in.Body.TargetEnvironmentID})
	j, m, err := h.svc.StartEnvironment(ctx, sc.p, sc.env.ID, in.Body.request(in.IdempotencyKey))
	if err != nil {
		return nil, migrationErr(err)
	}
	audit.SetDetail(ctx, "migrationId", m.ID)
	audit.SetDetail(ctx, "stacks", len(m.Stacks))
	return Accepted(j), nil
}

func (h *environmentMigrationsAPI) list(ctx context.Context, in *environmentMigrationsInput) (*environmentMigrationListOutput, error) {
	sc, err := h.source(ctx, in.EnvironmentID, "", false)
	if err != nil {
		return nil, err
	}
	sourceAllowed := h.migratesAny(ctx, sc.c, sc.env.ID)
	if err := h.available(); err != nil {
		if !sourceAllowed {
			return nil, Forbidden("not permitted: " + string(CapStackMigrate))
		}
		return nil, err
	}
	ms, err := h.svc.EnvironmentMigrations(ctx, sc.env.ID, 20)
	if err != nil {
		return nil, Internal(err)
	}
	visible := h.stackVisible(ctx, sc.c)
	out := &environmentMigrationListOutput{}
	out.Body.Items = []EnvironmentMigration{}
	permitted := sourceAllowed
	for _, m := range ms {
		if !h.readable(ctx, sc.c, sourceAllowed, m) {
			continue
		}
		permitted = true
		if e := newEnvironmentMigration(m, visible); len(e.Stacks) > 0 {
			out.Body.Items = append(out.Body.Items, e)
		}
	}
	if !permitted {
		return nil, Forbidden("not permitted: " + string(CapStackMigrate))
	}
	return out, nil
}

func (h *environmentMigrationsAPI) get(ctx context.Context, in *environmentMigrationGetInput) (*environmentMigrationOutput, error) {
	sc, err := h.source(ctx, in.EnvironmentID, "", false)
	if err != nil {
		return nil, err
	}
	sourceAllowed := h.migratesAny(ctx, sc.c, sc.env.ID)
	if err := h.available(); err != nil {
		if !sourceAllowed {
			return nil, Forbidden("not permitted: " + string(CapStackMigrate))
		}
		return nil, err
	}
	m, err := h.svc.GetEnvironmentMigration(ctx, in.MigrationID)
	found := err == nil && m.SourceEnvironmentID == sc.env.ID
	if !found {
		if !sourceAllowed {
			return nil, Forbidden("not permitted: " + string(CapStackMigrate))
		}
		return nil, NotFound("migration not found")
	}
	if !h.readable(ctx, sc.c, sourceAllowed, m) {
		return nil, Forbidden("not permitted: " + string(CapStackMigrate))
	}
	e := newEnvironmentMigration(m, h.stackVisible(ctx, sc.c))
	if len(e.Stacks) == 0 {
		return nil, NotFound("migration not found")
	}
	return &environmentMigrationOutput{Body: e}, nil
}

func registerEnvironmentMigrations(a huma.API, deps Deps) {
	h := &environmentMigrationsAPI{svc: deps.EnvironmentMigrations, agents: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), stacks: deps.Stacks}
	previewErrs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	env := BasePath + "/environments/{environmentId}"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-environment-migration-preview", Method: http.MethodPost, Path: env + "/migration-previews",
		Summary: "Preview an environment migration",
		Description: "The preflight check of moving every stack of the environment (or the ones named) to another environment, before " +
			"anything stops: each stack's own preview, the order they move in (stacks linked by a network or volume one creates and " +
			"another joins as external form a group, the creating stack first), the networks made by hand on the source that are " +
			"created on the destination first, the stacks left out (Docker Manager's own, stacks the caller may not migrate) and the " +
			"data together against the destination's free space. Needs stack.migrate on each stack plus stack.create and stack.deploy " +
			"on the destination (network.create for networks it creates). Changes nothing.",
		Tags: []string{tagEnvironments}, Errors: previewErrs,
	}, Capability: CapStackMigrate, Scope: ScopeEnvironment, AuditAction: "environment.migrate.preview"}, h.preview)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-environment-migration", Method: http.MethodPost, Path: env + "/migrations", Summary: "Migrate an environment",
		Description: "Re-runs the preview (409 migration_blocked with the blockers in details) and starts an environment.migrate job " +
			"(202; the migration ID is the job ID). Networks made by hand are created on the destination first; then group by group, " +
			"the group's stacks stop together and move one after the other as stack migrations. When a stack does not move the job " +
			"stops: that stack is back on its source, the group's other stacks still there start again, and stacks moved before stay " +
			"on the destination; migrating again moves the rest. Moved stacks' sources stay stopped until their removal is confirmed.",
		Tags: []string{tagEnvironments}, Errors: previewErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackMigrate, Scope: ScopeEnvironment, Idempotency: IdempotencyJob, AuditAction: "environment.migrate"}, h.start)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-environment-migrations", Method: http.MethodGet, Path: env + "/migrations",
		Summary: "List environment migrations",
		Description: "The latest migrations away from the environment (at most 20, newest first), with each stack the caller can see and its state. " +
			"Readable with stack.migrate on a stack of the environment, or on one of a migration's stacks where it is now (after every stack moved away).",
		Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, Capability: CapStackMigrate, Scope: ScopeEnvironment}, h.list)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-environment-migration", Method: http.MethodGet, Path: env + "/migrations/{migrationId}",
		Summary: "Get an environment migration",
		Description: "An environment migration's groups, each stack the caller can see with its stack migration and state, and the networks created on the destination. " +
			"Readable with stack.migrate on a stack of the environment, or on one of its stacks where it is now.",
		Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, Capability: CapStackMigrate, Scope: ScopeEnvironment}, h.get)
}
