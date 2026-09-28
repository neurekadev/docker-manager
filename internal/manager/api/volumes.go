package api

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Volume capabilities (#17).
const (
	CapVolumeRead   Capability = "volume.read"
	CapVolumeCreate Capability = "volume.create"
	CapVolumeRemove Capability = "volume.remove"
)

// Volume is a Docker volume of an environment. Its host path is never
// shown; file access is #15.
//
// Shaping (#17): volume.read shows everything; any other capability on the
// volume (its files, removal, ...) shows only name, environmentId and the
// in-use flag (view minimal).
type Volume struct {
	Name          string              `json:"name" example:"shop_data"`
	EnvironmentID string              `json:"environmentId"`
	InUse         bool                `json:"inUse" doc:"At least one container (running or not) mounts the volume."`
	Protection    *ResourceProtection `json:"protection,omitempty" doc:"Set for Docker Manager's own volumes (#32): removal and mounting into new containers are refused."`
	View          string              `json:"view" enum:"minimal,full"`
	Actions       []string            `json:"actions"`
	Driver        string              `json:"driver,omitempty" doc:"Full view."`
	Scope         string              `json:"scope,omitempty"`
	CreatedAt     *time.Time          `json:"createdAt,omitempty"`
	Labels        map[string]string   `json:"labels,omitempty"`
	Options       map[string]string   `json:"options,omitempty"`
	UsedBy        []ContainerRef      `json:"usedBy,omitempty"`
	Stack         *StackMembership    `json:"stack,omitempty"`
	Removal       *Removal            `json:"removal,omitempty" doc:"Full view of GET only."`
}

func volumeResource(env string, v protocol.VolumeInfo, stackIDs map[string]string) authz.Resource {
	return authz.Resource{Type: catalog.TypeVolume, ID: v.Name, EnvironmentID: env, Parents: parents(catalog.TypeVolume, stackIDs, v.Stack)}
}

func newVolume(env string, v protocol.VolumeInfo, view authz.View, stackIDs map[string]string) Volume {
	out := Volume{Name: v.Name, EnvironmentID: env, InUse: len(v.UsedBy) > 0, Protection: newProtection(v.Protection), View: view.Level.String(),
		Actions: Actions(view)}
	if !view.Full() {
		return out
	}
	created := &v.Created
	if v.Created.IsZero() {
		created = nil
	}
	out.Driver, out.Scope, out.CreatedAt, out.Labels, out.Options = v.Driver, v.Scope, created, v.Labels, v.Options
	out.UsedBy = newContainerRefs(v.UsedBy)
	out.Stack = membership(v.Stack, stackIDs, managedStack(v.Stack, stackIDs))
	return out
}

func volumeRemoval(v protocol.VolumeInfo, managed bool) Removal {
	r := newRemoval("All data in the volume is deleted permanently; Docker Manager cannot undo it (restore it from a backup, #10).",
		"Exact permission rules on this volume and its files are removed.")
	r.blockProtected(v.Protection)
	if managed {
		r.block(CodeStackManaged, "The volume belongs to a Docker Manager-managed stack; remove it from the stack instead.")
	}
	if len(v.UsedBy) > 0 {
		r.block(CodeVolumeInUse, "Containers mount the volume; remove them first.")
	}
	return r
}

type listVolumesInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
	SortParam
	Q      string `query:"q" maxLength:"128" doc:"Only volumes whose name contains this text (case-insensitive)."`
	InUse  string `query:"inUse" enum:"true,false" doc:"true: only volumes used by a container; false: only unused ones."`
	Driver string `query:"driver" maxLength:"128" doc:"Only volumes of this driver. Full view only."`
}

// VolumePath are the path parameters of a volume route.
type VolumePath struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	VolumeID      string `path:"volumeId" maxLength:"128" doc:"Volume name."`
}

type volumeOutput struct{ Body Volume }
type listVolumesOutput struct{ Body Page[Volume] }

type createVolumeInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		Name       string            `json:"name" minLength:"1" maxLength:"128" pattern:"^[a-zA-Z0-9][a-zA-Z0-9_.-]*$" example:"cache"`
		Driver     string            `json:"driver,omitempty" maxLength:"128" doc:"Default local."`
		DriverOpts map[string]string `json:"driverOpts,omitempty"`
		Labels     map[string]string `json:"labels,omitempty" doc:"docker-manager.* (except the docker-manager.*.exclude labels), dev.neureka.docker-manager.* and com.docker.compose.* are reserved."`
	}
}

