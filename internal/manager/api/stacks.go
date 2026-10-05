package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

const tagStacks = "Stacks"

// Stack capabilities (#17).
const (
	CapStackRead            Capability = "stack.read"
	CapStackCreate          Capability = "stack.create"
	CapStackImport          Capability = "stack.import"
	CapStackManage          Capability = "stack.manage"
	CapStackRemove          Capability = "stack.remove"
	CapStackDeploy          Capability = "stack.deploy"
	CapStackBuild           Capability = "stack.build"
	CapStackRename          Capability = "stack.rename"
	CapStackUpdate          Capability = "stack.update"
	CapStackDefinitionRead  Capability = "stack.definition.read"
	CapStackDefinitionWrite Capability = "stack.definition.write"
	capContainerDetailsRead            = "container.details.read"
	capContainerStop                   = "container.stop"
	capContainerRemove                 = "container.remove"
)

// Stack error codes (#7).
const (
	CodeStackNameTaken             = "stack_name_taken"
	CodeComposeProjectExists       = "compose_project_exists"
	CodeStackDirectoryExists       = "stack_directory_exists"
	CodeStackDefinitionChanged     = "stack_definition_changed"
	CodeStackNotAdoptable          = "stack_not_adoptable"
	CodeStackNotCopyable           = "stack_not_copyable"
	CodeStackRootUnavailable       = "stack_root_unavailable"
	CodeRevisionContentUnavailable = "revision_content_unavailable"
	CodeInvalidDefinition          = "invalid_definition"
	CodeDefinitionTooLarge         = "definition_too_large"
	CodeStackRenameBlocked         = "stack_rename_blocked"
)

// StackService is the stack service as seen by the API (implemented by
// internal/manager/stacks.Service).
type StackService interface {
	List(ctx context.Context, f domain.StackFilter) ([]domain.Stack, error)
	Get(ctx context.Context, id string) (domain.Stack, error)
	Online(ctx context.Context, environmentID string) bool
	Create(ctx context.Context, p authz.Principal, r domain.StackCreate) (domain.Stack, domain.StackValidation, error)
	CreateFromTemplate(ctx context.Context, p authz.Principal, r domain.StackFromTemplate) (domain.Stack, domain.StackValidation, error)
	Validate(ctx context.Context, d domain.StackDefinition) (domain.StackValidation, error)
	ValidateStack(ctx context.Context, st domain.Stack) (domain.StackValidation, error)
	Update(ctx context.Context, id string, expectRevision int64, p domain.StackPatch) (domain.Stack, error)
	Delete(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackRemoveOptions) (domain.Job, error)
	Deploy(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackDeployOptions) (domain.Job, error)
	Build(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackBuildOptions) (domain.Job, error)
	Operate(ctx context.Context, p authz.Principal, st domain.Stack, action string, r domain.StackJobRequest) (domain.Job, error)
	Restore(ctx context.Context, p authz.Principal, st domain.Stack, revisionID string) (domain.StackRestore, error)
	Revisions(ctx context.Context, stackID string, beforeSeq int64, limit int) ([]domain.StackRevision, error)
	Revision(ctx context.Context, stackID, revisionID string) (domain.StackRevision, error)
	Services(ctx context.Context, st domain.Stack) (domain.StackServicesView, error)
	ImageStatus(st domain.Stack) []domain.StackImageView
	Discovered(ctx context.Context, environmentID string) ([]domain.DiscoveredStack, error)
	Import(ctx context.Context, p authz.Principal, r domain.StackImport) (domain.Stack, error)
	ImportCopy(ctx context.Context, p authz.Principal, r domain.StackImport, jr domain.StackJobRequest) (domain.Stack, domain.Job, error)
	PreviewRename(ctx context.Context, st domain.Stack, name string) (domain.StackRenamePlan, error)
	Rename(ctx context.Context, p authz.Principal, st domain.Stack, name string, jr domain.StackJobRequest) (domain.Job, error)
	Pull(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest) (domain.Job, error)
}

// StackRevisionRef identifies a revision of the stack's definition.
type StackRevisionRef struct {
	ID   string     `json:"id"`
	Seq  int64      `json:"seq" doc:"Revision number (1 = first)."`
	Hash string     `json:"hash" doc:"SHA-256 of the definition (files and their hashes)."`
	At   *time.Time `json:"at,omitempty" doc:"appliedRevision: when a deploy last created, recreated or started a container (a deploy that found everything running as defined keeps the earlier time; absent when none did). sourceRevision: when it was last seen on disk."`
}

// StackDependency is a depends_on entry.
type StackDependency struct {
	Service   string `json:"service"`
	Condition string `json:"condition" enum:"service_started,service_healthy,service_completed_successfully"`
	Required  bool   `json:"required"`
	Restart   bool   `json:"restart"`
}

// StackServiceDef is a service of the stack's definition with its display
// metadata.
type StackServiceDef struct {
	Name        string            `json:"name" example:"web"`
	Image       string            `json:"image" example:"nginx:1.27" doc:"Resolved image reference."`
	Build       bool              `json:"build" doc:"The service has a build section (#33)."`
	DependsOn   []StackDependency `json:"dependsOn"`
	Description string            `json:"description,omitempty" doc:"Docker Manager display metadata (never written to Compose files)."`
	Icon        string            `json:"icon,omitempty" deprecated:"true" doc:"Deprecated, never returned: services have no icon of their own (the web shows one service icon)."`
}

// StackImage is the image a service runs after the last deploy.
type StackImage struct {
	Service  string `json:"service"`
	Image    string `json:"image" doc:"Reference as resolved from the definition (the tag text never changes, #20)."`
	ImageID  string `json:"imageId,omitempty"`
	Digest   string `json:"digest,omitempty" doc:"Repository digest applied on this host (empty for locally built images)."`
	Platform string `json:"platform,omitempty" example:"linux/amd64"`
	Build    bool   `json:"build"`
	// Pulled* describe a newer image a pull (POST /stacks/{id}/pulls) left
	// on the host; the next deploy runs it.
	PulledImageID string     `json:"pulledImageId,omitempty" doc:"A newer image for the reference that a pull without deploy put on the host; the next deploy runs it (absent after a deploy)."`
	PulledDigest  string     `json:"pulledDigest,omitempty" doc:"Repository digest of pulledImageId."`
	PulledAt      *time.Time `json:"pulledAt,omitempty" doc:"When the pull found it."`
}

// StackBind is a resolved bind-mount source.
type StackBind struct {
	Service  string `json:"service"`
	Source   string `json:"source" doc:"Host path."`
	Target   string `json:"target"`
	RelPath  string `json:"relPath,omitempty" doc:"Path inside the project directory (stack backups include it by default, #10)."`
	External bool   `json:"external" doc:"Outside the project directory: backups need an explicit opt-in (#10)."`
	ReadOnly bool   `json:"readOnly"`
}

// StackServiceState counts a service's containers.
type StackServiceState struct {
	Service    string `json:"service"`
	Containers int    `json:"containers"`
	Running    int    `json:"running"`
}

// StackEngineState is the live Engine state last observed.
type StackEngineState struct {
	State      string              `json:"state" enum:"unknown,running,partial,stopped,missing"`
	ObservedAt *time.Time          `json:"observedAt,omitempty"`
	Services   []StackServiceState `json:"services"`
}

// StackLocation is where the project directory lives on the host.
type StackLocation struct {
	Root string `json:"root" enum:"stacks,bind" doc:"stacks: the environment's stacks volume; bind: a registered stack root (#28)."`
	Dir  string `json:"dir" doc:"Project directory relative to the root."`
	// HostPath is only set on GET /stacks/{stackId} with
	// stack.definition.read (host paths follow the binds' rule).
	HostPath string `json:"hostPath,omitempty" doc:"The project directory's host path (GET of one stack with stack.definition.read, when the agent reported its stacks root)."`
}

// stackHostPaths resolves a stack's project directory on the host
// (implemented by internal/manager/stacks.Service).
type stackHostPaths interface {
	HostPath(ctx context.Context, stackID string) (string, error)
}

// StackJobRef names the stack's latest job.
type StackJobRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// Stack is a managed Compose project.
//
// Shaping (#17): stack.read shows it in full; any other capability on the
// stack (or inside it) shows only id, name, environmentId, status, view
// and actions. The Compose definition itself (files, revisions, bind
// sources) needs stack.definition.read.
type Stack struct {
	ID            string   `json:"id" example:"0190a6e0-7777-7000-8000-000000000007"`
	EnvironmentID string   `json:"environmentId"`
	Name          string   `json:"name" example:"shop" doc:"Compose project name."`
	Status        string   `json:"status" enum:"undeployed,deployed,stopped,down,failed" doc:"What Docker Manager last did to the stack (not the live Engine state, see engine)."`
	View          string   `json:"view" enum:"minimal,full"`
	Actions       []string `json:"actions" doc:"Granted stack capabilities."`
	Revision      int64    `json:"revision,omitempty" doc:"Metadata revision (the ETag); full view or with stack.manage."`

	DisplayName string            `json:"displayName,omitempty"`
	Description string            `json:"description,omitempty"`
	Icon        string            `json:"icon,omitempty" deprecated:"true" doc:"Deprecated, never returned: stacks have no icon of their own (the web shows the stack icon, or the image of the template the stack was created from)."`
	Links       []WebLink         `json:"links,omitempty" doc:"Web links (documentation, website, repository) in the user's order."`
	Origin      string            `json:"origin,omitempty" enum:"created,imported"`
	Template    *StackTemplateRef `json:"template,omitempty" doc:"The template version the stack was created from (its files are the stack's own)."`
	Location    *StackLocation    `json:"location,omitempty"`
	ConfigFiles []string          `json:"configFiles,omitempty" doc:"Explicit Compose files (empty: compose.yaml plus its override file)."`
	Services    []StackServiceDef `json:"services,omitempty" doc:"Services of the last deploy (or of the definition before the first deploy)."`
	// Revisions of the three distinguishable states (#7): what was last
	// deployed, what is on disk, and the last failed deploy.
	AppliedRevision   *StackRevisionRef   `json:"appliedRevision,omitempty" doc:"Last revision deployed successfully by Docker Manager (absent: never deployed by Docker Manager)."`
	SourceRevision    *StackRevisionRef   `json:"sourceRevision,omitempty" doc:"Newest revision observed on disk."`
	FailedRevision    *StackRevisionRef   `json:"failedRevision,omitempty" doc:"Revision of the last failed deploy (cleared by a successful one)."`
	UndeployedChanges bool                `json:"undeployedChanges,omitempty" doc:"The definition on disk differs from the last applied revision."`
	SourceBuild       bool                `json:"sourceBuild,omitempty" doc:"The definition on disk (sourceRevision) has a service with a build section, as last validated. Builds use these files (#33); services follow the last deploy."`
	Images            []StackImage        `json:"images,omitempty" doc:"Images applied by the last successful deploy."`
	Engine            *StackEngineState   `json:"engine,omitempty" doc:"Live Engine state as last observed (compare with status and appliedRevision to see drift)."`
	PreviousState     []StackServiceState `json:"previousState,omitempty" doc:"Engine state before the last deploy (recovery of a failed deploy)."`
	Recovery          string              `json:"recovery,omitempty" doc:"How to recover from a failed deploy."`
	Binds             []StackBind         `json:"binds,omitempty" doc:"Resolved bind sources (with stack.definition.read)."`
	LastJob           *StackJobRef        `json:"lastJob,omitempty"`
	Protection        *ResourceProtection `json:"protection,omitempty" doc:"Docker Manager's own Compose project (#32; get-stack only, while the environment is online): it can be imported, redeployed and updated, but stop, restart, down, delete and migrate are refused with 409 protected."`
	EnvironmentOnline bool                `json:"environmentOnline,omitempty" doc:"The environment's agent is connected."`
	ReadOnly          bool                `json:"readOnly,omitempty" doc:"The environment is offline: the last known revision and state are shown read-only."`
	CreatedAt         time.Time           `json:"createdAt,omitzero"`
	UpdatedAt         time.Time           `json:"updatedAt,omitzero"`
}

