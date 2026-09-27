package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Container metrics (#5 storage, #6 container identity): the stored samples
// of one container, by its name in the environment. Metrics outlive the
// agent's connection: while the environment is offline the container is
// authorized by its name alone and the range shows gaps for the offline
// time.

// ContainerMetricKeys are the metric keys of GET …/containers/{id}/metrics.
var ContainerMetricKeys = []string{
	"cpu.percent", "cpu.percent.max", "memory.used_bytes", "memory.used_bytes.max", "memory.limit_bytes",
	"network.rx_bytes_per_second", "network.tx_bytes_per_second", "block.read_bytes_per_second", "block.write_bytes_per_second", "pids",
}

// ContainerMetrics is a downsampled time range of one container's metrics.
type ContainerMetrics struct {
	EnvironmentID string         `json:"environmentId"`
	Container     string         `json:"container" doc:"Container name (metrics follow the name across recreations)."`
	From          time.Time      `json:"from"`
	To            time.Time      `json:"to"`
	StepSeconds   int            `json:"stepSeconds"`
	Resolution    string         `json:"resolution" enum:"raw,1m,15m"`
	Timestamps    []time.Time    `json:"timestamps"`
	Series        []MetricSeries `json:"series" doc:"CPU as a percentage of the environment's cores; memory against the container limit when set."`
	SkewCorrected bool           `json:"skewCorrected"`
	Incomplete    bool           `json:"incomplete"`
	Online        bool           `json:"online"`
}

type containerMetricsInput struct {
	ContainerPath
	From        time.Time `query:"from" doc:"Range start (RFC 3339; default: one hour before to)."`
	To          time.Time `query:"to" doc:"Range end (RFC 3339; default: now)."`
	StepSeconds int       `query:"stepSeconds" minimum:"0" maximum:"7776000" doc:"Bucket width in seconds (0: automatic)."`
	Series      []string  `query:"series" doc:"Metric keys to return (default: all)."`
}

type containerMetricsOutput struct{ Body ContainerMetrics }

// metricsContainer authorizes container.metrics.read on the container ref
// names: through the agent while the environment is online (name, ID or
// prefix), by name alone while it is offline.
func (h *dockerAPI) metricsContainer(ctx context.Context, sc *scope, ref string) (string, error) {
	d, err := h.svc.InspectContainer(ctx, sc.env.ID, ref)
	var res authz.Resource
	var de *domain.DockerError
	switch {
	case err == nil:
		res = containerResource(sc.env.ID, d.ContainerSummary, sc.stacks(ctx, h.svc))
	case errors.As(err, &de) && (de.Code == domain.DockerEnvironmentOffline || de.Code == domain.DockerEngineUnavailable):
		// History of an unreachable environment: the name is the identity
		// (its stack membership comes from the Locator).
		res = authz.Resource{Type: catalog.TypeContainer, ID: ref, EnvironmentID: sc.env.ID}
	default:
		return "", lookupErr(err, "container")
	}
	v := authz.ViewOf(sc.c, res)
	if !v.Visible() {
		return "", NotFound("container not found")
	}
	if !v.Has(string(CapContainerMetricsRead)) {
		return "", Forbidden("not permitted to read this container's metrics")
	}
	return res.ID, nil
}

func (h *dockerAPI) containerMetrics(ctx context.Context, in *containerMetricsInput) (*containerMetricsOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	if h.observe == nil {
		return nil, Unavailable(CodeUnavailable, "the observation service is not available")
	}
	name, err := h.metricsContainer(ctx, sc, in.ContainerID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, s := range in.Series {
		for _, k := range strings.Split(s, ",") {
			if k = strings.TrimSpace(k); k == "" {
				continue
			}
			if !slices.Contains(ContainerMetricKeys, k) {
				return nil, Invalid("unknown metric", Field("query.series", "unknown metric key "+k))
			}
			keys = append(keys, k)
		}
	}
	r, err := h.observe.Query(ctx, domain.MetricQuery{EnvironmentID: sc.env.ID, Kind: domain.MetricContainer, Name: name, From: in.From,
		To: in.To, Step: time.Duration(in.StepSeconds) * time.Second, Keys: keys})
	if errors.Is(err, domain.ErrMetricQuery) {
		msg := strings.TrimPrefix(err.Error(), domain.ErrMetricQuery.Error()+": ")
		return nil, Invalid("invalid metrics query", Field("query", msg))
	}
	if err != nil {
		return nil, Internal(err)
	}
	m := newEnvironmentMetrics(sc.env, r)
	return &containerMetricsOutput{Body: ContainerMetrics{EnvironmentID: sc.env.ID, Container: name, From: m.From, To: m.To,
		StepSeconds: m.StepSeconds, Resolution: m.Resolution, Timestamps: m.Timestamps, Series: m.Series, SkewCorrected: m.SkewCorrected,
		Incomplete: m.Incomplete, Online: m.Online}}, nil
}

// LatestContainerWindow is how old a container's newest sample may be to
// be reported as its current usage (samples are 10 s apart; the collector
// fetches them in batches).
const LatestContainerWindow = time.Minute

// LatestContainerMetric is one container's most recent sample.
type LatestContainerMetric struct {
	Container        string    `json:"container" doc:"Container name."`
	At               time.Time `json:"at" doc:"When the sample was taken."`
	CPUPercent       *float64  `json:"cpuPercent,omitempty" doc:"Share of the environment's cores, 0..100; absent when unknown."`
	MemoryUsedBytes  *int64    `json:"memoryUsedBytes,omitempty" doc:"Absent when unknown."`
	MemoryLimitBytes *int64    `json:"memoryLimitBytes,omitempty" doc:"The container's memory limit; absent when unlimited or unknown."`
}

// LatestContainerMetrics are the current usage of an environment's
// containers.
type LatestContainerMetrics struct {
	EnvironmentID string                  `json:"environmentId"`
	WindowSeconds int                     `json:"windowSeconds" doc:"Only containers sampled within this many seconds are listed."`
	Items         []LatestContainerMetric `json:"items" doc:"Sorted by container name; containers without a recent sample (stopped, new) are absent."`
}

type latestContainerMetricsInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
}

type latestContainerMetricsOutput struct{ Body LatestContainerMetrics }

// latestContainerMetrics lists the newest sample of every container the
// caller may chart (container.metrics.read on the container, resolved by
// name through the resource graph like the metrics.sampled events): one
// request instead of a range query per container. Works while the
// environment is offline (the samples stop, so the list empties).
func (h *dockerAPI) latestContainerMetrics(ctx context.Context, in *latestContainerMetricsInput) (*latestContainerMetricsOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	out := LatestContainerMetrics{EnvironmentID: sc.env.ID, WindowSeconds: int(LatestContainerWindow / time.Second), Items: []LatestContainerMetric{}}
	if h.observe == nil {
		return nil, Unavailable(CodeUnavailable, "the observation service is not available")
	}
	all, err := h.observe.LatestContainers(ctx, sc.env.ID, LatestContainerWindow)
	if err != nil {
		return nil, Internal(err)
	}
	for _, m := range all {
		res := authz.Resource{Type: catalog.TypeContainer, ID: m.Values.Name, EnvironmentID: sc.env.ID}
		if !sc.c.Can(string(CapContainerMetricsRead), res).Allowed {
			continue
		}
		out.Items = append(out.Items, LatestContainerMetric{Container: m.Values.Name, At: m.At, CPUPercent: m.Values.CPUPercent,
			MemoryUsedBytes: m.Values.MemoryBytes, MemoryLimitBytes: m.Values.MemoryLimitBytes})
	}
	return &latestContainerMetricsOutput{Body: out}, nil
}

func registerContainerMetrics(a huma.API, deps Deps) {
	h := newDockerAPI(deps)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-container-metrics", Method: http.MethodGet,
			Path:    BasePath + "/environments/{environmentId}/containers/{containerId}/metrics",
			Summary: "Get a container's metrics",
			Description: "Downsampled CPU, memory, network, block I/O and process counts of one container (#5 storage). " +
				"container.metrics.read shows the charts and the container's minimal view only (no details, logs or actions). " +
				"While the environment is offline the stored history stays readable by container name, with gaps for the offline time.",
			Tags: []string{tagContainers}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
				http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		},
		Capability: CapContainerMetricsRead, Scope: ScopeResource,
	}, h.containerMetrics)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-latest-container-metrics", Method: http.MethodGet,
			Path:    BasePath + "/environments/{environmentId}/metrics/containers",
			Summary: "List the current usage of an environment's containers",
			Description: "The newest CPU and memory sample (#5, 10 s resolution) of every container sampled within the last " +
				"windowSeconds, for the containers the caller holds container.metrics.read on (others are absent). " +
				"Tables poll it (or refresh on metrics.sampled) instead of one range query per container. " +
				"Unknown values are absent, never zero.",
			Tags: []string{tagContainers}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
				http.StatusServiceUnavailable},
		},
		Capability: CapContainerMetricsRead, Scope: ScopeEnvironment,
	}, h.latestContainerMetrics)
}
