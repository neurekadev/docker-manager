package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Container logs and exec terminals (#8). Logs need container.logs.read,
// terminals container.exec (api.AuthorizeExec: API tokens only with
// container.exec in their own grants, #31); metrics, restart or any other
// container capability never open them. Stream wire contracts:
// docs/internal/api/streams.md.

// Capabilities of container logs and terminals (#17).
const (
	CapContainerLogsRead Capability = "container.logs.read"
	CapContainerExec     Capability = "container.exec"
)

// ExecSubprotocol is the WebSocket subprotocol of exec sessions;
// ExecTicketPrefix prefixes the one-use attach ticket offered as a second
// subprotocol ("docker-manager.ticket.<ticket>", never in the URL).
const (
	ExecSubprotocol  = "docker-manager.exec.v1"
	ExecTicketPrefix = "docker-manager.ticket."
)

// ContainerIOService serves container logs and exec sessions
// (internal/manager/containerio). Errors are *domain.DockerError
// (transport, Engine) or the domain.Err* of containerio.go.
type ContainerIOService interface {
	Logs(ctx context.Context, env string, in protocol.ContainerLogsInput) (protocol.ContainerLogsOutput, error)
	// FollowLogs opens a live log feed; it ends when ctx ends.
	FollowLogs(ctx context.Context, env string, in protocol.ContainerLogsInput) (LogFeed, error)
	CreateExec(ctx context.Context, req ExecRequest) (ExecSession, error)
	// CheckAttach validates an attach before the WebSocket upgrade.
	CheckAttach(ctx context.Context, a ExecAttach) error
	// Attach upgrades the request and relays the session until it ends.
	Attach(ctx context.Context, w http.ResponseWriter, r *http.Request, a ExecAttach)
	DeleteExec(ctx context.Context, a ExecAttach) error
}

// LogFeed is a live container log feed.
type LogFeed interface {
	// Next returns the next event; domain.ErrContainerGone when the
	// container was removed, a *domain.DockerError when the agent went
	// away, ctx's error when ctx ended.
	Next(ctx context.Context) (LogEvent, error)
	Close()
}

// LogEvent is one line, or (Dropped > 0) the number of lines dropped
// because the client did not keep up.
type LogEvent struct {
	Line    protocol.LogLine
	Dropped int
}

// ExecRequest creates an exec session.
type ExecRequest struct {
	Principal     authz.Principal
	EnvironmentID string
	// Container is the authorization resource (re-checked while attached).
	Container authz.Resource
	Input     protocol.ExecCreateInput
}

// ExecSession is a created session and its one-use attach ticket.
type ExecSession struct {
	ID        string
	Ticket    string
	ExpiresAt time.Time
	// Command is the argv the agent started (the resolved shell).
	Command []string
}

// ExecAttach identifies a session for attach and delete.
type ExecAttach struct {
	Principal     authz.Principal
	EnvironmentID string
	ContainerID   string
	SessionID     string
	Ticket        string
	// Allowed re-checks container.exec while attached (Decision.Ended: the
	// token or account no longer works).
	Allowed func(ctx context.Context) authz.Decision
}

// LogLineDTO is one line of container output (secrets may appear in logs;
// they are never stored by the manager).
type LogLineDTO struct {
	At      time.Time `json:"at"`
	Stream  string    `json:"stream" enum:"stdout,stderr"`
	Line    string    `json:"line" example:"GET /healthz HTTP/1.1 200" doc:"The line without its newline; invalid UTF-8 is replaced by U+FFFD."`
	Partial bool      `json:"partial,omitempty" doc:"The line continues in the next entry (lines over 16 KiB are split)."`
}

func newLogLine(l protocol.LogLine) LogLineDTO {
	line := string(l.Data)
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "�")
	}
	return LogLineDTO{At: l.At.UTC(), Stream: l.Stream, Line: line, Partial: l.Partial}
}

