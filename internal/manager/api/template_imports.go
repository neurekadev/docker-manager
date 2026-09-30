package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Templates filled from other files (template registry): duplicating a
// version (this instance's or an added registry's) into a new private
// template, restoring a draft to a version, and saving a stack's files as
// a template (a new one, or the draft of an existing one).

// TemplateImportService is the part of the template service these routes
// use (implemented by *templates.Service).
type TemplateImportService interface {
	Duplicate(ctx context.Context, in domain.TemplateInput, registryID, templateID string, version int, userID string) (domain.Template, error)
	RestoreDraft(ctx context.Context, id string, number int) (domain.Template, error)
	CreateFrom(ctx context.Context, in domain.TemplateInput, r io.Reader, userID string) (domain.Template, error)
	ImportDraft(ctx context.Context, id string, r io.Reader) error
}

type importsAPI struct {
	svc        TemplateImportService
	templates  TemplateService
	files      FilesService
	authz      authz.Authorizer
	instanceID string
}

func (h *importsAPI) checker(ctx context.Context) (authz.Checker, authz.Principal, error) {
	if h.svc == nil || h.templates == nil {
		return nil, authz.Principal{}, Unavailable(CodeUnavailable, "templates are not available")
	}
	return CheckerFor(ctx, h.authz)
}

// fullView gives the creator the full view of what they created.
func fullView(c authz.Checker, id string) authz.View {
	v := authz.ViewOf(c, templateResource(id))
	if !v.Full() {
		v = authz.View{Level: authz.Full, Actions: v.Actions}
	}
	return v
}

type duplicateTemplateInput struct {
	Body struct {
		Name        string   `json:"name" minLength:"1" maxLength:"100" example:"My Nextcloud"`
		Description string   `json:"description,omitempty" maxLength:"1024"`
		Tags        []string `json:"tags,omitempty" maxItems:"16" example:"[\"cloud\"]"`
		InstanceID  string   `json:"instanceId,omitempty" maxLength:"64" doc:"The source's registry (empty: this instance)."`
		TemplateID  string   `json:"templateId" minLength:"1" maxLength:"64"`
		Version     int      `json:"version" minimum:"1"`
	}
}

func (h *importsAPI) duplicate(ctx context.Context, in *duplicateTemplateInput) (*templateOutput, error) {
	c, p, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapTemplateCreate), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapTemplateCreate))
	}
	b := in.Body
	own := b.InstanceID == "" || b.InstanceID == h.instanceID
	src := authz.Instance()
	if own {
		src = templateResource(b.TemplateID)
		if !authz.ViewOf(c, src).Visible() {
			return nil, NotFound("template not found")
		}
	}
	if !c.Can(string(CapTemplateUse), src).Allowed {
		return nil, Forbidden("not permitted: " + string(CapTemplateUse))
	}
	t, err := h.svc.Duplicate(ctx, domain.TemplateInput{Name: b.Name, Description: b.Description, Tags: b.Tags}, b.InstanceID, b.TemplateID,
		b.Version, p.UserID)
	if err != nil {
		var rc interface{ RegistryClass() string }
		if errors.As(err, &rc) || errors.Is(err, domain.ErrTemplateRegistryNotFound) {
			return nil, tmplRegistryError(err)
		}
		return nil, templateError(err)
	}
	return templateOut(t, fullView(c, t.ID)), nil
}

type restoreDraftInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Body       struct {
		Version int `json:"version" minimum:"1" example:"3" doc:"The version whose files replace the draft."`
	}
}

func (h *importsAPI) restoreDraft(ctx context.Context, in *restoreDraftInput) (*templateOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	res := templateResource(in.TemplateID)
	v := authz.ViewOf(c, res)
	if !v.Visible() {
		return nil, NotFound("template not found")
	}
	for _, cp := range []string{"template.files.write", "template.files.delete"} {
		if !c.Can(cp, res).Allowed {
			return nil, Forbidden("not permitted: " + cp)
		}
	}
	t, err := h.svc.RestoreDraft(ctx, in.TemplateID, in.Body.Version)
	if err != nil {
		return nil, templateError(err)
	}
	return templateOut(t, v), nil
}

type stackImportInput struct {
	Body struct {
		StackID     string   `json:"stackId" minLength:"1" maxLength:"64"`
		Paths       []string `json:"paths,omitempty" maxItems:"256" example:"[\"compose.yaml\",\".env\",\"config\"]" doc:"Entries of the stack's directory to include (default: everything)."`
		TemplateID  string   `json:"templateId,omitempty" maxLength:"64" doc:"Replace this template's draft (default: create a new private template)."`
		Name        string   `json:"name,omitempty" maxLength:"100" doc:"Name of the new template."`
		Description string   `json:"description,omitempty" maxLength:"1024"`
		Tags        []string `json:"tags,omitempty" maxItems:"16"`
	}
}