func stackResource(st domain.Stack) authz.Resource {
	return authz.Resource{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{}}
}

func revRef(r *domain.RevisionRef, at *time.Time) *StackRevisionRef {
	if r == nil {
		return nil
	}
	return &StackRevisionRef{ID: r.ID, Seq: r.Seq, Hash: r.Hash, At: at}
}

func states(in []domain.StackServiceState) []StackServiceState {
	out := []StackServiceState{}
	for _, s := range in {
		out = append(out, StackServiceState{Service: s.Service, Containers: s.Containers, Running: s.Running})
	}
	return out
}

func serviceDefs(st domain.Stack) []StackServiceDef {
	out := []StackServiceDef{}
	for _, s := range st.Services {
		d := StackServiceDef{Name: s.Name, Image: s.Image, Build: s.Build, DependsOn: []StackDependency{},
			Description: st.ServiceMeta[s.Name].Description}
		for _, dep := range s.DependsOn {
			d.DependsOn = append(d.DependsOn, StackDependency(dep))
		}
		out = append(out, d)
	}
	return out
}

// recoveryFor is the guidance shown for a failed deploy.
func recoveryFor(st domain.Stack) string {
	if st.Status != domain.StackFailed {
		return ""
	}
	if st.Applied == nil {
		return "The deploy failed. Fix the definition (see the job's error) and deploy again. Services that started keep running; " +
			"nothing is rolled back automatically."
	}
	return "The deploy failed. Fix the definition and deploy again, or restore revision " + strconv.FormatInt(st.Applied.Seq, 10) +
		" (the last applied one) to disk and deploy it; the images the stack ran before are still on the host. " +
		"Nothing is rolled back automatically."
}

// newStack shapes a stack for a caller's view (#17).
func newStack(st domain.Stack, v authz.View, online bool) Stack {
	out := Stack{ID: st.ID, EnvironmentID: st.EnvironmentID, Name: st.Name, Status: string(st.Status), View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		if v.Has(string(CapStackManage)) {
			out.Revision = st.Revision
		}
		return out
	}
	out.Revision = st.Revision
	out.DisplayName, out.Description, out.Origin = st.DisplayName, st.Meta.Description, st.Origin
	out.Links = webLinks(st.Links)
	out.Location = &StackLocation{Root: st.Root, Dir: st.Dir}
	out.Template = newStackTemplateRef(st.Template)
	out.ConfigFiles = st.ConfigFiles
	out.Services = serviceDefs(st)
	out.AppliedRevision = revRef(st.Applied, st.AppliedAt)
	out.SourceRevision = revRef(st.Observed, st.ObservedAt)
	out.FailedRevision = revRef(st.Failed, nil)
	out.UndeployedChanges = st.UndeployedChanges()
	out.SourceBuild = st.SourceBuild
	for _, i := range st.Images {
		out.Images = append(out.Images, StackImage(i))
	}
	out.Engine = &StackEngineState{State: string(st.EngineState), ObservedAt: st.EngineObservedAt, Services: states(st.EngineServices)}
	if st.Status == domain.StackFailed {
		out.PreviousState = states(st.PreviousState)
		out.Recovery = recoveryFor(st)
	}
	if v.Has(string(CapStackDefinitionRead)) {
		for _, b := range st.Binds {
			out.Binds = append(out.Binds, StackBind(b))
		}
	}
	if st.LastJobID != "" {
		out.LastJob = &StackJobRef{ID: st.LastJobID, Kind: string(st.LastJobKind)}
	}
	out.EnvironmentOnline, out.ReadOnly = online, !online
	out.CreatedAt, out.UpdatedAt = st.CreatedAt, st.UpdatedAt
	return out
}

// StackIssue is a validation finding.
type StackIssue struct {
	Code    string `json:"code" doc:"invalid_project, unsupported_compose_feature, obsolete_version, bind_outside_project, ..."`
	Message string `json:"message" example:"services.web.ports: invalid port \"80a\""`
	Service string `json:"service,omitempty"`
}

// StackValidation is the result of validating a Compose definition.
type StackValidation struct {
	Valid       bool              `json:"valid"`
	ProjectName string            `json:"projectName,omitempty"`
	Errors      []StackIssue      `json:"errors"`
	Warnings    []StackIssue      `json:"warnings" doc:"Non-fatal findings: obsolete keys (top-level version), bind sources outside the project directory (#10)."`
	Services    []StackServiceDef `json:"services"`
	Binds       []StackBind       `json:"binds"`
}

func newValidation(v domain.StackValidation) StackValidation {
	out := StackValidation{Valid: v.Valid, ProjectName: v.ProjectName, Errors: []StackIssue{}, Warnings: []StackIssue{},
		Services: []StackServiceDef{}, Binds: []StackBind{}}
	for _, i := range v.Errors {
		out.Errors = append(out.Errors, StackIssue(i))
	}
	for _, i := range v.Warnings {
		out.Warnings = append(out.Warnings, StackIssue(i))
	}
	for _, s := range v.Services {
		d := StackServiceDef{Name: s.Name, Image: s.Image, Build: s.Build, DependsOn: []StackDependency{}, Description: s.Meta.Description}
		for _, dep := range s.DependsOn {
			d.DependsOn = append(d.DependsOn, StackDependency(dep))
		}
		out.Services = append(out.Services, d)
	}
	for _, b := range v.Binds {
		out.Binds = append(out.Binds, StackBind(b))
	}
	return out
}

// StackDefinitionBody is a Compose definition in a request.
type StackDefinitionBody struct {
	Compose  string `json:"compose,omitempty" maxLength:"262144" doc:"Required: compose.yaml content."`
	Override string `json:"override,omitempty" maxLength:"262144" doc:"compose.override.yaml content (optional)."`
	Env      string `json:"env,omitempty" maxLength:"262144" doc:".env content (optional; may hold secrets: it is stored sealed and never logged)."`
}

func (b StackDefinitionBody) files() []domain.StackFile {
	files := []domain.StackFile{{Path: "compose.yaml", Content: []byte(b.Compose)}}
	if b.Override != "" {
		files = append(files, domain.StackFile{Path: "compose.override.yaml", Content: []byte(b.Override)})
	}
	if b.Env != "" {
		files = append(files, domain.StackFile{Path: ".env", Content: []byte(b.Env)})
	}
	return files
}