// ContainerLogs is a bounded tail of a container's output.
type ContainerLogs struct {
	Lines     []LogLineDTO `json:"lines" doc:"Oldest first."`
	Truncated bool         `json:"truncated" doc:"Older lines were left out (at most 5000 lines / 600 KiB per request)."`
}

type containerLogsInput struct {
	ContainerPath
	Tail   int       `query:"tail" minimum:"0" maximum:"5000" doc:"Lines from the end (default 500)."`
	Since  time.Time `query:"since" doc:"Only lines at or after this time (RFC 3339)."`
	Until  time.Time `query:"until" doc:"Only lines at or before this time (RFC 3339)."`
	Stdout bool      `query:"stdout" default:"true" doc:"Include stdout."`
	Stderr bool      `query:"stderr" default:"true" doc:"Include stderr."`
}

type streamContainerLogsInput struct {
	ContainerPath
	Tail        int       `query:"tail" minimum:"0" maximum:"5000" doc:"Lines from the end before following (default 200); ignored when resuming."`
	Since       time.Time `query:"since" doc:"Start at this time (RFC 3339) instead of the tail."`
	Stdout      bool      `query:"stdout" default:"true" doc:"Include stdout."`
	Stderr      bool      `query:"stderr" default:"true" doc:"Include stderr."`
	LastEventID string    `header:"Last-Event-ID" maxLength:"64" doc:"Resume after this line timestamp (sent by EventSource on reconnect); lines with the same timestamp may repeat."`
}

type containerLogsOutput struct{ Body ContainerLogs }

// ExecSessionCreate is the body of POST …/exec-sessions.
type ExecSessionCreate struct {
	Command    []string `json:"command,omitempty" example:"/bin/sh" maxItems:"256" doc:"argv run inside the container. Never a host shell. Mutually exclusive with shell; without either the shell is auto."`
	Shell      string   `json:"shell,omitempty" enum:"auto,bash,sh,zsh" doc:"A shell the agent finds in the container: the first of its common paths that exists (bash: /bin/bash, /usr/bin/bash, /usr/local/bin/bash; zsh: /bin/zsh, /usr/bin/zsh, /usr/local/bin/zsh; sh: /bin/sh, /usr/bin/sh, /busybox/sh; auto: Bash if present, else sh). 422 command_not_found when none exists. Mutually exclusive with command; the default when neither is given is auto."`
	Tty        *bool    `json:"tty,omitempty" doc:"Allocate a terminal (default true)."`
	Cols       uint     `json:"cols,omitempty" example:"120" minimum:"1" maximum:"1000" doc:"Terminal columns (default 80)."`
	Rows       uint     `json:"rows,omitempty" example:"32" minimum:"1" maximum:"1000" doc:"Terminal rows (default 24)."`
	WorkingDir string   `json:"workingDir,omitempty" maxLength:"4096"`
	User       string   `json:"user,omitempty" maxLength:"256" doc:"User (and group) in the container, e.g. 0 or www-data."`
}

// ExecSessionDTO is a created exec session.
type ExecSessionDTO struct {
	ID          string    `json:"id"`
	StreamURL   string    `json:"streamUrl" doc:"WebSocket URL path of the session (same origin)."`
	Subprotocol string    `json:"subprotocol" example:"docker-manager.exec.v1"`
	Ticket      string    `json:"ticket" doc:"One-use attach ticket: offer it as the WebSocket subprotocol docker-manager.ticket.<ticket> next to docker-manager.exec.v1. Bound to this session and caller; expires with expiresAt."`
	ExpiresAt   time.Time `json:"expiresAt" doc:"Attach before this time (60 s)."`
	Command     []string  `json:"command" example:"/bin/bash" doc:"The argv actually started (the resolved shell)."`
}

type createExecInput struct {
	ContainerPath
	Body ExecSessionCreate
}

// ExecSessionPath are the path parameters of an exec session route.
type ExecSessionPath struct {
	ContainerPath
	SessionID string `path:"sessionId" maxLength:"64" doc:"Exec session ID."`
}

