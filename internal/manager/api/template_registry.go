package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/auth/throttle"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
)

// This instance's public template registry (template registry): the
// published versions of public templates, readable without signing in so
// other Docker Manager instances can add this instance's URL as a registry
// (and people can browse it). Private templates, drafts and unpublished
// templates never appear. Requests are rate limited per client address.

// TemplateRegistryFormat identifies the index format.
const TemplateRegistryFormat = "docker-manager.template-registry/v1"

// registryMaxVersions bounds the versions listed per template.
const registryMaxVersions = 50

// TemplateRegistryIcon is a public template's icon.
type TemplateRegistryIcon struct {
	MediaType string `json:"mediaType" example:"image/svg+xml"`
	SHA256    string `json:"sha256" doc:"Changes with the icon."`
	Size      int64  `json:"size"`
	URL       string `json:"url" example:"/api/v1/template-registry/templates/0190a6e0-.../icon?v=3f2a..." doc:"Relative to the registry URL."`
}

// TemplateRegistryArchive describes a version's archive.
type TemplateRegistryArchive struct {
	SHA256      string `json:"sha256" doc:"SHA-256 of the tar.gz: check it after downloading."`
	Size        int64  `json:"size"`
	ContentSize int64  `json:"contentSize"`
	Entries     int    `json:"entries"`
	URL         string `json:"url" doc:"Relative to the registry URL."`
}

// TemplateRegistryVersion is a published version.
type TemplateRegistryVersion struct {
	Number      int                     `json:"number"`
	Label       string                  `json:"label" example:"1.2.0"`
	Notes       string                  `json:"notes,omitempty"`
	PublishedAt time.Time               `json:"publishedAt"`
	Archive     TemplateRegistryArchive `json:"archive"`
}

// TemplateRegistryEntry is a public template.
type TemplateRegistryEntry struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name" example:"Nextcloud"`
	Description string                    `json:"description,omitempty"`
	Tags        []string                  `json:"tags"`
	Links       []WebLink                 `json:"links,omitempty" doc:"Web links (documentation, website, repository); readers drop invalid ones."`
	Icon        *TemplateRegistryIcon     `json:"icon,omitempty"`
	UpdatedAt   time.Time                 `json:"updatedAt"`
	Versions    []TemplateRegistryVersion `json:"versions" doc:"Newest first (at most 50)."`
}

// TemplateRegistryIndex is this instance's public registry.
type TemplateRegistryIndex struct {
	Format     string                  `json:"format" enum:"docker-manager.template-registry/v1"`
	InstanceID string                  `json:"instanceId" doc:"Identifies the registry: an instance added again under another URL is recognized."`
	Name       string                  `json:"name" example:"Homelab" doc:"The instance's display name."`
	URL        string                  `json:"url,omitempty" example:"https://docker.example.com" doc:"The registry URL (DOCKER_MANAGER_PUBLIC_URL)."`
	UpdatedAt  time.Time               `json:"updatedAt" doc:"The newest change of a listed template or version."`
	Templates  []TemplateRegistryEntry `json:"templates"`
}

type templateRegistryOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         *TemplateRegistryIndex
}

type templateRegistryInput struct {
	IfNoneMatch string `header:"If-None-Match" maxLength:"256" doc:"The ETag of a copy you have: 304 when it is current."`
}

// registryAPI serves the public registry.
type registryAPI struct {
	svc        TemplateService
	settings   SettingsService
	instanceID string
	publicURL  string
	disabled   bool
	limit      *throttle.Limiter
	archives   *throttle.Limiter
}

// allow applies the per-address limits (archives have their own, lower).
func (h *registryAPI) allow(ctx context.Context, l *throttle.Limiter) error {
	if h.disabled || h.svc == nil {
		return NotFound("this instance does not share templates")
	}
	key := throttle.IPKey(requestinfo.ClientIP(ctx))
	if !l.Take(key) {
		secs := int(l.RetryAfter(key) / time.Second)
		return RateLimited("too many registry requests; try again later").WithHeader("Retry-After", strconv.Itoa(max(secs, 1)))
	}
	return nil
}