// StackRevisionFile is a definition file of a revision.
type StackRevisionFile struct {
	Path     string `json:"path" example:"compose.yaml"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Content  string `json:"content,omitempty" doc:"File content (revision detail only)."`
	Encoding string `json:"encoding,omitempty" enum:"utf-8,base64" doc:"base64 when the file is not valid UTF-8."`
}

// StackRevision is an immutable snapshot of the stack's definition.
type StackRevision struct {
	ID             string              `json:"id"`
	Seq            int64               `json:"seq"`
	Hash           string              `json:"hash"`
	Source         string              `json:"source" enum:"deploy,editor,file_manager,external,restore" doc:"How the revision was observed."`
	AuthorUserID   string              `json:"authorUserId,omitempty" doc:"Audit metadata."`
	AuthorTokenID  string              `json:"authorTokenId,omitempty" doc:"Audit metadata."`
	JobID          string              `json:"jobId,omitempty" doc:"The deploy job (source deploy)."`
	RestoredFrom   string              `json:"restoredFrom,omitempty" doc:"The revision a restore wrote back."`
	ContentOmitted bool                `json:"contentOmitted,omitempty" doc:"Only hashes were captured; the revision cannot be shown or restored."`
	Applied        bool                `json:"applied,omitempty" doc:"This is the stack's applied revision."`
	Files          []StackRevisionFile `json:"files"`
	CreatedAt      time.Time           `json:"createdAt"`
}

func newRevision(r domain.StackRevision, st domain.Stack, withContent bool) StackRevision {
	out := StackRevision{ID: r.ID, Seq: r.Seq, Hash: r.Hash, Source: string(r.Source), AuthorUserID: r.AuthorUserID,
		AuthorTokenID: r.AuthorTokenID, JobID: r.JobID, RestoredFrom: r.RestoredFrom, ContentOmitted: r.ContentOmitted,
		Applied: st.Applied != nil && st.Applied.ID == r.ID, Files: []StackRevisionFile{}, CreatedAt: r.CreatedAt}
	for _, f := range r.Files {
		rf := StackRevisionFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size}
		if withContent && f.Content != nil {
			rf.Content, rf.Encoding = string(f.Content), "utf-8"
			if !utf8.Valid(f.Content) {
				rf.Content, rf.Encoding = base64.StdEncoding.EncodeToString(f.Content), "base64"
			}
		}
		out.Files = append(out.Files, rf)
	}
	return out
}

// stackProtection reports Docker Manager's own Compose project (#32).
type stackProtection interface {
	Protection(ctx context.Context, st domain.Stack) (*protocol.Protection, error)
}

type stacksAPI struct {
	svc       StackService
	authz     authz.Authorizer
	deps      Deps
	heartbeat time.Duration
}

func (h *stacksAPI) checker(ctx context.Context) (authz.Checker, authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, err
	}
	if h.svc == nil {
		return nil, p, Unavailable(CodeUnavailable, "the stack service is not available")
	}
	return c, p, nil
}

// visibleStack loads a stack the caller may see (404 otherwise).
func (h *stacksAPI) visibleStack(ctx context.Context, id string) (authz.Checker, authz.Principal, domain.Stack, authz.View, error) {
	c, p, err := h.checker(ctx)
	if err != nil {
		return nil, p, domain.Stack{}, authz.View{}, err
	}
	st, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, p, domain.Stack{}, authz.View{}, stackErr(err)
	}
	v := authz.ViewOf(c, stackResource(st))
	if !v.Visible() {
		return nil, p, domain.Stack{}, authz.View{}, NotFound("stack not found")
	}
	return c, p, st, v, nil
}

// requireStack loads a stack and requires capability cp on it (404 when
// invisible, 403 when visible but not granted).
func (h *stacksAPI) requireStack(ctx context.Context, id string, cp Capability) (authz.Checker, authz.Principal, domain.Stack, authz.View, error) {
	c, p, st, v, err := h.visibleStack(ctx, id)
	if err != nil {
		return c, p, st, v, err
	}
	if !v.Has(string(cp)) {
		return c, p, st, v, Forbidden("not permitted: " + string(cp))
	}
	return c, p, st, v, nil
}

// requireInEnvironment requires a creation capability in an environment
// (404 when the environment is invisible, 403 otherwise).
func (h *stacksAPI) requireInEnvironment(ctx context.Context, env string, cp Capability) (authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return p, err
	}
	if env != "" && c.Can(string(cp), authz.InEnvironment(catalog.TypeStack, env)).Allowed {
		if h.svc == nil {
			return p, Unavailable(CodeUnavailable, "the stack service is not available")
		}
		return p, nil
	}
	if env != "" && authz.ViewOf(c, authz.EnvironmentResource(env)).Visible() {
		return p, Forbidden("not permitted: " + string(cp))
	}
	return p, NotFound("environment not found")
}

// stackErr maps stack service errors to API errors.
func stackErr(err error) error {
	var in *domain.InputError
	var se *domain.StackError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &in):
		return Invalid(in.Message, Field("body."+in.Field, in.Message))
	case errors.Is(err, domain.ErrStackNotFound):
		return NotFound("stack not found")
	case errors.Is(err, domain.ErrStackRevisionNotFound):
		return NotFound("revision not found")
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived")
	case errors.Is(err, domain.ErrStackNameTaken):
		return Conflict(CodeStackNameTaken, "the environment already has a stack with this project name or project directory; nothing was overwritten")
	case errors.As(err, &se):
		return stackCodeErr(se)
	case isRegistryErr(err):
		return registryError(err)
	case errors.As(err, new(*domain.DockerError)):
		return dockerErr(err) // #32: Docker Manager's own Compose project
	}
	return JobErrorFor(err)
}

// isRegistryErr reports registry selection failures of a deploy (#19).
func isRegistryErr(err error) bool {
	var amb *domain.AmbiguousRegistryError
	return errors.As(err, &amb) || errors.Is(err, domain.ErrRegistryConnectionRevoked) ||
		errors.Is(err, domain.ErrRegistryConnectionNotFound) || errors.Is(err, domain.ErrRegistryConnectionMismatch)
}

func stackCodeErr(se *domain.StackError) error {
	switch se.Code {
	case domain.StackErrOffline:
		return NewError(http.StatusServiceUnavailable, CodeEnvironmentOffline, se.Message).WithRetryable(true)
	case domain.StackErrEngineUnavailable:
		return NewError(http.StatusServiceUnavailable, CodeEngineUnavailable, se.Message).WithRetryable(true)
	case domain.StackErrEnvironmentUnsupported:
		return NewError(http.StatusNotImplemented, CodeAgentUnsupported, se.Message)
	case domain.StackErrAgentTimeout:
		return NewError(http.StatusGatewayTimeout, CodeTimeout, se.Message)
	case domain.StackErrInvalidDefinition:
		var details []ErrorDetail
		for _, i := range se.Issues {
			msg := i.Message
			if i.Service != "" {
				msg = "service " + i.Service + ": " + msg
			}
			details = append(details, Field("body.compose", i.Code+": "+msg))
		}
		return NewError(http.StatusUnprocessableEntity, CodeInvalidDefinition, se.Message, details...)
	case domain.StackErrDefinitionTooLarge:
		return NewError(http.StatusUnprocessableEntity, CodeDefinitionTooLarge, se.Message)
	case domain.StackErrRenameBlocked:
		var details []ErrorDetail
		for _, i := range se.Issues {
			details = append(details, ErrorDetail{Field: "rename." + i.Code, Message: i.Message})
		}
		return NewError(http.StatusConflict, CodeStackRenameBlocked, se.Message, details...)
	case domain.StackErrProjectNotFound:
		return NotFound(se.Message)
	case domain.StackErrAgent:
		return NewError(http.StatusBadGateway, CodeEngineError, se.Message)
	}
	codes := map[string]string{
		domain.StackErrProjectExists:      CodeComposeProjectExists,
		domain.StackErrDirectoryExists:    CodeStackDirectoryExists,
		domain.StackErrDefinitionChanged:  CodeStackDefinitionChanged,
		domain.StackErrNotAdoptable:       CodeStackNotAdoptable,
		domain.StackErrNotCopyable:        CodeStackNotCopyable,
		domain.StackErrRootUnavailable:    CodeStackRootUnavailable,
		domain.StackErrContentUnavailable: CodeRevisionContentUnavailable,
	}
	if code, ok := codes[se.Code]; ok {
		return Conflict(code, se.Message)
	}
	return Internal(se)
}

type listStacksInput struct {
	PageParams
	EnvironmentID string `query:"environmentId" maxLength:"64" doc:"Only stacks of this environment."`
}

type stackIDInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
}

type stackOutput struct {
	ETagHeader
	Body Stack
}

type listStacksOutput struct{ Body Page[Stack] }

type stacksCursor struct {
	After string `json:"a"`
}

func (h *stacksAPI) list(ctx context.Context, in *listStacksInput) (*listStacksOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := QueryFingerprint("stacks", in.EnvironmentID)
	var after stacksCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Stack]{
		Limit: in.PageLimit(), After: after.After,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.Stack, error) {
			return h.svc.List(ctx, domain.StackFilter{EnvironmentID: in.EnvironmentID, AfterID: afterID, Limit: n})
		},
		Position: func(st domain.Stack) string { return st.ID },
		Visible:  func(st domain.Stack) bool { return authz.ViewOf(c, stackResource(st)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	online := map[string]bool{}
	out := make([]Stack, 0, len(items))
	for _, st := range items {
		o, ok := online[st.EnvironmentID]
		if !ok {
			o = h.svc.Online(ctx, st.EnvironmentID)
			online[st.EnvironmentID] = o
		}
		out = append(out, newStack(st, authz.ViewOf(c, stackResource(st)), o))
	}
	cursor := ""
	if next != "" {
		if cursor, err = CursorFor(fingerprint, stacksCursor{After: next}); err != nil {
			return nil, Internal(err)
		}
	}
	return &listStacksOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *stacksAPI) stackOut(ctx context.Context, st domain.Stack, v authz.View) *stackOutput {
	out := &stackOutput{Body: newStack(st, v, h.svc.Online(ctx, st.EnvironmentID))}
	if out.Body.Revision != 0 {
		out.ETag = RevisionETag(st.Revision)
	}
	return out
}

func (h *stacksAPI) get(ctx context.Context, in *stackIDInput) (*stackOutput, error) {
	_, _, st, v, err := h.visibleStack(ctx, in.StackID)
	if err != nil {
		return nil, err
	}
	out := h.stackOut(ctx, st, v)
	if sp, ok := h.svc.(stackProtection); ok && out.Body.EnvironmentOnline {
		// Best effort: the UI disables the refused actions with the reason.
		if p, err := sp.Protection(ctx, st); err == nil {
			out.Body.Protection = newProtection(p)
		}
	}
	if hp, ok := h.svc.(stackHostPaths); ok && out.Body.Location != nil && v.Has(string(CapStackDefinitionRead)) {
		// Best effort: unknown while the agent has not reported its roots.
		if p, err := hp.HostPath(ctx, st.ID); err == nil {
			out.Body.Location.HostPath = p
		}
	}
	return out, nil
}

type createStackInput struct {
	Body struct {
		EnvironmentID string    `json:"environmentId,omitempty" maxLength:"64" doc:"Required: the environment to create the stack in."`
		Name          string    `json:"name,omitempty" example:"web" maxLength:"63" doc:"Required: Compose project name (lower-case letters, digits, '-' and '_'); also the project directory in the stacks volume."`
		DisplayName   string    `json:"displayName,omitempty" example:"Website" maxLength:"128"`
		Description   string    `json:"description,omitempty" maxLength:"1024"`
		Icon          string    `json:"icon,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated and ignored: stacks have no icon of their own."`
		Links         []WebLink `json:"links,omitempty" maxItems:"10" doc:"Web links (documentation, website, repository), at most 10."`
		StackDefinitionBody
	}
}

type createStackOutput struct {
	ETagHeader
	Location string `header:"Location"`
	Body     struct {
		Stack      Stack           `json:"stack"`
		Validation StackValidation `json:"validation"`
	}
}

func (h *stacksAPI) create(ctx context.Context, in *createStackInput) (*createStackOutput, error) {
	p, err := h.requireInEnvironment(ctx, in.Body.EnvironmentID, CapStackCreate)
	if err != nil {
		return nil, err
	}
	st, val, err := h.svc.Create(ctx, p, domain.StackCreate{
		StackDefinition: domain.StackDefinition{EnvironmentID: in.Body.EnvironmentID, Name: in.Body.Name, Files: in.Body.files()},
		DisplayName:     in.Body.DisplayName, Meta: domain.DisplayMeta{Description: in.Body.Description},
		Links: domainLinks(in.Body.Links),
	})
	if err != nil {
		return nil, stackErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID})
	audit.SetDetail(ctx, "project", st.Name)
	if len(st.Links) > 0 {
		// Never the URLs (they may carry query strings): their number.
		audit.SetDetail(ctx, "linkCount", len(st.Links))
	}
	c, _, _ := h.checker(ctx)
	out := &createStackOutput{Location: BasePath + "/stacks/" + st.ID, ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)}}
	out.Body.Stack = newStack(st, authz.ViewOf(c, stackResource(st)), true)
	out.Body.Validation = newValidation(val)
	return out, nil
}

type validateStackInput struct {
	Body struct {
		EnvironmentID string `json:"environmentId,omitempty" maxLength:"64" doc:"Required: the environment whose agent validates."`
		Name          string `json:"name,omitempty" example:"web" maxLength:"63" doc:"Required: project name the definition would be deployed as."`
		StackDefinitionBody
	}
}

type validationOutput struct{ Body StackValidation }

func (h *stacksAPI) validate(ctx context.Context, in *validateStackInput) (*validationOutput, error) {
	if _, err := h.requireInEnvironment(ctx, in.Body.EnvironmentID, CapStackCreate); err != nil {
		return nil, err
	}
	v, err := h.svc.Validate(ctx, domain.StackDefinition{EnvironmentID: in.Body.EnvironmentID, Name: in.Body.Name, Files: in.Body.files()})
	if err != nil {
		return nil, stackErr(err)
	}
	return &validationOutput{Body: newValidation(v)}, nil
}

// validateExisting validates an existing stack's definition as it is on
// disk, in its own project directory (as its deploy loads it). It needs
// stack.definition.write, the capability that edits those files, so the
// editor can check what it just saved without stack.create.
func (h *stacksAPI) validateExisting(ctx context.Context, in *stackIDInput) (*validationOutput, error) {
	_, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackDefinitionWrite)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.ValidateStack(ctx, st)
	if err != nil {
		return nil, stackErr(err)
	}
	audit.SetDetail(ctx, "valid", v.Valid)
	return &validationOutput{Body: newValidation(v)}, nil
}

// StackServiceMetaBody is per-service display metadata in a patch.
type StackServiceMetaBody struct {
	Description string `json:"description" maxLength:"1024"`
	Icon        string `json:"icon,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated and ignored: services have no icon of their own."`
}

type updateStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IfMatchParam
	Body struct {
		DisplayName *string                         `json:"displayName,omitempty" example:"Website" maxLength:"128"`
		Description *string                         `json:"description,omitempty" maxLength:"1024"`
		Icon        *string                         `json:"icon,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated and ignored: stacks have no icon of their own."`
		Links       *[]WebLink                      `json:"links,omitempty" maxItems:"10" doc:"Replaces the stack's links (an empty list removes them; absent: unchanged)."`
		Services    map[string]StackServiceMetaBody `json:"services,omitempty" doc:"Display metadata per service name (empty values clear it)."`
	}
}

func (h *stacksAPI) update(ctx context.Context, in *updateStackInput) (*stackOutput, error) {
	_, _, st, v, err := h.requireStack(ctx, in.StackID, CapStackManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(st.Revision)); err != nil {
		return nil, err
	}
	// A deprecated icon (of the stack or a service) is ignored.
	patch := domain.StackPatch{DisplayName: in.Body.DisplayName, Description: in.Body.Description,
		Links: domainLinksPatch(in.Body.Links)}
	if len(in.Body.Services) > 0 {
		patch.Services = map[string]domain.DisplayMeta{}
		for k, m := range in.Body.Services {
			patch.Services[k] = domain.DisplayMeta{Description: m.Description}
		}
	}
	before := st
	st, err = h.svc.Update(ctx, st.ID, st.Revision, patch)
	if errors.Is(err, domain.ErrStackRevisionStale) {
		cur, gerr := h.svc.Get(ctx, in.StackID)
		if gerr != nil {
			return nil, stackErr(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, stackErr(err)
	}
	// Links by their number only: a URL may carry a query string.
	audit.SetDiff(ctx, map[string]any{"displayName": before.DisplayName, "description": before.Meta.Description, "links": len(before.Links)},
		map[string]any{"displayName": st.DisplayName, "description": st.Meta.Description, "links": len(st.Links)})
	return h.stackOut(ctx, st, v), nil
}

type deleteStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	RemoveVolumes bool `query:"removeVolumes" doc:"Also remove the volumes the stack owns: named volumes its definition declares (not external) that Compose created for the project, and the anonymous volumes of its containers. External volumes, other projects' volumes, volumes other containers use and Docker Manager's own are kept. Default false: every volume is kept."`
}

func (h *stacksAPI) remove(ctx context.Context, in *deleteStackInput) (*JobAccepted, error) {
	_, p, st, _, err := h.requireStack(ctx, in.StackID, CapStackRemove)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.Delete(ctx, p, st, domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey},
		domain.StackRemoveOptions{Volumes: in.RemoveVolumes})
	if err != nil {
		return nil, stackErr(err)
	}
	if in.RemoveVolumes {
		audit.SetDetail(ctx, "removeVolumes", true)
	}
	return Accepted(j), nil
}

type deployStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	Body *struct {
		Pull                string   `json:"pull,omitempty" example:"missing" enum:"missing,always" doc:"missing (default): pull only images that are not on the host; always: pull every image first (with build, the build also pulls newer base images)."`
		Build               bool     `json:"build,omitempty" doc:"Rebuild every build section (default: only missing images are built)."`
		ForceRecreate       bool     `json:"forceRecreate,omitempty" doc:"Recreate the containers even when their configuration and image did not change (with services, only theirs: dependencies are recreated only when they changed). Anonymous volumes are kept."`
		RemoveOrphans       bool     `json:"removeOrphans,omitempty" doc:"Also remove the containers of services that are no longer in the Compose file (orphans, reported as drift unexpected_service). A deploy without it keeps them."`
		Services            []string `json:"services,omitempty" example:"web" maxItems:"64" doc:"Deploy only these services (and their dependencies)."`
		TimeoutSeconds      int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period for recreated containers."`
		BuildTimeoutSeconds int      `json:"buildTimeoutSeconds,omitempty" minimum:"0" maximum:"21600" doc:"Bounds the images the deploy builds (default 3600)."`
	}
}

