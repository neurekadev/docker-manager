package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/events"
)

// Audit by construction (#30). Register installs auditMiddleware and
// auditHandler on every audited operation (every non-GET operation, and
// GET operations declaring AuditAlways). After the operation answered, one
// record is appended with:
//
//   - action: Operation.AuditActionKey() (or the concrete key a selector
//     handler chose with audit.SetAction) and the operation ID;
//   - actor: the request principal (user or API token + owner), an actor
//     the handler set (sign-in), or anonymous;
//   - client IP (requestinfo, trusted proxies), user agent, request ID;
//   - targets from the path parameters ({stackId} -> stack, the
//     environment from {environmentId}) plus those the handler added;
//   - outcome from the final status (2xx/3xx success, 401/403 denied,
//     other 4xx failure, 5xx error), the error class from the returned
//     api.Error code (or the status' default code), the job ID of 202
//     responses, whether an Idempotency-Key replay answered;
//   - details the handler set (redacted by the audit package).
//
// Request bodies, query strings, headers other than the user agent and
// response bodies are never recorded. Unauthenticated requests to
// non-public operations that are answered 401 are not recorded: the
// authentication layer refused them before any effect, and recording them
// would let anyone flood the trail (they remain in the access log). Public
// operations (sign-in, setup, invitation redemption) are recorded with an
// anonymous actor.

