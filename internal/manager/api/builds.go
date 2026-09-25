package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
)

// Image builds (#33): manual builds from a Git URL, build records and
// saved build definitions. The flows live in internal/manager/builds.

const tagBuilds = "Image builds"

// Capability keys of the build routes.
const (
	CapImageBuild            Capability = "image.build"
	CapBuildDefinitionRead   Capability = "build_definition.read"
	CapBuildDefinitionManage Capability = "build_definition.manage"
)

// BuildService is the build service as seen by the API (implemented by
// *builds.Service).
type BuildService interface {
	Start(ctx context.Context, p authz.Principal, envID string, src domain.BuildSource, definitionID, idempotencyKey string) (domain.ImageBuild, domain.Job, error)
	Get(ctx context.Context, id string) (domain.ImageBuild, error)
	List(ctx context.Context, envID, definitionID, beforeID string, limit int) ([]domain.ImageBuild, error)
	CreateDefinition(ctx context.Context, envID, name, description string, src domain.BuildSource) (domain.BuildDefinition, error)
	GetDefinition(ctx context.Context, id string) (domain.BuildDefinition, error)
	ListDefinitions(ctx context.Context, envID, afterID string, limit int) ([]domain.BuildDefinition, error)
	UpdateDefinition(ctx context.Context, id string, revision int64, p domain.BuildDefinitionPatch) (domain.BuildDefinition, error)
	DeleteDefinition(ctx context.Context, id string, revision int64) error
	RunDefinition(ctx context.Context, p authz.Principal, id, idempotencyKey string) (domain.ImageBuild, domain.Job, error)
}

// BuildSource is what to build: an HTTP(S) Git repository at a ref.
// Credentials are never part of it: private repositories use a Git
// credential and private base images registry connections (by ID).
type BuildSource struct {
	GitURL      string `json:"gitUrl" minLength:"1" maxLength:"2048" example:"https://github.com/acme/app.git" doc:"http(s) repository URL without credentials (SSH is not supported in v1)."`
	Ref         string `json:"ref,omitempty" maxLength:"255" example:"main" doc:"Branch, tag, full ref or commit (default: the repository's HEAD). Resolved to a commit before the build; exactly that commit is built and recorded."`
	ContextPath string `json:"contextPath,omitempty" maxLength:"1024" example:"services/api" doc:"Build context directory inside the repository."`
	Dockerfile  string `json:"dockerfile,omitempty" maxLength:"255" example:"Dockerfile" doc:"Relative to the context (default Dockerfile)."`
	Target      string `json:"target,omitempty" maxLength:"128" doc:"Build stage."`
	// BuildArgs are visible in the image history; they are never written to the audit trail.
	BuildArgs       map[string]string `json:"buildArgs,omitempty" doc:"Build arguments. Warning: values end up in the image history; never pass secrets. Never recorded in the audit trail."`
	Tags            []string          `json:"tags" minItems:"1" maxItems:"16" example:"[\"registry.example.com/acme/app:1.4.2\"]" doc:"Image names to produce (name:tag)."`
	Platform        string            `json:"platform,omitempty" maxLength:"64" example:"linux/amd64" doc:"Default: the host platform."`
	NoCache         bool              `json:"noCache,omitempty"`
	Pull            bool              `json:"pull,omitempty" doc:"Always pull newer base images."`
	GitCredentialID string            `json:"gitCredentialId,omitempty" maxLength:"64" doc:"Git credential to use (default: the matching one; ambiguous matches need this)."`
	RegistryIDs     []string          `json:"registryIds,omitempty" maxItems:"16" doc:"Registry connections for private base images (default: every host-wide connection that applies in the environment)."`
	TimeoutSeconds  int               `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"21600" doc:"Build timeout (default 3600)."`
}