type streamExecInput struct {
	ExecSessionPath
	Protocols string `header:"Sec-WebSocket-Protocol" maxLength:"512" doc:"docker-manager.exec.v1 and docker-manager.ticket.<ticket>."`
}

type createExecOutput struct{ Body ExecSessionDTO }

// ioAPI serves logs and terminals.
type ioAPI struct {
	*dockerAPI
	io        ContainerIOService
	clk       clock.Clock
	heartbeat time.Duration
	maxAge    time.Duration
}

// ioContainer resolves a visible container of the environment.
func (h *ioAPI) ioContainer(ctx context.Context, envID, ref string) (*scope, protocol.ContainerDetails, authz.Resource, error) {
	sc, err := h.environment(ctx, envID, false)
	if err != nil {
		return nil, protocol.ContainerDetails{}, authz.Resource{}, err
	}
	d, err := h.svc.InspectContainer(ctx, sc.env.ID, ref)
	if err != nil {
		return nil, d, authz.Resource{}, lookupErr(err, "container")
	}
	res := containerResource(sc.env.ID, d.ContainerSummary, sc.stacks(ctx, h.svc))
	if !authz.ViewOf(sc.c, res).Visible() {
		return nil, d, res, NotFound("container not found")
	}
	return sc, d, res, nil
}

func (h *ioAPI) requireLogs(ctx context.Context, envID, ref string) (*scope, protocol.ContainerDetails, authz.Resource, error) {
	sc, d, res, err := h.ioContainer(ctx, envID, ref)
	if err != nil {
		return nil, d, res, err
	}
	if !sc.c.Can(string(CapContainerLogsRead), res).Allowed {
		return nil, d, res, Forbidden("not permitted to read this container's logs")
	}
	if h.io == nil {
		return nil, d, res, Unavailable(CodeUnavailable, "the log service is not available")
	}
	return sc, d, res, nil
}

func (h *ioAPI) logs(ctx context.Context, in *containerLogsInput) (*containerLogsOutput, error) {
	sc, d, _, err := h.requireLogs(ctx, in.EnvironmentID, in.ContainerID)
	if err != nil {
		return nil, err
	}
	out, err := h.io.Logs(ctx, sc.env.ID, protocol.ContainerLogsInput{ContainerID: d.ID, Tail: in.Tail, Since: in.Since, Until: in.Until,
		Stdout: in.Stdout, Stderr: in.Stderr})
	if err != nil {
		return nil, dockerErr(err)
	}
	body := ContainerLogs{Lines: make([]LogLineDTO, 0, len(out.Lines)), Truncated: out.Truncated}
	for _, l := range out.Lines {
		body.Lines = append(body.Lines, newLogLine(l))
	}
	audit.SetDetail(ctx, "lines", len(body.Lines))
	return &containerLogsOutput{Body: body}, nil
}

// LogsEnd is the data of the final `end` event of a log stream.
type LogsEnd struct {
	Reason string `json:"reason" enum:"container_removed,permissions_changed,agent_offline"`
}

// LogsDropped is the data of a `dropped` event.
type LogsDropped struct {
	Count int `json:"count"`
}

func (h *ioAPI) streamLogs(ctx context.Context, in *streamContainerLogsInput) (*huma.StreamResponse, error) {
	sc, d, res, err := h.requireLogs(ctx, in.EnvironmentID, in.ContainerID)
	if err != nil {
		return nil, err
	}
	since := in.Since
	if in.LastEventID != "" {
		t, err := time.Parse(time.RFC3339Nano, in.LastEventID)
		if err != nil {
			return nil, Invalid("invalid Last-Event-ID", Field("header.Last-Event-ID", "must be a line timestamp (RFC 3339)"))
		}
		since = t
	}
	feed, err := h.io.FollowLogs(ctx, sc.env.ID, protocol.ContainerLogsInput{ContainerID: d.ID, Tail: in.Tail, Since: since,
		Stdout: in.Stdout, Stderr: in.Stderr})
	if err != nil {
		return nil, dockerErr(err)
	}
	p := sc.p
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer feed.Close()
		h.runLogs(hctx, p, res, feed)
	}}, nil
}

