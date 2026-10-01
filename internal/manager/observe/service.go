// Package observe is the manager side of observation (#5): the metrics
// collector that fetches each online agent's sample buffer every 10 s,
// shortly after each sampling slot (host.metrics), into the metrics store
// with clock-skew correction and idempotent cursors, the live CPU and
// memory kept in memory about every second while a browser is watching
// (metrics.live, live.go), the Engine inventory cache refreshed on change
// (engine.info), the disk health reports (host.health, health.go), and
// the per-environment event journal behind the environment event stream
// (bounded replay of Docker events, status and metric invalidations).
// docs/internal/architecture/metrics.md.
package observe

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Agents is the session hub as seen by observation
// (internal/manager/agents.Hub implements it).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	// Online reports whether an environment's agent is online.
	Online(environmentID string) bool
}

// Defaults.
const (
	DefaultInterval          = protocol.MetricsInterval
	DefaultFetchTimeout      = 8 * time.Second
	DefaultConcurrency       = 8
	DefaultSkewTolerance     = 2 * time.Second
	DefaultInventoryInterval = 5 * time.Minute
	DefaultInventoryDebounce = time.Second
	// maxPagesPerFetch bounds the host.metrics requests of one fetch (a
	// full 30 min buffer of a large host needs a few).
	maxPagesPerFetch = 40
	inventoryTimeout = 15 * time.Second
)

// Options configures a Service.
type Options struct {
	Store  *metrics.Store
	Agents Agents
	Bus    *events.Bus
	Clock  clock.Clock
	Logger *slog.Logger
	// Interval is the collection period; FetchTimeout bounds one request;
	// Concurrency bounds environments fetched at once.
	Interval     time.Duration
	FetchTimeout time.Duration
	Concurrency  int
	// SkewTolerance: agent clock offsets within it are ignored.
	SkewTolerance time.Duration
	// InventoryInterval refreshes online environments' inventories
	// periodically; InventoryDebounce delays a refresh after a change.
	InventoryInterval time.Duration
	InventoryDebounce time.Duration
	// Journal bounds the per-environment event replay.
	JournalSize int
	JournalAge  time.Duration
	// Environments lists the environments to collect from (default: the
	// ones with an online agent among those seen on the bus).
	Environments func() []string
	// LiveDemand reports whether live metrics are wanted (a browser live
	// stream is open); nil disables live metrics. LiveInterval is the
	// request period, LiveTimeout bounds one request, LiveFresh is the
	// age up to which a live value is served (live.go).
	LiveDemand   func() bool
	LiveInterval time.Duration
	LiveTimeout  time.Duration
	LiveFresh    time.Duration
	// HealthInterval is how often online environments' disk health is
	// fetched (default DefaultHealthInterval, health.go).
	HealthInterval time.Duration
}

// Service runs collection, live metrics, inventory refresh and the event
// journal.
type Service struct {
	opts    Options
	log     *slog.Logger
	journal *Journal

	mu        sync.Mutex
	cursors   map[string]*envCursor
	inflight  map[string]bool
	skipUntil map[string]time.Time
	known     map[string]bool
	inv       map[string]Inventory
	host      map[string]HostExtra
	// Live metrics (live.go): the newest answer per environment, requests
	// in flight and agents that do not serve metrics.live.
	live         map[string]*liveValues
	liveInflight map[string]bool
	liveSkip     map[string]time.Time
	// Disk health reports (health.go).
	health healthState
}

// HostExtra are values of an environment's last sample that are not
// stored as series.
type HostExtra struct {
	At            time.Time
	CPUs          int
	UptimeSeconds *int64
	// NetworkScope is host or agent (protocol.HostSample.NetworkScope).
	NetworkScope string
}

// envCursor is the collector's cached cursor of one environment.
type envCursor struct {
	loaded bool
	c      domain.MetricCursor
	// skewed reports that the last estimate exceeded the tolerance.
	skewed bool
}

// Inventory is an environment's last Engine inventory.
type Inventory struct {
	protocol.EngineInventory
	ReceivedAt time.Time
}

// New returns a Service. Call Load before serving and Run to start it.
func New(opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = DefaultFetchTimeout
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.SkewTolerance <= 0 {
		opts.SkewTolerance = DefaultSkewTolerance
	}
	if opts.InventoryInterval <= 0 {
		opts.InventoryInterval = DefaultInventoryInterval
	}
	if opts.InventoryDebounce <= 0 {
		opts.InventoryDebounce = DefaultInventoryDebounce
	}
	if opts.LiveInterval <= 0 {
		opts.LiveInterval = DefaultLiveInterval
	}
	if opts.LiveTimeout <= 0 {
		opts.LiveTimeout = DefaultLiveTimeout
	}
	if opts.LiveFresh <= 0 {
		opts.LiveFresh = DefaultLiveFresh
	}
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = DefaultHealthInterval
	}
	s := &Service{opts: opts, log: opts.Logger.With("component", "observe"), cursors: map[string]*envCursor{}, inflight: map[string]bool{},
		skipUntil: map[string]time.Time{}, known: map[string]bool{}, inv: map[string]Inventory{}, host: map[string]HostExtra{},
		live: map[string]*liveValues{}, liveInflight: map[string]bool{}, liveSkip: map[string]time.Time{}, health: newHealthState()}
	s.journal = NewJournal(JournalOptions{Bus: opts.Bus, Clock: opts.Clock, Logger: s.log, Size: opts.JournalSize, MaxAge: opts.JournalAge})
	return s
}