func (h *stacksAPI) deploy(ctx context.Context, in *deployStackInput) (*JobAccepted, error) {
	_, p, st, _, err := h.requireStack(ctx, in.StackID, CapStackDeploy)
	if err != nil {
		return nil, err
	}
	r := domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey}
	var o domain.StackDeployOptions
	if b := in.Body; b != nil {
		r.Services, r.TimeoutSeconds = b.Services, b.TimeoutSeconds
		o = domain.StackDeployOptions{Pull: b.Pull, Build: b.Build, ForceRecreate: b.ForceRecreate, RemoveOrphans: b.RemoveOrphans,
			BuildTimeoutSeconds: b.BuildTimeoutSeconds}
	}
	j, err := h.svc.Deploy(ctx, p, st, r, o)
	if err != nil {
		return nil, stackErr(err)
	}
	return Accepted(j), nil
}

type pullStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
}

func (h *stacksAPI) pull(ctx context.Context, in *pullStackInput) (*JobAccepted, error) {
	_, p, st, _, err := h.requireStack(ctx, in.StackID, CapStackUpdate)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.Pull(ctx, p, st, domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return nil, stackErr(err)
	}
	return Accepted(j), nil
}

type buildStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	Body *struct {
		Services       []string `json:"services,omitempty" example:"web" maxItems:"64" doc:"Build only these services (each needs a build section); default: every service with a build section."`
		NoCache        bool     `json:"noCache,omitempty" doc:"Build without the build cache."`
		Pull           bool     `json:"pull,omitempty" doc:"Pull newer versions of the base images."`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"21600" doc:"Stops the build with a failure after this long (default 3600)."`
		RegistryIDs    []string `json:"registryIds,omitempty" maxItems:"16" doc:"Registry connections for base images; default: the environment's host-wide connection per registry."`
	}
}

func (h *stacksAPI) build(ctx context.Context, in *buildStackInput) (*JobAccepted, error) {
	_, p, st, _, err := h.requireStack(ctx, in.StackID, CapStackBuild)
	if err != nil {
		return nil, err
	}
	r := domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey}
	var o domain.StackBuildOptions
	if b := in.Body; b != nil {
		r.Services = b.Services
		o = domain.StackBuildOptions{NoCache: b.NoCache, Pull: b.Pull, TimeoutSeconds: b.TimeoutSeconds, RegistryIDs: b.RegistryIDs}
		// Names and options only: build argument values live in the
		// Compose files and are never audited (#30, #33).
		if len(b.Services) > 0 {
			audit.SetDetail(ctx, "services", b.Services)
		}
	}
	j, err := h.svc.Build(ctx, p, st, r, o)
	if err != nil {
		return nil, stackErr(err)
	}
	return Accepted(j), nil
}

type operateStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	Body struct {
		Action         string   `json:"action,omitempty" example:"restart" enum:"start,stop,restart,down" doc:"Required; selects the capability: stack.start, stack.stop, stack.restart or stack.down."`
		Services       []string `json:"services,omitempty" example:"web" maxItems:"64" doc:"Only these services (start/stop/restart); dependencies and restart: true dependents follow the lifecycle rules."`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period."`
	}
}

func (h *stacksAPI) operate(ctx context.Context, in *operateStackInput) (*JobAccepted, error) {
	if _, ok := map[string]bool{"start": true, "stop": true, "restart": true, "down": true}[in.Body.Action]; !ok {
		// Unknown action: answer 422 only to callers holding one of the
		// operation capabilities (the answer must not reveal more).
		_, _, _, v, err := h.visibleStack(ctx, in.StackID)
		if err != nil {
			return nil, err
		}
		if !v.Has("stack.start") && !v.Has("stack.stop") && !v.Has("stack.restart") && !v.Has("stack.down") {
			return nil, Forbidden("not permitted to operate this stack")
		}
		return nil, Invalid("action must be start, stop, restart or down", Field("body.action", "start, stop, restart or down"))
	}
	cp := Capability("stack." + in.Body.Action)
	audit.SetAction(ctx, string(cp))
	_, p, st, _, err := h.requireStack(ctx, in.StackID, cp)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.Operate(ctx, p, st, in.Body.Action, domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey,
		Services: in.Body.Services, TimeoutSeconds: in.Body.TimeoutSeconds})
	if err != nil {
		return nil, stackErr(err)
	}
	return Accepted(j), nil
}

type restoreStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	Body    struct {
		RevisionID string `json:"revisionId,omitempty" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f" maxLength:"64" doc:"Required: the revision to write back to disk."`
	}
}

// StackRestoreResult is the answer of a revision restore.
type StackRestoreResult struct {
	Stack    Stack         `json:"stack"`
	Revision StackRevision `json:"revision" doc:"The new revision (source restore) now on disk."`
	// DeployOffered: deploy it with POST /stacks/{stackId}/deployments.
	DeployOffered bool `json:"deployOffered" doc:"The restored definition differs from the applied revision: offer a deploy (never started automatically)."`
}

type restoreStackOutput struct{ Body StackRestoreResult }

func (h *stacksAPI) restore(ctx context.Context, in *restoreStackInput) (*restoreStackOutput, error) {
	_, p, st, v, err := h.requireStack(ctx, in.StackID, CapStackDefinitionWrite)
	if err != nil {
		return nil, err
	}
	if in.Body.RevisionID == "" {
		return nil, Invalid("revisionId is required", Field("body.revisionId", "required"))
	}
	res, err := h.svc.Restore(ctx, p, st, in.Body.RevisionID)
	if err != nil {
		return nil, stackErr(err)
	}
	audit.SetDetail(ctx, "restoredRevision", in.Body.RevisionID)
	audit.SetDetail(ctx, "newRevision", res.Revision.Seq)
	return &restoreStackOutput{Body: StackRestoreResult{Stack: newStack(res.Stack, v, true), Revision: newRevision(res.Revision, res.Stack, false),
		DeployOffered: res.DeployOffered}}, nil
}

type listRevisionsInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	PageParams
}

type listRevisionsOutput struct{ Body Page[StackRevision] }

type revisionsCursor struct {
	Before int64 `json:"b"`
}