type deleteVolumeInput struct {
	VolumePath
	IdempotencyKeyParam
}

func (h *dockerAPI) listVolumes(ctx context.Context, in *listVolumesInput) (*listVolumesOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	all, err := h.svc.ListVolumes(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	stacks := sc.stacks(ctx, h.svc)
	type item struct {
		vol protocol.VolumeInfo
		v   authz.View
	}
	var items []item
	for _, vol := range all {
		v := authz.ViewOf(sc.c, volumeResource(sc.env.ID, vol, stacks))
		switch {
		case !v.Visible():
			continue
		case in.Q != "" && !containsFold(vol.Name, in.Q):
			continue
		case in.InUse != "" && (in.InUse == "true") != (len(vol.UsedBy) > 0):
			continue
		case in.Driver != "" && (!v.Full() || vol.Driver != in.Driver):
			continue
		}
		items = append(items, item{vol, v})
	}
	key, desc, err := sortSpec(in.Sort, map[string]func(item) string{
		"name":      func(i item) string { return i.vol.Name },
		"createdAt": func(i item) string { return timeKey(i.vol.Created) },
	}, SortKey{Field: "name"}, func(i item) string { return i.vol.Name })
	if err != nil {
		return nil, err
	}
	page, next, total, err := memPage(items, key, desc, in.PageParams, QueryFingerprint("volumes", sc.env.ID, in.Sort, in.Q, in.InUse, in.Driver))
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(page))
	for _, it := range page {
		out = append(out, newVolume(sc.env.ID, it.vol, it.v, stacks))
	}
	return &listVolumesOutput{Body: NewPage(out, next, total)}, nil
}

func (h *dockerAPI) visibleVolume(ctx context.Context, sc *scope, name string) (protocol.VolumeInfo, authz.View, error) {
	vol, err := h.svc.InspectVolume(ctx, sc.env.ID, name)
	if err != nil {
		return vol, authz.View{}, lookupErr(err, "volume")
	}
	v := authz.ViewOf(sc.c, volumeResource(sc.env.ID, vol, sc.stacks(ctx, h.svc)))
	if !v.Visible() {
		return vol, v, NotFound("volume not found")
	}
	return vol, v, nil
}

func (h *dockerAPI) getVolume(ctx context.Context, in *VolumePath) (*volumeOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	vol, v, err := h.visibleVolume(ctx, sc, in.VolumeID)
	if err != nil {
		return nil, err
	}
	stacks := sc.stacks(ctx, h.svc)
	out := newVolume(sc.env.ID, vol, v, stacks)
	if v.Full() {
		r := volumeRemoval(vol, h.svc.StackManaged(ctx, sc.env.ID, vol.Stack))
		out.Removal = &r
	}
	return &volumeOutput{Body: out}, nil
}

func (h *dockerAPI) createVolume(ctx context.Context, in *createVolumeInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	if !sc.c.Can(string(CapVolumeCreate), authz.InEnvironment(catalog.TypeVolume, sc.env.ID)).Allowed {
		return nil, Forbidden("not permitted to create volumes in this environment")
	}
	b := in.Body
	j, err := h.svc.CreateVolume(ctx, sc.p, sc.env.ID, protocol.VolumeCreateInput{Name: b.Name, Driver: b.Driver, DriverOpts: b.DriverOpts, Labels: b.Labels},
		in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeVolume, ID: b.Name, EnvironmentID: sc.env.ID})
	return Accepted(j), nil
}

func (h *dockerAPI) deleteVolume(ctx context.Context, in *deleteVolumeInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	vol, v, err := h.visibleVolume(ctx, sc, in.VolumeID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapVolumeRemove)) {
		return nil, Forbidden("not permitted to remove this volume")
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeVolume, ID: vol.Name, EnvironmentID: sc.env.ID})
	j, err := h.svc.RemoveVolume(ctx, sc.p, sc.env.ID, vol, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

// VolumeSize is a volume's size on disk.
type VolumeSize struct {
	Name      string `json:"name" example:"shop_data"`
	SizeBytes *int64 `json:"sizeBytes,omitempty" doc:"Absent when the Engine does not know it (volumes of other drivers) or the volume is newer than computedAt."`
}

// VolumeUsageList is the disk usage of an environment's volumes.
type VolumeUsageList struct {
	EnvironmentID string       `json:"environmentId"`
	Supported     bool         `json:"supported" doc:"false: the environment's agent predates volume sizes (upgrade it); items is empty."`
	ComputedAt    *time.Time   `json:"computedAt,omitempty" doc:"When the Engine computed the sizes (reused for up to a minute)."`
	Items         []VolumeSize `json:"items" doc:"The volumes the caller holds volume.read on, sorted by name."`
}

type volumeUsageInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
}

