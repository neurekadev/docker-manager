package observe

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Publisher relays events on the manager session (session.Client.Events()).
// Publish never blocks and reports whether the event was queued; Drop
// consumes a sequence number without sending, so the manager sees a gap
// and resynchronizes the environment (docs/internal/protocol/agent-v1.md, "Sequence
// numbers and gaps").
type Publisher interface {
	Publish(p protocol.EventPayload) bool
	Drop()
}

// Event relay bounds.
const (
	// DefaultEventRate and DefaultEventBurst bound relayed events per
	// second (token bucket); excess events are dropped as a gap.
	DefaultEventRate  = 50
	DefaultEventBurst = 200
	// DefaultCoalesce drops a repeat of the same (type, resource, action)
	// within this window of Engine time.
	DefaultCoalesce = time.Second
	// maxCoalesceKeys bounds the coalescing memory.
	maxCoalesceKeys = 4096
	eventRetryMin   = time.Second
	eventRetryMax   = 30 * time.Second
)

// EventOptions configures an EventRelay.
type EventOptions struct {
	Clock     clock.Clock
	Logger    *slog.Logger
	Engine    func() EngineAPI
	Publisher Publisher
	Rate      float64
	Burst     float64
	Coalesce  time.Duration
}

// EventRelay streams Docker Engine events through the Moby adapter and
// relays the allowlisted lifecycle events (never environment variables,
// labels or contents) with coalescing and a rate bound.
type EventRelay struct {
	opts EventOptions
	log  *slog.Logger

	tokens   float64
	refilled time.Time
	last     map[string]lastEvent
	lastAt   time.Time
}

type lastEvent struct {
	action string
	at     time.Time
}

// NewEventRelay returns an EventRelay.
func NewEventRelay(opts EventOptions) *EventRelay {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Engine == nil {
		opts.Engine = func() EngineAPI { return nil }
	}
	if opts.Rate <= 0 {
		opts.Rate = DefaultEventRate
	}
	if opts.Burst <= 0 {
		opts.Burst = DefaultEventBurst
	}
	if opts.Coalesce <= 0 {
		opts.Coalesce = DefaultCoalesce
	}
	return &EventRelay{opts: opts, log: opts.Logger.With("component", "events"), tokens: opts.Burst, last: map[string]lastEvent{}}
}

// relayedActions are the lifecycle actions relayed per object type; every
// other action (exec_*, attach, top, archive, mount, ...) is noise for
// inventory invalidation and is dropped at the source.
var relayedActions = map[string][]string{
	"container": {"create", "start", "restart", "stop", "die", "kill", "pause", "unpause", "destroy", "rename", "update", "oom", "health_status"},
	"image":     {"pull", "delete", "tag", "untag", "import", "load"},
	"volume":    {"create", "destroy", "prune"},
	"network":   {"create", "destroy", "remove", "connect", "disconnect", "prune"},
	"daemon":    {"reload"},
}

// Map converts an Engine event into a relayed payload (ok false: not
// relayed). Container and network events are identified by name (the
// authorization identity, #17); volumes by name; images by reference when
// the Engine gives one.
func Map(e engine.Event) (protocol.EventPayload, bool) {
	action, detail, _ := strings.Cut(e.Action, ":")
	action = strings.TrimSpace(action)
	allowed := false
	for _, a := range relayedActions[e.Type] {
		if a == action {
			allowed = true
			break
		}
	}
	if !allowed {
		return protocol.EventPayload{}, false
	}
	p := protocol.EventPayload{Source: "engine", Type: e.Type, Action: action, At: e.Time.UTC(), ResourceID: e.ActorID}
	attrs := map[string]string{}
	name := e.Attributes["name"]
	switch e.Type {
	case "container":
		if name != "" {
			p.ResourceID = name
			attrs["name"] = name
		}
		if img := e.Attributes["image"]; img != "" {
			attrs["image"] = img
		}
		if v := e.Attributes["exitCode"]; v != "" {
			attrs["exitCode"] = v
		}
		if v := e.Attributes["signal"]; v != "" {
			attrs["signal"] = v
		}
		if action == "health_status" {
			attrs["health"] = strings.TrimSpace(detail)
		}
	case "network":
		if name != "" {
			p.ResourceID = name
			attrs["name"] = name
		}
	case "image":
		if name != "" {
			p.ResourceID = name
			attrs["name"] = name
		}
	}
	for k, v := range attrs {
		if len(v) > 256 {
			attrs[k] = v[:256]
		}
	}
	if len(attrs) > 0 {
		p.Attributes = attrs
	}
	if p.At.IsZero() {
		return protocol.EventPayload{}, false
	}
	return p, true
}

// Run relays events until ctx ends. When the Engine stream breaks it
// reconnects with backoff and asks the Engine for the events since the
// last one relayed, so short interruptions lose nothing.
func (r *EventRelay) Run(ctx context.Context) {
	clk := r.opts.Clock
	delay := eventRetryMin
	for ctx.Err() == nil {
		eng := r.opts.Engine()
		if eng != nil {
			f := engine.EventFilter{Types: []string{"container", "image", "volume", "network", "daemon"}, Since: r.lastAt}
			err := eng.Events(ctx, f, func(e engine.Event) error {
				delay = eventRetryMin
				r.handle(e)
				return nil
			})
			if ctx.Err() != nil {
				return
			}
			r.log.Warn("Docker event stream ended; reconnecting", "error", err, "delay", delay.String())
		}
		t := clk.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		delay = min(delay*2, eventRetryMax)
	}
}

// handle coalesces, rate-limits and publishes one Engine event.
func (r *EventRelay) handle(e engine.Event) {
	p, ok := Map(e)
	if !ok {
		return
	}
	if e.Time.After(r.lastAt) {
		r.lastAt = e.Time
	}
	// Only an immediate repeat of the resource's last relayed action is
	// coalesced: a different action in between is always relayed, so the
	// last event of a resource always reflects its latest change.
	key := p.Type + "\x00" + p.ResourceID
	action := p.Action + "\x00" + p.Attributes["health"]
	if prev, ok := r.last[key]; ok && prev.action == action && p.At.Sub(prev.at) < r.opts.Coalesce && !p.At.Before(prev.at) {
		return // a repeat within the window (or a replay after reconnect)
	}
	if len(r.last) >= maxCoalesceKeys {
		clear(r.last)
	}
	r.last[key] = lastEvent{action: action, at: p.At}
	if !r.take() {
		// Over the rate: the manager resynchronizes on the gap.
		r.opts.Publisher.Drop()
		return
	}
	r.opts.Publisher.Publish(p)
}

// take removes a token from the bucket.
func (r *EventRelay) take() bool {
	now := r.opts.Clock.Now()
	if !r.refilled.IsZero() {
		r.tokens = min(r.opts.Burst, r.tokens+now.Sub(r.refilled).Seconds()*r.opts.Rate)
	}
	r.refilled = now
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}