func (b BuildSource) domain() domain.BuildSource {
	return domain.BuildSource{GitURL: b.GitURL, Ref: b.Ref, ContextPath: b.ContextPath, Dockerfile: b.Dockerfile, Target: b.Target,
		BuildArgs: b.BuildArgs, Tags: append([]string{}, b.Tags...), Platform: b.Platform, NoCache: b.NoCache, Pull: b.Pull,
		GitCredentialID: b.GitCredentialID, RegistryConnectionIDs: b.RegistryIDs, TimeoutSeconds: b.TimeoutSeconds}
}

func newBuildSource(s domain.BuildSource) BuildSource {
	return BuildSource{GitURL: s.GitURL, Ref: s.Ref, ContextPath: s.ContextPath, Dockerfile: s.Dockerfile, Target: s.Target,
		BuildArgs: s.BuildArgs, Tags: append([]string{}, s.Tags...), Platform: s.Platform, NoCache: s.NoCache, Pull: s.Pull,
		GitCredentialID: s.GitCredentialID, RegistryIDs: s.RegistryConnectionIDs, TimeoutSeconds: s.TimeoutSeconds}
}

// ImageBuild is a build record. Its ID is the build's job ID: follow
// progress and the BuildKit log with GET /api/v1/jobs/{id}/events/stream.
type ImageBuild struct {
	ID              string     `json:"id" doc:"Build ID (equal to its job ID)."`
	EnvironmentID   string     `json:"environmentId"`
	JobID           string     `json:"jobId"`
	DefinitionID    string     `json:"definitionId,omitempty"`
	GitURL          string     `json:"gitUrl"`
	Ref             string     `json:"ref,omitempty"`
	ContextPath     string     `json:"contextPath,omitempty"`
	Dockerfile      string     `json:"dockerfile,omitempty"`
	Target          string     `json:"target,omitempty"`
	Tags            []string   `json:"tags"`
	Platform        string     `json:"platform,omitempty"`
	NoCache         bool       `json:"noCache"`
	Pull            bool       `json:"pull"`
	BuildArgNames   []string   `json:"buildArgNames" doc:"Names of the build arguments (values are not shown here)."`
	GitCredentialID string     `json:"gitCredentialId,omitempty"`
	RegistryIDs     []string   `json:"registryIds"`
	Status          string     `json:"status" enum:"queued,running,succeeded,failed,cancelled,interrupted"`
	ResolvedCommit  string     `json:"resolvedCommit,omitempty" doc:"The exact commit built."`
	ResolvedRef     string     `json:"resolvedRef,omitempty"`
	ImageID         string     `json:"imageId,omitempty" doc:"The built image's ID (config digest)."`
	ErrorClass      string     `json:"errorClass,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	DurationMs      int64      `json:"durationMs,omitempty" doc:"Build duration once finished."`
}

func newImageBuild(b domain.ImageBuild) ImageBuild {
	out := ImageBuild{ID: b.ID, EnvironmentID: b.EnvironmentID, JobID: b.JobID, DefinitionID: b.DefinitionID, GitURL: b.GitURL, Ref: b.Ref,
		ContextPath: b.ContextPath, Dockerfile: b.Dockerfile, Target: b.Target, Tags: append([]string{}, b.Tags...), Platform: b.Platform,
		NoCache: b.NoCache, Pull: b.Pull, BuildArgNames: append([]string{}, b.BuildArgKeys...), GitCredentialID: b.GitCredentialID,
		RegistryIDs: append([]string{}, b.RegistryConnectionIDs...), Status: string(b.Status), ResolvedCommit: b.ResolvedCommit,
		ResolvedRef: b.ResolvedRef, ImageID: b.ImageID, ErrorClass: b.ErrorClass, ErrorMessage: b.ErrorMessage, CreatedAt: b.CreatedAt,
		StartedAt: b.StartedAt, FinishedAt: b.FinishedAt}
	if b.StartedAt != nil && b.FinishedAt != nil {
		out.DurationMs = b.FinishedAt.Sub(*b.StartedAt).Milliseconds()
	}
	return out
}

// BuildDefinition is a saved build (#33). Shaping (#17):
// build_definition.read shows it in full (with its source), other
// capabilities on it only id, name and environment.
type BuildDefinition struct {
	ID            string       `json:"id"`
	EnvironmentID string       `json:"environmentId"`
	Name          string       `json:"name"`
	View          string       `json:"view" enum:"minimal,full"`
	Actions       []string     `json:"actions"`
	Description   string       `json:"description,omitempty"`
	Source        *BuildSource `json:"source,omitempty" doc:"Full view."`
	LastBuildID   string       `json:"lastBuildId,omitempty"`
	Revision      int64        `json:"revision,omitempty"`
	CreatedAt     time.Time    `json:"createdAt,omitzero"`
	UpdatedAt     time.Time    `json:"updatedAt,omitzero"`
}

func definitionResource(d domain.BuildDefinition) authz.Resource {
	return authz.Resource{Type: catalog.TypeBuildDefinition, ID: d.ID, EnvironmentID: d.EnvironmentID, Parents: []authz.ResourceRef{}}
}

func newBuildDefinition(d domain.BuildDefinition, v authz.View) BuildDefinition {
	out := BuildDefinition{ID: d.ID, EnvironmentID: d.EnvironmentID, Name: d.Name, View: v.Level.String(), Actions: Actions(v)}
	if v.Has(string(CapBuildDefinitionManage)) {
		out.Revision = d.Revision
	}
	if !v.Full() {
		return out
	}
	src := newBuildSource(d.Source)
	out.Description, out.Source, out.LastBuildID = d.Description, &src, d.LastBuildID
	out.Revision, out.CreatedAt, out.UpdatedAt = d.Revision, d.CreatedAt, d.UpdatedAt
	return out
}

func buildError(err error) error {
	switch {
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived")
	case errors.Is(err, domain.ErrImageBuildNotFound):
		return NotFound("image build not found")
	case errors.Is(err, domain.ErrBuildDefinitionNotFound):
		return NotFound("build definition not found")
	case errors.Is(err, domain.ErrBuildDefinitionNameTaken):
		return Conflict(CodeBuildDefinitionNameTaken, "another build definition in this environment already uses this name")
	case errors.Is(err, domain.ErrJobInvalid), errors.Is(err, domain.ErrJobIdempotencyConflict), errors.Is(err, domain.ErrJobNotFound),
		errors.Is(err, domain.ErrJobForbidden), errors.Is(err, domain.ErrJobUnknownKind), errors.Is(err, domain.ErrJobKindUnavailable):
		return JobErrorFor(err)
	}
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return Invalid("invalid build", Field("body."+fe.Field, fe.Message))
	}
	return gitCredentialError(err)
}

type buildsAPI struct {
	svc   BuildService
	authz authz.Authorizer
}

func (h *buildsAPI) service() (BuildService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "image builds are not available")
	}
	return h.svc, nil
}

// inEnvironment returns the caller's checker after checking capability in
// the environment: 404 when the environment is not visible, 403 when it
// is but the capability is not granted.
func (h *buildsAPI) inEnvironment(ctx context.Context, envID string, capability Capability, typ string) (authz.Checker, authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, err
	}
	if !authz.ViewOf(c, authz.EnvironmentResource(envID)).Visible() {
		return nil, p, NotFound("environment not found")
	}
	if !c.Can(string(capability), authz.InEnvironment(typ, envID)).Allowed {
		return nil, p, Forbidden("not permitted: " + string(capability) + " in this environment")
	}
	return c, p, nil
}

type createBuildInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body BuildSource
}

type listBuildsInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
	DefinitionID string `query:"definitionId" maxLength:"64" doc:"Only runs of this build definition."`
}

type buildIDInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	BuildID       string `path:"buildId" maxLength:"64" doc:"Build ID (the job ID)."`
}

type buildOutput struct{ Body ImageBuild }
type buildListOutput struct{ Body Page[ImageBuild] }

type definitionIDInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	DefinitionID  string `path:"definitionId" maxLength:"64" doc:"Build definition ID."`
}

type createDefinitionInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	Body          struct {
		Name        string      `json:"name" minLength:"1" maxLength:"100"`
		Description string      `json:"description,omitempty" maxLength:"1000"`
		Source      BuildSource `json:"source"`
	}
}

type updateDefinitionInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	DefinitionID  string `path:"definitionId" maxLength:"64" doc:"Build definition ID."`
	IfMatchParam
	Body struct {
		Name        *string      `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Description *string      `json:"description,omitempty" maxLength:"1000"`
		Source      *BuildSource `json:"source,omitempty" doc:"Replaces the whole source."`
	}
}

type deleteDefinitionInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	DefinitionID  string `path:"definitionId" maxLength:"64" doc:"Build definition ID."`
	IfMatchParam
}

type runDefinitionInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	DefinitionID  string `path:"definitionId" maxLength:"64" doc:"Build definition ID."`
	IdempotencyKeyParam
}

type definitionOutput struct {
	ETagHeader
	Body BuildDefinition
}

type definitionListOutput struct{ Body Page[BuildDefinition] }

type buildCursor struct {
	Before string `json:"b"`
}

func (h *buildsAPI) create(ctx context.Context, in *createBuildInput) (*JobAccepted, error) {
	_, p, err := h.inEnvironment(ctx, in.EnvironmentID, CapImageBuild, catalog.TypeImage)
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	_, job, err := svc.Start(ctx, p, in.EnvironmentID, in.Body.domain(), "", in.IdempotencyKey)
	if err != nil {
		return nil, buildError(err)
	}
	return Accepted(job), nil
}

func (h *buildsAPI) list(ctx context.Context, in *listBuildsInput) (*buildListOutput, error) {
	if _, _, err := h.inEnvironment(ctx, in.EnvironmentID, CapImageRead, catalog.TypeImage); err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("image-builds", in.EnvironmentID, in.DefinitionID)
	var cur buildCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &cur); err != nil {
			return nil, err
		}
	}
	limit := in.PageLimit()
	bs, err := svc.List(ctx, in.EnvironmentID, in.DefinitionID, cur.Before, limit+1)
	if err != nil {
		return nil, buildError(err)
	}
	next := ""
	if len(bs) > limit {
		bs = bs[:limit]
		if next, err = CursorFor(fp, buildCursor{Before: bs[limit-1].ID}); err != nil {
			return nil, Internal(err)
		}
	}
	out := make([]ImageBuild, 0, len(bs))
	for _, b := range bs {
		out = append(out, newImageBuild(b))
	}
	return &buildListOutput{Body: NewPage(out, next, nil)}, nil
}

