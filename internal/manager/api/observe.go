package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/observe"
)

// Observation routes (#5): metrics, capacity, the cross-environment
// overview and the environment event stream. Units and storage:
// docs/architecture/metrics.md; the stream: docs/api/streams.md.

// Observation capabilities (#17 catalog).
const (
	CapEnvironmentMetricsRead Capability = "environment.metrics.read"
	CapEnvironmentEventsRead  Capability = "environment.events.read"
	CapContainerMetricsRead   Capability = "container.metrics.read"
)

// HostMetricKeys are the metric keys of GET …/metrics (host and disks).
var HostMetricKeys = []string{
	"cpu.percent", "cpu.percent.max", "memory.used_bytes", "memory.used_bytes.max", "memory.total_bytes",
	"load.1", "load.5", "load.15",
	"network.rx_bytes_per_second", "network.rx_bytes_per_second.max", "network.tx_bytes_per_second", "network.tx_bytes_per_second.max",
	"disk.used_bytes", "disk.total_bytes",
}

// ObserveService is the observation service as seen by the API
// (internal/manager/observe.Service implements it).
type ObserveService interface {
	Inventory(environmentID string) (observe.Inventory, bool)
	Host(environmentID string) (observe.HostExtra, bool)
	Skew(environmentID string) time.Duration
	Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error)
	Latest(ctx context.Context, environmentID string) (domain.LatestMetrics, bool, error)
	LatestContainers(ctx context.Context, environmentID string, window time.Duration) ([]domain.LatestContainerMetrics, error)
	Journal() *observe.Journal
}

// MetricSeries is one series of a metrics response.
type MetricSeries struct {
	Key   string `json:"key" example:"cpu.percent" doc:"Metric key; .max variants are the maximum within each bucket, the others the sample-weighted average."`
	Unit  string `json:"unit" enum:"percent,bytes,bytes_per_second,load,count"`
	Mount string `json:"mount,omitempty" example:"docker" doc:"Filesystem role of disk series: docker (Docker's data root), stacks, bind-N. Never a host path."`
	// Values align with timestamps; null is a gap (no sample), never zero.
	Values []*float64 `json:"values" doc:"One value per timestamp; null where no sample exists (agent offline, value unknown)."`
}

// EnvironmentMetrics is a downsampled time range of host metrics.
type EnvironmentMetrics struct {
	EnvironmentID string         `json:"environmentId"`
	From          time.Time      `json:"from"`
	To            time.Time      `json:"to"`
	StepSeconds   int            `json:"stepSeconds" doc:"Bucket width: a multiple of the storage resolution."`
	Resolution    string         `json:"resolution" enum:"raw,1m,15m" doc:"Storage level read: 10 s samples (24 h), 1 min rollups (7 d) or 15 min rollups (90 d)."`
	Timestamps    []time.Time    `json:"timestamps" doc:"Bucket start times."`
	Series        []MetricSeries `json:"series"`
	// SkewCorrected: some samples in the range carried agent timestamps
	// shifted for clock skew (or clamped to the manager's receive time).
	SkewCorrected bool `json:"skewCorrected" doc:"Some samples' agent timestamps were corrected for clock skew or clamped."`
	Incomplete    bool `json:"incomplete" doc:"Some samples lacked containers (Engine unavailable or not every container sampled in time)."`
	Online        bool `json:"online" doc:"The environment is online now; while offline no new samples arrive (gaps)."`
}

// CapacityDisk is one filesystem's usage.
type CapacityDisk struct {
	Mount      string `json:"mount" example:"docker"`
	UsedBytes  int64  `json:"usedBytes"`
	TotalBytes int64  `json:"totalBytes"`
	FreeBytes  int64  `json:"freeBytes"`
}

