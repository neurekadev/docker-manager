package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/live"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// The live invalidation stream (#23): GET /api/v1/live/stream. One
// multiplexed, versioned, permission-filtered SSE stream per open UI tab
// carrying invalidations (never resource bodies or secrets). Wire contract:
// docs/api/streams.md ("Live invalidation stream").

// LiveHub is the live stream hub as the API uses it (*live.Hub).
type LiveHub interface {
	Subscribe(principal, lastEventID string) (live.Subscription, error)
	Cursor(r live.Record) string
}

// FileWatch keeps volumes with an open file view watched (#23,
// *files.Watcher).
type FileWatch interface {
	Hold(environmentID, volume string) (release func())
}

// Live stream filter bounds.
const (
	maxLiveScopes = 16
)

// liveIDRE matches environment and stack IDs in filters.
var liveIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

type streamLiveEventsInput struct {
	LastEventID   string `header:"Last-Event-ID" maxLength:"64" doc:"Resume after this cursor (sent automatically by EventSource on reconnect)."`
	Cursor        string `query:"cursor" maxLength:"64" doc:"Resume after this cursor when the client reconnects by itself and cannot set Last-Event-ID (a new EventSource). Last-Event-ID wins."`
	Topics        string `query:"topics" maxLength:"512" doc:"Comma-separated topics to receive (default: every topic). Unknown topics are 422; topics the caller may not see produce nothing."`
	EnvironmentID string `query:"environmentId" maxLength:"64" doc:"Only events of this environment (plus instance-wide ones such as jobs without an environment, policies and settings)."`
	StackID       string `query:"stackId" maxLength:"1200" doc:"Comma-separated stack IDs (at most 16): narrows file events to these stacks' project directories (open file views)."`
	Volume        string `query:"volume" maxLength:"4200" doc:"Comma-separated <environmentId>/<volume name> (at most 16): narrows file events to these volumes and keeps them watched while the stream is open (needs volume.files.read)."`
}

// LiveHello opens the live stream.
type LiveHello struct {
	Version     string   `json:"version" enum:"dockyard.live/v1"`
	Cursor      string   `json:"cursor" doc:"Stream position. Fresh streams: fetch every open view now; events after it follow."`
	HeartbeatMs int64    `json:"heartbeatMs"`
	Topics      []string `json:"topics" doc:"The topics this stream carries."`
	Resumed     bool     `json:"resumed" doc:"The Last-Event-ID/cursor was accepted: the missed events follow and cached data stays valid."`
}

// LiveInvalidate says a resource changed: refetch its queries and lists.
type LiveInvalidate struct {
	Topic         string    `json:"topic" example:"containers"`
	Kind          string    `json:"kind" example:"container" doc:"Resource type (for example container, stack, backup_policy, inventory, metrics)."`
	ResourceID    string    `json:"resourceId" doc:"Stable resource ID (container and network names, volume names, image references)."`
	EnvironmentID string    `json:"environmentId,omitempty"`
	Revision      int64     `json:"revision,omitempty" doc:"The resource's revision after the change, when it has one: ignore when not above the cached revision."`
	Action        string    `json:"action" enum:"created,updated,deleted"`
	At            time.Time `json:"at"`
}

// LiveJob is a job's current state (details: GET /jobs/{jobId}/events/stream).
type LiveJob struct {
	JobID           string    `json:"jobId"`
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	EnvironmentID   string    `json:"environmentId,omitempty"`
	ProgressPercent *int      `json:"progressPercent,omitempty"`
	Revision        int64     `json:"revision" doc:"The job's event sequence: ignore when not above the cached one."`
	At              time.Time `json:"at"`
}

// LiveAgentStatus is an environment's connection state.
type LiveAgentStatus struct {
	EnvironmentID string    `json:"environmentId"`
	Status        string    `json:"status" enum:"online,offline"`
	At            time.Time `json:"at"`
}

// LiveFileScope names a watched file scope.
type LiveFileScope struct {
	Kind          string `json:"kind" enum:"stack,volume,environment" doc:"environment: every file scope of the environment."`
	ID            string `json:"id" doc:"Stack ID, volume name, or the environment ID."`
	EnvironmentID string `json:"environmentId"`
}

