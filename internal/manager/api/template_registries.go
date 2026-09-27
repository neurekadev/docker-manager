package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
)

// Template registries of other instances and the template catalog
// (template registry): the owner adds other Docker Manager instances by
// URL; everyone with template.read browses this instance's templates and
// the cached templates of every added registry in one catalog.

// Template registry capabilities and error codes.
const (
	capTemplateRegistryManage = "template_registry.manage"

	CodeTemplateRegistryUnreachable = "template_registry_unreachable"
	CodeTemplateRegistryInvalid     = "template_registry_invalid"
	CodeTemplateRegistryInsecure    = "template_registry_insecure"
	CodeTemplateRegistryIsSelf      = "template_registry_is_self"
	CodeTemplateRegistryExists      = "template_registry_exists"
)

// TemplateRegistryService is the registry side of the template service
// (implemented by *templates.Service).
type TemplateRegistryService interface {
	Registries(ctx context.Context) ([]domain.TemplateRegistry, error)
	Registry(ctx context.Context, instanceID string) (domain.TemplateRegistry, error)
	AddRegistry(ctx context.Context, rawURL, userID string) (domain.TemplateRegistry, error)
	RemoveRegistry(ctx context.Context, instanceID string) error
	SyncRegistry(ctx context.Context, instanceID string) (domain.TemplateRegistry, error)
	RegistryTemplates(ctx context.Context, registryID string) ([]domain.RegistryTemplate, error)
	RegistryTemplate(ctx context.Context, registryID, templateID string) (domain.RegistryTemplate, error)
	RegistryIcon(ctx context.Context, registryID, templateID string) (domain.TemplateIcon, []byte, error)
	RegistryDefinition(ctx context.Context, registryID, templateID string, version int) (domain.RegistryTemplateVersion, []domain.TemplateFileContent, error)
}

// registryError maps registry errors (the service's *RegistryError is
// matched by its Class, which the api package cannot import).
func tmplRegistryError(err error) error {
	var rc interface{ RegistryClass() string }
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrTemplateRegistryNotFound):
		return NotFound("template registry not found")
	case errors.Is(err, domain.ErrTemplateNotFound), errors.Is(err, domain.ErrTemplateVersionNotFound):
		return NotFound("template not found")
	case errors.Is(err, domain.ErrTemplateIconNotFound):
		return NotFound("the template has no icon")
	case errors.Is(err, domain.ErrTemplateRegistryIsSelf):
		return Conflict(CodeTemplateRegistryIsSelf, "this is this instance's own address: its templates are listed already")
	case errors.Is(err, domain.ErrTemplateRegistryExists):
		return Conflict(CodeTemplateRegistryExists, "this registry is added already")
	case errors.As(err, &rc):
		switch rc.RegistryClass() {
		case "insecure":
			return NewError(http.StatusUnprocessableEntity, CodeTemplateRegistryInsecure, err.Error(), Field("body.url", err.Error()))
		case "not_found", "unreachable":
			return NewError(http.StatusBadGateway, CodeTemplateRegistryUnreachable, err.Error())
		default:
			return NewError(http.StatusBadGateway, CodeTemplateRegistryInvalid, err.Error())
		}
	}
	return templateError(err)
}