// runLogs writes the SSE log stream: `log` events (id = line timestamp),
// `dropped` counts, heartbeats; it ends with `end` (container removed,
// permissions changed, agent offline) or `close` (max age, revoked
// session).
func (h *ioAPI) runLogs(hctx huma.Context, p authz.Principal, res authz.Resource, feed LogFeed) {
	ctx := hctx.Context()
	stream := StartSSE(hctx)
	defer stream.CloseIfRevoked(ctx)
	clk := h.clk
	hb := clk.NewTicker(h.heartbeat)
	defer hb.Stop()
	maxAge := clk.NewTimer(h.maxAge)
	defer maxAge.Stop()
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type next struct {
		ev  LogEvent
		err error
	}
	ch := make(chan next, 1)
	go func() {
		for {
			ev, err := feed.Next(fctx)
			select {
			case ch <- next{ev, err}:
			case <-fctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-maxAge.C():
			_ = stream.Event("close", "", CloseEvent{Reason: "max_age"})
			return
		case <-hb.C():
			if d := authz.For(ctx, h.authz, p).Can(string(CapContainerLogsRead), res); !d.Allowed {
				if d.Ended {
					_ = stream.Event("close", "", CloseEvent{Reason: "session_expired"})
				} else {
					_ = stream.Event("end", "", LogsEnd{Reason: "permissions_changed"})
				}
				return
			}
			if stream.Heartbeat() != nil {
				return
			}
		case n := <-ch:
			switch {
			case n.err != nil:
				if ctx.Err() != nil {
					return
				}
				reason := "agent_offline"
				if errors.Is(n.err, domain.ErrContainerGone) {
					reason = "container_removed"
				}
				_ = stream.Event("end", "", LogsEnd{Reason: reason})
				return
			case n.ev.Dropped > 0:
				if stream.Event("dropped", "", LogsDropped{Count: n.ev.Dropped}) != nil {
					return
				}
			default:
				l := n.ev.Line
				if stream.Event("log", l.At.UTC().Format(time.RFC3339Nano), newLogLine(l)) != nil {
					return
				}
			}
		}
	}
}

// execResource authorizes container.exec on a visible container.
func (h *ioAPI) execContainer(ctx context.Context, envID, ref string) (*scope, protocol.ContainerDetails, authz.Resource, error) {
	sc, d, res, err := h.ioContainer(ctx, envID, ref)
	if err != nil {
		return nil, d, res, err
	}
	if _, err := AuthorizeExec(ctx, h.authz, res); err != nil {
		return nil, d, res, err
	}
	if h.io == nil {
		return nil, d, res, Unavailable(CodeUnavailable, "the terminal service is not available")
	}
	return sc, d, res, nil
}

func execErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrExecSessionNotFound):
		return NotFound("exec session not found (unknown, expired, ended or not yours)")
	case errors.Is(err, domain.ErrExecSessionLimit):
		return RateLimited("too many open terminals: close another one (4 per user, 8 per container)")
	}
	return dockerErr(err)
}