// LiveFilesChanged says entries of a stack or volume changed (never
// contents; names only for holders of the scope's files-read capability).
type LiveFilesChanged struct {
	Scope    LiveFileScope `json:"scope"`
	Paths    []string      `json:"paths" doc:"Root-relative changed entries or directories: refresh the listing of each path's directory and of the path itself."`
	Overflow bool          `json:"overflow" doc:"Refresh every listing of the scope."`
	At       time.Time     `json:"at"`
}

// LivePermissionsChanged precedes the close of a stream whose caller's
// permissions changed.
type LivePermissionsChanged struct {
	At time.Time `json:"at"`
}

// LiveReset asks the client to discard cached data and refetch.
type LiveReset struct {
	Reason        string `json:"reason" enum:"server_restart,cursor_expired,gap,overflow"`
	Cursor        string `json:"cursor"`
	EnvironmentID string `json:"environmentId,omitempty" doc:"Only this environment's data is stale (its agent reconnected or lost events)."`
}

// liveFilter is a stream's topic and scope selection.
type liveFilter struct {
	topics  map[string]bool
	env     string
	stacks  map[string]bool
	volumes map[string]bool // "<env>/<name>"
}

func parseLiveFilter(in *streamLiveEventsInput) (liveFilter, error) {
	f := liveFilter{topics: map[string]bool{}, env: in.EnvironmentID}
	all := live.Topics()
	if strings.TrimSpace(in.Topics) == "" {
		for _, t := range all {
			f.topics[t] = true
		}
	} else {
		for _, t := range strings.Split(in.Topics, ",") {
			t = strings.TrimSpace(t)
			if !slices.Contains(all, t) {
				return f, Invalid("unknown live topic", Field("query.topics", "unknown topic "+strconv.Quote(t)+"; topics: "+strings.Join(all, ", ")))
			}
			f.topics[t] = true
		}
	}
	list := func(v, field string, check func(string) bool) (map[string]bool, error) {
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		out := map[string]bool{}
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if !check(s) {
				return nil, Invalid("invalid live filter", Field(field, strconv.Quote(s)+" is malformed"))
			}
			out[s] = true
		}
		if len(out) > maxLiveScopes {
			return nil, Invalid("too many live filters", Field(field, "at most 16 entries"))
		}
		return out, nil
	}
	var err error
	if f.stacks, err = list(in.StackID, "query.stackId", liveIDRE.MatchString); err != nil {
		return f, err
	}
	f.volumes, err = list(in.Volume, "query.volume", func(s string) bool {
		env, name, ok := strings.Cut(s, "/")
		return ok && liveIDRE.MatchString(env) && protocol.ValidVolumeName(name)
	})
	return f, err
}

// envMatch applies the environment filter.
func (f liveFilter) envMatch(env string) bool { return f.env == "" || env == "" || env == f.env }

// filesMatch applies the stack/volume narrowing of file events.
func (f liveFilter) filesMatch(e events.Event) bool {
	if f.stacks == nil && f.volumes == nil {
		return true
	}
	switch e.Attributes["scopeKind"] {
	case protocol.ScopeStack:
		return f.stacks[e.Attributes["scopeId"]]
	case protocol.ScopeVolume:
		return f.volumes[e.EnvironmentID+"/"+e.Attributes["scopeId"]]
	}
	return e.ResourceID == "*" // whole-environment invalidations reach every file view
}

