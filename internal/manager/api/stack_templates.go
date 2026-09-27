package api

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Stacks from templates (template registry): creating a stack from a
// published version, the version's Compose sources for the create dialog
// (its .env to edit), and the icon map that lets stack lists show their
// template's current icon.

// StackTemplateRef names the template version a stack was created from.
type StackTemplateRef struct {
	InstanceID   string `json:"instanceId" doc:"The registry: the manager instance that owns the template."`
	TemplateID   string `json:"templateId"`
	Name         string `json:"name" example:"Nextcloud" doc:"The template's name when the stack was created."`
	Version      int    `json:"version"`
	VersionLabel string `json:"versionLabel" example:"1.2.0"`
}

func newStackTemplateRef(t *domain.StackTemplateRef) *StackTemplateRef {
	if t == nil {
		return nil
	}
	return &StackTemplateRef{InstanceID: t.InstanceID, TemplateID: t.TemplateID, Name: t.Name, Version: t.Version, VersionLabel: t.VersionLabel}
}

// templateUseAllowed reports whether the caller may use a template of the
// given registry: this instance's templates need template.use on the
// template.
func (h *stacksAPI) templateUseAllowed(ctx context.Context, instanceID, templateID string) (bool, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return false, err
	}
	if instanceID != h.deps.InstanceID {
		return c.Can(string(CapTemplateUse), authz.Instance()).Allowed, nil
	}
	return c.Can(string(CapTemplateUse), templateResource(templateID)).Allowed, nil
}

type createStackFromTemplateInput struct {
	Body struct {
		EnvironmentID string `json:"environmentId" minLength:"1" maxLength:"64" doc:"The environment to create the stack in."`
		Name          string `json:"name" minLength:"1" maxLength:"63" example:"nextcloud" doc:"Compose project name (lower-case letters, digits, '-' and '_'); also the project directory in the stacks volume."`
		DisplayName   string `json:"displayName,omitempty" example:"Nextcloud" maxLength:"128"`
		Description   string `json:"description,omitempty" maxLength:"1024"`
		InstanceID    string `json:"instanceId,omitempty" maxLength:"64" doc:"The registry (the owning manager's instance ID); empty: this instance."`
		TemplateID    string `json:"templateId" minLength:"1" maxLength:"64"`
		Version       int    `json:"version" minimum:"1" doc:"The published version number."`
	}
}

func (h *stacksAPI) createFromTemplate(ctx context.Context, in *createStackFromTemplateInput) (*createStackOutput, error) {
	p, err := h.requireInEnvironment(ctx, in.Body.EnvironmentID, CapStackCreate)
	if err != nil {
		return nil, err
	}
	instance := in.Body.InstanceID
	if instance == "" {
		instance = h.deps.InstanceID
	}
	ok, err := h.templateUseAllowed(ctx, instance, in.Body.TemplateID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, Forbidden("not permitted: " + string(CapTemplateUse))
	}
	st, val, err := h.svc.CreateFromTemplate(ctx, p, domain.StackFromTemplate{EnvironmentID: in.Body.EnvironmentID, Name: in.Body.Name,
		DisplayName: in.Body.DisplayName, Meta: domain.DisplayMeta{Description: in.Body.Description},
		InstanceID: instance, TemplateID: in.Body.TemplateID, Version: in.Body.Version})
	if err != nil {
		if errors.Is(err, domain.ErrTemplateNotFound) || errors.Is(err, domain.ErrTemplateVersionNotFound) {
			return nil, NotFound("template version not found")
		}
		return nil, stackErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID})
	audit.SetDetail(ctx, "project", st.Name)
	audit.SetDetail(ctx, "templateId", in.Body.TemplateID)
	audit.SetDetail(ctx, "templateVersion", in.Body.Version)
	c, _, _ := h.checker(ctx)
	out := &createStackOutput{Location: BasePath + "/stacks/" + st.ID, ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)}}
	out.Body.Stack = newStack(st, authz.ViewOf(c, stackResource(st)), true)
	out.Body.Validation = newValidation(val)
	return out, nil
}