func (h *ioAPI) createExec(ctx context.Context, in *createExecInput) (*createExecOutput, error) {
	sc, d, res, err := h.execContainer(ctx, in.EnvironmentID, in.ContainerID)
	if err != nil {
		return nil, err
	}
	b := in.Body
	cmd, shell := b.Command, b.Shell
	switch {
	case len(cmd) > 0 && shell != "":
		return nil, Invalid("give either a command or a shell", Field("body.shell", "cannot be combined with command"))
	case len(cmd) == 0 && shell == "":
		shell = protocol.ShellAuto
	}
	if _, ok := protocol.ShellCandidates(shell); shell != "" && !ok {
		return nil, Invalid("unknown shell", Field("body.shell", "one of auto, bash, sh, zsh"))
	}
	for i, a := range cmd {
		if a == "" && i == 0 || strings.ContainsRune(a, 0) || len(a) > 4096 {
			return nil, Invalid("invalid command", Field("body.command", "non-empty arguments without NUL, at most 4096 bytes each"))
		}
	}
	tty := b.Tty == nil || *b.Tty
	cols, rows := b.Cols, b.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	s, err := h.io.CreateExec(ctx, ExecRequest{Principal: sc.p, EnvironmentID: sc.env.ID, Container: res,
		Input: protocol.ExecCreateInput{ContainerID: d.ID, Cmd: cmd, Shell: shell, Tty: tty, Cols: cols, Rows: rows,
			WorkingDir: b.WorkingDir, User: b.User}})
	if err != nil {
		return nil, execErr(err)
	}
	audit.SetDetail(ctx, "sessionId", s.ID)
	audit.SetDetail(ctx, "tty", tty)
	command := s.Command
	if command == nil {
		command = []string{}
	}
	return &createExecOutput{Body: ExecSessionDTO{ID: s.ID, Subprotocol: ExecSubprotocol, Ticket: s.Ticket, ExpiresAt: s.ExpiresAt,
		Command: command, StreamURL: BasePath + "/environments/" + sc.env.ID + "/containers/" + in.ContainerID + "/exec-sessions/" + s.ID + "/stream"}}, nil
}

// ticketFrom extracts the attach ticket from the offered subprotocols.
func ticketFrom(protocols string) string {
	for _, p := range strings.Split(protocols, ",") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(p), ExecTicketPrefix); ok {
			return t
		}
	}
	return ""
}

func (h *ioAPI) attachment(sc *scope, d protocol.ContainerDetails, res authz.Resource, sid, ticket string) ExecAttach {
	p := sc.p
	return ExecAttach{Principal: p, EnvironmentID: sc.env.ID, ContainerID: d.ID, SessionID: sid, Ticket: ticket,
		Allowed: func(ctx context.Context) authz.Decision { return authz.CanExec(authz.For(ctx, h.authz, p), res) }}
}

func (h *ioAPI) streamExec(ctx context.Context, in *streamExecInput) (*huma.StreamResponse, error) {
	sc, d, res, err := h.execContainer(ctx, in.EnvironmentID, in.ContainerID)
	if err != nil {
		return nil, err
	}
	a := h.attachment(sc, d, res, in.SessionID, ticketFrom(in.Protocols))
	if err := h.io.CheckAttach(ctx, a); err != nil {
		return nil, execErr(err)
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humago.Unwrap(hctx)
		h.io.Attach(hctx.Context(), w, r, a)
	}}, nil
}

func (h *ioAPI) deleteExec(ctx context.Context, in *ExecSessionPath) (*struct{}, error) {
	sc, d, res, err := h.execContainer(ctx, in.EnvironmentID, in.ContainerID)
	if err != nil {
		return nil, err
	}
	if err := h.io.DeleteExec(ctx, h.attachment(sc, d, res, in.SessionID, "")); err != nil {
		return nil, execErr(err)
	}
	audit.SetDetail(ctx, "sessionId", in.SessionID)
	return nil, nil
}

