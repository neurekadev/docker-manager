package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Stack templates (template registry): the instance's own templates, their
// icons and published versions. Drafts are edited through the file routes
// under /templates/{templateId}/files (files.go). The flows live in
// internal/manager/templates; this file is the transport contract.

const tagTemplates = "Templates"

// Template capabilities (#17 catalog).
const (
	CapTemplateRead    Capability = "template.read"
	CapTemplateUse     Capability = "template.use"
	CapTemplateCreate  Capability = "template.create"
	CapTemplateManage  Capability = "template.manage"
	CapTemplatePublish Capability = "template.publish"
	CapTemplateRemove  Capability = "template.remove"
)

// Template error codes.
const (
	CodeTemplateNameTaken         = "template_name_taken"
	CodeTemplateVersionLabelTaken = "template_version_label_taken"
	CodeTemplateTooLarge          = "template_too_large"
	CodeTemplateIconUnsupported   = "template_icon_unsupported"
	CodeTemplateIconTooLarge      = "template_icon_too_large"
	CodeTemplatePublicAckRequired = "template_public_ack_required"
	CodeTemplateDefinitionInvalid = "template_definition_invalid"
)

// TemplateService is the template service as seen by the API (implemented
// by *templates.Service).
type TemplateService interface {
	List(ctx context.Context, afterID string, limit int) ([]domain.Template, error)
	Get(ctx context.Context, id string) (domain.Template, error)
	Create(ctx context.Context, in domain.TemplateInput, userID string) (domain.Template, error)
	Update(ctx context.Context, id string, revision int64, p domain.TemplatePatch) (domain.Template, error)
	SetVisibility(ctx context.Context, id string, revision int64, v domain.TemplateVisibility, acknowledged bool) (domain.Template, error)
	Delete(ctx context.Context, id string, revision int64) error
	Icon(ctx context.Context, id string) (domain.TemplateIcon, []byte, error)
	SetIcon(ctx context.Context, id string, data []byte) (domain.Template, error)
	RemoveIcon(ctx context.Context, id string) (domain.Template, error)
	Versions(ctx context.Context, id string) ([]domain.TemplateVersion, error)
	Version(ctx context.Context, id string, number int) (domain.TemplateVersion, error)
	Publish(ctx context.Context, id, label, notes string, acknowledged bool, userID string) (domain.TemplateVersion, error)
	DeleteVersion(ctx context.Context, id string, number int) error
}

func templateResource(id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeTemplate, ID: id, Parents: []authz.ResourceRef{}}
}

// templateError maps template service errors.
func templateError(err error) error {
	var tooLarge *domain.TemplateTooLargeError
	var icon *domain.TemplateIconError
	var def *domain.TemplateDefinitionError
	var fe *domain.FieldError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrTemplateNotFound):
		return NotFound("template not found")
	case errors.Is(err, domain.ErrTemplateVersionNotFound):
		return NotFound("template version not found")
	case errors.Is(err, domain.ErrTemplateIconNotFound):
		return NotFound("the template has no icon")
	case errors.Is(err, domain.ErrTemplateNameTaken):
		return Conflict(CodeTemplateNameTaken, "another template already uses this name")
	case errors.Is(err, domain.ErrTemplateVersionLabelTaken):
		return Conflict(CodeTemplateVersionLabelTaken, "another version of this template already uses this label")
	case errors.Is(err, domain.ErrTemplatePublicAckRequired):
		return NewError(http.StatusUnprocessableEntity, CodeTemplatePublicAckRequired,
			"every file of a public template, .env included, is readable by anyone with the registry URL; confirm with acknowledgePublic",
			Field("body.acknowledgePublic", "must be true"))
	case errors.As(err, &tooLarge):
		return NewError(http.StatusRequestEntityTooLarge, CodeTemplateTooLarge, tooLarge.Message)
	case errors.As(err, &icon):
		if icon.TooLarge {
			return NewError(http.StatusRequestEntityTooLarge, CodeTemplateIconTooLarge, icon.Message)
		}
		return NewError(http.StatusUnsupportedMediaType, CodeTemplateIconUnsupported, icon.Message)
	case errors.As(err, &def):
		return NewError(http.StatusUnprocessableEntity, CodeTemplateDefinitionInvalid, def.Message)
	case errors.As(err, &fe):
		return Invalid(fe.Message, Field("body."+fe.Field, fe.Message))
	}
	return Internal(err)
}

