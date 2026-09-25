// Package events is the manager's in-process event bus: resource changes
// (environments online/offline, agents enrolled or revoked, relayed Docker
// events, file-scope invalidations) published by the manager's services and
// consumed by the live invalidation stream (#23) and other internal
// subscribers.
//
// Semantics:
//
//   - Every published event gets the next global sequence number (Seq,
//     starting at 1, contiguous). Subscribers receive events in Seq order.
//   - Delivery is bounded: each subscription has a fixed buffer. When it is
//     full the event is dropped for that subscriber only, and the
//     subscriber observes the jump in Seq: a gap means "resynchronize"
//     (refetch the affected snapshots), exactly like the stream's gap
//     signal to browsers (#23). Publishers never block.
//   - Events carry identifiers, kinds and revisions, never secrets or file
//     contents. File invalidations carry paths only for the internal
//     consumer, which must filter them by the reader's file permissions
//     before anything leaves the manager (#23).
package events

import (
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
)

// Event types.
const (
	EnvironmentCreated    = "environment.created"
	EnvironmentUpdated    = "environment.updated"
	EnvironmentOnline     = "environment.online"
	EnvironmentOffline    = "environment.offline"
	EnvironmentArchived   = "environment.archived"
	EnvironmentReattached = "environment.reattached"
	// EnvironmentResync: the manager lost track of an environment's event
	// history (agent reconnect or an event sequence gap); consumers refetch
	// its inventory.
	EnvironmentResync = "environment.resync"

	AgentEnrolled           = "agent.enrolled"
	AgentUpdated            = "agent.updated"
	AgentRevoked            = "agent.revoked"
	AgentCredentialRotated  = "agent.credential_rotated" //nolint:gosec // G101: an event type, not a credential
	AgentCapabilitiesUpdate = "agent.capabilities_updated"

	EnrollmentCreated  = "enrollment.created"
	EnrollmentRevoked  = "enrollment.revoked"
	EnrollmentUsed     = "enrollment.used"
	EnrollmentRejected = "enrollment.rejected"

	// DockerEvent relays one allowlisted Docker Engine event (#5).
	DockerEvent = "docker.event"
	// FilesInvalidated: paths changed under a watched file scope; with
	// Overflow set (or no paths) the whole scope is invalid (#15, #23).
	FilesInvalidated = "files.invalidated"

	// MetricsSampled: new metric samples of an environment were stored
	// (#5). Attributes["host"] is "true" when host values arrived; Members
	// lists the containers with new samples.
	MetricsSampled = "metrics.sampled"
	// InventoryUpdated: an environment's Engine inventory (identity,
	// capacity, Docker counts) was refreshed (#5).
	InventoryUpdated = "inventory.updated"

	// Stack changes (#7): created/imported, updated (status, jobs, Engine
	// state, metadata), removed, and a new revision of the definition.
	StackCreated          = "stack.created"
	StackUpdated          = "stack.updated"
	StackRemoved          = "stack.removed"
	StackRevisionRecorded = "stack.revision_recorded"
)

// Resource types.
const (
	ResourceEnvironment = "environment"
	ResourceAgent       = "agent"
	ResourceEnrollment  = "enrollment"
	ResourceContainer   = "container"
	ResourceImage       = "image"
	ResourceVolume      = "volume"
	ResourceNetwork     = "network"
	ResourceFileScope   = "file_scope"
	ResourceStack       = "stack"
)

// Event is one published change.
type Event struct {
	// Seq is assigned by Publish: global, contiguous, starting at 1.
	Seq  uint64
	Type string
	// ResourceType and ResourceID identify the changed resource.
	ResourceType string
	ResourceID   string
	// EnvironmentID scopes environment-bound resources.
	EnvironmentID string
	// Revision of the resource after the change, when it has one.
	Revision int64
	At       time.Time
	// Attributes are small, non-secret facts (e.g. a Docker event's action,
	// a file scope kind). Never secrets or contents.
	Attributes map[string]string
	// Paths are changed root-relative paths of a file scope (internal only;
	// filter by permission before sending anywhere).
	Paths []string
	// Overflow marks a file-scope invalidation that covers the whole scope.
	Overflow bool
	// Members are the resource IDs a batch event covers (metrics.sampled:
	// container names). Internal: filter per member before anything leaves
	// the manager.
	Members []string
}

// Bus distributes events to subscribers. The zero value is not usable; call
// New.
type Bus struct {
	clk clock.Clock

	mu   sync.Mutex
	seq  uint64
	subs map[*Subscription]struct{}
}

// New returns a bus stamping events with clk (nil: the wall clock).
func New(clk clock.Clock) *Bus {
	if clk == nil {
		clk = clock.Real()
	}
	return &Bus{clk: clk, subs: map[*Subscription]struct{}{}}
}

// Publish assigns the next sequence number and timestamp (when unset) and
// delivers e to every subscriber that has room. It never blocks.
func (b *Bus) Publish(e Event) Event {
	if b == nil {
		return e
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	if e.At.IsZero() {
		e.At = b.clk.Now().UTC()
	}
	for s := range b.subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped++
		}
	}
	return e
}

// Seq returns the last assigned sequence number.
func (b *Bus) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Subscription receives events in order until Close.
type Subscription struct {
	bus     *Bus
	ch      chan Event
	filter  func(Event) bool
	dropped uint64
}

// DefaultBuffer is the per-subscription buffer of Subscribe(0).
const DefaultBuffer = 256

// Subscribe registers a subscriber with a buffer of n events (0:
// DefaultBuffer). filter, when set, selects the events it receives (filtered
// events do not count as drops; subscribers with a filter detect gaps with
// Dropped instead of Seq).
func (b *Bus) Subscribe(n int, filter func(Event) bool) *Subscription {
	if n <= 0 {
		n = DefaultBuffer
	}
	s := &Subscription{bus: b, ch: make(chan Event, n), filter: filter}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// C delivers the events.
func (s *Subscription) C() <-chan Event { return s.ch }

// Dropped returns how many events were dropped because the buffer was full.
func (s *Subscription) Dropped() uint64 {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.dropped
}

// Close unregisters the subscription. Buffered events stay readable.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	delete(s.bus.subs, s)
}
