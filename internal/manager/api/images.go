package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Image capabilities (#17).
const (
	CapImageRead   Capability = "image.read"
	CapImagePull   Capability = "image.pull"
	CapImageTag    Capability = "image.tag"
	CapImageRemove Capability = "image.remove"
)

// Image is an image of an environment. Images are identified by their ID
// (sha256:...) in paths and permission rules; references (repository:tag)
// can change.
//
// Shaping (#17): image.read shows everything; image.tag or image.remove
// on the image shows only id, references and environmentId (view minimal).
type Image struct {
	ID            string              `json:"id" example:"sha256:4e1b5f1a6d8e..."`
	EnvironmentID string              `json:"environmentId"`
	RepoTags      []string            `json:"repoTags" doc:"References (repository:tag); empty for untagged (dangling) images."`
	Protection    *ResourceProtection `json:"protection,omitempty" doc:"Set for images DockYard's own containers run (#32): removal is refused."`
	View          string              `json:"view" enum:"minimal,full"`
	Actions       []string            `json:"actions"`

	RepoDigests []string          `json:"repoDigests,omitempty" doc:"Full view."`
	CreatedAt   *time.Time        `json:"createdAt,omitempty"`
	Size        int64             `json:"size,omitempty" doc:"Bytes."`
	Labels      map[string]string `json:"labels,omitempty"`
	UsedBy      []ContainerRef    `json:"usedBy,omitempty" doc:"Containers created from the image (running or not)."`
	InUse       bool              `json:"inUse,omitempty" doc:"Full view: used by at least one container."`
	Details     *ImageDetails     `json:"details,omitempty" doc:"Full view of GET only."`
}

// ImageDetails is the configuration of an inspected image.
type ImageDetails struct {
	OS            string   `json:"os,omitempty"`
	Architecture  string   `json:"architecture,omitempty"`
	Variant       string   `json:"variant,omitempty"`
	Author        string   `json:"author,omitempty"`
	Entrypoint    []string `json:"entrypoint"`
	Cmd           []string `json:"cmd"`
	WorkingDir    string   `json:"workingDir,omitempty"`
	User          string   `json:"user,omitempty"`
	ExposedPorts  []string `json:"exposedPorts"`
	Volumes       []string `json:"volumes"`
	HasHealthTest bool     `json:"hasHealthTest"`
	Removal       Removal  `json:"removal"`
}

func imageResource(env, id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeImage, ID: id, EnvironmentID: env, Parents: []authz.ResourceRef{}}
}

func newImage(env string, im protocol.ImageSummary, v authz.View) Image {
	out := Image{ID: im.ID, EnvironmentID: env, RepoTags: nonNil(im.RepoTags), Protection: newProtection(im.Protection), View: v.Level.String(),
		Actions: Actions(v), InUse: len(im.UsedBy) > 0}
	if !v.Full() {
		out.InUse = false
		return out
	}
	created := im.Created
	out.RepoDigests, out.CreatedAt, out.Size, out.Labels, out.UsedBy = im.RepoDigests, &created, im.Size, im.Labels, newContainerRefs(im.UsedBy)
	return out
}

func imageRemoval(im protocol.ImageSummary) Removal {
	r := newRemoval("The image and its unused parent layers are deleted from this environment; pulling it again downloads it.",
		"Exact permission rules on this image are removed.")
	r.blockProtected(im.Protection)
	if len(im.UsedBy) > 0 {
		r.block(CodeImageInUse, "The image is used by containers; remove them first.")
	}
	if len(im.RepoTags) > 1 {
		r.Consequences = append(r.Consequences, "The image has several tags: removal needs force=true and removes every tag.")
	}
	return r
}

type listImagesInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
	SortParam
	Q        string `query:"q" maxLength:"256" doc:"Only images with a reference containing this text (case-insensitive)."`
	Dangling string `query:"dangling" enum:"true,false" doc:"true: only untagged images; false: only tagged ones."`
}

// ImagePath are the path parameters of an image route.
type ImagePath struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	ImageID       string `path:"imageId" maxLength:"71" pattern:"^(sha256:)?[a-f0-9]{12,64}$" doc:"Image ID (sha256:<hex>) or a unique prefix of at least 12 hex digits."`
}

type imageOutput struct{ Body Image }
type listImagesOutput struct{ Body Page[Image] }

type pullImageInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		Reference            string `json:"reference" minLength:"1" maxLength:"512" example:"ghcr.io/org/app:1.2" doc:"Image reference; without a tag, latest."`
		Platform             string `json:"platform,omitempty" maxLength:"64" example:"linux/arm64" doc:"Default: the Engine's platform."`
		RegistryConnectionID string `json:"registryConnectionId,omitempty" maxLength:"64" doc:"Registry connection to authenticate with (#19); default: the matching connection, else anonymous."`
	}
}

type tagImageInput struct {
	ImagePath
	Body struct {
		Repository string `json:"repository" minLength:"1" maxLength:"512" example:"registry.example.com/team/app"`
		Tag        string `json:"tag,omitempty" maxLength:"128" pattern:"^[A-Za-z0-9_][A-Za-z0-9_.-]*$" doc:"Default latest."`
	}
}

type deleteImageInput struct {
	ImagePath
	IdempotencyKeyParam
	Force bool `query:"force" doc:"Remove an image with several tags (every tag). Images in use are never removed."`
}

func (h *dockerAPI) listImages(ctx context.Context, in *listImagesInput) (*listImagesOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	all, err := h.svc.ListImages(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	type item struct {
		im protocol.ImageSummary
		v  authz.View
	}
	var items []item
	for _, im := range all {
		v := authz.ViewOf(sc.c, imageResource(sc.env.ID, im.ID))
		switch {
		case !v.Visible():
			continue
		case in.Q != "" && !slices.ContainsFunc(im.RepoTags, func(t string) bool { return containsFold(t, in.Q) }):
			continue
		case in.Dangling != "" && (in.Dangling == "true") != (len(im.RepoTags) == 0):
			continue
		}
		items = append(items, item{im, v})
	}
	first := func(i item) string {
		if len(i.im.RepoTags) > 0 {
			return "0" + i.im.RepoTags[0]
		}
		return "1" // untagged images last
	}
	key, desc, err := sortSpec(in.Sort, map[string]func(item) string{
		"reference": first,
		"createdAt": func(i item) string { return timeKey(i.im.Created) },
		"size":      func(i item) string { return sizeKey(i.im.Size) },
	}, SortKey{Field: "reference"}, func(i item) string { return i.im.ID })
	if err != nil {
		return nil, err
	}
	page, next, total, err := memPage(items, key, desc, in.PageParams, QueryFingerprint("images", sc.env.ID, in.Sort, in.Q, in.Dangling))
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(page))
	for _, it := range page {
		out = append(out, newImage(sc.env.ID, it.im, it.v))
	}
	return &listImagesOutput{Body: NewPage(out, next, total)}, nil
}

func (h *dockerAPI) visibleImage(ctx context.Context, sc *scope, id string) (protocol.ImageDetails, authz.View, error) {
	im, err := h.svc.InspectImage(ctx, sc.env.ID, id)
	if err != nil {
		return im, authz.View{}, lookupErr(err, "image")
	}
	v := authz.ViewOf(sc.c, imageResource(sc.env.ID, im.ID))
	if !v.Visible() {
		return im, v, NotFound("image not found")
	}
	return im, v, nil
}

func (h *dockerAPI) getImage(ctx context.Context, in *ImagePath) (*imageOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	im, v, err := h.visibleImage(ctx, sc, in.ImageID)
	if err != nil {
		return nil, err
	}
	return &imageOutput{Body: fullImage(sc.env.ID, im, v)}, nil
}

func fullImage(env string, im protocol.ImageDetails, v authz.View) Image {
	out := newImage(env, im.ImageSummary, v)
	if v.Full() {
		out.Details = &ImageDetails{OS: im.OS, Architecture: im.Architecture, Variant: im.Variant, Author: im.Author,
			Entrypoint: nonNil(im.Entrypoint), Cmd: nonNil(im.Cmd), WorkingDir: im.WorkingDir, User: im.User,
			ExposedPorts: nonNil(im.ExposedPorts), Volumes: nonNil(im.Volumes), HasHealthTest: im.HasHealthTest,
			Removal: imageRemoval(im.ImageSummary)}
	}
	return out
}