func registerStackTemplates(a huma.API, h *stacksAPI) {
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-template-creation", Method: http.MethodPost, Path: BasePath + "/stacks/template-creations",
		Summary: "Create a stack from a template",
		Description: "Creates a stack from a published template version: validates its Compose definition on the environment's agent, " +
			"copies every file of the version into a new project directory of the stacks volume (never an existing one) and records it " +
			"as the first revision. Nothing is deployed; to use your own .env values, save the stack's .env (file routes) before " +
			"deploying. Needs stack.create in the environment and template.use on the template (added registries: on the instance). " +
			"409 stack_name_taken, compose_project_exists, stack_directory_exists; 422 invalid_definition.",
		Tags: []string{tagStacks}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusNotImplemented},
	}, Capability: CapStackCreate, Scope: ScopeEnvironment}, h.createFromTemplate)
}

// --- template version definitions and the icon map (templates.go) ---

// TemplateDefinitionFile is one Compose source of a version.
type TemplateDefinitionFile struct {
	Path    string `json:"path" example:".env"`
	Content string `json:"content" doc:"UTF-8 text (.env values included: needs template.use)."`
}

// TemplateDefinition is a version's Compose files and .env.
type TemplateDefinition struct {
	Version TemplateVersion          `json:"version"`
	Files   []TemplateDefinitionFile `json:"files"`
}

type templateDefinitionOutput struct{ Body TemplateDefinition }

func (h *templatesAPI) definition(ctx context.Context, in *templateVersionInput) (*templateDefinitionOutput, error) {
	svc, _, t, _, err := h.visible(ctx, in.TemplateID, CapTemplateUse)
	if err != nil {
		return nil, err
	}
	v, files, err := svc.Definition(ctx, t.ID, in.Version)
	if err != nil {
		return nil, templateError(err)
	}
	out := TemplateDefinition{Version: newTemplateVersion(v), Files: []TemplateDefinitionFile{}}
	for _, f := range files {
		if utf8.Valid(f.Content) {
			out.Files = append(out.Files, TemplateDefinitionFile{Path: f.Path, Content: string(f.Content)})
		}
	}
	return &templateDefinitionOutput{Body: out}, nil
}

// TemplateIconRef is the current icon of a template, for stacks created
// from it.
type TemplateIconRef struct {
	InstanceID string `json:"instanceId"`
	TemplateID string `json:"templateId"`
	URL        string `json:"url" example:"/api/v1/templates/0190a6e0-.../icon?v=3f2a..."`
}

// TemplateIconMap lists the icons of every known template.
type TemplateIconMap struct {
	InstanceID string            `json:"instanceId" doc:"This instance's ID (its own templates' registry)."`
	Items      []TemplateIconRef `json:"items"`
}

type templateIconMapOutput struct{ Body TemplateIconMap }

func (h *templatesAPI) iconMap(ctx context.Context, _ *struct{}) (*templateIconMapOutput, error) {
	// Icons are decoration shown with stacks: any signed-in user reads them.
	svc, _, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := svc.List(ctx, "", 0)
	if err != nil {
		return nil, Internal(err)
	}
	out := TemplateIconMap{InstanceID: h.instanceID, Items: []TemplateIconRef{}}
	for _, t := range ts {
		if i := newTemplateIcon(t.ID, t.Icon); i != nil {
			out.Items = append(out.Items, TemplateIconRef{InstanceID: h.instanceID, TemplateID: t.ID, URL: i.URL})
		}
	}
	return &templateIconMapOutput{Body: out}, nil
}

func registerTemplateUse(a huma.API, h *templatesAPI) {
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-version-definition", Method: http.MethodGet,
		Path:    BasePath + "/templates/{templateId}/versions/{version}/definition",
		Summary: "Get a version's Compose files",
		Description: "The Compose files and .env at the root of a published version, as text: what a stack created from it starts " +
			"with. Contains .env values; needs template.use.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusNotFound, http.StatusForbidden},
	}, Capability: CapTemplateUse, Scope: ScopeResource, Audit: AuditAlways}, h.definition)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-template-icons", Method: http.MethodGet, Path: BasePath + "/template-icons",
		Summary: "List template icons",
		Description: "The current icon of every template with one, by registry (instance ID) and template ID, so stack lists show " +
			"the icon of the template a stack was created from. Any signed-in user may read it; it changes with the templates live topic.",
		Tags: []string{tagTemplates}, Errors: []int{http.StatusUnauthorized},
	}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.iconMap)
}