func (h *importsAPI) fromStack(ctx context.Context, in *stackImportInput) (*templateOutput, error) {
	c, p, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if h.files == nil {
		return nil, Unavailable(CodeUnavailable, "the file service is not available")
	}
	b := in.Body
	root, err := h.files.StackRoot(ctx, b.StackID)
	if errors.Is(err, domain.ErrFileScopeNotFound) {
		return nil, NotFound("stack not found")
	}
	if err != nil {
		return nil, Internal(err)
	}
	stack := authz.Resource{Type: catalog.TypeStack, ID: b.StackID, EnvironmentID: root.EnvironmentID}
	if !authz.ViewOf(c, stack).Visible() {
		return nil, NotFound("stack not found")
	}
	for _, cp := range []string{"stack.files.download", "stack.definition.read"} {
		if !c.Can(cp, stack).Allowed {
			return nil, Forbidden("not permitted: " + cp)
		}
	}
	var target domain.Template
	if b.TemplateID != "" {
		res := templateResource(b.TemplateID)
		if !authz.ViewOf(c, res).Visible() {
			return nil, NotFound("template not found")
		}
		for _, cp := range []string{"template.files.write", "template.files.delete"} {
			if !c.Can(cp, res).Allowed {
				return nil, Forbidden("not permitted: " + cp)
			}
		}
		if target, err = h.templates.Get(ctx, b.TemplateID); err != nil {
			return nil, templateError(err)
		}
	} else {
		if !c.Can(string(CapTemplateCreate), authz.Instance()).Allowed {
			return nil, Forbidden("not permitted: " + string(CapTemplateCreate))
		}
		if b.Name == "" {
			return nil, Invalid("a name is required", Field("body.name", "name the new template"))
		}
	}
	paths := b.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	if paths, err = cleanPaths(paths, "body.paths", MaxFilesPerJob); err != nil {
		return nil, err
	}
	st, err := h.files.Download(ctx, root, protocol.FilesDownloadInput{Paths: paths, Format: protocol.FormatTarGz})
	if err != nil {
		return nil, fileErr(err, "body.paths", false)
	}
	body := &streamReader{st: st, mapErr: h.files.StreamError}
	done := false
	defer func() {
		if !done {
			st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		}
	}()
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: b.StackID, EnvironmentID: root.EnvironmentID})
	var t domain.Template
	if b.TemplateID != "" {
		if err := h.svc.ImportDraft(ctx, target.ID, body); err != nil {
			return nil, importErr(err)
		}
		if t, err = h.templates.Get(ctx, target.ID); err != nil {
			return nil, templateError(err)
		}
		audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeTemplate, ID: t.ID})
	} else if t, err = h.svc.CreateFrom(ctx, domain.TemplateInput{Name: b.Name, Description: b.Description, Tags: b.Tags}, body, p.UserID); err != nil {
		return nil, importErr(err)
	}
	// Read the rest (the gzip trailer) so the agent's stream ends cleanly.
	_, _ = io.Copy(io.Discard, body)
	done = true
	_ = st.CloseWrite()
	return templateOut(t, fullView(c, t.ID)), nil
}

// streamReader reads a download, mapping the agent's failure.
type streamReader struct {
	st     ByteStream
	mapErr func(error) error
}

func (r *streamReader) Read(p []byte) (int, error) {
	n, err := r.st.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, &streamFailure{err: r.mapErr(err)}
	}
	return n, err
}

type streamFailure struct{ err error }

func (e *streamFailure) Error() string { return e.err.Error() }
func (e *streamFailure) Unwrap() error { return e.err }

func importErr(err error) error {
	var sf *streamFailure
	if errors.As(err, &sf) {
		return fileErr(sf.err, "body.paths", false)
	}
	return templateError(err)
}

func registerTemplateImports(a huma.API, deps Deps) {
	var svc TemplateImportService
	if s, ok := deps.Templates.(TemplateImportService); ok {
		svc = s
	}
	h := &importsAPI{svc: svc, templates: deps.Templates, files: deps.Files, authz: authz.OrDenyAll(deps.Authorizer), instanceID: deps.InstanceID}
	errs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable}

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-duplicate", Method: http.MethodPost, Path: BasePath + "/templates/duplicates",
		Summary: "Duplicate a template version",
		Description: "Creates a private template whose draft holds every file of a published version: of this instance's templates " +
			"(template.use on it) or of an added registry's (template.use on the instance; downloaded and checked now). Needs " +
			"template.create.",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusCreated, Errors: errs,
	}, Capability: CapTemplateCreate, Scope: ScopeInstance}, h.duplicate)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-draft-restore", Method: http.MethodPost, Path: BasePath + "/templates/{templateId}/draft-restores",
		Summary: "Restore a draft to a version",
		Description: "Replaces the template's draft with the files of one of its versions (the draft's current files are gone). " +
			"Needs template.files.write and template.files.delete.",
		Tags: []string{tagTemplates}, Errors: errs,
	}, Capability: "template.files.write", Scope: ScopeResource}, h.restoreDraft)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-stack-import", Method: http.MethodPost, Path: BasePath + "/templates/stack-imports",
		Summary: "Save a stack as a template",
		Description: "Copies the chosen entries of a stack's project directory (default: all of it) from its host into a new private " +
			"template (template.create) or into an existing template's draft, replacing it (template.files.write and " +
			"template.files.delete). Needs stack.files.download and stack.definition.read on the stack. Escaping symlinks, hard-linked " +
			"and special files are left out; the template size limit applies (413 template_too_large: leave out data folders).",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusCreated, Errors: append(errs, http.StatusGatewayTimeout),
	}, Capability: CapTemplateCreate, Scope: ScopeInstance}, h.fromStack)
}