func (h *dockerAPI) imageAction(ctx context.Context, envID, id string, want Capability, mutation bool) (*scope, protocol.ImageDetails, authz.View, error) {
	sc, err := h.environment(ctx, envID, mutation)
	if err != nil {
		return nil, protocol.ImageDetails{}, authz.View{}, err
	}
	im, v, err := h.visibleImage(ctx, sc, id)
	if err != nil {
		return nil, im, v, err
	}
	if !v.Has(string(want)) {
		return nil, im, v, Forbidden("not permitted to " + strings.TrimPrefix(string(want), "image.") + " this image")
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeImage, ID: im.ID, EnvironmentID: sc.env.ID})
	return sc, im, v, nil
}

func (h *dockerAPI) pullImage(ctx context.Context, in *pullImageInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	if !sc.c.Can(string(CapImagePull), authz.InEnvironment(catalog.TypeImage, sc.env.ID)).Allowed {
		return nil, Forbidden("not permitted to pull images in this environment")
	}
	j, err := h.svc.PullImage(ctx, sc.p, sc.env.ID, protocol.ImagePullInput{Reference: in.Body.Reference, Platform: in.Body.Platform},
		in.Body.RegistryConnectionID, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeImage, ID: in.Body.Reference, EnvironmentID: sc.env.ID})
	if in.Body.RegistryConnectionID != "" {
		audit.SetDetail(ctx, "registryConnectionId", in.Body.RegistryConnectionID)
	}
	return Accepted(j), nil
}

func (h *dockerAPI) tagImage(ctx context.Context, in *tagImageInput) (*imageOutput, error) {
	sc, im, _, err := h.imageAction(ctx, in.EnvironmentID, in.ImageID, CapImageTag, true)
	if err != nil {
		return nil, err
	}
	target := in.Body.Repository
	if in.Body.Tag != "" {
		target += ":" + in.Body.Tag
	}
	updated, err := h.svc.TagImage(ctx, sc.env.ID, im, target)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.SetDetail(ctx, "tag", target)
	return &imageOutput{Body: fullImage(sc.env.ID, updated, authz.ViewOf(sc.c, imageResource(sc.env.ID, updated.ID)))}, nil
}

func (h *dockerAPI) deleteImage(ctx context.Context, in *deleteImageInput) (*JobAccepted, error) {
	sc, im, _, err := h.imageAction(ctx, in.EnvironmentID, in.ImageID, CapImageRemove, true)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.RemoveImage(ctx, sc.p, sc.env.ID, im, in.Force, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

func registerImages(a huma.API, deps Deps) {
	h := newDockerAPI(deps)
	base := BasePath + "/environments/{environmentId}/images"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-images", Method: http.MethodGet, Path: base, Summary: "List images",
			Description: "Every image of the environment with the containers using it, filtered per item (#17). Sort fields: " +
				"reference (default; untagged last), createdAt, size. total counts the visible matches.",
			Tags: []string{tagImages}, Errors: dockerReadErrors,
		},
		Capability: CapImageRead, Scope: ScopeEnvironment,
	}, h.listImages)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-image", Method: http.MethodGet, Path: base + "/{imageId}", Summary: "Get an image",
			Description: "The image's configuration, users and removal consequences (full view with image.read).",
			Tags:        []string{tagImages}, Errors: dockerReadErrors,
		},
		Capability: CapImageRead, Scope: ScopeResource,
	}, h.getImage)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-image", Method: http.MethodDelete, Path: base + "/{imageId}", Summary: "Remove an image",
			Description: "Starts an image.remove job (202). Images used by any container are refused (409 image_in_use); an image " +
				"with several tags needs force=true (409 conflict otherwise).",
			Tags: []string{tagImages}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapImageRemove, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.deleteImage)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-image-pull", Method: http.MethodPost, Path: base + "/pulls", Summary: "Pull an image",
			Description: "Starts an image.pull job (202) with progress in its events. A registry connection (#19) authenticates the " +
				"pull when selected or matching; credentials go to the agent for that operation only and never appear in the job. " +
				"Registry failures fail the job with the class unauthorized, forbidden, not_found, rate_limited or registry_unavailable.",
			Tags: []string{tagImages}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapImagePull, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.pullImage)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-image-tag", Method: http.MethodPost, Path: base + "/{imageId}/tags", Summary: "Tag an image",
			Description: "Adds repository[:tag] to the image (a short agent request, no job). An existing tag moves to this image.",
			Tags:        []string{tagImages}, Errors: dockerJobErrors,
		},
		Capability: CapImageTag, Scope: ScopeResource,
	}, h.tagImage)
}