func (h *buildsAPI) get(ctx context.Context, in *buildIDInput) (*buildOutput, error) {
	if _, _, err := h.inEnvironment(ctx, in.EnvironmentID, CapImageRead, catalog.TypeImage); err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	b, err := svc.Get(ctx, in.BuildID)
	if err != nil {
		return nil, buildError(err)
	}
	if b.EnvironmentID != in.EnvironmentID {
		return nil, NotFound("image build not found")
	}
	return &buildOutput{Body: newImageBuild(b)}, nil
}

// definition loads a definition of the environment visible to the caller.
func (h *buildsAPI) definition(ctx context.Context, envID, id string) (BuildService, authz.Checker, authz.Principal, domain.BuildDefinition, authz.View, error) {
	svc, err := h.service()
	if err != nil {
		return nil, nil, authz.Principal{}, domain.BuildDefinition{}, authz.View{}, err
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, domain.BuildDefinition{}, authz.View{}, err
	}
	d, err := svc.GetDefinition(ctx, id)
	if err != nil {
		return nil, nil, p, d, authz.View{}, buildError(err)
	}
	v := authz.ViewOf(c, definitionResource(d))
	if d.EnvironmentID != envID || !v.Visible() {
		return nil, nil, p, d, v, NotFound("build definition not found")
	}
	return svc, c, p, d, v, nil
}

func definitionETag(d BuildDefinition) ETagHeader {
	if d.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(d.Revision)}
}