func registerContainerIO(a huma.API, deps Deps) {
	h := &ioAPI{dockerAPI: newDockerAPI(deps), io: deps.ContainerIO, heartbeat: deps.SSEHeartbeat, maxAge: deps.StreamMaxAge}
	h.clk = deps.clock()
	if h.heartbeat <= 0 {
		h.heartbeat = DefaultSSEHeartbeat
	}
	if h.maxAge <= 0 {
		h.maxAge = DefaultStreamMaxAge
	}
	base := BasePath + "/environments/{environmentId}/containers/{containerId}"
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-container-logs", Method: http.MethodGet, Path: base + "/logs",
			Summary: "Get a container's logs",
			Description: "A bounded tail of the container's stdout/stderr (default 500 lines, at most 5000 lines or 600 KiB, oldest dropped " +
				"first), read through the environment's agent. Needs container.logs.read: metrics, restart and other container grants do not " +
				"open logs. Logs can contain secrets; the manager never stores them.",
			Tags: []string{tagContainers}, Errors: errs,
		},
		Capability: CapContainerLogsRead, Scope: ScopeResource,
	}, h.logs)

	logSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(LogLineDTO{}), true, "")
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stream-container-logs", Method: http.MethodGet, Path: base + "/logs/stream",
			Summary: "Follow a container's logs (SSE)",
			Description: "Server-sent events: `log` (id = the line's RFC 3339 timestamp, data LogLine), `dropped {count}` when the client " +
				"could not keep up, `end {reason}` (container_removed, permissions_changed, agent_offline) and `close` (max_age after 1 h, " +
				"session_expired, permissions_changed). A stopped container keeps the stream open. Reconnect with Last-Event-ID to resume " +
				"(lines with the same timestamp may repeat). Protocol: docs/internal/api/streams.md.",
			Tags: []string{tagContainers}, Errors: errs,
			Responses: map[string]*huma.Response{
				"200": {Description: "Event stream", Content: map[string]*huma.MediaType{"text/event-stream": {Schema: logSchema}}},
			},
		},
		Capability: CapContainerLogsRead, Scope: ScopeResource,
	}, h.streamLogs)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-container-exec-session", Method: http.MethodPost, Path: base + "/exec-sessions",
			Summary: "Open a terminal in a container",
			Description: "Creates an exec session running command, or a shell the agent finds in the container (default: shell auto, " +
				"Bash if present, else sh), inside the container (never on the host) and a one-use attach ticket; " +
				"attach within 60 s with GET …/exec-sessions/{sessionId}/stream (WebSocket, subprotocols docker-manager.exec.v1 and " +
				"docker-manager.ticket.<ticket>). The response's command is the argv actually started. Needs container.exec (API tokens only " +
				"with container.exec in their own grants). 409 when the container is not running, 422 command_not_found when the container " +
				"has none of the shell's paths, 429 beyond 4 terminals per user or 8 per container.",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusCreated,
			Errors: append(errs, http.StatusConflict, http.StatusTooManyRequests),
		},
		Capability: CapContainerExec, Scope: ScopeResource,
	}, h.createExec)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stream-container-exec-session", Method: http.MethodGet, Path: base + "/exec-sessions/{sessionId}/stream",
			Summary: "Attach a terminal (WebSocket)",
			Description: "Upgrades to a WebSocket (subprotocol docker-manager.exec.v1; the ticket from the create call as subprotocol " +
				"docker-manager.ticket.<ticket>). Binary frames: 0+stdin to the process, 1+stdout / 2+stderr from it; text frames: " +
				"{\"type\":\"resize\",\"cols\",\"rows\"} from the client, {\"type\":\"exit\",\"code\"} and {\"type\":\"error\",...} from the " +
				"server. Close codes and limits: docs/internal/api/streams.md.",
			Tags: []string{tagContainers}, Errors: errs,
			Responses: map[string]*huma.Response{"101": {Description: "Switching to the WebSocket protocol"}},
		},
		Capability: CapContainerExec, Scope: ScopeResource,
	}, h.streamExec)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-container-exec-session", Method: http.MethodDelete, Path: base + "/exec-sessions/{sessionId}",
			Summary:     "Close a terminal",
			Description: "Detaches the session and closes the process's stdin. A process that ignores end of input keeps running in the container until it exits (the Engine cannot kill exec processes).",
			Tags:        []string{tagContainers}, DefaultStatus: http.StatusNoContent, Errors: errs,
		},
		Capability: CapContainerExec, Scope: ScopeResource,
	}, h.deleteExec)
}