var pathParamRE = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)\}`)

type pathTarget struct {
	param string
	typ   string
}

// pathTargets maps each path parameter to a target type named after the
// collection segment in front of it (/build-definitions/{definitionId} ->
// build_definition, /me/api-tokens/{tokenId} -> api_token).
func pathTargets(path string) []pathTarget {
	segs := strings.Split(path, "/")
	var out []pathTarget
	for i, s := range segs {
		m := pathParamRE.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		typ := ""
		if i > 0 && !strings.Contains(segs[i-1], "{") {
			typ = singular(strings.ReplaceAll(segs[i-1], "-", "_"))
		}
		if typ == "" {
			typ = camelToSnake(strings.TrimSuffix(m[1], "Id"))
		}
		out = append(out, pathTarget{param: m[1], typ: typ})
	}
	return out
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "sses"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "s"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// AuditOutcomeFor maps a final HTTP status to an audit outcome.
func AuditOutcomeFor(status int) domain.AuditOutcome {
	switch {
	case status < 400:
		return domain.AuditSuccess
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return domain.AuditDenied
	case status < 500:
		return domain.AuditFailure
	}
	return domain.AuditError
}

// auditCapture records the final status and whether a stored response was
// replayed.
type auditCapture struct {
	humaCtx
	status   int
	replayed bool
}

func (c *auditCapture) SetStatus(code int) {
	c.status = code
	c.humaCtx.SetStatus(code)
}

func (c *auditCapture) SetHeader(name, value string) {
	if strings.EqualFold(name, HeaderIdempotentReplayed) {
		c.replayed = true
	}
	c.humaCtx.SetHeader(name, value)
}

func auditMiddleware(op Operation) func(huma.Context, func(huma.Context)) {
	action := op.AuditActionKey()
	var allowed []string
	for _, v := range op.CapabilityValues {
		allowed = append(allowed, string(v))
	}
	targets := pathTargets(op.Path)
	public := op.Capability == CapabilityPublic
	return func(hctx huma.Context, next func(huma.Context)) {
		deps, _ := depsFrom(hctx.Context())
		if deps.Audit == nil {
			next(hctx)
			return
		}
		draft := audit.NewDraft(action, allowed)
		ctx := audit.WithDraft(hctx.Context(), draft) // the recorder is installed by withDeps
		c := &auditCapture{humaCtx: huma.WithContext(hctx, ctx)}
		finished := false
		defer func() {
			if finished {
				return
			}
			v := recover()
			if v == nil {
				return // runtime.Goexit: nothing was answered
			}
			c.status = http.StatusInternalServerError
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				audit.SetErrorClass(ctx, "aborted")
			} else {
				audit.SetErrorClass(ctx, CodeInternal)
			}
			recordRequest(ctx, deps.Audit, op, public, targets, draft, c)
			panic(v)
		}()
		next(c)
		finished = true
		ev, ok := recordRequest(ctx, deps.Audit, op, public, targets, draft, c)
		if ok && !public && op.Method != http.MethodGet && ev.Outcome == domain.AuditSuccess && !c.replayed {
			publishChanges(deps.Events, op, ev, c.status)
		}
	}
}

// publishChanges announces a successful mutation's targets on the event
// bus (events.ResourceChanged), so live streams (#23) invalidate them:
// policies, backups, registries, settings, groups, users, tokens, ...
// Jobs and file operations have their own, more precise sources (the job
// engine, the agent's file invalidations) and are left out.
func publishChanges(bus *events.Bus, op Operation, ev domain.AuditEvent, status int) {
	if bus == nil || strings.Contains(ev.Action, ".files.") {
		return
	}
	kind := "update"
	switch {
	case op.Method == http.MethodDelete:
		kind = "delete"
	case status == http.StatusCreated:
		kind = "create"
	}
	for _, t := range ev.Targets {
		if t.Type == "" || t.ID == "" || t.Type == events.ResourceJob {
			continue
		}
		env := t.EnvironmentID
		if env == "" && t.Type != "environment" {
			env = ev.EnvironmentID
		}
		bus.Publish(events.Event{Type: events.ResourceChanged, ResourceType: t.Type, ResourceID: t.ID, EnvironmentID: env,
			Attributes: map[string]string{"action": ev.Action, "op": kind}})
	}
}

func recordRequest(ctx context.Context, rec audit.Recorder, op Operation, public bool, targets []pathTarget, draft *audit.Draft, c *auditCapture) (domain.AuditEvent, bool) {
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	action, actor, ev := draft.Snapshot()
	ev.Action, ev.OperationID = action, op.OperationID
	switch p, ok := authz.PrincipalFrom(ctx); {
	case actor != nil:
		ev.Actor = *actor
	case ok:
		ev.Actor = audit.ActorFor(p)
	case !public && status == http.StatusUnauthorized:
		return ev, false // refused by authentication before any effect; see above
	default:
		ev.Actor = domain.AuditActor{Kind: domain.AuditActorAnonymous}
	}
	env := c.Param("environmentId")
	if ev.EnvironmentID == "" {
		ev.EnvironmentID = env
	}
	var fromPath []domain.AuditTarget
	for _, t := range targets {
		v := c.Param(t.param)
		if v == "" || (t.param == "environmentId" && len(targets) > 1) {
			continue
		}
		tenv := env
		if t.param == "environmentId" {
			tenv = ""
		}
		fromPath = append(fromPath, domain.AuditTarget{Type: t.typ, ID: v, EnvironmentID: tenv})
	}
	ev.Targets = append(fromPath, ev.Targets...)
	ev.UserAgent = c.Header("User-Agent")
	if ev.Outcome == "" {
		ev.Outcome = AuditOutcomeFor(status)
	}
	if ev.ErrorClass == "" && status >= 400 {
		ev.ErrorClass = CodeForStatus(status)
	}
	if ev.Details == nil {
		ev.Details = map[string]any{}
	}
	ev.Details["status"] = status
	if c.replayed {
		ev.Details["idempotentReplay"] = true
	}
	if err := rec.Record(context.WithoutCancel(ctx), ev); err != nil {
		logging.FromContext(ctx).Error("audit record failed", "operation", op.OperationID, "action", action, "error", err)
	}
	return ev, true
}

// auditHandler captures the handler's error class and the job a 202
// response started.
func auditHandler[I, O any](handler func(context.Context, *I) (*O, error)) func(context.Context, *I) (*O, error) {
	return func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		d, ok := audit.DraftFrom(ctx)
		if !ok {
			return out, err
		}
		if err != nil {
			if !d.HasErrorClass() {
				audit.SetErrorClass(ctx, errorClassOf(err))
			}
			return out, err
		}
		if ja, isJob := any(out).(*JobAccepted); isJob && ja != nil {
			audit.SetJob(ctx, ja.Body.ID)
			audit.AddTarget(ctx, domain.AuditTarget{Type: "job", ID: ja.Body.ID})
		}
		return out, err
	}
}

func errorClassOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return CodeForStatus(se.GetStatus())
	}
	return CodeInternal
}
