package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Global search for the ⌘K command palette (#4, #22). Every hit is filtered
// with the same rule as the resource's own list route (#17: authz.ViewOf
// with the resource's parents), and every hit carries identity and status
// only (the minimal-discovery shape), whatever the caller's view: the
// palette navigates to the resource, whose own route then shapes the
// details.

// Search limits.
const (
	searchMaxQuery     = 100
	searchDefaultLimit = 20
	searchMaxLimit     = 50
	// searchDockerMinQuery is the shortest query that also searches the
	// Docker objects of online environments (agent requests); shorter
	// queries search the manager's own records only.
	searchDockerMinQuery = 2
	// SearchAgentTimeout bounds the agent requests of one search per
	// environment; slower environments are reported in gaps.
	SearchAgentTimeout = 3 * time.Second
)

// Search hit types.
const (
	SearchEnvironment = "environment"
	SearchStack       = "stack"
	SearchService     = "service"
	SearchContainer   = "container"
	SearchImage       = "image"
	SearchVolume      = "volume"
	SearchNetwork     = "network"
)

var searchTypes = []string{SearchEnvironment, SearchStack, SearchService, SearchContainer, SearchImage, SearchVolume, SearchNetwork}

// SearchHit is one search result: identity and status only.
type SearchHit struct {
	Type            string `json:"type" enum:"environment,stack,service,container,image,volume,network"`
	ID              string `json:"id" doc:"The identifier used in the resource's routes: environment ID, stack ID, <stackId>/<service>, container ID, image ID, volume name or network ID."`
	Name            string `json:"name" example:"nextcloud" doc:"Display name (environment or stack name, service, container, volume or network name, first image tag)."`
	EnvironmentID   string `json:"environmentId,omitempty"`
	EnvironmentName string `json:"environmentName,omitempty"`
	StackID         string `json:"stackId,omitempty" doc:"The stack of a service, or of a container, volume or network that belongs to a Docker Manager stack."`
	Status          string `json:"status,omitempty" doc:"online/offline (environments), the deployment status (stacks) or the container state."`
}

// SearchGap is an environment whose Docker objects could not be searched.
type SearchGap struct {
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	Reason          string `json:"reason" enum:"offline,timeout,error" doc:"offline: the agent is not connected; timeout: it did not answer in time; error: it answered with an error."`
}

// SearchResults is the answer of GET /search.
type SearchResults struct {
	Query string      `json:"query"`
	Items []SearchHit `json:"items" doc:"Best matches first: exact names, then prefixes, then other matches; alphabetical within each rank."`
	Gaps  []SearchGap `json:"gaps" doc:"Visible environments whose containers, images, volumes and networks are missing from the results."`
}

type searchInput struct {
	Query         string `query:"q" minLength:"1" maxLength:"100" required:"true" doc:"Case-insensitive text found anywhere in the name."`
	Limit         int    `query:"limit" minimum:"1" maximum:"50" default:"20"`
	Types         string `query:"types" maxLength:"128" doc:"Comma-separated hit types to return (default all): environment, stack, service, container, image, volume, network."`
	EnvironmentID string `query:"environmentId" maxLength:"64" doc:"Search only this environment (the palette's environment switcher)."`
}

type searchOutput struct{ Body SearchResults }

type searchAPI struct {
	deps  Deps
	authz authz.Authorizer
}

func registerSearch(a huma.API, deps Deps) {
	h := &searchAPI{deps: deps, authz: authz.OrDenyAll(deps.Authorizer)}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "search", Method: http.MethodGet, Path: BasePath + "/search", Summary: "Search",
			Description: "Global search for the command palette: environments, stacks and their services, and (for queries of at least " +
				"two characters) the containers, images, volumes and networks of online environments. Every hit is filtered like the " +
				"resource's own list route (#17) and carries identity and status only. Environments whose Docker objects could not be " +
				"searched are listed in gaps.",
			Tags: []string{"Search"}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.search)
}

// searchMatch ranks name against the lower-cased query: 0 exact, 1 prefix,
// 2 substring; ok is false when it does not match.
func searchMatch(name, q string) (rank int, ok bool) {
	n := strings.ToLower(name)
	switch {
	case n == q:
		return 0, true
	case strings.HasPrefix(n, q):
		return 1, true
	case strings.Contains(n, q):
		return 2, true
	}
	return 0, false
}

type rankedHit struct {
	SearchHit
	rank int
}