func (h *buildsAPI) listDefinitions(ctx context.Context, in *struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
}) (*definitionListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !authz.ViewOf(c, authz.EnvironmentResource(in.EnvironmentID)).Visible() {
		return nil, NotFound("environment not found")
	}
	fp := QueryFingerprint("build-definitions", in.EnvironmentID)
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.BuildDefinition]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.BuildDefinition, error) {
			return svc.ListDefinitions(ctx, in.EnvironmentID, afterID, n)
		},
		Position: func(d domain.BuildDefinition) string { return d.ID },
		Visible:  func(d domain.BuildDefinition) bool { return authz.ViewOf(c, definitionResource(d)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]BuildDefinition, 0, len(items))
	for _, d := range items {
		out = append(out, newBuildDefinition(d, authz.ViewOf(c, definitionResource(d))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &definitionListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *buildsAPI) createDefinition(ctx context.Context, in *createDefinitionInput) (*definitionOutput, error) {
	c, _, err := h.inEnvironment(ctx, in.EnvironmentID, CapBuildDefinitionManage, catalog.TypeBuildDefinition)
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	d, err := svc.CreateDefinition(ctx, in.EnvironmentID, in.Body.Name, in.Body.Description, in.Body.Source.domain())
	if err != nil {
		return nil, buildError(err)
	}
	body := newBuildDefinition(d, authz.ViewOf(c, definitionResource(d)))
	return &definitionOutput{ETagHeader: definitionETag(body), Body: body}, nil
}

func (h *buildsAPI) getDefinition(ctx context.Context, in *definitionIDInput) (*definitionOutput, error) {
	_, _, _, d, v, err := h.definition(ctx, in.EnvironmentID, in.DefinitionID)
	if err != nil {
		return nil, err
	}
	body := newBuildDefinition(d, v)
	return &definitionOutput{ETagHeader: definitionETag(body), Body: body}, nil
}

func (h *buildsAPI) updateDefinition(ctx context.Context, in *updateDefinitionInput) (*definitionOutput, error) {
	svc, c, _, d, v, err := h.definition(ctx, in.EnvironmentID, in.DefinitionID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapBuildDefinitionManage)) {
		return nil, Forbidden("not permitted to manage this build definition")
	}
	if err := in.CheckIfMatch(RevisionETag(d.Revision)); err != nil {
		return nil, err
	}
	p := domain.BuildDefinitionPatch{Name: in.Body.Name, Description: in.Body.Description}
	if in.Body.Source != nil {
		s := in.Body.Source.domain()
		p.Source = &s
	}
	d, err = svc.UpdateDefinition(ctx, d.ID, d.Revision, p)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, stale(d.Revision)
	}
	if err != nil {
		return nil, buildError(err)
	}
	body := newBuildDefinition(d, authz.ViewOf(c, definitionResource(d)))
	return &definitionOutput{ETagHeader: definitionETag(body), Body: body}, nil
}

func (h *buildsAPI) deleteDefinition(ctx context.Context, in *deleteDefinitionInput) (*struct{}, error) {
	svc, _, _, d, v, err := h.definition(ctx, in.EnvironmentID, in.DefinitionID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapBuildDefinitionManage)) {
		return nil, Forbidden("not permitted to manage this build definition")
	}
	if err := in.CheckIfMatch(RevisionETag(d.Revision)); err != nil {
		return nil, err
	}
	if err := svc.DeleteDefinition(ctx, d.ID, d.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			cur, gerr := svc.GetDefinition(ctx, d.ID)
			if gerr != nil {
				return nil, buildError(gerr)
			}
			return nil, stale(cur.Revision)
		}
		return nil, buildError(err)
	}
	return nil, nil
}

func (h *buildsAPI) runDefinition(ctx context.Context, in *runDefinitionInput) (*JobAccepted, error) {
	svc, c, p, d, _, err := h.definition(ctx, in.EnvironmentID, in.DefinitionID)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapImageBuild), definitionResource(d)).Allowed {
		return nil, Forbidden("not permitted to run this build definition (image.build)")
	}
	_, job, err := svc.RunDefinition(ctx, p, d.ID, in.IdempotencyKey)
	if err != nil {
		return nil, buildError(err)
	}
	return Accepted(job), nil
}