func (h *stacksAPI) listRevisions(ctx context.Context, in *listRevisionsInput) (*listRevisionsOutput, error) {
	_, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackDefinitionRead)
	if err != nil {
		return nil, err
	}
	fingerprint := QueryFingerprint("stack-revisions", st.ID)
	var cur revisionsCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fingerprint, &cur); err != nil {
			return nil, err
		}
	}
	limit := in.PageLimit()
	revs, err := h.svc.Revisions(ctx, st.ID, cur.Before, limit+1)
	if err != nil {
		return nil, Internal(err)
	}
	cursor := ""
	if len(revs) > limit {
		revs = revs[:limit]
		if cursor, err = CursorFor(fingerprint, revisionsCursor{Before: revs[len(revs)-1].Seq}); err != nil {
			return nil, Internal(err)
		}
	}
	items := make([]StackRevision, 0, len(revs))
	for _, r := range revs {
		items = append(items, newRevision(r, st, false))
	}
	return &listRevisionsOutput{Body: NewPage(items, cursor, nil)}, nil
}

type revisionInput struct {
	StackID    string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	RevisionID string `path:"revisionId" maxLength:"64" doc:"Revision ID."`
}

type revisionOutput struct{ Body StackRevision }

func (h *stacksAPI) getRevision(ctx context.Context, in *revisionInput) (*revisionOutput, error) {
	_, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackDefinitionRead)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.Revision(ctx, st.ID, in.RevisionID)
	if err != nil {
		return nil, stackErr(err)
	}
	return &revisionOutput{Body: newRevision(r, st, true)}, nil
}

// StackPort is a published container port.
type StackPort struct {
	PrivatePort uint16 `json:"privatePort"`
	PublicPort  uint16 `json:"publicPort,omitempty"`
	HostIP      string `json:"hostIp,omitempty"`
	Protocol    string `json:"protocol"`
}

// StackContainer is a service container. Identity and state are shown
// with any view of the stack; image, ports, restart policy and resources
// need container.details.read on the container.
type StackContainer struct {
	ID            string             `json:"id,omitempty"`
	Name          string             `json:"name,omitempty"`
	State         string             `json:"state" enum:"created,running,paused,restarting,removing,exited,dead"`
	Health        string             `json:"health,omitempty"`
	View          string             `json:"view" enum:"minimal,full"`
	Image         string             `json:"image,omitempty"`
	ImageID       string             `json:"imageId,omitempty"`
	ExitCode      *int               `json:"exitCode,omitempty"`
	RestartPolicy string             `json:"restartPolicy,omitempty"`
	Ports         []StackPort        `json:"ports,omitempty"`
	NanoCPUs      int64              `json:"nanoCpus,omitempty" doc:"CPU limit in 1e-9 CPUs (0: unlimited)."`
	CPUShares     int64              `json:"cpuShares,omitempty"`
	Memory        int64              `json:"memory,omitempty" doc:"Memory limit in bytes (0: unlimited)."`
	PidsLimit     *int64             `json:"pidsLimit,omitempty"`
	StartedAt     *time.Time         `json:"startedAt,omitempty"`
	Networks      []ContainerNetwork `json:"networks,omitempty" doc:"The container's addresses per network (empty while it is stopped). Full view."`
	Volumes       []StackVolumeMount `json:"volumes,omitempty" doc:"The volumes the container mounts, by destination (never bind mounts; absent from older agents). Full view."`
}

// StackVolumeMount is a volume a stack container mounts.
type StackVolumeMount struct {
	Name        string `json:"name" example:"shop_db-data" doc:"Volume name."`
	Destination string `json:"destination" example:"/var/lib/postgresql/data" doc:"Path inside the container."`
	ReadOnly    bool   `json:"readOnly,omitempty"`
	Anonymous   bool   `json:"anonymous,omitempty" doc:"A volume the Engine created for an anonymous mount (its name is a random ID)."`
}

// StackServiceStatus is a service with its containers and drift.
type StackServiceStatus struct {
	Name        string            `json:"name"`
	Image       string            `json:"image,omitempty" doc:"Image of the definition."`
	Build       bool              `json:"build"`
	DependsOn   []StackDependency `json:"dependsOn"`
	Description string            `json:"description,omitempty"`
	Icon        string            `json:"icon,omitempty" deprecated:"true" doc:"Deprecated, never returned: services have no icon of their own (the web shows one service icon)."`
	Applied     *StackImage       `json:"applied,omitempty" doc:"Image applied by the last deploy."`
	Status      string            `json:"status" enum:"running,partial,exited,created,missing"`
	Containers  []StackContainer  `json:"containers"`
	Drift       []string          `json:"drift" doc:"missing (no container), not_running (stopped or exited, not a finished one-shot), running_while_stopped (runs although the stack was stopped, also with down), unexpected_service (an orphan: the service is no longer in the deployed definition but its containers are still on the host; a deploy with removeOrphans removes them), image_changed (runs another image than the last deploy applied)."`
	// Metrics are #5's (per-service metrics are not part of this response yet).
}

// StackServices is GET /stacks/{stackId}/services.
type StackServices struct {
	Live       bool                 `json:"live" doc:"Read from the Engine now; false: the environment is offline and the last observed state is shown."`
	ObservedAt *time.Time           `json:"observedAt,omitempty"`
	Drift      bool                 `json:"drift" doc:"The Engine state differs from what Docker Manager last applied."`
	Services   []StackServiceStatus `json:"services"`
}

type servicesOutput struct{ Body StackServices }

func (h *stacksAPI) services(ctx context.Context, in *stackIDInput) (*servicesOutput, error) {
	c, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackRead)
	if err != nil {
		return nil, err
	}
	view, err := h.svc.Services(ctx, st)
	if err != nil {
		return nil, stackErr(err)
	}
	out := StackServices{Live: view.Live, ObservedAt: view.ObservedAt, Drift: view.Drift, Services: []StackServiceStatus{}}
	for _, sv := range view.Services {
		s := StackServiceStatus{Name: sv.Name, Description: sv.Meta.Description, Status: sv.Status,
			Containers: []StackContainer{}, Drift: append([]string{}, sv.Drift...), DependsOn: []StackDependency{}}
		if e := sv.Expected; e != nil {
			s.Image, s.Build = e.Image, e.Build
			for _, d := range e.DependsOn {
				s.DependsOn = append(s.DependsOn, StackDependency(d))
			}
		}
		if a := sv.Applied; a != nil {
			img := StackImage(*a)
			s.Applied = &img
		}
		for _, ct := range sv.Containers {
			s.Containers = append(s.Containers, shapeContainer(c, st, sv.Name, ct))
		}
		out.Services = append(out.Services, s)
	}
	return &servicesOutput{Body: out}, nil
}

func shapeContainer(c authz.Checker, st domain.Stack, service string, ct domain.StackContainer) StackContainer {
	out := StackContainer{ID: ct.ID, Name: ct.Name, State: ct.State, Health: ct.Health, View: authz.Minimal.String()}
	if ct.Name == "" {
		return out // offline summary
	}
	res := authz.Resource{Type: catalog.TypeContainer, ID: ct.Name, EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{
		{Type: catalog.TypeService, ID: authz.ServiceID(st.ID, service)}, {Type: catalog.TypeStack, ID: st.ID}}}
	if !c.Can(capContainerDetailsRead, res).Allowed {
		return out
	}
	code := ct.ExitCode
	out.View = authz.Full.String()
	out.Image, out.ImageID, out.ExitCode, out.RestartPolicy, out.StartedAt = ct.Image, ct.ImageID, &code, ct.RestartPolicy, ct.StartedAt
	out.NanoCPUs, out.CPUShares, out.Memory, out.PidsLimit = ct.NanoCPUs, ct.CPUShares, ct.Memory, ct.PidsLimit
	for _, p := range ct.Ports {
		out.Ports = append(out.Ports, StackPort(p))
	}
	for _, n := range ct.Networks {
		out.Networks = append(out.Networks, ContainerNetwork{Name: n.Network, IPAddress: n.IPv4, IPv6Address: n.IPv6})
	}
	for _, v := range ct.Volumes {
		out.Volumes = append(out.Volumes, StackVolumeMount(v))
	}
	return out
}

// StackImageStatus is one service's applied image and update eligibility.
type StackImageStatus struct {
	Service  string `json:"service" example:"web"`
	Image    string `json:"image" example:"nginx:1.27"`
	ImageID  string `json:"imageId,omitempty"`
	Digest   string `json:"digest,omitempty" doc:"Digest applied on this host by the last deploy (#20 baseline)."`
	Platform string `json:"platform,omitempty"`
	Build    bool   `json:"build"`
	Eligible bool   `json:"eligible" doc:"The service could follow its tag's digest (#20)."`
	Reason   string `json:"reason,omitempty" enum:"build_only,digest_pinned,untagged,pull_policy_conflict,invalid_reference"`
	// ReasonMessage explains the reason (or warns about a non-version tag).
	ReasonMessage string `json:"reasonMessage,omitempty"`
	NonVersionTag bool   `json:"nonVersionTag" doc:"Eligible, but the tag (latest, main, ...) can change meaning."`
	// Update is the state of the stack's update policy for the service (#20).
	Update          string     `json:"update" enum:"no_policy,ineligible,unchecked,up_to_date,update_available,quarantined,check_failed,run_failed" doc:"Update state of the service under the stack's update policy; no_policy when the stack has none."`
	PolicyID        string     `json:"policyId,omitempty" doc:"The stack's update policy."`
	CandidateDigest string     `json:"candidateDigest,omitempty" doc:"The registry's newer host-platform digest (update_available, quarantined)."`
	CheckedAt       *time.Time `json:"checkedAt,omitempty"`
	// Pulled* report a newer image a pull without deploy left on the host.
	PulledImageID string     `json:"pulledImageId,omitempty" doc:"A newer image for the service's reference is on the host (POST /stacks/{id}/pulls); deploy the stack to run it."`
	PulledDigest  string     `json:"pulledDigest,omitempty"`
	PulledAt      *time.Time `json:"pulledAt,omitempty"`
}

type imageStatusOutput struct {
	Body struct {
		Images []StackImageStatus `json:"images"`
	}
}

func (h *stacksAPI) imageStatus(ctx context.Context, in *stackIDInput) (*imageStatusOutput, error) {
	_, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackRead)
	if err != nil {
		return nil, err
	}
	out := &imageStatusOutput{}
	out.Body.Images = []StackImageStatus{}
	policyID := ""
	cands := map[string]domain.UpdateCandidate{}
	if u := h.deps.Updates; u != nil {
		p, err := u.ForTarget(ctx, st.EnvironmentID, domain.UpdateTargetStack, st.ID)
		if err != nil {
			return nil, Internal(err)
		}
		if p != nil {
			policyID = p.ID
			list, err := u.Candidates(ctx, p.ID)
			if err != nil {
				return nil, Internal(err)
			}
			for _, c := range list {
				cands[c.Service] = c
			}
		}
	}
	for _, i := range h.svc.ImageStatus(st) {
		s := StackImageStatus{Service: i.Service, Image: i.Image, ImageID: i.ImageID, Digest: i.Digest, Platform: i.Platform, Build: i.Build,
			Eligible: i.Eligible, Reason: i.Reason, ReasonMessage: i.ReasonMessage, NonVersionTag: i.NonVersionTag, Update: "no_policy",
			PolicyID: policyID, PulledImageID: i.PulledImageID, PulledDigest: i.PulledDigest, PulledAt: i.PulledAt}
		if policyID != "" {
			s.Update = string(domain.CandidateUnchecked)
			if c, ok := cands[i.Service]; ok {
				s.Update, s.CandidateDigest, s.CheckedAt = string(c.Status), c.CandidateDigest, c.CheckedAt
			}
		}
		out.Body.Images = append(out.Body.Images, s)
	}
	return out, nil
}

