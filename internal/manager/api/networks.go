package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Network capabilities (#17).
const (
	CapNetworkRead   Capability = "network.read"
	CapNetworkCreate Capability = "network.create"
	CapNetworkRemove Capability = "network.remove"
)

// Network is a Docker network of an environment. Permission rules name it
// by name (#17).
//
// Shaping (#17): network.read shows everything; network.remove on the
// network shows only id, name and environmentId (view minimal).
type Network struct {
	ID            string              `json:"id"`
	Name          string              `json:"name" example:"shop_default"`
	EnvironmentID string              `json:"environmentId"`
	Protection    *ResourceProtection `json:"protection,omitempty" doc:"Set for DockYard's own networks (#32): removal is refused."`
	View          string              `json:"view" enum:"minimal,full"`
	Actions       []string            `json:"actions"`
	Driver        string              `json:"driver,omitempty" doc:"Full view."`
	Scope         string              `json:"scope,omitempty"`
	Internal      bool                `json:"internal,omitempty"`
	Attachable    bool                `json:"attachable,omitempty"`
	EnableIPv6    bool                `json:"enableIpv6,omitempty"`
	CreatedAt     *time.Time          `json:"createdAt,omitempty"`
	Labels        map[string]string   `json:"labels,omitempty"`
	Subnets       []string            `json:"subnets,omitempty"`
	Gateways      []string            `json:"gateways,omitempty"`
	Builtin       bool                `json:"builtin,omitempty" doc:"A predefined network (bridge, host, none)."`
	Stack         *StackMembership    `json:"stack,omitempty"`
	Containers    []ContainerRef      `json:"containers,omitempty" doc:"Attached containers (GET only; lists do not report them)."`
	Removal       *Removal            `json:"removal,omitempty" doc:"Full view of GET only."`
}

func networkResource(env string, n protocol.NetworkInfo, stackIDs map[string]string) authz.Resource {
	return authz.Resource{Type: catalog.TypeNetwork, ID: n.Name, EnvironmentID: env, Parents: parents(catalog.TypeNetwork, stackIDs, n.Stack)}
}

func newNetwork(env string, n protocol.NetworkInfo, v authz.View, stackIDs map[string]string) Network {
	out := Network{ID: n.ID, Name: n.Name, EnvironmentID: env, Protection: newProtection(n.Protection), View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	created := &n.Created
	if n.Created.IsZero() {
		created = nil
	}
	out.Driver, out.Scope, out.Internal, out.Attachable, out.EnableIPv6, out.CreatedAt = n.Driver, n.Scope, n.Internal, n.Attachable, n.EnableIPv6, created
	out.Labels, out.Subnets, out.Gateways, out.Builtin = n.Labels, n.Subnets, n.Gateways, n.Builtin
	out.Stack = membership(n.Stack, stackIDs, managedStack(n.Stack, stackIDs))
	if len(n.Containers) > 0 {
		out.Containers = newContainerRefs(n.Containers)
	}
	return out
}

func networkRemoval(n protocol.NetworkInfo, managed bool) Removal {
	r := newRemoval("The network is deleted; containers can no longer be attached to it.",
		"Exact permission rules on this network are removed.")
	r.blockProtected(n.Protection)
	if n.Builtin {
		r.block(CodeNetworkBuiltin, "Predefined networks cannot be removed.")
	}
	if managed {
		r.block(CodeStackManaged, "The network belongs to a DockYard-managed stack; remove it from the stack instead.")
	}
	if len(n.Containers) > 0 {
		r.block(CodeNetworkInUse, "Containers are attached; disconnect or remove them first.")
	}
	return r
}

type listNetworksInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
	SortParam
	Q      string `query:"q" maxLength:"128" doc:"Only networks whose name contains this text (case-insensitive)."`
	Driver string `query:"driver" maxLength:"128" doc:"Only networks of this driver. Full view only."`
}

// NetworkPath are the path parameters of a network route.
type NetworkPath struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	NetworkID     string `path:"networkId" maxLength:"128" doc:"Network name, ID or unique ID prefix (12+ characters)."`
}

type networkOutput struct{ Body Network }
type listNetworksOutput struct{ Body Page[Network] }

type createNetworkInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		Name       string            `json:"name" minLength:"1" maxLength:"128" pattern:"^[a-zA-Z0-9][a-zA-Z0-9_.-]*$" example:"backend"`
		Driver     string            `json:"driver,omitempty" maxLength:"128" doc:"Default bridge."`
		Internal   bool              `json:"internal,omitempty" doc:"No external connectivity."`
		Attachable bool              `json:"attachable,omitempty"`
		Labels     map[string]string `json:"labels,omitempty" doc:"dev.neureka.dockyard.* and com.docker.compose.* are reserved."`
		Options    map[string]string `json:"options,omitempty" doc:"Driver options."`
	}
}

type deleteNetworkInput struct {
	NetworkPath
	IdempotencyKeyParam
}