// EnvironmentCapacity is an environment's capacity and current usage.
type EnvironmentCapacity struct {
	EnvironmentID    string         `json:"environmentId"`
	Online           bool           `json:"online"`
	SampledAt        *time.Time     `json:"sampledAt,omitempty" doc:"Time of the latest sample; absent before the first."`
	CPUs             int            `json:"cpus" doc:"Cores of the environment (Engine inventory or /proc)."`
	CPUPercent       *float64       `json:"cpuPercent,omitempty" doc:"Busy share of all cores, 0..100."`
	MemoryTotalBytes *int64         `json:"memoryTotalBytes,omitempty"`
	MemoryUsedBytes  *int64         `json:"memoryUsedBytes,omitempty"`
	Load1            *float64       `json:"load1,omitempty"`
	Load5            *float64       `json:"load5,omitempty"`
	Load15           *float64       `json:"load15,omitempty"`
	NetworkRxBPS     *float64       `json:"networkRxBytesPerSecond,omitempty"`
	NetworkTxBPS     *float64       `json:"networkTxBytesPerSecond,omitempty"`
	NetworkScope     string         `json:"networkScope,omitempty" enum:"host,agent" doc:"host: the host's interfaces; agent: only the agent container's namespace (see docs/architecture/metrics.md)."`
	UptimeSeconds    *int64         `json:"uptimeSeconds,omitempty"`
	Disks            []CapacityDisk `json:"disks"`
}

// OverviewUsage is an environment's latest usage (environment.metrics.read).
type OverviewUsage struct {
	SampledAt        time.Time `json:"sampledAt"`
	CPUPercent       *float64  `json:"cpuPercent,omitempty"`
	MemoryUsedBytes  *int64    `json:"memoryUsedBytes,omitempty"`
	MemoryTotalBytes *int64    `json:"memoryTotalBytes,omitempty"`
	DiskUsedBytes    *int64    `json:"diskUsedBytes,omitempty" doc:"Docker data-root filesystem."`
	DiskTotalBytes   *int64    `json:"diskTotalBytes,omitempty"`
}

// DockerCounts are an environment's Docker object counts from the Engine
// inventory (-1: unknown).
type DockerCounts struct {
	Containers        int `json:"containers"`
	ContainersRunning int `json:"containersRunning"`
	ContainersPaused  int `json:"containersPaused"`
	ContainersStopped int `json:"containersStopped"`
	Images            int `json:"images"`
	Volumes           int `json:"volumes"`
	Networks          int `json:"networks"`
}

// OverviewEnvironment is one environment of the overview.
type OverviewEnvironment struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Online  bool     `json:"online"`
	View    string   `json:"view" enum:"minimal,full"`
	Actions []string `json:"actions"`
	// Usage needs environment.metrics.read, Docker counts
	// environment.system.read.
	Usage  *OverviewUsage `json:"usage,omitempty" doc:"Latest host usage (environment.metrics.read)."`
	Docker *DockerCounts  `json:"docker,omitempty" doc:"Docker object counts (environment.system.read)."`
}

// OverviewTotals aggregate the visible environments.
type OverviewTotals struct {
	Environments int `json:"environments" example:"2"`
	Online       int `json:"online"`
	Offline      int `json:"offline"`
	// Containers sum the Docker counts of the environments whose counts the
	// caller may read (CountedEnvironments of them).
	Containers          int `json:"containers"`
	ContainersRunning   int `json:"containersRunning"`
	CountedEnvironments int `json:"countedEnvironments" doc:"Environments whose Docker counts are included (environment.system.read and a known inventory)."`
}

// Overview is the cross-environment summary.
type Overview struct {
	Totals       OverviewTotals        `json:"totals"`
	Environments []OverviewEnvironment `json:"environments"`
}