// Journal returns the event journal (environment event streams).
func (s *Service) Journal() *Journal { return s.journal }

// Store returns the metrics store.
func (s *Service) Store() *metrics.Store { return s.opts.Store }

// Load reads the stored inventories and disk health reports (the last
// known state is served while an environment is offline).
func (s *Service) Load(ctx context.Context) error {
	recs, err := s.opts.Store.Inventories(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, r := range recs {
		var inv protocol.EngineInventory
		if json.Unmarshal(r.Data, &inv) != nil {
			continue
		}
		s.inv[r.EnvironmentID] = Inventory{EngineInventory: inv, ReceivedAt: r.ReceivedAt}
		s.known[r.EnvironmentID] = true
	}
	s.mu.Unlock()
	return s.loadHealth(ctx)
}

// Run collects metrics, asks for live metrics, refreshes inventories and
// disk health and feeds the journal until ctx ends.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); s.journal.Run(ctx) }()
	go func() { defer wg.Done(); s.runInventory(ctx) }()
	go func() { defer wg.Done(); s.runCollector(ctx) }()
	go func() { defer wg.Done(); s.runLive(ctx) }()
	go func() { defer wg.Done(); s.runHealth(ctx) }()
	wg.Wait()
}

// Inventory returns an environment's last Engine inventory.
func (s *Service) Inventory(environmentID string) (Inventory, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.inv[environmentID]
	return inv, ok
}

// Host returns the non-series values of an environment's last sample.
func (s *Service) Host(environmentID string) (HostExtra, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.host[environmentID]
	return h, ok
}

// Skew returns the clock offset applied to an environment's samples
// (manager minus agent; zero within the tolerance).
func (s *Service) Skew(environmentID string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cursors[environmentID]; c != nil {
		return c.c.Skew
	}
	return 0
}

// Online reports whether an environment's agent is online.
func (s *Service) Online(environmentID string) bool { return s.opts.Agents.Online(environmentID) }

// Query runs a metrics query.
func (s *Service) Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	return s.opts.Store.Query(ctx, q)
}

// QueryContainers returns stored series of every container of an
// environment with values in the range that visible accepts
// (metrics.Store.QueryContainers).
func (s *Service) QueryContainers(ctx context.Context, q domain.MetricQuery, visible func(name string) bool) (domain.MetricResult, error) {
	return s.opts.Store.QueryContainers(ctx, q, visible)
}

// Latest returns an environment's latest host sample: the stored one, with
// the live CPU and memory while they are fresh (live.go).
func (s *Service) Latest(ctx context.Context, environmentID string) (domain.LatestMetrics, bool, error) {
	l, ok, err := s.opts.Store.Latest(ctx, environmentID)
	if err != nil {
		return l, ok, err
	}
	l, ok = withLiveHost(l, ok, s.liveFor(environmentID))
	return l, ok, nil
}

// LatestTemperatures returns an environment's latest temperature of each
// sensor and when it was read (ok false: none yet).
func (s *Service) LatestTemperatures(ctx context.Context, environmentID string) ([]domain.TemperatureValues, time.Time, bool, error) {
	return s.opts.Store.LatestTemperatures(ctx, environmentID)
}

// LatestContainers returns the latest sample of each container of an
// environment sampled within window, with the live CPU and memory while
// they are fresh (live.go).
func (s *Service) LatestContainers(ctx context.Context, environmentID string, window time.Duration) ([]domain.LatestContainerMetrics, error) {
	stored, err := s.opts.Store.LatestContainers(ctx, environmentID, window)
	if err != nil {
		return nil, err
	}
	return withLiveContainers(stored, s.liveFor(environmentID)), nil
}

// noteEnvironment remembers an environment seen on the bus.
func (s *Service) noteEnvironment(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	s.known[id] = true
	s.mu.Unlock()
}

// environments are the environments to collect from.
func (s *Service) environments() []string {
	if s.opts.Environments != nil {
		return s.opts.Environments()
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.known))
	for id := range s.known {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	out := ids[:0]
	for _, id := range ids {
		if s.opts.Agents.Online(id) {
			out = append(out, id)
		}
	}
	return out
}
