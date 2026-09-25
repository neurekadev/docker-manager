package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Docker resources of an environment (#6): containers.go, images.go,
// volumes.go, networks.go. Every route is scoped by {environmentId}: the
// environment's own agent answers, so an ID or name of another environment
// is not found there. Reads are agent requests; every mutation answers
// 202 with a job (#26). Shaping follows #17: the type's read capability
// shows everything, any other capability on the object only its identity
// and status (view minimal) with the granted actions.

const (
	tagContainers = "Containers"
	tagImages     = "Images"
	tagVolumes    = "Volumes"
	tagNetworks   = "Networks"
)

// Docker resource error codes (#6).
const (
	CodeEnvironmentOffline    = "environment_offline"
	CodeEngineUnavailable     = "engine_unavailable"
	CodeUnsupportedAPIVersion = "unsupported_api_version"
	CodeEngineError           = "engine_error"
	CodeAgentUnsupported      = "agent_unsupported"
	CodeStackManaged          = "stack_managed"
	CodeContainerRunning      = "container_running"
	CodeImageInUse            = "image_in_use"
	CodeVolumeInUse           = "volume_in_use"
	CodeNetworkInUse          = "network_in_use"
	CodeNetworkBuiltin        = "network_builtin"
	CodeResourceNameTaken     = "resource_name_taken"
	CodeRecreateRequired      = "recreate_required"
)

// DockerService is the Docker resource service as seen by the API
// (implemented by internal/manager/resources.Service). Errors are
// *domain.DockerError or job engine errors.
type DockerService interface {
	ListContainers(ctx context.Context, env string) ([]protocol.ContainerSummary, error)
	InspectContainer(ctx context.Context, env, ref string) (protocol.ContainerDetails, error)
	ManagedSpec(ctx context.Context, env string, labels map[string]string) (*domain.ManagedContainer, *protocol.ContainerSpec, error)
	StackManaged(ctx context.Context, env string, st *protocol.StackRef) bool
	StackIDs(ctx context.Context, env string) map[string]string
	CreateContainer(ctx context.Context, p authz.Principal, env string, spec protocol.ContainerSpec, start bool, key string) (domain.Job, error)
	ContainerAction(ctx context.Context, p authz.Principal, env string, kind domain.JobKind, d protocol.ContainerDetails, in protocol.ContainerActionInput, key string) (domain.Job, error)
	UpdateContainer(ctx context.Context, p authz.Principal, env string, d protocol.ContainerDetails, in protocol.ContainerUpdateInput, key string) (domain.Job, error)

	ListImages(ctx context.Context, env string) ([]protocol.ImageSummary, error)
	InspectImage(ctx context.Context, env, ref string) (protocol.ImageDetails, error)
	TagImage(ctx context.Context, env string, im protocol.ImageDetails, target string) (protocol.ImageDetails, error)
	PullImage(ctx context.Context, p authz.Principal, env string, in protocol.ImagePullInput, connectionID, key string) (domain.Job, error)
	RemoveImage(ctx context.Context, p authz.Principal, env string, im protocol.ImageDetails, force bool, key string) (domain.Job, error)

	ListVolumes(ctx context.Context, env string) ([]protocol.VolumeInfo, error)
	InspectVolume(ctx context.Context, env, name string) (protocol.VolumeInfo, error)
	CreateVolume(ctx context.Context, p authz.Principal, env string, in protocol.VolumeCreateInput, key string) (domain.Job, error)
	RemoveVolume(ctx context.Context, p authz.Principal, env string, v protocol.VolumeInfo, key string) (domain.Job, error)

	ListNetworks(ctx context.Context, env string) ([]protocol.NetworkInfo, error)
	InspectNetwork(ctx context.Context, env, ref string) (protocol.NetworkInfo, error)
	CreateNetwork(ctx context.Context, p authz.Principal, env string, in protocol.NetworkCreateInput, key string) (domain.Job, error)
	RemoveNetwork(ctx context.Context, p authz.Principal, env string, n protocol.NetworkInfo, key string) (domain.Job, error)
}

// Removal describes what removing a resource would do (#6: clear deletion
// consequences) and what currently prevents it.
type Removal struct {
	Allowed      bool             `json:"allowed" doc:"A removal request would be accepted now (it is re-checked when it runs)."`
	Blockers     []RemovalBlocker `json:"blockers" doc:"Why a removal is refused (the error code the request would get)."`
	Consequences []string         `json:"consequences" doc:"What a removal deletes or changes, in plain language."`
}

// RemovalBlocker is one reason a removal is refused.
type RemovalBlocker struct {
	Code    string `json:"code" example:"volume_in_use"`
	Message string `json:"message"`
}

func newRemoval(consequences ...string) Removal {
	return Removal{Allowed: true, Blockers: []RemovalBlocker{}, Consequences: consequences}
}

func (r *Removal) block(code, message string) {
	r.Allowed = false
	r.Blockers = append(r.Blockers, RemovalBlocker{Code: code, Message: message})
}

// StackMembership is the Compose project a container, volume or network
// belongs to.
type StackMembership struct {
	Project string `json:"project"`
	Service string `json:"service,omitempty" doc:"Compose service (containers)."`
	StackID string `json:"stackId,omitempty" doc:"The DockYard stack (#7) when the project is one."`
	Managed bool   `json:"managed" doc:"A DockYard-managed stack: direct updates and removals are refused with stack_managed."`
}

// ContainerRef names a container using another object.
type ContainerRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

func newContainerRefs(cs []protocol.ContainerRef) []ContainerRef {
	out := make([]ContainerRef, 0, len(cs))
	for _, c := range cs {
		out = append(out, ContainerRef(c))
	}
	return out
}

// dockerAPI serves the Docker resource routes.
type dockerAPI struct {
	svc      DockerService
	agents   AgentService
	observe  ObserveService
	authz    authz.Authorizer
	instance string
}

func newDockerAPI(deps Deps) *dockerAPI {
	return &dockerAPI{svc: deps.Docker, agents: deps.Agents, observe: deps.Observe, authz: authz.OrDenyAll(deps.Authorizer), instance: deps.InstanceID}
}

// scope is one request's environment, checker and principal.
type scope struct {
	c        authz.Checker
	p        authz.Principal
	env      domain.Environment
	stackIDs map[string]string
}

// environment authenticates the caller and loads the environment, which
// must be visible to them (404 otherwise, before any agent is asked).
// Mutations of an archived environment are refused (409).
func (h *dockerAPI) environment(ctx context.Context, envID string, mutation bool) (*scope, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if h.agents == nil {
		return nil, Unavailable(CodeUnavailable, "the environment service is not available")
	}
	env, err := h.agents.GetEnvironment(ctx, envID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return nil, NotFound("environment not found")
	}
	if err != nil {
		return nil, Internal(err)
	}
	if !authz.ViewOf(c, authz.EnvironmentResource(env.ID)).Visible() {
		return nil, NotFound("environment not found")
	}
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "the Docker resource service is not available")
	}
	if mutation && env.Status == domain.EnvironmentArchived {
		return nil, Conflict(CodeEnvironmentArchived, "the environment is archived")
	}
	return &scope{c: c, p: p, env: env}, nil
}

