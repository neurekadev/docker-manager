package live

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/events"
)

// JobSource publishes events.JobUpdated on the bus for jobs the engine
// reports changed (jobs.Engine.OnChange): creation, state changes and
// progress. Changes are batched per window, so a job reporting progress
// many times a second is read from the database at most once per window.
type JobSource struct {
	get    func(ctx context.Context, id string) (domain.Job, error)
	bus    *events.Bus
	clk    clock.Clock
	log    *slog.Logger
	window time.Duration

	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
}

// NewJobSource returns a source reading jobs with get (jobs.Engine.Get).
func NewJobSource(get func(ctx context.Context, id string) (domain.Job, error), bus *events.Bus, clk clock.Clock, log *slog.Logger) *JobSource {
	if clk == nil {
		clk = clock.Real()
	}
	if log == nil {
		log = slog.Default()
	}
	return &JobSource{get: get, bus: bus, clk: clk, log: log.With("component", "live"), window: DefaultCoalesce,
		pending: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// Changed is the engine's change listener: it records the IDs and returns
// at once.
func (s *JobSource) Changed(ids []string) {
	s.mu.Lock()
	for _, id := range ids {
		s.pending[id] = true
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run publishes the batched changes until ctx ends.
func (s *JobSource) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		// Let the window's other changes arrive, then read each job once.
		t := s.clk.NewTimer(s.window)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		s.Publish(ctx)
	}
}

// Publish reads the pending jobs and publishes one event per job (Run
// calls it; tests may too). Jobs deleted meanwhile are skipped: their lists
// refresh with the next change.
func (s *JobSource) Publish(ctx context.Context) {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[string]bool{}
	s.mu.Unlock()
	for id := range batch {
		j, err := s.get(ctx, id)
		if errors.Is(err, domain.ErrJobNotFound) {
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("could not read a changed job for the live stream", "job_id", id, "error", err)
			}
			continue
		}
		s.bus.Publish(JobEvent(j))
	}
}

// JobEvent is the bus event announcing a job's current state.
func JobEvent(j domain.Job) events.Event {
	attrs := map[string]string{"state": string(j.State), "kind": string(j.Kind)}
	if j.Progress.Percent >= 0 && !j.State.Terminal() {
		attrs["percent"] = strconv.Itoa(j.Progress.Percent)
	}
	return events.Event{Type: events.JobUpdated, ResourceType: events.ResourceJob, ResourceID: j.ID,
		EnvironmentID: j.EnvironmentID, Revision: j.LastEventSeq, Attributes: attrs, Job: &j}
}