// composeProjectAttribute is the Compose project label of relayed Docker
// events.
const composeProjectAttribute = "com.docker.compose.project"

// StackEvent is one entry of a stack's event stream.
type StackEvent struct {
	Seq        uint64            `json:"seq"`
	Type       string            `json:"type" enum:"stack.created,stack.updated,stack.removed,stack.revision_recorded,docker.event"`
	StackID    string            `json:"stackId"`
	ResourceID string            `json:"resourceId,omitempty" doc:"The container of an engine event."`
	At         time.Time         `json:"at"`
	Attributes map[string]string `json:"attributes,omitempty" doc:"Small facts (job ID and kind, revision source and number); never file contents."`
}

func (h *stacksAPI) stream(ctx context.Context, in *stackIDInput) (*huma.StreamResponse, error) {
	c, _, st, v, err := h.requireStack(ctx, in.StackID, CapStackRead)
	if err != nil {
		return nil, err
	}
	if h.deps.Events == nil {
		return nil, Unavailable(CodeUnavailable, "the event bus is not available")
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		h.runStream(hctx, c, st, v)
	}}, nil
}

// runStream writes `event: stack` with the current stack, then every stack
// event of this stack as it happens (filtered by the caller's view), with
// heartbeats; it ends after stack.removed.
func (h *stacksAPI) runStream(hctx huma.Context, c authz.Checker, st domain.Stack, v authz.View) {
	ctx := hctx.Context()
	stream := StartSSE(hctx)
	defer stream.CloseIfRevoked(ctx)
	sub := h.deps.Events.Subscribe(0, func(e events.Event) bool {
		switch {
		case e.ResourceType == events.ResourceStack:
			return e.ResourceID == st.ID
		case e.Type == events.DockerEvent:
			// Engine events of the stack's containers, when the agent
			// relays the Compose project label (#5).
			return e.EnvironmentID == st.EnvironmentID && e.Attributes[composeProjectAttribute] == st.Name
		}
		return false
	})
	defer sub.Close()
	hb := h.deps.clock().NewTicker(h.heartbeat)
	defer hb.Stop()
	if stream.Event("stack", "", newStack(st, v, h.svc.Online(ctx, st.EnvironmentID))) != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C():
			if stream.Heartbeat() != nil {
				return
			}
		case e := <-sub.C():
			if !authz.EventVisible(c, e) {
				continue
			}
			name, data := e.Type, StackEvent{Seq: e.Seq, Type: e.Type, StackID: st.ID, At: e.At, Attributes: e.Attributes}
			if e.Type == events.DockerEvent {
				name, data.ResourceID = "engine", e.ResourceID
			}
			if stream.Event(name, strconv.FormatUint(e.Seq, 10), data) != nil {
				return
			}
			if e.Type == events.StackRemoved {
				return
			}
		}
	}
}

// DiscoveredStackService is a service of a discovered project.
type DiscoveredStackService struct {
	Name       string `json:"name"`
	Image      string `json:"image"`
	Containers int    `json:"containers"`
	Running    int    `json:"running"`
}

// DiscoveredStack is a Compose project found on the Engine (read-only).
type DiscoveredStack struct {
	Name       string                   `json:"name" example:"nextcloud" doc:"Compose project name."`
	WorkingDir string                   `json:"workingDir,omitempty" example:"nextcloud" doc:"Project directory from the containers' labels, or the folder a containerless project was found in (host path)."`
	Location   *StackLocation           `json:"location,omitempty" doc:"Where it lies under a verified stack root (adoptable in place)."`
	Services   []DiscoveredStackService `json:"services"`
	Adoptable  bool                     `json:"adoptable" doc:"Can be imported in place from its real files."`
	Copyable   bool                     `json:"copyable" doc:"Not adoptable in place, but the agent reads its directory through an import mount (below /import): it can be imported by copy (POST .../stacks/import-copies)."`
	SourceDir  string                   `json:"sourceDir,omitempty" example:"/var/lib/docker/volumes/arcane_data/_data/projects/nextcloud" doc:"Where a copyable project's files are on the host: workingDir, or the path a Compose manager running in a container (Arcane, Dockge, ...) saw, translated through its mounts."`
	Reason     string                   `json:"reason,omitempty" doc:"Why it cannot be adopted in place (import it by copy or with an explicit Compose source)."`
	StackID    string                   `json:"stackId,omitempty" doc:"The Docker Manager stack already managing it."`
	Protected  bool                     `json:"protected,omitempty" doc:"Docker Manager's own Compose project (#32): an import by copy copies it while it runs and restarts nothing; its next deploy moves it onto the copy."`
	// Containerless and Volumes are absent from older agents.
	Containerless bool     `json:"containerless,omitempty" doc:"The project has no containers (never started, or brought down with Compose down): found through its Compose file in a folder of a stack root or an import mount, its services come from that file. Its import starts nothing and leaves the stack undeployed until its first deploy."`
	Volumes       []string `json:"volumes,omitempty" example:"[\"nextcloud_db\"]" doc:"The project's existing named Docker volumes (sorted, at most 100; Docker Manager's own left out). The import keeps the project name, so its first deploy reuses them."`
}

type discoveredInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
}

type discoveredOutput struct {
	Body struct {
		Projects []DiscoveredStack `json:"projects"`
	}
}

func (h *stacksAPI) discovered(ctx context.Context, in *discoveredInput) (*discoveredOutput, error) {
	if _, err := h.requireInEnvironment(ctx, in.EnvironmentID, CapStackImport); err != nil {
		return nil, err
	}
	list, err := h.svc.Discovered(ctx, in.EnvironmentID)
	if err != nil {
		return nil, stackErr(err)
	}
	out := &discoveredOutput{}
	out.Body.Projects = []DiscoveredStack{}
	for _, d := range list {
		ds := DiscoveredStack{Name: d.Name, WorkingDir: d.WorkingDir, Services: []DiscoveredStackService{}, Adoptable: d.Adoptable,
			Copyable: d.Copyable, SourceDir: d.SourceDir, Reason: d.Reason, StackID: d.StackID, Protected: d.Protected,
			Containerless: d.Containerless, Volumes: d.Volumes}
		if d.Root != "" {
			ds.Location = &StackLocation{Root: d.Root, Dir: d.Dir}
		}
		for _, s := range d.Services {
			ds.Services = append(ds.Services, DiscoveredStackService(s))
		}
		out.Body.Projects = append(out.Body.Projects, ds)
	}
	return out, nil
}

type importStackInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	Body          struct {
		ProjectName string `json:"projectName,omitempty" example:"nextcloud" maxLength:"63" doc:"Required: the discovered Compose project to adopt."`
		DisplayName string `json:"displayName,omitempty" maxLength:"128"`
		Description string `json:"description,omitempty" maxLength:"1024"`
		Icon        string `json:"icon,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated and ignored: stacks have no icon of their own."`
		// Source is the explicit Compose source for projects that cannot be
		// adopted in place.
		Source *StackDefinitionBody `json:"source,omitempty" doc:"Explicit Compose source (written into a new directory of the stacks volume); omit to adopt the project in place."`
	}
}

type importStackOutput struct {
	ETagHeader
	Location string `header:"Location"`
	Body     Stack
}

func (h *stacksAPI) importStack(ctx context.Context, in *importStackInput) (*importStackOutput, error) {
	p, err := h.requireInEnvironment(ctx, in.EnvironmentID, CapStackImport)
	if err != nil {
		return nil, err
	}
	r := domain.StackImport{EnvironmentID: in.EnvironmentID, ProjectName: in.Body.ProjectName, DisplayName: in.Body.DisplayName,
		Meta: domain.DisplayMeta{Description: in.Body.Description}}
	if in.Body.Source != nil {
		r.Files = in.Body.Source.files()
	}
	st, err := h.svc.Import(ctx, p, r)
	if err != nil {
		return nil, stackErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID})
	audit.SetDetail(ctx, "project", st.Name)
	audit.SetDetail(ctx, "inPlace", in.Body.Source == nil)
	logging.FromContext(ctx).Info("stack imported", "stack_id", st.ID, "project", st.Name)
	c, _, _ := h.checker(ctx)
	return &importStackOutput{Location: BasePath + "/stacks/" + st.ID, ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)},
		Body: newStack(st, authz.ViewOf(c, stackResource(st)), true)}, nil
}

type importCopyInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		ProjectName    string `json:"projectName,omitempty" example:"nextcloud" maxLength:"63" doc:"Required: the discovered Compose project to import (copyable in the discovery list)."`
		DisplayName    string `json:"displayName,omitempty" maxLength:"128"`
		Description    string `json:"description,omitempty" maxLength:"1024"`
		Icon           string `json:"icon,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated and ignored: stacks have no icon of their own."`
		TimeoutSeconds int    `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period of the project's services (default: each service's own)."`
	}
}

func (h *stacksAPI) importCopy(ctx context.Context, in *importCopyInput) (*JobAccepted, error) {
	p, err := h.requireInEnvironment(ctx, in.EnvironmentID, CapStackImport)
	if err != nil {
		return nil, err
	}
	r := domain.StackImport{EnvironmentID: in.EnvironmentID, ProjectName: in.Body.ProjectName, DisplayName: in.Body.DisplayName,
		Meta: domain.DisplayMeta{Description: in.Body.Description}}
	st, j, err := h.svc.ImportCopy(ctx, p, r, domain.StackJobRequest{TimeoutSeconds: in.Body.TimeoutSeconds})
	if err != nil {
		return nil, stackErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID})
	audit.SetDetail(ctx, "project", st.Name)
	audit.SetDetail(ctx, "jobId", j.ID)
	logging.FromContext(ctx).Info("stack import by copy requested", "stack_id", st.ID, "project", st.Name, "job_id", j.ID)
	return Accepted(j), nil
}

// StackRenameVolume is a volume a stack rename moves to the new project's
// name.
type StackRenameVolume struct {
	Key     string `json:"key,omitempty" example:"data" doc:"Compose key of a named volume (absent for an anonymous volume)."`
	Name    string `json:"name" example:"shop_data" doc:"Current volume name."`
	NewName string `json:"newName,omitempty" example:"store_data" doc:"Name after the rename (an anonymous volume's is only known once its container is recreated)."`
	Service string `json:"service,omitempty" doc:"Service of an anonymous volume's mount."`
	Target  string `json:"target,omitempty" doc:"Mount path of an anonymous volume."`
	Action  string `json:"action" enum:"move,recreate,absent" doc:"move: the data moves to a volume with the new name (same disk, nothing is copied); recreate: a volume with driver options (host path, NFS, ...) is recreated with the same options under the new name, the data stays where they point; absent: the volume does not exist yet and is created on the first start."`
}