// liveEvent filters and shapes one record for the caller: SSE event name
// and data, or ok false.
func liveEvent(c authz.Checker, f liveFilter, r live.Record, cursor string) (string, any, bool) {
	e := r.Event
	if r.Reset != "" {
		if e.Type == "" {
			return "reset", LiveReset{Reason: r.Reset, Cursor: cursor}, true
		}
		env := e.EnvironmentID
		if !f.envMatch(env) || !authz.EventVisible(c, e) {
			return "", nil, false
		}
		return "reset", LiveReset{Reason: r.Reset, Cursor: cursor, EnvironmentID: env}, true
	}
	topic, kind := live.Classify(e)
	if topic == "" || !f.topics[topic] || !f.envMatch(e.EnvironmentID) || !authz.EventVisible(c, e) {
		return "", nil, false
	}
	switch e.Type {
	case events.JobUpdated:
		j := LiveJob{JobID: e.ResourceID, Kind: e.Attributes["kind"], State: e.Attributes["state"], EnvironmentID: e.EnvironmentID,
			Revision: e.Revision, At: e.At}
		if p, err := strconv.Atoi(e.Attributes["percent"]); err == nil {
			j.ProgressPercent = &p
		}
		return "job", j, true
	case events.EnvironmentOnline, events.EnvironmentOffline:
		st := "online"
		if e.Type == events.EnvironmentOffline {
			st = "offline"
		}
		return "agent", LiveAgentStatus{EnvironmentID: envOfEvent(e), Status: st, At: e.At}, true
	case events.FilesInvalidated:
		if !f.filesMatch(e) {
			return "", nil, false
		}
		fc := LiveFilesChanged{Scope: LiveFileScope{Kind: e.Attributes["scopeKind"], ID: e.Attributes["scopeId"], EnvironmentID: e.EnvironmentID},
			Paths: slices.Clone(e.Paths), Overflow: e.Overflow || len(e.Paths) == 0, At: e.At}
		if e.ResourceID == "*" {
			fc.Scope = LiveFileScope{Kind: "environment", ID: e.EnvironmentID, EnvironmentID: e.EnvironmentID}
			fc.Paths, fc.Overflow = nil, true
		}
		if fc.Paths == nil {
			fc.Paths = []string{}
		}
		return "files.changed", fc, true
	case events.MetricsSampled, events.InventoryUpdated:
		return "invalidate", LiveInvalidate{Topic: topic, Kind: kind, ResourceID: e.EnvironmentID, EnvironmentID: e.EnvironmentID,
			Action: live.ActionUpdated, At: e.At}, true
	}
	id := e.ResourceID
	if e.ResourceType == events.ResourceEnvironment {
		id = envOfEvent(e)
	}
	return "invalidate", LiveInvalidate{Topic: topic, Kind: kind, ResourceID: id, EnvironmentID: e.EnvironmentID, Revision: e.Revision,
		Action: live.ActionOf(e), At: e.At}, true
}

func envOfEvent(e events.Event) string {
	if e.EnvironmentID != "" {
		return e.EnvironmentID
	}
	return e.ResourceID
}

type liveAPI struct {
	deps   Deps
	maxAge time.Duration
}

func (h *liveAPI) stream(ctx context.Context, in *streamLiveEventsInput) (*huma.StreamResponse, error) {
	c, p, err := CheckerFor(ctx, h.deps.Authorizer)
	if err != nil {
		return nil, err
	}
	f, err := parseLiveFilter(in)
	if err != nil {
		return nil, err
	}
	if h.deps.Live == nil {
		return nil, Unavailable(CodeUnavailable, "the live stream is not available")
	}
	last := in.LastEventID
	if last == "" {
		last = in.Cursor
	}
	sub, err := h.deps.Live.Subscribe(p.Key(), last)
	if errors.Is(err, live.ErrTooManyStreams) {
		return nil, RateLimited("too many open live streams; use one per tab")
	}
	if err != nil {
		return nil, Internal(err)
	}
	var holds []func()
	if h.deps.FileWatch != nil {
		for v := range f.volumes {
			env, name, _ := strings.Cut(v, "/")
			if c.Can("volume.files.read", authz.Resource{Type: catalog.TypeVolume, ID: name, EnvironmentID: env}).Allowed {
				holds = append(holds, h.deps.FileWatch.Hold(env, name))
			}
		}
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() {
			for _, release := range holds {
				release()
			}
		}()
		defer sub.Sub.Close()
		h.run(hctx, c, f, sub)
	}}, nil
}