// public returns a public template with at least one version.
func (h *registryAPI) public(ctx context.Context, id string) (domain.Template, error) {
	t, err := h.svc.Get(ctx, id)
	if err != nil || t.Visibility != domain.TemplatePublic || t.Latest == nil {
		if err == nil || errors.Is(err, domain.ErrTemplateNotFound) {
			return domain.Template{}, NotFound("template not found")
		}
		return domain.Template{}, Internal(err)
	}
	return t, nil
}

func registryPath(parts ...string) string {
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return BasePath + "/template-registry/templates/" + strings.Join(parts, "/")
}

func (h *registryAPI) index(ctx context.Context, in *templateRegistryInput) (*templateRegistryOutput, error) {
	if err := h.allow(ctx, h.limit); err != nil {
		return nil, err
	}
	all, err := h.svc.List(ctx, "", 0)
	if err != nil {
		return nil, Internal(err)
	}
	idx := TemplateRegistryIndex{Format: TemplateRegistryFormat, InstanceID: h.instanceID, Name: "Docker Manager", URL: h.publicURL,
		Templates: []TemplateRegistryEntry{}}
	if h.settings != nil {
		if s, err := h.settings.Get(ctx); err == nil && s.Name != "" {
			idx.Name = s.Name
		}
	}
	for _, t := range all {
		if t.Visibility != domain.TemplatePublic || t.Latest == nil {
			continue
		}
		vs, err := h.svc.Versions(ctx, t.ID)
		if err != nil {
			return nil, Internal(err)
		}
		e := TemplateRegistryEntry{ID: t.ID, Name: t.Name, Description: t.Description, Tags: slices.Clone(t.Tags), UpdatedAt: t.UpdatedAt,
			Links: webLinks(t.Links), Versions: []TemplateRegistryVersion{}}
		if e.Tags == nil {
			e.Tags = []string{}
		}
		if t.Icon != nil {
			e.Icon = &TemplateRegistryIcon{MediaType: t.Icon.MediaType, SHA256: t.Icon.SHA256, Size: t.Icon.Size,
				URL: registryPath(t.ID, "icon") + "?v=" + t.Icon.SHA256}
			if t.Icon.UpdatedAt.After(e.UpdatedAt) {
				e.UpdatedAt = t.Icon.UpdatedAt
			}
		}
		for _, v := range vs {
			if len(e.Versions) == registryMaxVersions {
				break
			}
			e.Versions = append(e.Versions, TemplateRegistryVersion{Number: v.Number, Label: v.Label, Notes: v.Notes, PublishedAt: v.CreatedAt,
				Archive: TemplateRegistryArchive{SHA256: v.ArchiveSHA256, Size: v.ArchiveSize, ContentSize: v.ContentSize, Entries: v.Entries,
					URL: registryPath(t.ID, "versions", strconv.Itoa(v.Number), "archive")}})
			if v.CreatedAt.After(e.UpdatedAt) {
				e.UpdatedAt = v.CreatedAt
			}
		}
		if e.UpdatedAt.After(idx.UpdatedAt) {
			idx.UpdatedAt = e.UpdatedAt
		}
		idx.Templates = append(idx.Templates, e)
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return nil, Internal(err)
	}
	sum := sha256.Sum256(b)
	etag := ETag("r1-" + hex.EncodeToString(sum[:16]))
	out := &templateRegistryOutput{Status: http.StatusOK, ETag: etag, CacheControl: "no-cache"}
	if in.IfNoneMatch != "" && slices.Contains(strings.Split(strings.ReplaceAll(in.IfNoneMatch, " ", ""), ","), etag) {
		out.Status = http.StatusNotModified
		return out, nil
	}
	out.Body = &idx
	return out, nil
}

type registryIconInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    string `query:"v" maxLength:"64" doc:"The icon's sha256: the response is then cacheable forever."`
}