func registerBuilds(a huma.API, deps Deps) {
	h := &buildsAPI{svc: deps.Builds, authz: authz.OrDenyAll(deps.Authorizer)}
	envPath := BasePath + "/environments/{environmentId}"
	jobErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-image-build", Method: http.MethodPost, Path: envPath + "/images/builds",
			Summary: "Build an image from a Git repository",
			Description: "Starts an image.build job on the environment's agent: the ref is resolved to a commit (in-process ls-remote " +
				"with the matching Git credential), then the Engine's BuildKit builds exactly that commit (remote Git context; no docker " +
				"or buildx CLI) with private base images authenticated by registry connections (#19). Progress and the BuildKit log " +
				"stream as job events; builds per environment are limited by DOCKYARD_JOB_MAX_CONCURRENT_BUILDS; cancel with " +
				"POST /jobs/{id}/cancellations. The build record is GET .../image-builds/{jobId}. Build argument values are never audited. " +
				"409 ambiguous_git_credential, git_credential_revoked, registry_connection_revoked.",
			Tags: []string{tagBuilds}, Errors: jobErrs,
		},
		Capability: CapImageBuild, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.create)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-image-builds", Method: http.MethodGet, Path: envPath + "/image-builds",
			Summary: "List image builds", Description: "Build records of the environment, newest first, with their outcome (resolved commit, image ID, duration).",
			Tags: []string{tagBuilds}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapImageRead, Scope: ScopeEnvironment,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-image-build", Method: http.MethodGet, Path: envPath + "/image-builds/{buildId}",
			Summary: "Get an image build", Description: "The build record; its log is the job's event stream (GET /api/v1/jobs/{buildId}/events/stream).",
			Tags: []string{tagBuilds}, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapImageRead, Scope: ScopeResource,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-build-definitions", Method: http.MethodGet, Path: envPath + "/build-definitions",
			Summary: "List build definitions", Description: "Saved builds of the environment, filtered per item (#17).",
			Tags: []string{tagBuilds}, Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapBuildDefinitionRead, Scope: ScopeEnvironment,
	}, h.listDefinitions)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-build-definition", Method: http.MethodPost, Path: envPath + "/build-definitions",
			Summary: "Save a build definition", DefaultStatus: http.StatusCreated,
			Description: "Saves a build (source, tags, options) to re-run on demand; scheduled rebuilds are not in v1. " +
				"409 build_definition_name_taken.",
			Tags: []string{tagBuilds}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBuildDefinitionManage, Scope: ScopeEnvironment,
	}, h.createDefinition)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-build-definition", Method: http.MethodGet, Path: envPath + "/build-definitions/{definitionId}",
			Summary: "Get a build definition", Tags: []string{tagBuilds}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapBuildDefinitionRead, Scope: ScopeResource,
	}, h.getDefinition)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-build-definition", Method: http.MethodPatch, Path: envPath + "/build-definitions/{definitionId}",
			Summary: "Update a build definition", Description: "Requires If-Match.",
			Tags: []string{tagBuilds}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
				http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapBuildDefinitionManage, Scope: ScopeResource,
	}, h.updateDefinition)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-build-definition", Method: http.MethodDelete, Path: envPath + "/build-definitions/{definitionId}",
			Summary: "Delete a build definition", DefaultStatus: http.StatusNoContent,
			Description: "Its build records stay. Requires If-Match.",
			Tags:        []string{tagBuilds}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapBuildDefinitionManage, Scope: ScopeResource,
	}, h.deleteDefinition)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-build-definition-run", Method: http.MethodPost, Path: envPath + "/build-definitions/{definitionId}/runs",
			Summary: "Run a build definition",
			Description: "Starts a build of the definition (like POST .../images/builds); image.build on the definition suffices " +
				"for the images it tags.",
			Tags: []string{tagBuilds}, Errors: jobErrs,
		},
		Capability: CapImageBuild, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.runDefinition)
}