// stacks returns the environment's project -> stack ID map (once per request).
func (sc *scope) stacks(ctx context.Context, svc DockerService) map[string]string {
	if sc.stackIDs == nil {
		sc.stackIDs = svc.StackIDs(ctx, sc.env.ID)
		if sc.stackIDs == nil {
			sc.stackIDs = map[string]string{}
		}
	}
	return sc.stackIDs
}

// parents are the authorization parents of an object in a Compose project:
// its service (containers) and stack when the project is a DockYard stack.
func parents(typ string, stackIDs map[string]string, st *protocol.StackRef) []authz.ResourceRef {
	out := []authz.ResourceRef{}
	if st == nil {
		return out
	}
	id, ok := stackIDs[st.Project]
	if !ok {
		return out
	}
	if typ == catalog.TypeContainer && st.Service != "" {
		out = append(out, authz.ResourceRef{Type: catalog.TypeService, ID: authz.ServiceID(id, st.Service)})
	}
	return append(out, authz.ResourceRef{Type: catalog.TypeStack, ID: id})
}

func membership(st *protocol.StackRef, stackIDs map[string]string, managed bool) *StackMembership {
	if st == nil {
		return nil
	}
	return &StackMembership{Project: st.Project, Service: st.Service, StackID: stackIDs[st.Project], Managed: managed}
}

// lookupErr maps the error of a lookup by a path identifier: an invalid or
// ambiguous identifier is reported like a missing object (404), so it
// reveals nothing about objects the caller may not see.
func lookupErr(err error, what string) error {
	var de *domain.DockerError
	if errors.As(err, &de) && (de.Code == domain.DockerInvalid || de.Code == domain.DockerConflict || de.Code == domain.DockerNotFound) {
		return NotFound(what + " not found")
	}
	return dockerErr(err)
}