func (h *liveAPI) run(hctx huma.Context, c authz.Checker, f liveFilter, sub live.Subscription) {
	ctx := hctx.Context()
	stream := StartSSE(hctx)
	defer stream.CloseIfRevoked(ctx)
	defer func() {
		if authz.CloseReason(ctx) == "permissions_changed" {
			// Clients drop every cached query, refetch /me/permissions
			// and reconnect (CloseIfRevoked then sends close).
			_ = stream.Event("permissions.changed", "", LivePermissionsChanged{At: h.deps.clock().Now().UTC()})
		}
	}()
	clk := h.deps.clock()
	heartbeat := h.deps.SSEHeartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultSSEHeartbeat
	}
	hb := clk.NewTicker(heartbeat)
	defer hb.Stop()
	maxAge := clk.NewTimer(h.maxAge)
	defer maxAge.Stop()
	topics := make([]string, 0, len(f.topics))
	for _, t := range live.Topics() {
		if f.topics[t] {
			topics = append(topics, t)
		}
	}
	if stream.Event("hello", "", LiveHello{Version: live.Version, Cursor: sub.Cursor, HeartbeatMs: heartbeat.Milliseconds(),
		Topics: topics, Resumed: sub.Resumed}) != nil {
		return
	}
	if sub.Reset != "" && stream.Event("reset", "", LiveReset{Reason: sub.Reset, Cursor: sub.Cursor}) != nil {
		return
	}
	send := func(r live.Record) error {
		cursor := h.deps.Live.Cursor(r)
		name, data, ok := liveEvent(c, f, r, cursor)
		if !ok {
			return nil
		}
		if name == "reset" {
			return stream.Event(name, "", data)
		}
		return stream.Event(name, cursor, data)
	}
	for _, r := range sub.Replay {
		if send(r) != nil {
			return
		}
	}
	for {
		if cursor, lost := sub.Sub.Overflowed(); lost {
			if stream.Event("reset", "", LiveReset{Reason: live.ResetOverflow, Cursor: cursor}) != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case r := <-sub.Sub.C():
			if send(r) != nil {
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

func registerLive(a huma.API, deps Deps) {
	h := &liveAPI{deps: deps, maxAge: DefaultStreamMaxAge}
	if deps.StreamMaxAge > 0 {
		h.maxAge = deps.StreamMaxAge
	}
	schemas := a.OpenAPI().Components.Schemas
	var oneOf []*huma.Schema
	for _, t := range []any{LiveHello{}, LiveInvalidate{}, LiveJob{}, LiveAgentStatus{}, LiveFilesChanged{}, LivePermissionsChanged{},
		LiveReset{}, CloseEvent{}} {
		oneOf = append(oneOf, schemas.Schema(reflect.TypeOf(t), true, ""))
	}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stream-live-events", Method: http.MethodGet, Path: BasePath + "/live/stream",
			Summary: "Stream live invalidations (SSE)",
			Description: "The one multiplexed live stream per open UI tab (#23): `hello` (cursor; fetch open views), then `invalidate` " +
				"(a resource changed: refetch it), `job` (job state and progress), `agent` (environment online/offline), `files.changed` " +
				"(entries of a stack or volume changed), `reset` (discard cached data and refetch), `permissions.changed` + `close` (drop all " +
				"cached data, refetch /me/permissions, reconnect). Each resumable event has `id: <cursor>`; reconnect with Last-Event-ID (or " +
				"`cursor`) to replay what was missed (newest 10 000 events or 15 min). Every event is filtered by the caller's permissions: " +
				"nothing about a resource the caller may not see, file names only with the scope's files-read capability. Events never carry " +
				"resource bodies, file contents or secrets. `: heartbeat` comments keep it alive; at most 8 streams per user or token (429). " +
				"Wire contract: docs/api/streams.md.",
			Tags:   []string{"Live"},
			Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{
				"200": {
					Description: "Event stream",
					Content: map[string]*huma.MediaType{
						"text/event-stream": {Schema: &huma.Schema{Description: "Each data line is one of these payloads.", OneOf: oneOf}},
					},
				},
			},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.stream)
}