type volumeUsageOutput struct{ Body VolumeUsageList }

// volumeUsage lists the sizes of the volumes the caller may read in full.
// The Engine walks every local volume to compute them, so the resource
// service reuses one computation per environment for a minute and shares
// it between concurrent callers.
func (h *dockerAPI) volumeUsage(ctx context.Context, in *volumeUsageInput) (*volumeUsageOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	all, err := h.svc.ListVolumes(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	stacks := sc.stacks(ctx, h.svc)
	var visible []string
	for _, vol := range all {
		if authz.ViewOf(sc.c, volumeResource(sc.env.ID, vol, stacks)).Full() {
			visible = append(visible, vol.Name)
		}
	}
	out := VolumeUsageList{EnvironmentID: sc.env.ID, Supported: true, Items: []VolumeSize{}}
	if len(visible) == 0 {
		// Nothing to show: the walk is not worth starting.
		return &volumeUsageOutput{Body: out}, nil
	}
	r, err := h.svc.VolumeUsage(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	if r.Unsupported {
		out.Supported = false
		return &volumeUsageOutput{Body: out}, nil
	}
	at := r.ComputedAt
	out.ComputedAt = &at
	slices.Sort(visible)
	for _, name := range visible {
		item := VolumeSize{Name: name}
		if n, ok := r.Sizes[name]; ok && n >= 0 {
			item.SizeBytes = &n
		}
		out.Items = append(out.Items, item)
	}
	return &volumeUsageOutput{Body: out}, nil
}

func registerVolumes(a huma.API, deps Deps) {
	h := newDockerAPI(deps)
	base := BasePath + "/environments/{environmentId}/volumes"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-volumes", Method: http.MethodGet, Path: base, Summary: "List volumes",
			Description: "Every volume of the environment with the containers using it and its Compose stack, filtered per item (#17). " +
				"Sort fields: name (default), createdAt. total counts the visible matches.",
			Tags: []string{tagVolumes}, Errors: dockerReadErrors,
		},
		Capability: CapVolumeRead, Scope: ScopeEnvironment,
	}, h.listVolumes)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-volume", Method: http.MethodPost, Path: base, Summary: "Create a volume",
			Description: "Starts a volume.create job (202); 409 resource_name_taken when the name exists.",
			Tags:        []string{tagVolumes}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapVolumeCreate, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.createVolume)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-volume", Method: http.MethodGet, Path: base + "/{volumeId}", Summary: "Get a volume",
			Description: "The volume with its users, stack and removal consequences (full view with volume.read).",
			Tags:        []string{tagVolumes}, Errors: dockerReadErrors,
		},
		Capability: CapVolumeRead, Scope: ScopeResource,
	}, h.getVolume)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-volume", Method: http.MethodDelete, Path: base + "/{volumeId}", Summary: "Remove a volume",
			Description: "Starts a volume.remove job (202) that deletes the volume's data permanently. Refused while containers use it " +
				"(409 volume_in_use) and for volumes of a Docker Manager-managed stack (409 stack_managed).",
			Tags: []string{tagVolumes}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapVolumeRemove, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.deleteVolume)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-volume-usage", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/disk-usage/volumes",
			Summary: "List the disk usage of volumes",
			Description: "The size of every volume the caller holds volume.read on. The Engine computes the sizes by walking the " +
				"volumes, so Docker Manager reuses one computation per environment for up to a minute (computedAt) and concurrent " +
				"requests share it; the first request after that can take a while on large volumes. supported is false (and items " +
				"empty) when the environment's agent predates the request.",
			Tags: []string{tagVolumes}, Errors: dockerReadErrors,
		},
		Capability: CapVolumeRead, Scope: ScopeEnvironment,
	}, h.volumeUsage)
}