func (h *searchAPI) search(ctx context.Context, in *searchInput) (*searchOutput, error) {
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	out := &searchOutput{Body: SearchResults{Query: in.Query, Items: []SearchHit{}, Gaps: []SearchGap{}}}
	if q == "" {
		return out, nil
	}
	want := map[string]bool{}
	for t := range strings.SplitSeq(in.Types, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !slices.Contains(searchTypes, t) {
			return nil, Invalid("unknown search type", Field("query.types", "unknown type "+t))
		}
		want[t] = true
	}
	if len(want) == 0 {
		for _, t := range searchTypes {
			want[t] = true
		}
	}
	limit := cmp.Or(in.Limit, searchDefaultLimit)

	all, visible, err := h.environments(ctx, c, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	var hits []rankedHit
	add := func(rank int, hit SearchHit) { hits = append(hits, rankedHit{SearchHit: hit, rank: rank}) }
	names := map[string]string{}
	var envs []domain.Environment
	for _, env := range all {
		if !visible[env.ID] {
			continue
		}
		// Only visible environments are named (a stack grant alone does not
		// reveal its environment's name).
		names[env.ID] = env.Name
		envs = append(envs, env)
		if want[SearchEnvironment] {
			if r, ok := searchMatch(env.Name, q); ok {
				add(r, SearchHit{Type: SearchEnvironment, ID: env.ID, Name: env.Name, EnvironmentID: env.ID, EnvironmentName: env.Name,
					Status: onlineStatus(env.Online)})
			}
		}
	}
	if (want[SearchStack] || want[SearchService]) && h.deps.Stacks != nil {
		// Stacks are filtered per stack, like GET /stacks: a grant on a
		// stack shows it even where its environment is otherwise hidden.
		if err := h.stacks(ctx, c, q, all, want, add); err != nil {
			return nil, err
		}
	}
	if len(q) >= searchDockerMinQuery && h.deps.Docker != nil &&
		(want[SearchContainer] || want[SearchImage] || want[SearchVolume] || want[SearchNetwork]) {
		out.Body.Gaps = h.docker(ctx, c, q, envs, want, add)
	}

	slices.SortStableFunc(hits, func(a, b rankedHit) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
			cmp.Compare(slices.Index(searchTypes, a.Type), slices.Index(searchTypes, b.Type)), cmp.Compare(a.ID, b.ID))
	})
	for _, hit := range hits[:min(len(hits), limit)] {
		if hit.EnvironmentName == "" {
			hit.EnvironmentName = names[hit.EnvironmentID]
		}
		out.Body.Items = append(out.Body.Items, hit.SearchHit)
	}
	return out, nil
}

func onlineStatus(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}

// environments are the active environments (one of them with only) and
// which of them the caller may see.
func (h *searchAPI) environments(ctx context.Context, c authz.Checker, only string) (all []domain.Environment, visible map[string]bool, err error) {
	visible = map[string]bool{}
	if h.deps.Agents == nil {
		return nil, visible, nil
	}
	after := ""
	for {
		page, err := h.deps.Agents.ListEnvironments(ctx, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive},
			AfterID: after, Limit: 200})
		if err != nil {
			return nil, nil, Internal(err)
		}
		for _, env := range page {
			if only != "" && env.ID != only {
				continue
			}
			all = append(all, env)
			if authz.ViewOf(c, environmentResource(env)).Visible() {
				visible[env.ID] = true
			}
		}
		if len(page) < 200 {
			return all, visible, nil
		}
		after = page[len(page)-1].ID
	}
}

func (h *searchAPI) stacks(ctx context.Context, c authz.Checker, q string, envs []domain.Environment, want map[string]bool,
	add func(int, SearchHit)) error {
	for _, env := range envs {
		after := ""
		for {
			page, err := h.deps.Stacks.List(ctx, domain.StackFilter{EnvironmentID: env.ID, AfterID: after, Limit: 200})
			if err != nil {
				return Internal(err)
			}
			for _, st := range page {
				sv := authz.ViewOf(c, stackResource(st))
				if !sv.Visible() {
					continue
				}
				if want[SearchStack] {
					rank, ok := searchMatch(st.Name, q)
					if r, dok := searchMatch(st.DisplayName, q); st.DisplayName != "" && dok && (!ok || r < rank) {
						rank, ok = r, true
					}
					if ok {
						add(rank, SearchHit{Type: SearchStack, ID: st.ID, Name: cmp.Or(st.DisplayName, st.Name), EnvironmentID: st.EnvironmentID,
							StackID: st.ID, Status: string(st.Status)})
					}
				}
				if want[SearchService] {
					for _, svc := range st.Services {
						r, ok := searchMatch(svc.Name, q)
						if !ok {
							continue
						}
						res := authz.Resource{Type: catalog.TypeService, ID: authz.ServiceID(st.ID, svc.Name), EnvironmentID: st.EnvironmentID,
							Parents: []authz.ResourceRef{{Type: catalog.TypeStack, ID: st.ID}}}
						// GET /stacks/{id}/services lists them with stack.read (the full
						// view); a grant on the service itself shows it too.
						if sv.Full() || authz.ViewOf(c, res).Visible() {
							add(r, SearchHit{Type: SearchService, ID: authz.ServiceID(st.ID, svc.Name), Name: svc.Name, EnvironmentID: st.EnvironmentID,
								StackID: st.ID})
						}
					}
				}
			}
			if len(page) < 200 {
				break
			}
			after = page[len(page)-1].ID
		}
	}
	return nil
}