// dockerErr maps service errors to API errors.
func dockerErr(err error) error {
	var de *domain.DockerError
	var amb *domain.AmbiguousRegistryError
	switch {
	case errors.As(err, &amb):
		return Conflict(CodeAmbiguousRegistryConnection, err.Error())
	case errors.Is(err, domain.ErrRegistryConnectionNotFound):
		return Invalid("unknown registry connection", Field("body.registryConnectionId", "no registry connection has this ID"))
	case errors.Is(err, domain.ErrRegistryConnectionRevoked):
		return Conflict(CodeRegistryConnectionRevoked, "the registry connection is revoked; rotate a new credential into it or select another one")
	case errors.Is(err, domain.ErrRegistryConnectionMismatch):
		return Invalid("the selected registry connection does not apply to this image",
			Field("body.registryConnectionId", "not a candidate for this image (host, repository matcher or binding differ)"))
	case !errors.As(err, &de):
		return JobErrorFor(err)
	}
	switch de.Code {
	case domain.DockerInvalid:
		if de.Field != "" {
			return Invalid(de.Message, Field("body."+de.Field, de.Message))
		}
		return Invalid(de.Message)
	case domain.DockerNotFound:
		return NotFound(de.Message)
	case domain.DockerRecreateRequired:
		return NewError(http.StatusUnprocessableEntity, CodeRecreateRequired, de.Message)
	}
	if e, ok := LookupErrorCode(de.Code); ok {
		return NewError(e.Status, e.Code, de.Message).WithRetryable(e.Retryable)
	}
	return Internal(err)
}

// sortedPage pages an in-memory list: items are filtered for visibility and
// the query by the caller, sorted by keys (a total order: the last key is
// unique), and the cursor holds the last item's keys.
type sortKeys []string

func compareKeys(a, b sortKeys, desc []bool) int {
	for i := range a {
		c := strings.Compare(a[i], b[i])
		if i < len(desc) && desc[i] {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

// memPage sorts items by key (per sort key direction), continues after the
// cursor and returns one page, the next cursor and the total.
func memPage[T any](items []T, key func(T) sortKeys, desc []bool, params PageParams, fingerprint string) ([]T, string, *int64, error) {
	slices.SortStableFunc(items, func(a, b T) int { return compareKeys(key(a), key(b), desc) })
	total := int64(len(items))
	start := 0
	if params.Cursor != "" {
		var after sortKeys
		if err := DecodeCursorFor(params.Cursor, fingerprint, &after); err != nil {
			return nil, "", nil, err
		}
		start = len(items)
		for i, it := range items {
			if k := key(it); len(k) == len(after) && compareKeys(k, after, desc) > 0 {
				start = i
				break
			}
		}
	}
	end := min(start+params.PageLimit(), len(items))
	page := items[start:end]
	next := ""
	if end < len(items) && len(page) > 0 {
		c, err := CursorFor(fingerprint, key(page[len(page)-1]))
		if err != nil {
			return nil, "", nil, err
		}
		next = c
	}
	return page, next, &total, nil
}

// timeKey renders a time for string sorting.
func timeKey(t time.Time) string { return t.UTC().Format("20060102150405.000000000") }

// sizeKey renders a non-negative size for string sorting.
func sizeKey(n int64) string { return fmt.Sprintf("%020d", max(n, 0)) }

// sortSpec parses ?sort into key extractors: fields maps a sort field to
// the item's key; the unique key breaks ties.
func sortSpec[T any](raw string, fields map[string]func(T) string, def SortKey, unique func(T) string) (func(T) sortKeys, []bool, error) {
	allowed := make([]string, 0, len(fields))
	for f := range fields {
		allowed = append(allowed, f)
	}
	slices.Sort(allowed)
	keys, err := ParseSort(raw, allowed, def)
	if err != nil {
		return nil, nil, err
	}
	desc := make([]bool, 0, len(keys)+1)
	for _, k := range keys {
		desc = append(desc, k.Desc)
	}
	desc = append(desc, false)
	return func(it T) sortKeys {
		out := make(sortKeys, 0, len(keys)+1)
		for _, k := range keys {
			out = append(out, fields[k.Field](it))
		}
		return append(out, unique(it))
	}, desc, nil
}

// containsFold reports whether s contains q, case-insensitively.
func containsFold(s, q string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(q))
}

// matchLabel reports whether labels match a "key" or "key=value" filter.
func matchLabel(labels map[string]string, filter string) bool {
	k, v, hasV := strings.Cut(filter, "=")
	got, ok := labels[k]
	return ok && (!hasV || got == v)
}