// --- DTOs ---

// TemplateIconInfo describes a template's icon; fetch it from url.
type TemplateIconInfo struct {
	MediaType string    `json:"mediaType" enum:"image/png,image/jpeg,image/gif,image/webp,image/svg+xml"`
	SHA256    string    `json:"sha256" doc:"Changes with the icon; part of url so browsers cache each icon forever."`
	Size      int64     `json:"size"`
	URL       string    `json:"url" example:"/api/v1/templates/0190a6e0-.../icon?v=3f2a..." doc:"Any signed-in user may load it."`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newTemplateIcon(id string, i *domain.TemplateIcon) *TemplateIconInfo {
	if i == nil {
		return nil
	}
	return &TemplateIconInfo{MediaType: i.MediaType, SHA256: i.SHA256, Size: i.Size, UpdatedAt: i.UpdatedAt,
		URL: BasePath + "/templates/" + url.PathEscape(id) + "/icon?v=" + i.SHA256}
}

// TemplateFileInfo is one Compose source of a version.
type TemplateFileInfo struct {
	Path string `json:"path" example:"compose.yaml"`
	Size int64  `json:"size"`
}

// TemplateVersion is one published, immutable version.
type TemplateVersion struct {
	Number        int                `json:"number" doc:"Increases with every publication; never reused."`
	Label         string             `json:"label" example:"1.2.0"`
	Notes         string             `json:"notes"`
	ArchiveSHA256 string             `json:"archiveSha256" doc:"SHA-256 of the version's tar.gz archive."`
	ArchiveSize   int64              `json:"archiveSize"`
	ContentSize   int64              `json:"contentSize" doc:"Bytes of the files it contains."`
	Entries       int                `json:"entries" doc:"Files, directories and symlinks it contains."`
	Definition    []TemplateFileInfo `json:"definition" doc:"The Compose files and .env at its root."`
	PublishedAt   time.Time          `json:"publishedAt"`
}

func newTemplateVersion(v domain.TemplateVersion) TemplateVersion {
	out := TemplateVersion{Number: v.Number, Label: v.Label, Notes: v.Notes, ArchiveSHA256: v.ArchiveSHA256, ArchiveSize: v.ArchiveSize,
		ContentSize: v.ContentSize, Entries: v.Entries, Definition: []TemplateFileInfo{}, PublishedAt: v.CreatedAt}
	for _, f := range v.Definition {
		out.Definition = append(out.Definition, TemplateFileInfo{Path: f.Path, Size: f.Size})
	}
	return out
}

// Template is one of the instance's stack templates.
//
// Shaping (#17): template.read shows it in full; any other capability on it
// only id, name, visibility and icon.
type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name" example:"Nextcloud"`
	Visibility  string            `json:"visibility" enum:"private,public" doc:"public: listed in this instance's public registry for other managers (every file, .env included, is readable there)."`
	Icon        *TemplateIconInfo `json:"icon,omitempty"`
	View        string            `json:"view" enum:"minimal,full"`
	Actions     []string          `json:"actions"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Latest      *TemplateVersion  `json:"latest,omitempty" doc:"The newest published version (absent before the first)."`
	Versions    int               `json:"versions" doc:"Published versions."`
	Revision    int64             `json:"revision,omitempty"`
	CreatedAt   time.Time         `json:"createdAt,omitzero"`
	UpdatedAt   time.Time         `json:"updatedAt,omitzero"`
}

func newTemplate(t domain.Template, v authz.View) Template {
	out := Template{ID: t.ID, Name: t.Name, Visibility: string(t.Visibility), Icon: newTemplateIcon(t.ID, t.Icon),
		View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	out.Description, out.Tags, out.Versions = t.Description, t.Tags, t.Versions
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if t.Latest != nil {
		l := newTemplateVersion(*t.Latest)
		out.Latest = &l
	}
	out.Revision, out.CreatedAt, out.UpdatedAt = t.Revision, t.CreatedAt, t.UpdatedAt
	return out
}

// --- handlers ---

type templatesAPI struct {
	svc   TemplateService
	authz authz.Authorizer
}

func (h *templatesAPI) checker(ctx context.Context) (TemplateService, authz.Checker, authz.Principal, error) {
	if h.svc == nil {
		return nil, nil, authz.Principal{}, Unavailable(CodeUnavailable, "templates are not available")
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, err
	}
	return h.svc, c, p, nil
}

// visible loads a template the caller may see (404 otherwise) and checks
// cp when given (403 when visible but not granted).
func (h *templatesAPI) visible(ctx context.Context, id string, cp Capability) (TemplateService, authz.Principal, domain.Template, authz.View, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, p, domain.Template{}, authz.View{}, err
	}
	t, err := svc.Get(ctx, id)
	if err != nil {
		return nil, p, t, authz.View{}, templateError(err)
	}
	v := authz.ViewOf(c, templateResource(t.ID))
	if !v.Visible() {
		return nil, p, t, v, NotFound("template not found")
	}
	if cp != "" && !v.Has(string(cp)) {
		return nil, p, t, v, Forbidden("not permitted: " + string(cp))
	}
	return svc, p, t, v, nil
}

type templateIDInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
}

type templateOutput struct {
	ETagHeader
	Body Template
}

func templateOut(t domain.Template, v authz.View) *templateOutput {
	return &templateOutput{ETagHeader: ETagHeader{ETag: RevisionETag(t.Revision)}, Body: newTemplate(t, v)}
}

type listTemplatesInput struct {
	PageParams
	Query      string `query:"q" maxLength:"100" doc:"Matches the name, description or a tag (case-insensitive)."`
	Tag        string `query:"tag" maxLength:"32" doc:"Only templates with this tag."`
	Visibility string `query:"visibility" enum:"private,public" doc:"Only private or public templates."`
}

type templateListOutput struct{ Body Page[Template] }

func (h *templatesAPI) list(ctx context.Context, in *listTemplatesInput) (*templateListOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	f := domain.TemplateFilter{Query: in.Query, Tag: in.Tag, Visibility: domain.TemplateVisibility(in.Visibility)}
	fp := QueryFingerprint("templates", in.Query, in.Tag, in.Visibility)
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Template]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.Template, error) {
			return svc.List(ctx, afterID, n)
		},
		Position: func(t domain.Template) string { return t.ID },
		Visible: func(t domain.Template) bool {
			v := authz.ViewOf(c, templateResource(t.ID))
			// Filters on full-view fields apply to full views only.
			return v.Visible() && (f == domain.TemplateFilter{} || (v.Full() && f.Matches(t)) ||
				(!v.Full() && f.Query == "" && f.Tag == "" && (f.Visibility == "" || f.Visibility == t.Visibility)))
		},
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Template, 0, len(items))
	for _, t := range items {
		out = append(out, newTemplate(t, authz.ViewOf(c, templateResource(t.ID))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &templateListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *templatesAPI) get(ctx context.Context, in *templateIDInput) (*templateOutput, error) {
	_, _, t, v, err := h.visible(ctx, in.TemplateID, "")
	if err != nil {
		return nil, err
	}
	return templateOut(t, v), nil
}

type createTemplateInput struct {
	IdempotencyKeyParam
	Body struct {
		Name        string   `json:"name" minLength:"1" maxLength:"100" example:"Nextcloud"`
		Description string   `json:"description,omitempty" maxLength:"1024"`
		Tags        []string `json:"tags,omitempty" maxItems:"16" example:"[\"cloud\",\"files\"]" doc:"Lowercase letters, digits and dashes (at most 32 characters each)."`
	}
}

func (h *templatesAPI) create(ctx context.Context, in *createTemplateInput) (*templateOutput, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapTemplateCreate), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapTemplateCreate))
	}
	t, err := svc.Create(ctx, domain.TemplateInput{Name: in.Body.Name, Description: in.Body.Description, Tags: in.Body.Tags}, p.UserID)
	if err != nil {
		return nil, templateError(err)
	}
	v := authz.ViewOf(c, templateResource(t.ID))
	if !v.Full() {
		// The creator sees what they created even without template.read.
		v = authz.View{Level: authz.Full, Actions: v.Actions}
	}
	return templateOut(t, v), nil
}

type updateTemplateInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	IfMatchParam
	Body struct {
		Name        *string   `json:"name,omitempty" minLength:"1" maxLength:"100" example:"Nextcloud"`
		Description *string   `json:"description,omitempty" maxLength:"1024"`
		Tags        *[]string `json:"tags,omitempty" maxItems:"16" example:"[\"cloud\",\"files\"]"`
	}
}

func (h *templatesAPI) update(ctx context.Context, in *updateTemplateInput) (*templateOutput, error) {
	svc, _, t, v, err := h.visible(ctx, in.TemplateID, CapTemplateManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(t.Revision)); err != nil {
		return nil, err
	}
	after, err := svc.Update(ctx, t.ID, t.Revision, domain.TemplatePatch{Name: in.Body.Name, Description: in.Body.Description, Tags: in.Body.Tags})
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, h.stale(ctx, svc, t.ID)
	}
	if err != nil {
		return nil, templateError(err)
	}
	return templateOut(after, v), nil
}

func (h *templatesAPI) stale(ctx context.Context, svc TemplateService, id string) error {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return templateError(err)
	}
	return stale(cur.Revision)
}

type templateVisibilityInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	IfMatchParam
	Body struct {
		Visibility        string `json:"visibility" enum:"private,public" example:"public"`
		AcknowledgePublic bool   `json:"acknowledgePublic,omitempty" doc:"Required to make a template public: every file of its published versions, .env included, becomes readable by anyone with the registry URL."`
	}
}

func (h *templatesAPI) setVisibility(ctx context.Context, in *templateVisibilityInput) (*templateOutput, error) {
	svc, _, t, v, err := h.visible(ctx, in.TemplateID, CapTemplatePublish)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(t.Revision)); err != nil {
		return nil, err
	}
	after, err := svc.SetVisibility(ctx, t.ID, t.Revision, domain.TemplateVisibility(in.Body.Visibility), in.Body.AcknowledgePublic)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, h.stale(ctx, svc, t.ID)
	}
	if err != nil {
		return nil, templateError(err)
	}
	return templateOut(after, v), nil
}

type deleteTemplateInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	IfMatchParam
}

func (h *templatesAPI) remove(ctx context.Context, in *deleteTemplateInput) (*struct{}, error) {
	svc, _, t, _, err := h.visible(ctx, in.TemplateID, CapTemplateRemove)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(t.Revision)); err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, t.ID, t.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			return nil, h.stale(ctx, svc, t.ID)
		}
		return nil, templateError(err)
	}
	return &struct{}{}, nil
}

type getTemplateIconInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    string `query:"v" maxLength:"64" doc:"The icon's sha256 (from icon.url): the response is then cacheable forever."`
}

// iconResponse writes icon bytes with headers that keep an SVG from ever
// running as a document.
func iconResponse(icon domain.TemplateIcon, data []byte, version string) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", icon.MediaType)
		hctx.SetHeader("Content-Length", strconv.Itoa(len(data)))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
		hctx.SetHeader("Content-Disposition", "inline")
		hctx.SetHeader("ETag", ETag(icon.SHA256))
		if version == icon.SHA256 {
			hctx.SetHeader("Cache-Control", "private, max-age=31536000, immutable")
		}
		hctx.SetStatus(http.StatusOK)
		_, _ = hctx.BodyWriter().Write(data)
	}}
}

func (h *templatesAPI) getIcon(ctx context.Context, in *getTemplateIconInput) (*huma.StreamResponse, error) {
	// Icons are decoration: any signed-in user may load them (people who
	// see a stack created from a template see its icon).
	svc, _, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	icon, data, err := svc.Icon(ctx, in.TemplateID)
	if err != nil {
		return nil, templateError(err)
	}
	return iconResponse(icon, data, in.Version), nil
}

type setTemplateIconInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Body       struct {
		Data []byte `json:"data" example:"PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=" doc:"The image bytes, base64-encoded: PNG, JPEG, GIF, WebP or SVG, at most 256 KiB (raster images at most 1024x1024 pixels). The type is detected from the bytes."`
	}
}

func (h *templatesAPI) setIcon(ctx context.Context, in *setTemplateIconInput) (*templateOutput, error) {
	svc, _, t, v, err := h.visible(ctx, in.TemplateID, CapTemplateManage)
	if err != nil {
		return nil, err
	}
	after, err := svc.SetIcon(ctx, t.ID, in.Body.Data)
	if err != nil {
		return nil, templateError(err)
	}
	return templateOut(after, v), nil
}

func (h *templatesAPI) removeIcon(ctx context.Context, in *templateIDInput) (*templateOutput, error) {
	svc, _, t, v, err := h.visible(ctx, in.TemplateID, CapTemplateManage)
	if err != nil {
		return nil, err
	}
	after, err := svc.RemoveIcon(ctx, t.ID)
	if err != nil {
		return nil, templateError(err)
	}
	return templateOut(after, v), nil
}

type templateVersionListOutput struct{ Body Page[TemplateVersion] }

func (h *templatesAPI) listVersions(ctx context.Context, in *templateIDInput) (*templateVersionListOutput, error) {
	svc, _, t, _, err := h.visible(ctx, in.TemplateID, CapTemplateRead)
	if err != nil {
		return nil, err
	}
	vs, err := svc.Versions(ctx, t.ID)
	if err != nil {
		return nil, templateError(err)
	}
	out := make([]TemplateVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, newTemplateVersion(v))
	}
	return &templateVersionListOutput{Body: NewPage(out, "", nil)}, nil
}

type templateVersionInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    int    `path:"version" minimum:"1" doc:"Version number."`
}

type templateVersionOutput struct{ Body TemplateVersion }

func (h *templatesAPI) getVersion(ctx context.Context, in *templateVersionInput) (*templateVersionOutput, error) {
	svc, _, t, _, err := h.visible(ctx, in.TemplateID, CapTemplateRead)
	if err != nil {
		return nil, err
	}
	v, err := svc.Version(ctx, t.ID, in.Version)
	if err != nil {
		return nil, templateError(err)
	}
	return &templateVersionOutput{Body: newTemplateVersion(v)}, nil
}

type publishTemplateInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Body       struct {
		Label             string `json:"label" minLength:"1" maxLength:"32" example:"1.2.0" doc:"Letters, digits and . + _ -; unique per template."`
		Notes             string `json:"notes,omitempty" maxLength:"4096" doc:"What changed (shown to people creating stacks from it)."`
		AcknowledgePublic bool   `json:"acknowledgePublic,omitempty" doc:"Required when the template is public: every file of the version, .env included, becomes readable by anyone with the registry URL."`
	}
}

func (h *templatesAPI) publish(ctx context.Context, in *publishTemplateInput) (*templateVersionOutput, error) {
	svc, p, t, _, err := h.visible(ctx, in.TemplateID, CapTemplatePublish)
	if err != nil {
		return nil, err
	}
	v, err := svc.Publish(ctx, t.ID, in.Body.Label, in.Body.Notes, in.Body.AcknowledgePublic, p.UserID)
	if err != nil {
		return nil, templateError(err)
	}
	return &templateVersionOutput{Body: newTemplateVersion(v)}, nil
}

func (h *templatesAPI) deleteVersion(ctx context.Context, in *templateVersionInput) (*struct{}, error) {
	svc, _, t, _, err := h.visible(ctx, in.TemplateID, CapTemplatePublish)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteVersion(ctx, t.ID, in.Version); err != nil {
		return nil, templateError(err)
	}
	return &struct{}{}, nil
}

func registerTemplates(a huma.API, deps Deps) {
	h := &templatesAPI{svc: deps.Templates, authz: authz.OrDenyAll(deps.Authorizer)}
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-templates", Method: http.MethodGet, Path: BasePath + "/templates", Summary: "List templates",
		Description: "The instance's own stack templates in creation order, filtered per item (#17): template.read shows one in full, " +
			"any other capability on it only id, name, visibility and icon. q, tag and visibility filter the list.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusUnprocessableEntity},
	}, Capability: CapTemplateRead, Scope: ScopeResource}, h.list)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template", Method: http.MethodPost, Path: BasePath + "/templates", Summary: "Create a template",
		Description: "Creates a private template whose draft holds a starter compose.yaml. Edit the draft with the file routes " +
			"(/templates/{templateId}/files), then publish a version.",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}, Capability: CapTemplateCreate, Scope: ScopeInstance, Idempotency: IdempotencyStored}, h.create)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template", Method: http.MethodGet, Path: BasePath + "/templates/{templateId}", Summary: "Get a template",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound},
	}, Capability: CapTemplateRead, Scope: ScopeResource}, h.get)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "update-template", Method: http.MethodPatch, Path: BasePath + "/templates/{templateId}", Summary: "Update a template",
		Description: "Edits the name, description and tags. Requires If-Match.", Tags: []string{tagTemplates}, Errors: editErrs,
	}, Capability: CapTemplateManage, Scope: ScopeResource}, h.update)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-template", Method: http.MethodDelete, Path: BasePath + "/templates/{templateId}", Summary: "Delete a template",
		Description: "Deletes the template with its draft, icon and versions. Stacks created from it keep working with their own files. " +
			"Requires If-Match.",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusNoContent, Errors: editErrs,
	}, Capability: CapTemplateRemove, Scope: ScopeResource}, h.remove)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "replace-template-visibility", Method: http.MethodPut, Path: BasePath + "/templates/{templateId}/visibility",
		Summary: "Make a template public or private",
		Description: "public lists the template's published versions in this instance's public registry: anyone with the registry URL " +
			"can read every file of them, .env included (422 template_public_ack_required without acknowledgePublic). private removes " +
			"it from the registry; managers that already downloaded a version keep their copy. Requires If-Match.",
		Tags: []string{tagTemplates}, Errors: editErrs,
	}, Capability: CapTemplatePublish, Scope: ScopeResource}, h.setVisibility)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-icon", Method: http.MethodGet, Path: BasePath + "/templates/{templateId}/icon", Summary: "Get a template's icon",
		Description: "The icon's bytes (image/png, image/jpeg, image/gif, image/webp or image/svg+xml) for any signed-in user. With v set " +
			"to the icon's sha256 the response is cacheable forever; icons are served with a sandboxing Content-Security-Policy.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound},
		Responses: map[string]*huma.Response{"200": {Description: "Icon bytes", Content: map[string]*huma.MediaType{
			"image/*": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
	}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.getIcon)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "replace-template-icon", Method: http.MethodPut, Path: BasePath + "/templates/{templateId}/icon", Summary: "Set a template's icon",
		Description: "Replaces the icon: PNG, JPEG, GIF, WebP or SVG, at most 256 KiB (413 template_icon_too_large), raster images at most " +
			"1024x1024 pixels; the type is detected from the bytes (415 template_icon_unsupported). SVG icons may not contain scripts, " +
			"embedded documents or DOCTYPE/ENTITY declarations. Stacks created from the template show the new icon at once.",
		Tags: []string{tagTemplates}, MaxBodyBytes: 512 << 10,
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity},
	}, Capability: CapTemplateManage, Scope: ScopeResource}, h.setIcon)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-template-icon", Method: http.MethodDelete, Path: BasePath + "/templates/{templateId}/icon", Summary: "Remove a template's icon",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapTemplateManage, Scope: ScopeResource}, h.removeIcon)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-template-versions", Method: http.MethodGet, Path: BasePath + "/templates/{templateId}/versions",
		Summary: "List a template's versions", Description: "Published versions, newest first.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound, http.StatusForbidden},
	}, Capability: CapTemplateRead, Scope: ScopeResource}, h.listVersions)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-version", Method: http.MethodPost, Path: BasePath + "/templates/{templateId}/versions",
		Summary: "Publish a version",
		Description: "Freezes the draft as a new immutable version: a canonical tar.gz of every file, stored encrypted. The draft needs a " +
			"compose.yaml (or compose.yml, docker-compose.y(a)ml) at its root and no Compose file may set a top-level name: " +
			"(422 template_definition_invalid); devices, sockets, hard-linked files and symlinks leaving the template are refused. " +
			"Public templates need acknowledgePublic (422 template_public_ack_required). 409 template_version_label_taken, " +
			"413 template_too_large.",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
	}, Capability: CapTemplatePublish, Scope: ScopeResource}, h.publish)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-version", Method: http.MethodGet, Path: BasePath + "/templates/{templateId}/versions/{version}",
		Summary: "Get a template version", Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound, http.StatusForbidden},
	}, Capability: CapTemplateRead, Scope: ScopeResource}, h.getVersion)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-template-version", Method: http.MethodDelete, Path: BasePath + "/templates/{templateId}/versions/{version}",
		Summary: "Delete a template version",
		Description: "Removes a published version. Stacks created from it keep working; other managers stop offering it after their " +
			"next registry sync. Its number is never reused.",
		Tags: []string{tagTemplates}, DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapTemplatePublish, Scope: ScopeResource}, h.deleteVersion)
}