// TemplateRegistryInfo is an added registry (or this instance's own).
type TemplateRegistryInfo struct {
	InstanceID   string     `json:"instanceId"`
	Name         string     `json:"name" example:"Homelab"`
	URL          string     `json:"url" example:"https://docker.example.com"`
	Own          bool       `json:"own" doc:"This instance's own registry: always listed, cannot be removed."`
	Removable    bool       `json:"removable"`
	Status       string     `json:"status" enum:"ok,error"`
	ErrorClass   string     `json:"errorClass,omitempty" enum:"unreachable,insecure,invalid,not_found"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	Templates    int        `json:"templates" doc:"Templates listed (own registry: public templates with a version)."`
	SyncedAt     *time.Time `json:"syncedAt,omitempty"`
	AttemptedAt  *time.Time `json:"attemptedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt,omitzero"`
}

func newRegistryInfo(r domain.TemplateRegistry) TemplateRegistryInfo {
	return TemplateRegistryInfo{InstanceID: r.InstanceID, Name: r.Name, URL: r.URL, Removable: true, Status: string(r.Status),
		ErrorClass: r.ErrorClass, ErrorMessage: r.ErrorMessage, Templates: r.Templates, SyncedAt: r.SyncedAt, AttemptedAt: r.AttemptedAt,
		CreatedAt: r.CreatedAt}
}

// TemplateCatalogVersion is a published version in the catalog.
type TemplateCatalogVersion struct {
	Number      int       `json:"number"`
	Label       string    `json:"label" example:"1.2.0"`
	Notes       string    `json:"notes,omitempty"`
	PublishedAt time.Time `json:"publishedAt"`
	ContentSize int64     `json:"contentSize"`
}

// TemplateCatalogItem is a template of any registry.
type TemplateCatalogItem struct {
	InstanceID   string                   `json:"instanceId" doc:"The registry."`
	RegistryName string                   `json:"registryName" example:"Homelab"`
	Own          bool                     `json:"own" doc:"This instance's template (open it under /templates/{templateId})."`
	TemplateID   string                   `json:"templateId"`
	Name         string                   `json:"name" example:"Nextcloud"`
	Description  string                   `json:"description,omitempty"`
	Tags         []string                 `json:"tags"`
	IconURL      string                   `json:"iconUrl,omitempty"`
	Visibility   string                   `json:"visibility,omitempty" enum:"private,public" doc:"Own templates only."`
	Versions     []TemplateCatalogVersion `json:"versions" doc:"Published versions, newest first."`
	Actions      []string                 `json:"actions" doc:"template.use when you may create stacks from it."`
	UpdatedAt    time.Time                `json:"updatedAt,omitzero"`
}

type tmplRegistriesAPI struct {
	svc        TemplateRegistryService
	templates  TemplateService
	authz      authz.Authorizer
	instanceID string
	publicURL  string
	settings   SettingsService
}

func (h *tmplRegistriesAPI) checker(ctx context.Context) (authz.Checker, authz.Principal, error) {
	if h.svc == nil {
		return nil, authz.Principal{}, Unavailable(CodeUnavailable, "template registries are not available")
	}
	return CheckerFor(ctx, h.authz)
}

func (h *tmplRegistriesAPI) readAll(ctx context.Context) (authz.Checker, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapTemplateRead), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapTemplateRead))
	}
	return c, nil
}

func (h *tmplRegistriesAPI) admin(ctx context.Context) (authz.Principal, error) {
	c, p, err := h.checker(ctx)
	if err != nil {
		return p, err
	}
	if !c.Can(capTemplateRegistryManage, authz.Instance()).Allowed {
		return p, Forbidden("only the instance owner may add and remove template registries")
	}
	return p, nil
}

func (h *tmplRegistriesAPI) ownName(ctx context.Context) string {
	if h.settings != nil {
		if s, err := h.settings.Get(ctx); err == nil && s.Name != "" {
			return s.Name
		}
	}
	return "This instance"
}

type templateRegistryListOutput struct{ Body Page[TemplateRegistryInfo] }

func (h *tmplRegistriesAPI) list(ctx context.Context, _ *struct{}) (*templateRegistryListOutput, error) {
	if _, err := h.readAll(ctx); err != nil {
		return nil, err
	}
	rs, err := h.svc.Registries(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	own := TemplateRegistryInfo{InstanceID: h.instanceID, Name: h.ownName(ctx), URL: h.publicURL, Own: true, Status: "ok"}
	if h.templates != nil {
		if ts, err := h.templates.List(ctx, "", 0); err == nil {
			for _, t := range ts {
				if t.Visibility == domain.TemplatePublic && t.Latest != nil {
					own.Templates++
				}
			}
		}
	}
	out := []TemplateRegistryInfo{own}
	for _, r := range rs {
		out = append(out, newRegistryInfo(r))
	}
	return &templateRegistryListOutput{Body: NewPage(out, "", nil)}, nil
}

type addRegistryInput struct {
	Body struct {
		URL string `json:"url" minLength:"1" maxLength:"512" example:"https://docker.example.com" doc:"The other Docker Manager's address (its /registry page works too). HTTPS only."`
	}
}

type registryInfoOutput struct{ Body TemplateRegistryInfo }

func (h *tmplRegistriesAPI) add(ctx context.Context, in *addRegistryInput) (*registryInfoOutput, error) {
	p, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.AddRegistry(ctx, in.Body.URL, p.UserID)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	return &registryInfoOutput{Body: newRegistryInfo(r)}, nil
}

type tmplRegistryIDInput struct {
	InstanceID string `path:"instanceId" maxLength:"64" doc:"The registry's instance ID."`
}

func (h *tmplRegistriesAPI) remove(ctx context.Context, in *tmplRegistryIDInput) (*struct{}, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	if err := h.svc.RemoveRegistry(ctx, in.InstanceID); err != nil {
		return nil, tmplRegistryError(err)
	}
	return &struct{}{}, nil
}

func (h *tmplRegistriesAPI) syncNow(ctx context.Context, in *tmplRegistryIDInput) (*registryInfoOutput, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	r, err := h.svc.SyncRegistry(ctx, in.InstanceID)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	return &registryInfoOutput{Body: newRegistryInfo(r)}, nil
}

type registryTemplateInput struct {
	InstanceID string `path:"instanceId" maxLength:"64" doc:"The registry's instance ID."`
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    string `query:"v" maxLength:"64" doc:"The icon's sha256: cacheable forever."`
}

func (h *tmplRegistriesAPI) icon(ctx context.Context, in *registryTemplateInput) (*huma.StreamResponse, error) {
	if _, _, err := h.checker(ctx); err != nil {
		return nil, err
	}
	icon, data, err := h.svc.RegistryIcon(ctx, in.InstanceID, in.TemplateID)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	return iconResponse(icon, data, in.Version), nil
}

func registryIconURL(registryID, templateID, sha string) string {
	return BasePath + "/template-registries/" + url.PathEscape(registryID) + "/templates/" + url.PathEscape(templateID) + "/icon?v=" + sha
}

// catalog builds the catalog items the caller may see.
func (h *tmplRegistriesAPI) catalog(ctx context.Context, c authz.Checker) ([]TemplateCatalogItem, error) {
	var out []TemplateCatalogItem
	if h.templates != nil {
		own, err := h.templates.List(ctx, "", 0)
		if err != nil {
			return nil, Internal(err)
		}
		name := h.ownName(ctx)
		for _, t := range own {
			v := authz.ViewOf(c, templateResource(t.ID))
			if !v.Full() {
				continue
			}
			it := TemplateCatalogItem{InstanceID: h.instanceID, RegistryName: name, Own: true, TemplateID: t.ID, Name: t.Name,
				Description: t.Description, Tags: nonNilTags(t.Tags), Visibility: string(t.Visibility), Versions: []TemplateCatalogVersion{},
				Actions: []string{}, UpdatedAt: t.UpdatedAt}
			if i := newTemplateIcon(t.ID, t.Icon); i != nil {
				it.IconURL = i.URL
			}
			if v.Has(string(CapTemplateUse)) {
				it.Actions = append(it.Actions, string(CapTemplateUse))
			}
			if t.Latest != nil {
				vs, err := h.templates.Versions(ctx, t.ID)
				if err != nil {
					return nil, Internal(err)
				}
				for _, x := range vs {
					it.Versions = append(it.Versions, TemplateCatalogVersion{Number: x.Number, Label: x.Label, Notes: x.Notes,
						PublishedAt: x.CreatedAt, ContentSize: x.ContentSize})
				}
			}
			out = append(out, it)
		}
	}
	if !c.Can(string(CapTemplateRead), authz.Instance()).Allowed {
		return out, nil
	}
	rs, err := h.svc.Registries(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	names := map[string]string{}
	for _, r := range rs {
		names[r.InstanceID] = r.Name
	}
	remote, err := h.svc.RegistryTemplates(ctx, "")
	if err != nil {
		return nil, Internal(err)
	}
	use := c.Can(string(CapTemplateUse), authz.Instance()).Allowed
	for _, t := range remote {
		out = append(out, catalogItemOf(t, names[t.RegistryID], use))
	}
	return out, nil
}

func catalogItemOf(t domain.RegistryTemplate, registryName string, use bool) TemplateCatalogItem {
	it := TemplateCatalogItem{InstanceID: t.RegistryID, RegistryName: registryName, TemplateID: t.TemplateID, Name: t.Name,
		Description: t.Description, Tags: nonNilTags(t.Tags), Versions: []TemplateCatalogVersion{}, Actions: []string{}, UpdatedAt: t.UpdatedAt}
	if t.IconSHA256 != "" {
		it.IconURL = registryIconURL(t.RegistryID, t.TemplateID, t.IconSHA256)
	}
	if use {
		it.Actions = append(it.Actions, string(CapTemplateUse))
	}
	for _, v := range t.Versions {
		it.Versions = append(it.Versions, TemplateCatalogVersion{Number: v.Number, Label: v.Label, Notes: v.Notes, PublishedAt: v.PublishedAt,
			ContentSize: v.ContentSize})
	}
	return it
}

func nonNilTags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

type tmplCatalogInput struct {
	Query    string `query:"q" maxLength:"100" doc:"Matches the name, description or a tag."`
	Tag      string `query:"tag" maxLength:"32"`
	Registry string `query:"registry" maxLength:"64" doc:"Only this registry's templates (instance ID)."`
}

type tmplCatalogOutput struct{ Body Page[TemplateCatalogItem] }

func (h *tmplRegistriesAPI) listCatalog(ctx context.Context, in *tmplCatalogInput) (*tmplCatalogOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	items, err := h.catalog(ctx, c)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	out := make([]TemplateCatalogItem, 0, len(items))
	for _, it := range items {
		if in.Registry != "" && it.InstanceID != in.Registry {
			continue
		}
		if in.Tag != "" && !slices.Contains(it.Tags, in.Tag) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Name), q) && !strings.Contains(strings.ToLower(it.Description), q) &&
			!slices.ContainsFunc(it.Tags, func(t string) bool { return strings.Contains(t, q) }) {
			continue
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b TemplateCatalogItem) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return &tmplCatalogOutput{Body: NewPage(out, "", nil)}, nil
}

type catalogItemInput struct {
	InstanceID string `path:"instanceId" maxLength:"64" doc:"The registry's instance ID."`
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
}

type catalogItemOutput struct{ Body TemplateCatalogItem }

func (h *tmplRegistriesAPI) getCatalogItem(ctx context.Context, in *catalogItemInput) (*catalogItemOutput, error) {
	c, err := h.readAll(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.Registry(ctx, in.InstanceID)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	t, err := h.svc.RegistryTemplate(ctx, in.InstanceID, in.TemplateID)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	return &catalogItemOutput{Body: catalogItemOf(t, r.Name, c.Can(string(CapTemplateUse), authz.Instance()).Allowed)}, nil
}

type catalogDefinitionInput struct {
	InstanceID string `path:"instanceId" maxLength:"64" doc:"The registry's instance ID."`
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    int    `path:"version" minimum:"1" doc:"Version number."`
}

// RegistryDefinition is a registry template version's Compose files.
type RegistryDefinition struct {
	Version TemplateCatalogVersion   `json:"version"`
	Files   []TemplateDefinitionFile `json:"files"`
}

type registryDefinitionOutput struct{ Body RegistryDefinition }

func (h *tmplRegistriesAPI) definition(ctx context.Context, in *catalogDefinitionInput) (*registryDefinitionOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapTemplateUse), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapTemplateUse))
	}
	v, files, err := h.svc.RegistryDefinition(ctx, in.InstanceID, in.TemplateID, in.Version)
	if err != nil {
		return nil, tmplRegistryError(err)
	}
	out := RegistryDefinition{Version: TemplateCatalogVersion{Number: v.Number, Label: v.Label, Notes: v.Notes, PublishedAt: v.PublishedAt,
		ContentSize: v.ContentSize}, Files: []TemplateDefinitionFile{}}
	for _, f := range files {
		if utf8.Valid(f.Content) {
			out.Files = append(out.Files, TemplateDefinitionFile{Path: f.Path, Content: string(f.Content)})
		}
	}
	return &registryDefinitionOutput{Body: out}, nil
}

// remoteIcons lists the cached icons of registry templates (the icon map).
func remoteIcons(ctx context.Context, svc TemplateRegistryService) []TemplateIconRef {
	if svc == nil {
		return nil
	}
	ts, err := svc.RegistryTemplates(ctx, "")
	if err != nil {
		return nil
	}
	var out []TemplateIconRef
	for _, t := range ts {
		if t.IconSHA256 != "" {
			out = append(out, TemplateIconRef{InstanceID: t.RegistryID, TemplateID: t.TemplateID, URL: registryIconURL(t.RegistryID, t.TemplateID, t.IconSHA256)})
		}
	}
	return out
}

func registerTemplateRegistries(a huma.API, deps Deps) {
	h := &tmplRegistriesAPI{svc: deps.TemplateRegistries, templates: deps.Templates, authz: authz.OrDenyAll(deps.Authorizer),
		instanceID: deps.InstanceID, publicURL: deps.Deployment.PublicURL, settings: deps.Settings}
	const owner = " Instance owner only (never delegable, never with an API token)."

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-template-registries", Method: http.MethodGet, Path: BasePath + "/template-registries",
		Summary: "List template registries",
		Description: "This instance's own registry (first; never removable) and the registries of other Docker Manager instances " +
			"added by the owner, with their sync state.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusForbidden},
	}, Capability: CapTemplateRead, Scope: ScopeInstance}, h.list)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-registry", Method: http.MethodPost, Path: BasePath + "/template-registries",
		Summary: "Add a template registry", DefaultStatus: http.StatusCreated,
		Description: "Adds another Docker Manager by its address (HTTPS; its /registry page works too) and reads its public templates " +
			"now; they are synced every DOCKER_MANAGER_TEMPLATE_REGISTRY_SYNC_INTERVAL afterwards. The registry is identified by the " +
			"other instance's ID: adding it again (even under a new address) restores the icons of stacks created from its templates. " +
			"409 template_registry_exists / template_registry_is_self, 422 template_registry_insecure, 502 template_registry_unreachable " +
			"/ template_registry_invalid." + owner,
		Tags: []string{tagTemplates}, Security: cookieOnly,
		Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway},
	}, Capability: CapabilityOwner, Scope: ScopeInstance}, h.add)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-template-registry", Method: http.MethodDelete, Path: BasePath + "/template-registries/{instanceId}",
		Summary: "Remove a template registry", DefaultStatus: http.StatusNoContent,
		Description: "Removes the registry and its cached templates and icons. Stacks created from its templates keep working; they " +
			"show their template's icon again when the registry is added back." + owner,
		Tags: []string{tagTemplates}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapabilityOwner, Scope: ScopeInstance}, h.remove)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-template-registry-sync", Method: http.MethodPost, Path: BasePath + "/template-registries/{instanceId}/syncs",
		Summary: "Sync a template registry now", Description: "Reads the registry's templates now (a failed read is a 200 with status error)." + owner,
		Tags: []string{tagTemplates}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapabilityOwner, Scope: ScopeInstance}, h.syncNow)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-registry-template-icon", Method: http.MethodGet,
		Path:    BasePath + "/template-registries/{instanceId}/templates/{templateId}/icon",
		Summary: "Get a registry template's icon",
		Description: "The cached icon of a registry's template for any signed-in user (stacks created from it show it); sandboxed, " +
			"cacheable forever with v set to its sha256.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound},
		Responses: map[string]*huma.Response{"200": {Description: "Icon bytes", Content: map[string]*huma.MediaType{
			"image/*": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
	}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.icon)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-template-catalog", Method: http.MethodGet, Path: BasePath + "/template-catalog",
		Summary: "Browse templates of every registry",
		Description: "This instance's templates the caller sees in full (template.read on them) and, with template.read on the " +
			"instance, the cached templates of every added registry; q, tag and registry filter it.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusUnprocessableEntity},
	}, Capability: CapTemplateRead, Scope: ScopeResource}, h.listCatalog)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-catalog-item", Method: http.MethodGet, Path: BasePath + "/template-catalog/{instanceId}/{templateId}",
		Summary: "Get a registry template", Tags: []string{tagTemplates}, Errors: []int{http.StatusForbidden, http.StatusNotFound},
	}, Capability: CapTemplateRead, Scope: ScopeInstance}, h.getCatalogItem)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-catalog-definition", Method: http.MethodGet,
		Path:    BasePath + "/template-catalog/{instanceId}/{templateId}/versions/{version}/definition",
		Summary: "Get a registry template version's Compose files",
		Description: "Downloads the version from its registry (checked against the cached digest) and returns its Compose files and " +
			".env. Needs template.use on the instance. 502 when the registry cannot be read.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway},
	}, Capability: CapTemplateUse, Scope: ScopeInstance, Audit: AuditAlways}, h.definition)
}