// dockerObjects are one environment's Docker objects (or why they are
// missing).
type dockerObjects struct {
	containers []protocol.ContainerSummary
	images     []protocol.ImageSummary
	volumes    []protocol.VolumeInfo
	networks   []protocol.NetworkInfo
	stackIDs   map[string]string
	gap        string
}

// docker searches the Docker objects of the online environments. The agent
// requests run concurrently (bounded per environment); authorization runs
// afterwards on this goroutine (the checker is per request, not shared).
func (h *searchAPI) docker(ctx context.Context, c authz.Checker, q string, envs []domain.Environment, want map[string]bool,
	add func(int, SearchHit)) []SearchGap {
	objs := make([]dockerObjects, len(envs))
	var wg sync.WaitGroup
	for i, env := range envs {
		if !env.Online {
			objs[i].gap = "offline"
			continue
		}
		wg.Go(func() { objs[i] = h.fetchDocker(ctx, env.ID, want) })
	}
	wg.Wait()

	gaps := []SearchGap{}
	for i, env := range envs {
		o := objs[i]
		if o.gap != "" {
			gaps = append(gaps, SearchGap{EnvironmentID: env.ID, EnvironmentName: env.Name, Reason: o.gap})
			continue
		}
		for _, ct := range o.containers {
			if r, ok := searchMatch(ct.Name, q); ok && authz.ViewOf(c, containerResource(env.ID, ct, o.stackIDs)).Visible() {
				add(r, SearchHit{Type: SearchContainer, ID: ct.ID, Name: ct.Name, EnvironmentID: env.ID, StackID: stackOf(ct.Stack, o.stackIDs),
					Status: ct.State})
			}
		}
		for _, im := range o.images {
			name := imageName(im)
			if r, ok := searchMatch(strings.Join(append([]string{name}, im.RepoTags...), " "), q); ok && authz.ViewOf(c, imageResource(env.ID, im.ID)).Visible() {
				if rn, nok := searchMatch(name, q); nok {
					r = rn
				} else {
					r = max(r, 2)
				}
				add(r, SearchHit{Type: SearchImage, ID: im.ID, Name: name, EnvironmentID: env.ID})
			}
		}
		for _, v := range o.volumes {
			if r, ok := searchMatch(v.Name, q); ok && authz.ViewOf(c, volumeResource(env.ID, v, o.stackIDs)).Visible() {
				add(r, SearchHit{Type: SearchVolume, ID: v.Name, Name: v.Name, EnvironmentID: env.ID, StackID: stackOf(v.Stack, o.stackIDs)})
			}
		}
		for _, n := range o.networks {
			if r, ok := searchMatch(n.Name, q); ok && authz.ViewOf(c, networkResource(env.ID, n, o.stackIDs)).Visible() {
				add(r, SearchHit{Type: SearchNetwork, ID: n.ID, Name: n.Name, EnvironmentID: env.ID, StackID: stackOf(n.Stack, o.stackIDs)})
			}
		}
	}
	return gaps
}

func (h *searchAPI) fetchDocker(ctx context.Context, env string, want map[string]bool) dockerObjects {
	ctx, cancel := context.WithTimeout(ctx, SearchAgentTimeout)
	defer cancel()
	svc := h.deps.Docker
	o := dockerObjects{stackIDs: svc.StackIDs(ctx, env)}
	if o.stackIDs == nil {
		o.stackIDs = map[string]string{}
	}
	err := func() (err error) {
		if want[SearchContainer] {
			if o.containers, err = svc.ListContainers(ctx, env); err != nil {
				return err
			}
		}
		if want[SearchImage] {
			if o.images, err = svc.ListImages(ctx, env); err != nil {
				return err
			}
		}
		if want[SearchVolume] {
			if o.volumes, err = svc.ListVolumes(ctx, env); err != nil {
				return err
			}
		}
		if want[SearchNetwork] {
			o.networks, err = svc.ListNetworks(ctx, env)
		}
		return err
	}()
	switch {
	case err == nil:
	case ctx.Err() != nil:
		o = dockerObjects{gap: "timeout"}
	default:
		o = dockerObjects{gap: "error"}
		var de *domain.DockerError
		if errors.As(err, &de) {
			switch de.Code {
			case domain.DockerEnvironmentOffline:
				o.gap = "offline"
			case domain.DockerTimeout:
				o.gap = "timeout"
			}
		}
	}
	return o
}

func stackOf(st *protocol.StackRef, stackIDs map[string]string) string {
	if st == nil {
		return ""
	}
	return stackIDs[st.Project]
}

func imageName(im protocol.ImageSummary) string {
	for _, t := range im.RepoTags {
		if t != "" && t != "<none>:<none>" {
			return t
		}
	}
	id := strings.TrimPrefix(im.ID, "sha256:")
	return id[:min(len(id), 12)]
}