type environmentMetricsInput struct {
	EnvironmentID string    `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	From          time.Time `query:"from" doc:"Range start (RFC 3339; default: one hour before to)."`
	To            time.Time `query:"to" doc:"Range end (RFC 3339; default: now)."`
	StepSeconds   int       `query:"stepSeconds" minimum:"0" maximum:"7776000" doc:"Bucket width in seconds (0: automatic, about 300 points). Rounded up to the storage resolution; at most 1000 buckets."`
	Series        []string  `query:"series" doc:"Metric keys to return (default: all)."`
}

type environmentMetricsOutput struct{ Body EnvironmentMetrics }
type environmentCapacityOutput struct{ Body EnvironmentCapacity }
type overviewOutput struct{ Body Overview }

type streamEnvironmentEventsInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	LastEventID   string `header:"Last-Event-ID" maxLength:"64" doc:"Resume after this cursor (sent automatically by EventSource on reconnect)."`
}

type observeAPI struct {
	agents *agentsAPI
	svc    ObserveService
	deps   Deps
	maxAge time.Duration
}

func (h *observeAPI) service() (ObserveService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "the observation service is not available")
	}
	return h.svc, nil
}

// environmentWith loads a visible environment and requires cap (403 when
// visible without it).
func (h *observeAPI) environmentWith(ctx context.Context, id string, capability Capability, what string) (authz.Checker, domain.Environment, error) {
	c, env, v, err := h.agents.visibleEnvironment(ctx, id)
	if err != nil {
		return nil, env, err
	}
	if !v.Has(string(capability)) {
		return nil, env, Forbidden("not permitted to " + what)
	}
	return c, env, nil
}

func (h *observeAPI) metrics(ctx context.Context, in *environmentMetricsInput) (*environmentMetricsOutput, error) {
	_, env, err := h.environmentWith(ctx, in.EnvironmentID, CapEnvironmentMetricsRead, "read this environment's metrics")
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, s := range in.Series {
		for _, k := range strings.Split(s, ",") {
			if k = strings.TrimSpace(k); k == "" {
				continue
			}
			if !slices.Contains(HostMetricKeys, k) {
				return nil, Invalid("unknown metric", Field("query.series", "unknown metric key "+k))
			}
			keys = append(keys, k)
		}
	}
	r, err := svc.Query(ctx, domain.MetricQuery{EnvironmentID: env.ID, Kind: domain.MetricHost, From: in.From, To: in.To,
		Step: time.Duration(in.StepSeconds) * time.Second, Keys: keys})
	if errors.Is(err, domain.ErrMetricQuery) {
		msg := strings.TrimPrefix(err.Error(), domain.ErrMetricQuery.Error()+": ")
		return nil, Invalid("invalid metrics query", Field("query", msg))
	}
	if err != nil {
		return nil, Internal(err)
	}
	return &environmentMetricsOutput{Body: newEnvironmentMetrics(env, r)}, nil
}

func newEnvironmentMetrics(env domain.Environment, r domain.MetricResult) EnvironmentMetrics {
	out := EnvironmentMetrics{EnvironmentID: env.ID, From: r.From, To: r.To, StepSeconds: int(r.Step / time.Second), Resolution: r.Resolution,
		Timestamps: r.Timestamps, Series: make([]MetricSeries, 0, len(r.Series)), Online: env.Online,
		SkewCorrected: r.Flags&(domain.SampleSkewCorrected|domain.SampleClamped) != 0,
		Incomplete:    r.Flags&(domain.SampleContainersIncomplete|domain.SampleEngineUnavailable) != 0}
	if out.Timestamps == nil {
		out.Timestamps = []time.Time{}
	}
	for _, s := range r.Series {
		out.Series = append(out.Series, MetricSeries{Key: s.Key, Unit: s.Unit, Mount: s.Mount, Values: s.Values})
	}
	return out
}

func (h *observeAPI) capacity(ctx context.Context, in *environmentIDInput) (*environmentCapacityOutput, error) {
	_, env, err := h.environmentWith(ctx, in.EnvironmentID, CapEnvironmentMetricsRead, "read this environment's capacity")
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	out := EnvironmentCapacity{EnvironmentID: env.ID, Online: env.Online, Disks: []CapacityDisk{}}
	if inv, ok := svc.Inventory(env.ID); ok {
		out.CPUs = inv.CPUs
		if inv.MemoryBytes > 0 {
			m := inv.MemoryBytes
			out.MemoryTotalBytes = &m
		}
	}
	if hx, ok := svc.Host(env.ID); ok {
		if out.CPUs == 0 {
			out.CPUs = hx.CPUs
		}
		out.UptimeSeconds, out.NetworkScope = hx.UptimeSeconds, hx.NetworkScope
	}
	l, ok, err := svc.Latest(ctx, env.ID)
	if err != nil {
		return nil, Internal(err)
	}
	if ok {
		at := l.At
		out.SampledAt = &at
		out.CPUPercent, out.MemoryUsedBytes = l.Host.CPUPercent, l.Host.MemoryUsedBytes
		if l.Host.MemoryTotalBytes != nil {
			out.MemoryTotalBytes = l.Host.MemoryTotalBytes
		}
		out.Load1, out.Load5, out.Load15 = l.Host.Load1, l.Host.Load5, l.Host.Load15
		out.NetworkRxBPS, out.NetworkTxBPS = l.Host.NetworkRxBPS, l.Host.NetworkTxBPS
		for _, d := range l.Disks {
			out.Disks = append(out.Disks, CapacityDisk{Mount: d.Mount, UsedBytes: d.UsedBytes, TotalBytes: d.TotalBytes,
				FreeBytes: max(d.TotalBytes-d.UsedBytes, 0)})
		}
	}
	return &environmentCapacityOutput{Body: out}, nil
}

func dockerCounts(inv observe.Inventory) *DockerCounts {
	return &DockerCounts{Containers: inv.Containers, ContainersRunning: inv.ContainersRunning, ContainersPaused: inv.ContainersPaused,
		ContainersStopped: inv.ContainersStopped, Images: inv.Images, Volumes: inv.Volumes, Networks: inv.Networks}
}

func (h *observeAPI) overview(ctx context.Context, _ *struct{}) (*overviewOutput, error) {
	c, err := h.agents.checker(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	out := Overview{Environments: []OverviewEnvironment{}}
	after := ""
	for {
		page, err := h.agents.svc.ListEnvironments(ctx, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive},
			AfterID: after, Limit: 200})
		if err != nil {
			return nil, Internal(err)
		}
		for _, env := range page {
			v := authz.ViewOf(c, environmentResource(env))
			if !v.Visible() {
				continue
			}
			item := OverviewEnvironment{ID: env.ID, Name: env.Name, Online: env.Online, View: v.Level.String(), Actions: Actions(v)}
			out.Totals.Environments++
			if env.Online {
				out.Totals.Online++
			} else {
				out.Totals.Offline++
			}
			if v.Has(string(CapEnvironmentMetricsRead)) {
				l, ok, err := svc.Latest(ctx, env.ID)
				if err != nil {
					return nil, Internal(err)
				}
				if ok {
					u := &OverviewUsage{SampledAt: l.At, CPUPercent: l.Host.CPUPercent, MemoryUsedBytes: l.Host.MemoryUsedBytes,
						MemoryTotalBytes: l.Host.MemoryTotalBytes}
					for _, d := range l.Disks {
						if d.Mount == "docker" {
							used, total := d.UsedBytes, d.TotalBytes
							u.DiskUsedBytes, u.DiskTotalBytes = &used, &total
						}
					}
					item.Usage = u
				}
			}
			if v.Has(string(CapEnvironmentSystemRead)) {
				if inv, ok := svc.Inventory(env.ID); ok {
					item.Docker = dockerCounts(inv)
					out.Totals.CountedEnvironments++
					out.Totals.Containers += max(inv.Containers, 0)
					out.Totals.ContainersRunning += max(inv.ContainersRunning, 0)
				}
			}
			out.Environments = append(out.Environments, item)
		}
		if len(page) < 200 {
			break
		}
		after = page[len(page)-1].ID
	}
	return &overviewOutput{Body: out}, nil
}

// Environment event stream payloads (docs/api/streams.md).

// EnvironmentStreamHello opens the stream.
type EnvironmentStreamHello struct {
	Version     string `json:"version" enum:"docker-manager.environment-events/v1"`
	Cursor      string `json:"cursor" doc:"Position of the stream; fetch snapshots now, events after it follow."`
	HeartbeatMs int64  `json:"heartbeatMs"`
}

// EnvironmentStreamReset asks the client to refetch.
type EnvironmentStreamReset struct {
	Reason string `json:"reason" enum:"server_restart,cursor_expired,gap,overflow"`
	Cursor string `json:"cursor"`
}

// EngineEvent is one relayed Docker Engine event.
type EngineEvent struct {
	Type       string            `json:"type" enum:"container,image,volume,network,daemon"`
	Action     string            `json:"action" example:"die"`
	ResourceID string            `json:"resourceId" doc:"Container and network name, volume name, image reference or ID."`
	Attributes map[string]string `json:"attributes" doc:"Allowlisted attributes (name, image, exitCode, signal, health); image and signal need the container's details."`
	At         time.Time         `json:"at"`
}

// EnvironmentStatusEvent is an environment status change.
type EnvironmentStatusEvent struct {
	EnvironmentID string    `json:"environmentId"`
	Status        string    `json:"status" enum:"online,offline,resync,updated,archived,reattached"`
	Reason        string    `json:"reason,omitempty" doc:"resync: reconnect or event_gap (refetch the environment's inventory)."`
	At            time.Time `json:"at"`
}

// MetricsEvent says new samples arrived (refetch open charts).
type MetricsEvent struct {
	EnvironmentID string    `json:"environmentId"`
	Host          bool      `json:"host" doc:"Host samples arrived (environment.metrics.read)."`
	Containers    bool      `json:"containers" doc:"Samples of containers the caller may chart arrived (container.metrics.read)."`
	At            time.Time `json:"at"`
}

// InventoryEvent says the Engine inventory was refreshed.
type InventoryEvent struct {
	EnvironmentID string    `json:"environmentId"`
	At            time.Time `json:"at"`
}

var statusOf = map[string]string{
	events.EnvironmentOnline: "online", events.EnvironmentOffline: "offline", events.EnvironmentResync: "resync",
	events.EnvironmentUpdated: "updated", events.EnvironmentArchived: "archived", events.EnvironmentReattached: "reattached",
}

// minimalEngineAttributes are the event attributes of a resource's minimal
// view (identity and status, #17).
var minimalEngineAttributes = map[string]bool{"name": true, "exitCode": true, "health": true}

// streamEvent filters and shapes one journal entry for the caller: the
// SSE event name and data, or ok false when the caller may not see it.
func streamEvent(c authz.Checker, e events.Event) (string, any, bool) {
	if !authz.EventVisible(c, e) {
		return "", nil, false
	}
	switch e.Type {
	case events.DockerEvent:
		attrs := map[string]string{}
		full := false
		if e.ResourceType == catalog.TypeContainer {
			full = authz.ViewOf(c, authz.Resource{Type: catalog.TypeContainer, ID: e.ResourceID, EnvironmentID: e.EnvironmentID}).Full()
		}
		for k, v := range e.Attributes {
			if k == "source" || k == "type" || k == "action" {
				continue
			}
			if full || e.ResourceType != catalog.TypeContainer || minimalEngineAttributes[k] {
				attrs[k] = v
			}
		}
		return "engine", EngineEvent{Type: e.ResourceType, Action: e.Attributes["action"], ResourceID: e.ResourceID, Attributes: attrs, At: e.At}, true
	case events.MetricsSampled:
		env := authz.EnvironmentResource(e.EnvironmentID)
		m := MetricsEvent{EnvironmentID: e.EnvironmentID, At: e.At,
			Host:       e.Attributes["host"] == "true" && c.Can(string(CapEnvironmentMetricsRead), env).Allowed,
			Containers: authz.ContainerMetricsVisible(c, e) != ""}
		return "metrics", m, m.Host || m.Containers
	case events.InventoryUpdated:
		return "inventory", InventoryEvent{EnvironmentID: e.EnvironmentID, At: e.At}, true
	}
	if st, ok := statusOf[e.Type]; ok {
		id := e.EnvironmentID
		if id == "" {
			id = e.ResourceID
		}
		return "status", EnvironmentStatusEvent{EnvironmentID: id, Status: st, Reason: e.Attributes["reason"], At: e.At}, true
	}
	return "", nil, false
}

// DefaultStreamMaxAge ends SSE streams so clients re-authenticate
// (docs/api/streams.md, "Max age").
const DefaultStreamMaxAge = time.Hour

func (h *observeAPI) stream(ctx context.Context, in *streamEnvironmentEventsInput) (*huma.StreamResponse, error) {
	c, env, err := h.environmentWith(ctx, in.EnvironmentID, CapEnvironmentEventsRead, "watch this environment's events")
	if err != nil {
		return nil, err
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		h.runStream(hctx, c, svc.Journal(), env.ID, in.LastEventID)
	}}, nil
}

func (h *observeAPI) runStream(hctx huma.Context, c authz.Checker, j *observe.Journal, env, lastEventID string) {
	ctx := hctx.Context()
	stream := StartSSE(hctx)
	defer stream.CloseIfRevoked(ctx)
	sub := j.Subscribe(env, lastEventID)
	defer sub.Listener.Close()
	clk := h.deps.clock()
	heartbeat := h.deps.SSEHeartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultSSEHeartbeat
	}
	hb := clk.NewTicker(heartbeat)
	defer hb.Stop()
	maxAge := clk.NewTimer(h.maxAge)
	defer maxAge.Stop()
	if stream.Event("hello", "", EnvironmentStreamHello{Version: "docker-manager.environment-events/v1", Cursor: sub.Cursor,
		HeartbeatMs: heartbeat.Milliseconds()}) != nil {
		return
	}
	if sub.Reset != "" && stream.Event("reset", "", EnvironmentStreamReset{Reason: sub.Reset, Cursor: sub.Cursor}) != nil {
		return
	}
	send := func(en observe.Entry) error {
		if en.Reset != "" {
			return stream.Event("reset", "", EnvironmentStreamReset{Reason: en.Reset, Cursor: sub.Listener.Cursor(en)})
		}
		name, data, ok := streamEvent(c, en.Event)
		if !ok {
			return nil
		}
		return stream.Event(name, sub.Listener.Cursor(en), data)
	}
	for _, en := range sub.Replay {
		if send(en) != nil {
			return
		}
	}
	for {
		if cursor, lost := sub.Listener.Overflowed(); lost {
			if stream.Event("reset", "", EnvironmentStreamReset{Reason: observe.ResetOverflow, Cursor: cursor}) != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case en := <-sub.Listener.C():
			if send(en) != nil {
				return
			}
		case <-hb.C():
			if stream.Heartbeat() != nil {
				return
			}
		case <-maxAge.C():
			_ = stream.Event("close", "", CloseEvent{Reason: "max_age"})
			return
		}
	}
}

func registerObserve(a huma.API, deps Deps) {
	h := &observeAPI{agents: &agentsAPI{svc: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}, svc: deps.Observe, deps: deps,
		maxAge: DefaultStreamMaxAge}
	if deps.StreamMaxAge > 0 {
		h.maxAge = deps.StreamMaxAge
	}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-environment-metrics", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/metrics",
			Summary: "Get an environment's host metrics",
			Description: "Host CPU, memory, load, network and per-filesystem disk series of a time range, downsampled to one value per step " +
				"from the finest storage level still holding the range (10 s for 24 h, 1 min for 7 d, 15 min for 90 d). Missing samples " +
				"(the agent was offline, a value unknown) are null, never zero. Units and flags: docs/architecture/metrics.md.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapEnvironmentMetricsRead, Scope: ScopeEnvironment,
	}, h.metrics)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-environment-capacity", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/capacity",
			Summary: "Get an environment's capacity and current usage",
			Description: "Cores, memory, filesystems (by role, never host paths) and the latest sample's usage. Values of an offline " +
				"environment are the last known ones (see sampledAt and online).",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapEnvironmentMetricsRead, Scope: ScopeEnvironment,
	}, h.capacity)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-overview", Method: http.MethodGet, Path: BasePath + "/overview",
			Summary: "Get the cross-environment overview",
			Description: "Every active environment the caller may see with its connection state; the latest host usage where the caller " +
				"holds environment.metrics.read and Docker counts where it holds environment.system.read. Totals count only those.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.overview)

	schemas := a.OpenAPI().Components.Schemas
	var oneOf []*huma.Schema
	for _, t := range []any{EnvironmentStreamHello{}, EnvironmentStreamReset{}, EngineEvent{}, EnvironmentStatusEvent{}, MetricsEvent{},
		InventoryEvent{}, CloseEvent{}} {
		oneOf = append(oneOf, schemas.Schema(reflect.TypeOf(t), true, ""))
	}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stream-environment-events", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/events/stream",
			Summary: "Stream an environment's events (SSE)",
			Description: "Server-sent events: `hello` (cursor), then `engine` (Docker events), `status` (online/offline/resync), " +
				"`metrics` (new samples: refetch open charts) and `inventory` (Engine inventory refreshed), each with `id: <cursor>`. " +
				"Every event is filtered by the capability of its resource; container events of a container the caller sees only " +
				"minimally carry only name, exit code and health. Reconnect with Last-Event-ID to replay the retained events " +
				"(newest 1 000 or 15 min); outside them the stream starts with `reset`. `: heartbeat` comments keep it alive. " +
				"Wire contract: docs/api/streams.md.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
			Responses: map[string]*huma.Response{
				"200": {
					Description: "Event stream",
					Content: map[string]*huma.MediaType{
						"text/event-stream": {Schema: &huma.Schema{Description: "Each data line is one of these payloads.", OneOf: oneOf}},
					},
				},
			},
		},
		Capability: CapEnvironmentEventsRead, Scope: ScopeEnvironment,
	}, h.stream)
}