// StackRenameContainer is a container outside the stack that mounts a
// volume the rename moves.
type StackRenameContainer struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name,omitempty" doc:"Absent when the caller cannot see the container (hidden is true)."`
	Hidden  bool     `json:"hidden,omitempty" doc:"The caller cannot see this container; the rename is blocked unless the caller may stop and remove it (container.stop, container.remove)."`
	Running bool     `json:"running" doc:"It runs now: it is stopped, recreated on the new volume names and started again."`
	Volumes []string `json:"volumes" doc:"The moved volumes it mounts (current names)."`
}

// StackRenamePreview is what renaming a stack's Compose project does,
// computed before anything changes.
type StackRenamePreview struct {
	From         string                 `json:"from" example:"shop" doc:"Current Compose project name."`
	To           string                 `json:"to" example:"store" doc:"New Compose project name."`
	FromDir      string                 `json:"fromDir" example:"shop" doc:"Current project directory (relative to its root)."`
	ToDir        string                 `json:"toDir" example:"store" doc:"Project directory after the rename: renamed in place when it is named after the project, else unchanged."`
	DeclaredName string                 `json:"declaredName,omitempty" doc:"The top-level name: the Compose files set. Such a stack is renamed by changing name: in its files (a blocker unless it is the requested name)."`
	Running      []string               `json:"running" doc:"Services that run now: they stop and start again under the new name."`
	Volumes      []StackRenameVolume    `json:"volumes" doc:"Volumes of the stack and what happens to each."`
	Containers   []StackRenameContainer `json:"containers" doc:"Containers outside the stack that mount a moved volume: stopped and recreated on the new names with their full configuration."`
	Blockers     []StackIssue           `json:"blockers" doc:"Reasons the rename is refused (nothing changes); empty when it can run."`
	Warnings     []StackIssue           `json:"warnings"`
}

func newRenamePreview(c authz.Checker, env string, p domain.StackRenamePlan) StackRenamePreview {
	out := StackRenamePreview{From: p.From, To: p.To, FromDir: p.FromDir, ToDir: p.ToDir, DeclaredName: p.DeclaredName,
		Running: []string{}, Volumes: []StackRenameVolume{}, Containers: []StackRenameContainer{}, Blockers: []StackIssue{}, Warnings: []StackIssue{}}
	out.Running = append(out.Running, p.Running...)
	for _, v := range p.Volumes {
		out.Volumes = append(out.Volumes, StackRenameVolume{Key: v.Key, Name: v.Name, NewName: v.NewName, Service: v.Service, Target: v.Target, Action: v.Action})
	}
	for _, ct := range p.Containers {
		rc := StackRenameContainer{Running: ct.Running, Volumes: append([]string{}, ct.Volumes...)}
		// Containers outside the stack are named only to callers who may
		// see them (#17); the others are counted.
		res := authz.Resource{Type: catalog.TypeContainer, ID: ct.Name, EnvironmentID: env}
		if authz.ViewOf(c, res).Visible() {
			rc.ID, rc.Name = ct.ID, ct.Name
		} else {
			rc.Hidden = true
		}
		out.Containers = append(out.Containers, rc)
		// The rename refuses to recreate a container the caller may not
		// stop and remove (Service.Rename, MayRecreate).
		if !c.Can(capContainerStop, res).Allowed || !c.Can(capContainerRemove, res).Allowed {
			out.Blockers = append(out.Blockers, StackIssue{Code: "container_not_permitted",
				Message: "a container outside the stack that uses volume " + strings.Join(ct.Volumes, ", ") +
					" would be stopped and recreated, and you may not do that (it needs container.stop and container.remove on it)"})
		}
	}
	for _, i := range p.Blockers {
		out.Blockers = append(out.Blockers, StackIssue(i))
	}
	for _, i := range p.Warnings {
		out.Warnings = append(out.Warnings, StackIssue(i))
	}
	return out
}

// StackRenameBody names a stack's new Compose project name.
type StackRenameBody struct {
	Name string `json:"name,omitempty" example:"store" maxLength:"63" doc:"Required: the new Compose project name (lower-case letters, digits, '-' and '_', starting with a letter or digit)."`
}

type renamePreviewInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	Body    StackRenameBody
}

type renamePreviewOutput struct{ Body StackRenamePreview }

func (h *stacksAPI) previewRename(ctx context.Context, in *renamePreviewInput) (*renamePreviewOutput, error) {
	c, _, st, _, err := h.requireStack(ctx, in.StackID, CapStackRename)
	if err != nil {
		return nil, err
	}
	plan, err := h.svc.PreviewRename(ctx, st, in.Body.Name)
	if err != nil {
		return nil, stackErr(err)
	}
	return &renamePreviewOutput{Body: newRenamePreview(c, st.EnvironmentID, plan)}, nil
}

type renameStackInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IfMatchParam
	IdempotencyKeyParam
	Body struct {
		StackRenameBody
		TimeoutSeconds int `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"Stop grace period of the stack's services and the recreated containers (default: each one's own)."`
	}
}

func (h *stacksAPI) rename(ctx context.Context, in *renameStackInput) (*JobAccepted, error) {
	c, p, st, _, err := h.requireStack(ctx, in.StackID, CapStackRename)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(st.Revision)); err != nil {
		return nil, err
	}
	// Names only (#30): the Compose project's old and new name.
	audit.SetDetail(ctx, "fromName", st.Name)
	audit.SetDetail(ctx, "toName", in.Body.Name)
	// Outside containers the rename recreates need the caller's own rights
	// on them: stack.rename alone never stops or replaces them.
	may := func(ct domain.StackRenameContainer) bool {
		res := authz.Resource{Type: catalog.TypeContainer, ID: ct.Name, EnvironmentID: st.EnvironmentID}
		return c.Can(capContainerStop, res).Allowed && c.Can(capContainerRemove, res).Allowed
	}
	j, err := h.svc.Rename(ctx, p, st, in.Body.Name, domain.StackJobRequest{IdempotencyKey: in.IdempotencyKey,
		TimeoutSeconds: in.Body.TimeoutSeconds, MayRecreate: may})
	if err != nil {
		return nil, stackErr(err)
	}
	audit.SetDetail(ctx, "jobId", j.ID)
	logging.FromContext(ctx).Info("stack rename requested", "stack_id", st.ID, "from", st.Name, "to", in.Body.Name, "job_id", j.ID)
	return Accepted(j), nil
}