func (h *registryAPI) icon(ctx context.Context, in *registryIconInput) (*huma.StreamResponse, error) {
	if err := h.allow(ctx, h.limit); err != nil {
		return nil, err
	}
	t, err := h.public(ctx, in.TemplateID)
	if err != nil {
		return nil, err
	}
	icon, data, err := h.svc.Icon(ctx, t.ID)
	if err != nil {
		return nil, templateError(err)
	}
	return iconResponse(icon, data, in.Version), nil
}

type registryArchiveInput struct {
	TemplateID string `path:"templateId" maxLength:"64" doc:"Template ID."`
	Version    int    `path:"version" minimum:"1" doc:"Version number."`
}

func (h *registryAPI) archive(ctx context.Context, in *registryArchiveInput) (*huma.StreamResponse, error) {
	if err := h.allow(ctx, h.archives); err != nil {
		return nil, err
	}
	t, err := h.public(ctx, in.TemplateID)
	if err != nil {
		return nil, err
	}
	v, data, err := h.svc.Archive(ctx, t.ID, in.Version)
	if err != nil {
		return nil, templateError(err)
	}
	name := strings.ToLower(strings.Join(strings.FieldsFunc(t.Name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}), "-"))
	if name == "" {
		name = "template"
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "application/gzip")
		hctx.SetHeader("Content-Length", strconv.Itoa(len(data)))
		hctx.SetHeader("Content-Disposition", contentDisposition(name+"-"+v.Label+".tar.gz"))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("ETag", ETag(v.ArchiveSHA256))
		hctx.SetStatus(http.StatusOK)
		_, _ = hctx.BodyWriter().Write(data)
	}}, nil
}

func registerTemplateRegistry(a huma.API, deps Deps) {
	h := &registryAPI{svc: deps.Templates, settings: deps.Settings, instanceID: deps.InstanceID, publicURL: deps.Deployment.PublicURL,
		disabled: deps.TemplateRegistryDisabled,
		limit:    throttle.New(throttle.Limit{Every: time.Second, Burst: 60}, deps.clock(), 10000),
		archives: throttle.New(throttle.Limit{Every: 3 * time.Second, Burst: 20}, deps.clock(), 10000)}
	errs := []int{http.StatusNotFound, http.StatusTooManyRequests}
	const note = " Public: no sign-in; rate limited per client address (429 with Retry-After). 404 when the instance does not share " +
		"templates (DOCKER_MANAGER_TEMPLATE_REGISTRY_ENABLED=false)."

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-registry", Method: http.MethodGet, Path: BasePath + "/template-registry",
		Summary: "Get this instance's public template registry",
		Description: "The published versions of this instance's public templates (newest 50 per template), for other Docker Manager " +
			"instances that added this instance's URL as a registry. Private templates, drafts and templates without a version never " +
			"appear. Revalidate with If-None-Match (304)." + note,
		Tags: []string{tagTemplates}, Errors: errs,
		Responses: map[string]*huma.Response{"304": {Description: "Not modified: your copy (If-None-Match) is current."}},
	}, Capability: CapabilityPublic, Scope: ScopeNone}, h.index)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-template-registry-icon", Method: http.MethodGet, Path: BasePath + "/template-registry/templates/{templateId}/icon",
		Summary: "Get a public template's icon", Description: "Served sandboxed; cacheable forever with v set to its sha256." + note,
		Tags: []string{tagTemplates}, Errors: errs,
		Responses: map[string]*huma.Response{"200": {Description: "Icon bytes", Content: map[string]*huma.MediaType{
			"image/*": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
	}, Capability: CapabilityPublic, Scope: ScopeNone}, h.icon)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "download-template-registry-archive", Method: http.MethodGet,
		Path:    BasePath + "/template-registry/templates/{templateId}/versions/{version}/archive",
		Summary: "Download a public template version",
		Description: "The version's tar.gz (every file, .env included). Check its SHA-256 against the index. Archives have a lower " +
			"rate limit than the index." + note,
		Tags: []string{tagTemplates}, Errors: errs,
		Responses: map[string]*huma.Response{"200": {Description: "The version's tar.gz", Content: map[string]*huma.MediaType{
			"application/gzip": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
	}, Capability: CapabilityPublic, Scope: ScopeNone}, h.archive)
}