func (h *dockerAPI) listNetworks(ctx context.Context, in *listNetworksInput) (*listNetworksOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	all, err := h.svc.ListNetworks(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	stacks := sc.stacks(ctx, h.svc)
	type item struct {
		n protocol.NetworkInfo
		v authz.View
	}
	var items []item
	for _, n := range all {
		v := authz.ViewOf(sc.c, networkResource(sc.env.ID, n, stacks))
		switch {
		case !v.Visible():
			continue
		case in.Q != "" && !containsFold(n.Name, in.Q):
			continue
		case in.Driver != "" && (!v.Full() || n.Driver != in.Driver):
			continue
		}
		items = append(items, item{n, v})
	}
	key, desc, err := sortSpec(in.Sort, map[string]func(item) string{
		"name":      func(i item) string { return i.n.Name },
		"createdAt": func(i item) string { return timeKey(i.n.Created) },
	}, SortKey{Field: "name"}, func(i item) string { return i.n.ID })
	if err != nil {
		return nil, err
	}
	page, next, total, err := memPage(items, key, desc, in.PageParams, QueryFingerprint("networks", sc.env.ID, in.Sort, in.Q, in.Driver))
	if err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(page))
	for _, it := range page {
		out = append(out, newNetwork(sc.env.ID, it.n, it.v, stacks))
	}
	return &listNetworksOutput{Body: NewPage(out, next, total)}, nil
}

func (h *dockerAPI) visibleNetwork(ctx context.Context, sc *scope, ref string) (protocol.NetworkInfo, authz.View, error) {
	n, err := h.svc.InspectNetwork(ctx, sc.env.ID, ref)
	if err != nil {
		return n, authz.View{}, lookupErr(err, "network")
	}
	v := authz.ViewOf(sc.c, networkResource(sc.env.ID, n, sc.stacks(ctx, h.svc)))
	if !v.Visible() {
		return n, v, NotFound("network not found")
	}
	return n, v, nil
}

func (h *dockerAPI) getNetwork(ctx context.Context, in *NetworkPath) (*networkOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	n, v, err := h.visibleNetwork(ctx, sc, in.NetworkID)
	if err != nil {
		return nil, err
	}
	out := newNetwork(sc.env.ID, n, v, sc.stacks(ctx, h.svc))
	if v.Full() {
		r := networkRemoval(n, h.svc.StackManaged(ctx, sc.env.ID, n.Stack))
		out.Removal = &r
	}
	return &networkOutput{Body: out}, nil
}

func (h *dockerAPI) createNetwork(ctx context.Context, in *createNetworkInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	if !sc.c.Can(string(CapNetworkCreate), authz.InEnvironment(catalog.TypeNetwork, sc.env.ID)).Allowed {
		return nil, Forbidden("not permitted to create networks in this environment")
	}
	b := in.Body
	j, err := h.svc.CreateNetwork(ctx, sc.p, sc.env.ID, protocol.NetworkCreateInput{Name: b.Name, Driver: b.Driver, Internal: b.Internal,
		Attachable: b.Attachable, Labels: b.Labels, Options: b.Options}, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeNetwork, ID: b.Name, EnvironmentID: sc.env.ID})
	return Accepted(j), nil
}

func (h *dockerAPI) deleteNetwork(ctx context.Context, in *deleteNetworkInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	n, v, err := h.visibleNetwork(ctx, sc, in.NetworkID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapNetworkRemove)) {
		return nil, Forbidden("not permitted to remove this network")
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeNetwork, ID: n.Name, EnvironmentID: sc.env.ID})
	j, err := h.svc.RemoveNetwork(ctx, sc.p, sc.env.ID, n, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

func registerNetworks(a huma.API, deps Deps) {
	h := newDockerAPI(deps)
	base := BasePath + "/environments/{environmentId}/networks"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-networks", Method: http.MethodGet, Path: base, Summary: "List networks",
			Description: "Every network of the environment, filtered per item (#17). Sort fields: name (default), createdAt. " +
				"total counts the visible matches.",
			Tags: []string{tagNetworks}, Errors: dockerReadErrors,
		},
		Capability: CapNetworkRead, Scope: ScopeEnvironment,
	}, h.listNetworks)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-network", Method: http.MethodPost, Path: base, Summary: "Create a network",
			Description: "Starts a network.create job (202); 409 resource_name_taken when the name exists.",
			Tags:        []string{tagNetworks}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapNetworkCreate, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.createNetwork)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-network", Method: http.MethodGet, Path: base + "/{networkId}", Summary: "Get a network",
			Description: "The network with its attached containers, stack and removal consequences (full view with network.read).",
			Tags:        []string{tagNetworks}, Errors: dockerReadErrors,
		},
		Capability: CapNetworkRead, Scope: ScopeResource,
	}, h.getNetwork)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-network", Method: http.MethodDelete, Path: base + "/{networkId}", Summary: "Remove a network",
			Description: "Starts a network.remove job (202). Refused for predefined networks (409 network_builtin), networks with " +
				"attached containers (409 network_in_use) and networks of a DockYard-managed stack (409 stack_managed).",
			Tags: []string{tagNetworks}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapNetworkRemove, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.deleteNetwork)
}