func registerStacks(a huma.API, deps Deps) {
	h := &stacksAPI{svc: deps.Stacks, authz: authz.OrDenyAll(deps.Authorizer), deps: deps, heartbeat: deps.SSEHeartbeat}
	if h.heartbeat <= 0 {
		h.heartbeat = DefaultSSEHeartbeat
	}
	read := []int{http.StatusUnauthorized, http.StatusNotFound}
	mutate := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	jobErrs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}
	stacks := BasePath + "/stacks"
	one := stacks + "/{stackId}"
	registerStackTemplates(a, h)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-stacks", Method: http.MethodGet, Path: stacks, Summary: "List stacks",
		Description: "Managed Compose stacks in creation order, filtered per item (#17): stack.read shows a stack in full, any other " +
			"capability on it (or inside it) only its identity and status. Pages may hold fewer items than limit; follow nextCursor.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, Capability: CapStackRead, Scope: ScopeResource}, h.list)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack", Method: http.MethodPost, Path: stacks, Summary: "Create a stack", DefaultStatus: http.StatusCreated,
		Description: "Validates the definition on the environment's agent, writes compose.yaml (and the optional override and .env) into " +
			"a new project directory <name> of the environment's stacks volume and records it as the first revision. Nothing existing " +
			"is overwritten: 409 stack_name_taken (Docker Manager stack), compose_project_exists (a Compose project of that name runs on the " +
			"Engine: import it), stack_directory_exists (the directory exists). 422 invalid_definition lists the findings. Does not deploy.",
		Tags: []string{tagStacks}, Errors: mutate,
	}, Capability: CapStackCreate, Scope: ScopeEnvironment}, h.create)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-stack", Method: http.MethodGet, Path: one, Summary: "Get a stack",
		Description: "The stack's deployment status (what Docker Manager last did), last applied revision and images, the newest revision " +
			"observed on disk (undeployedChanges when they differ), the failed revision and recovery guidance after a failed deploy, and " +
			"the Engine state as last observed. While the environment is offline the last known state is returned with readOnly.",
		Tags: []string{tagStacks}, Errors: read,
	}, Capability: CapStackRead, Scope: ScopeResource}, h.get)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "update-stack", Method: http.MethodPatch, Path: one, Summary: "Edit a stack's display metadata",
		Description: "Display name, description, links and per-service descriptions, stored in Docker Manager and " +
			"never written to Compose files. Links are absolute http(s) addresses without credentials (at most 10, each listed once); " +
			"a problem is a 422 naming the field (body.links[1].url). Requires If-Match.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
	}, Capability: CapStackManage, Scope: ScopeResource}, h.update)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-stack", Method: http.MethodDelete, Path: one, Summary: "Delete a stack",
		Description: "Starts a stack.remove job (202): the stack is brought down (Compose down: containers and networks removed; the project directory " +
			"is kept on the host) and, when that succeeds, removed from Docker Manager with its revisions and the permission rules naming " +
			"it. Volumes are kept unless removeVolumes=true, which also removes the volumes the stack owns (never external, other " +
			"projects', in-use or protected ones; each is reported as a job item, kept ones with the reason). removeVolumes needs an " +
			"up-to-date, connected agent (501 agent_unsupported).",
		Tags: []string{tagStacks}, Errors: jobErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackRemove, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.remove)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-validation", Method: http.MethodPost, Path: stacks + "/validations", Summary: "Validate a Compose definition",
		Description: "Validates a definition on the environment's agent as if it were the project <name> in the stacks volume, without " +
			"side effects: syntax and paths, unsupported features (docs/internal/support-matrix.md), warnings for obsolete keys and bind sources " +
			"outside the project directory.",
		Tags: []string{tagStacks}, Errors: mutate,
	}, Capability: CapStackCreate, Scope: ScopeEnvironment}, h.validate)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-definition-validation", Method: http.MethodPost, Path: one + "/validations",
		Summary: "Validate a stack's definition on disk",
		Description: "Validates the stack's current Compose files on the environment's agent in the stack's own project directory, " +
			"exactly as a deploy loads them (explicit Compose files, override, .env, service env_files, relative paths), without " +
			"side effects: nothing is written, no revision is recorded and no job runs. Same result as create-stack-validation " +
			"(findings are the answer, never an error; no file contents or .env values). Needs stack.definition.write (the " +
			"capability that edits those files). 503 environment_offline while the agent is offline.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusNotImplemented, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
	}, Capability: CapStackDefinitionWrite, Scope: ScopeResource, AuditAction: "stack.validate"}, h.validateExisting)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-stack-revisions", Method: http.MethodGet, Path: one + "/revisions", Summary: "List a stack's revisions",
		Description: "Immutable revisions of the definition, newest first: recorded whenever the files change (stack editor, file manager, " +
			"external edit, restore, or a deploy of files no revision holds yet); redeploying unchanged files reuses their revision. Metadata only; GET a revision for its contents. Diffs are computed " +
			"client-side from two revisions' contents.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, Capability: CapStackDefinitionRead, Scope: ScopeResource}, h.listRevisions)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-stack-revision", Method: http.MethodGet, Path: one + "/revisions/{revisionId}", Summary: "Get a stack revision",
		Description: "The revision with the contents of its files (compose, override and env files, which may hold secrets; " +
			"every read is audited). To show a diff, fetch both revisions and compare their files client-side.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapStackDefinitionRead, Scope: ScopeResource, Audit: AuditAlways}, h.getRevision)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-deployment", Method: http.MethodPost, Path: one + "/deployments", Summary: "Deploy a stack",
		Description: "Starts a stack.deploy job (202). The agent deploys the definition on disk when the job runs (up with builds through " +
			"the Engine's BuildKit, dependency order and conditions) and reports the exact bytes it used, which become the applied " +
			"revision. Deploys of one stack serialize (job lock). A failed deploy keeps the last applied revision; nothing is rolled back.",
		Tags: []string{tagStacks}, Errors: jobErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackDeploy, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.deploy)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-pull", Method: http.MethodPost, Path: one + "/pulls", Summary: "Pull a stack's images",
		Description: "Starts a stack.pull job (202): the agent pulls the images of the stack's definition on disk (with the registry " +
			"connections a deploy would use; build-only services are skipped) and changes no container. Services whose reference now " +
			"names another image than the one they run show it in image-status (pulledImageId) until the next deploy runs it. Needs " +
			"stack.update and an up-to-date, connected agent (501 agent_unsupported, 503 environment_offline).",
		Tags: []string{tagStacks}, Errors: append(slices.Clone(jobErrs), http.StatusNotImplemented, http.StatusServiceUnavailable),
		DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackUpdate, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.pull)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-build", Method: http.MethodPost, Path: one + "/builds", Summary: "Build a stack's images",
		Description: "Starts a stack.build job (202): the agent rebuilds the images of the stack's Compose build sections (or of " +
			"the named services) from the definition on disk through the Engine's BuildKit, without deploying them. BuildKit " +
			"progress and build output stream as the job's events (credentials removed); cancel the job to stop the build " +
			"(images built before stay, the interrupted one keeps its previous version). Base images authenticate with the named " +
			"registry connections, or the environment's host-wide connection per registry. Build arguments come from the Compose " +
			"files, end up in the image history and are never audited. Builds per environment are capped (build class).",
		Tags: []string{tagStacks}, Errors: jobErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackBuild, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.build)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-operation", Method: http.MethodPost, Path: one + "/operations", Summary: "Start, stop, restart or bring down a stack",
		Description: "Starts a stack.start/stop/restart/down job (202); the body's action selects the capability. Start, stop and restart " +
			"follow the deployed dependency graph: stop in reverse dependency order, start dependencies first and wait for their " +
			"depends_on conditions, restart propagates to restart: true dependents. Down removes containers and networks, never volumes.",
		Tags: []string{tagStacks}, Errors: jobErrs, DefaultStatus: http.StatusAccepted,
	}, Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start", "stack.stop", "stack.restart", "stack.down"},
		Scope: ScopeResource, Idempotency: IdempotencyJob}, h.operate)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-revision-restore", Method: http.MethodPost, Path: one + "/revision-restores", Summary: "Restore a revision to disk",
		Description: "Writes the revision's bytes back to the project directory (the definition currently on disk is recorded first; " +
			"409 stack_definition_changed if it changes meanwhile) and records a restore revision. It never deploys: when the result " +
			"differs from the applied revision, deployOffered is true. 503 environment_offline while the agent is offline (revisions " +
			"are read-only then).",
		Tags: []string{tagStacks}, Errors: mutate,
	}, Capability: CapStackDefinitionWrite, Scope: ScopeResource}, h.restore)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-stack-services", Method: http.MethodGet, Path: one + "/services", Summary: "List a stack's services",
		Description: "Services of the applied definition joined with their containers on the Engine (live while the environment is " +
			"online, else the last observed state with live false), with drift from Docker Manager's intent. Container image, ports, " +
			"restart policy and resources need container.details.read on the container; per-service metrics are #5's.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway, http.StatusGatewayTimeout},
	}, Capability: CapStackRead, Scope: ScopeResource}, h.services)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-stack-image-status", Method: http.MethodGet, Path: one + "/image-status", Summary: "Get a stack's image status",
		Description: "Images applied by the last deploy (reference, image ID, repository digest, platform) and whether each service " +
			"could follow its tag's digest (#20: build-only, digest-pinned and untagged services are ineligible). Update candidates " +
			"arrive with #20.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapStackRead, Scope: ScopeResource}, h.imageStatus)

	stackSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(Stack{}), true, "")
	eventSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(StackEvent{}), true, "")
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "stream-stack-events", Method: http.MethodGet, Path: one + "/events/stream", Summary: "Stream a stack's events (SSE)",
		Description: "Server-sent events: first `event: stack` with the current Stack, then `stack.updated`, `stack.revision_recorded` " +
			"and `stack.removed` events (id = bus sequence) as they happen, and `engine` events of the stack's containers the caller " +
			"may see; refetch the stack (or its services/revisions) on each. No replay: after a reconnect the new snapshot is the state. " +
			"Comments `: heartbeat` keep the connection alive. Job progress is on /jobs/{jobId}/events/stream.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
		Responses: map[string]*huma.Response{"200": {Description: "Event stream", Content: map[string]*huma.MediaType{
			"text/event-stream": {Schema: &huma.Schema{Description: "A Stack (event: stack), then StackEvents.",
				OneOf: []*huma.Schema{stackSchema, eventSchema}}},
		}}},
	}, Capability: CapStackRead, Scope: ScopeResource}, h.stream)

	envStacks := BasePath + "/environments/{environmentId}/stacks"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-discovered-stacks", Method: http.MethodGet, Path: envStacks + "/discovered", Summary: "List discovered Compose projects",
		Description: "Compose projects the Engine knows from container labels, read-only: whether each can be adopted in place (its " +
			"directory is under the stacks volume or a registered root) and the Docker Manager stack already managing it. Labels never " +
			"reconstruct a Compose source.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
	}, Capability: CapStackImport, Scope: ScopeEnvironment}, h.discovered)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-import", Method: http.MethodPost, Path: envStacks + "/imports", Summary: "Import a Compose project",
		DefaultStatus: http.StatusCreated,
		Description: "Adopts a discovered project. Without source, in place: its real files become the first revision (409 " +
			"stack_not_adoptable when its directory is outside the stacks volume and registered roots). With source, the given " +
			"definition is written into a new directory <projectName> of the stacks volume. Never overwrites: 409 stack_name_taken " +
			"when Docker Manager already manages the project, stack_directory_exists when the directory exists.",
		Tags: []string{tagStacks}, Errors: mutate,
	}, Capability: CapStackImport, Scope: ScopeEnvironment}, h.importStack)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-import-copy", Method: http.MethodPost, Path: envStacks + "/import-copies",
		Summary: "Import a Compose project by copying its directory", DefaultStatus: http.StatusAccepted,
		Description: "Starts a stack.import job (202) for a discovered project whose directory the agent reads through an import " +
			"mount (copyable in the discovery list). The stack is created at once (the job's target, origin imported). The job " +
			"stops the project's running services, copies its whole directory (Compose files and everything next to them, with " +
			"owners, permissions, times, links and extended attributes) into a new directory <projectName> of the stacks volume, " +
			"verifies the copy, recreates the containers from it (anonymous volumes are kept) and starts the services that ran " +
			"before. Docker Manager's own project (protected in the discovery list) is copied while it runs: nothing stops or is " +
			"recreated, and the stack's next deploy moves it onto the copy. The original directory is only read. A failure before " +
			"the containers are recreated changes nothing (the copy is removed, the services start again from the original and the " +
			"stack is forgotten); after that the stack is failed on its copy and a deploy finishes it. 409 stack_not_copyable (no " +
			"import mount, adoptable in place, or the directory exists), stack_name_taken; 501 agent_unsupported for older agents.",
		Tags: []string{tagStacks}, Errors: mutate,
	}, Capability: CapStackImport, Scope: ScopeEnvironment, Idempotency: IdempotencyStored}, h.importCopy)

	renameErrs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusNotImplemented, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-rename-preview", Method: http.MethodPost, Path: one + "/rename-previews", Summary: "Preview a stack rename",
		Description: "Computes what renaming the stack's Compose project to name does, before anything changes: the services that stop " +
			"and start again, each volume and its new name (move: the data moves; recreate: a volume with driver options is recreated " +
			"with the same options; absent: created on the first start), the containers outside the stack that mount a moved volume " +
			"(stopped and recreated on the new names; hidden when the caller cannot see them; blocked unless the caller may stop and remove them), the project directory's new name and " +
			"blockers: the Compose files set a top-level name: (rename such a stack by changing name:; declaredName is the only " +
			"allowed target when it differs from the current name), Docker Manager's own project, a target volume or directory that " +
			"exists, a moved volume another Compose project uses or Docker Manager holds, an outside container on one of the project's " +
			"networks. 409 stack_name_taken when another stack has the name or directory, 422 for an invalid or unchanged name, 501 " +
			"agent_unsupported for older agents, 503 environment_offline. Changes nothing.",
		Tags: []string{tagStacks}, Errors: renameErrs,
	}, Capability: CapStackRename, Scope: ScopeResource, AuditAction: "stack.rename.preview"}, h.previewRename)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-rename", Method: http.MethodPost, Path: one + "/renames", Summary: "Rename a stack's Compose project",
		DefaultStatus: http.StatusAccepted,
		Description: "Re-runs the preview (409 stack_rename_blocked with the blockers in details) and starts a stack.rename job (202). " +
			"The stack stops, its named volumes move to the new project's names (their data follows), containers outside the stack " +
			"that mount them are stopped and recreated on the new names, the project directory is renamed when it is named after the " +
			"project, the old project's containers are removed, the project is created under the new name from the files on disk " +
			"(undeployed changes are applied) and the services that ran start again. A failure before the old containers are removed " +
			"undoes everything; after that the stack has its new name and is failed until a deploy finishes it. The stack keeps its ID, " +
			"revisions, policies and permission rules. Requires If-Match (the stack's ETag). Idempotency-Key replays the first answer " +
			"(the rename changes what the request is checked against). 409 protected for Docker Manager's own project.",
		Tags: []string{tagStacks}, Errors: append(slices.Clone(renameErrs), http.StatusPreconditionFailed, http.StatusPreconditionRequired),
	}, Capability: CapStackRename, Scope: ScopeResource, Idempotency: IdempotencyStored}, h.rename)
}
