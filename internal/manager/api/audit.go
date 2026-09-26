package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
)

const tagAudit = "Audit"

// Audit capabilities (#30, #17): instance-scoped, owner-only by default,
// high-risk and all-or-nothing.
const (
	CapAuditRead   Capability = audit.CapabilityRead
	CapAuditExport Capability = audit.CapabilityExport
)

// AuditService is the audit trail as seen by the API (implemented by
// *audit.Log).
type AuditService interface {
	audit.Recorder
	Records(ctx context.Context, f domain.AuditFilter) ([]domain.AuditRecord, error)
}

// AuditActor identifies who acted.
type AuditActor struct {
	Kind    string `json:"kind" enum:"user,api_token,service,agent,anonymous" doc:"user; api_token (userId is the token's owner); service (the manager's own scheduled or maintenance work); agent; anonymous (no authenticated principal, e.g. a failed sign-in)."`
	UserID  string `json:"userId,omitempty"`
	TokenID string `json:"tokenId,omitempty"`
	AgentID string `json:"agentId,omitempty"`
}

// AuditTarget is a resource an audited action touched.
type AuditTarget struct {
	Type          string `json:"type" example:"stack" doc:"Resource type (stack, container, volume, image, network, job, user, group, api_token, agent, environment, ...)."`
	ID            string `json:"id"`
	EnvironmentID string `json:"environmentId,omitempty" doc:"Environment (host) of environment-scoped resources."`
}

// AuditEvent is one audit record (#30).
type AuditEvent struct {
	ID            string         `json:"id"`
	Seq           int64          `json:"seq" doc:"Position in the hash chain; newer records have higher values."`
	At            time.Time      `json:"at"`
	Category      string         `json:"category" enum:"identity,authorization,credentials,operations,system"`
	Action        string         `json:"action" example:"stack.deploy" doc:"Capability key of the operation (#17) or a lifecycle key such as job.finished or audit.purge."`
	OperationID   string         `json:"operationId,omitempty" doc:"API operation that was called, if the record stems from a request."`
	Actor         AuditActor     `json:"actor"`
	ClientIP      string         `json:"clientIp,omitempty" doc:"Client address as resolved through the trusted reverse proxies."`
	UserAgent     string         `json:"userAgent,omitempty"`
	EnvironmentID string         `json:"environmentId,omitempty"`
	Targets       []AuditTarget  `json:"targets"`
	Outcome       string         `json:"outcome" enum:"success,partial,failure,denied,error"`
	ErrorClass    string         `json:"errorClass,omitempty" doc:"Stable error code or job error class; messages are never recorded."`
	JobID         string         `json:"jobId,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	Details       map[string]any `json:"details" doc:"Redacted, action-specific details (e.g. rule diffs). Never contains secret values, tokens, file contents or .env values."`
	PrevHash      string         `json:"prevHash" doc:"Hash of the preceding record (tamper evidence)."`
	Hash          string         `json:"hash" doc:"SHA-256 over prevHash and the canonical record."`
}

// NewAuditEvent converts a stored record to its transport form.
func NewAuditEvent(r domain.AuditRecord) AuditEvent {
	out := AuditEvent{
		ID: r.ID, Seq: r.Seq, At: r.At, Category: string(r.Category), Action: r.Action, OperationID: r.OperationID,
		Actor:    AuditActor{Kind: string(r.Actor.Kind), UserID: r.Actor.UserID, TokenID: r.Actor.TokenID, AgentID: r.Actor.AgentID},
		ClientIP: r.ClientIP, UserAgent: r.UserAgent, EnvironmentID: r.EnvironmentID, Targets: []AuditTarget{},
		Outcome: string(r.Outcome), ErrorClass: r.ErrorClass, JobID: r.JobID, RequestID: r.RequestID,
		Details: map[string]any{}, PrevHash: r.PrevHash, Hash: r.Hash,
	}
	for _, t := range r.Targets {
		out.Targets = append(out.Targets, AuditTarget(t))
	}
	if len(r.Details) > 0 {
		_ = json.Unmarshal(r.Details, &out.Details)
	}
	return out
}

// AuditFilterParams are the filters shared by the list and the export.
type AuditFilterParams struct {
	ActorKind     []string `query:"actorKind,explode" enum:"user,api_token,service,agent,anonymous" doc:"Only records by these actor kinds."`
	ActorID       string   `query:"actorId" maxLength:"128" doc:"Only records whose actor is this user, API token or agent ID (a user ID also matches that user's API tokens)."`
	Action        []string `query:"action,explode" maxLength:"128" pattern:"^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$" doc:"Only these action keys (e.g. stack.deploy, job.finished)."`
	Category      []string `query:"category,explode" enum:"identity,authorization,credentials,operations,system" doc:"Only these categories."`
	Outcome       []string `query:"outcome,explode" enum:"success,partial,failure,denied,error" doc:"Only these outcomes."`
	EnvironmentID string   `query:"environmentId" maxLength:"128" doc:"Only records in (or targeting) this environment (host)."`
	Resource      string   `query:"resource" maxLength:"300" doc:"Only records touching this resource, as type:id (e.g. stack:0190a6e0-..., job:0190...)."`
	JobID         string   `query:"jobId" maxLength:"64" doc:"Only records of this job."`
	Since         string   `query:"since" maxLength:"40" doc:"Only records at or after this RFC 3339 time."`
	Until         string   `query:"until" maxLength:"40" doc:"Only records before this RFC 3339 time."`
}

func (p *AuditFilterParams) filter() (domain.AuditFilter, error) {
	f := domain.AuditFilter{ActorID: p.ActorID, Actions: p.Action, EnvironmentID: p.EnvironmentID, JobID: p.JobID}
	for _, k := range p.ActorKind {
		f.ActorKinds = append(f.ActorKinds, domain.AuditActorKind(k))
	}
	for _, c := range p.Category {
		f.Categories = append(f.Categories, domain.AuditCategory(c))
	}
	for _, o := range p.Outcome {
		f.Outcomes = append(f.Outcomes, domain.AuditOutcome(o))
	}
	var details []ErrorDetail
	if p.Resource != "" {
		typ, id, ok := strings.Cut(p.Resource, ":")
		if !ok || typ == "" || id == "" {
			details = append(details, Field("query.resource", "want type:id"))
		} else {
			f.Resource = &domain.AuditTarget{Type: typ, ID: id}
		}
	}
	parse := func(field, v string) time.Time {
		if v == "" {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			details = append(details, Field("query."+field, "want an RFC 3339 time such as 2026-09-24T12:00:00Z"))
		}
		return t
	}
	f.Since, f.Until = parse("since", p.Since), parse("until", p.Until)
	if len(details) > 0 {
		return f, Invalid("invalid audit filter", details...)
	}
	return f, nil
}

func (p *AuditFilterParams) fingerprint() string {
	return QueryFingerprint(strings.Join(p.ActorKind, ","), p.ActorID, strings.Join(p.Action, ","),
		strings.Join(p.Category, ","), strings.Join(p.Outcome, ","), p.EnvironmentID, p.Resource, p.JobID, p.Since, p.Until)
}

// describe lists the filters in effect (audit details of an export).
func (p *AuditFilterParams) describe() map[string]any {
	out := map[string]any{}
	add := func(k string, v any, set bool) {
		if set {
			out[k] = v
		}
	}
	add("actorKind", p.ActorKind, len(p.ActorKind) > 0)
	add("actorId", p.ActorID, p.ActorID != "")
	add("action", p.Action, len(p.Action) > 0)
	add("category", p.Category, len(p.Category) > 0)
	add("outcome", p.Outcome, len(p.Outcome) > 0)
	add("environmentId", p.EnvironmentID, p.EnvironmentID != "")
	add("resource", p.Resource, p.Resource != "")
	add("jobId", p.JobID, p.JobID != "")
	add("since", p.Since, p.Since != "")
	add("until", p.Until, p.Until != "")
	return out
}

type listAuditInput struct {
	PageParams
	AuditFilterParams
}

type listAuditOutput struct{ Body Page[AuditEvent] }

type exportAuditInput struct {
	Format string `query:"format" enum:"ndjson,csv" default:"ndjson" doc:"ndjson: one AuditEvent per line (details exactly as hashed); csv: one row per record, formula-injection safe."`
	AuditFilterParams
}

type auditCursor struct {
	Before int64 `json:"b"`
}

type auditAPI struct {
	svc   AuditService
	authz authz.Authorizer
	deps  Deps
}

// authorize requires a principal and the (all-or-nothing, instance-wide)
// capability. The audit log's existence is no secret, so a missing grant
// is 403.
func (h *auditAPI) authorize(ctx context.Context, c Capability) error {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return Unauthenticated("authentication required")
	}
	if !h.authz.Can(ctx, p, string(c), authz.Resource{Type: "instance"}).Allowed {
		return Forbidden("not permitted to " + strings.TrimPrefix(string(c), "audit.") + " the audit log")
	}
	if h.svc == nil {
		return Unavailable(CodeUnavailable, "the audit log is not available")
	}
	return nil
}

func (h *auditAPI) list(ctx context.Context, in *listAuditInput) (*listAuditOutput, error) {
	if err := h.authorize(ctx, CapAuditRead); err != nil {
		return nil, err
	}
	f, err := in.filter()
	if err != nil {
		return nil, err
	}
	fp := in.fingerprint()
	if in.Cursor != "" {
		var c auditCursor
		if err := DecodeCursorFor(in.Cursor, fp, &c); err != nil {
			return nil, err
		}
		f.BeforeSeq = c.Before
	}
	limit := in.PageLimit()
	f.Limit = limit + 1
	recs, err := h.svc.Records(ctx, f)
	if err != nil {
		return nil, Internal(err)
	}
	cursor := ""
	if len(recs) > limit {
		recs = recs[:limit]
		if cursor, err = CursorFor(fp, auditCursor{Before: recs[limit-1].Seq}); err != nil {
			return nil, Internal(err)
		}
	}
	items := make([]AuditEvent, 0, len(recs))
	for _, r := range recs {
		items = append(items, NewAuditEvent(r))
	}
	return &listAuditOutput{Body: NewPage(items, cursor, nil)}, nil
}

// exportBatch is the number of records read per query while exporting.
const exportBatch = 500

// auditCSVHeader is the CSV column order.
var auditCSVHeader = []string{"seq", "id", "at", "category", "action", "operationId", "actorKind", "actorUserId",
	"actorTokenId", "actorAgentId", "clientIp", "userAgent", "environmentId", "targets", "outcome", "errorClass",
	"jobId", "requestId", "details", "prevHash", "hash"}

func auditCSVRow(r domain.AuditRecord) []string {
	row := []string{strconv.FormatInt(r.Seq, 10), r.ID, r.At.UTC().Format(time.RFC3339Nano), string(r.Category), r.Action,
		r.OperationID, string(r.Actor.Kind), r.Actor.UserID, r.Actor.TokenID, r.Actor.AgentID, r.ClientIP, r.UserAgent,
		r.EnvironmentID, targetsJSON(r.Targets), string(r.Outcome), r.ErrorClass, r.JobID, r.RequestID,
		string(r.Details), r.PrevHash, r.Hash}
	for i := range row {
		row[i] = audit.SafeCSVCell(row[i])
	}
	return row
}

func targetsJSON(ts []domain.AuditTarget) string {
	out := make([]AuditTarget, 0, len(ts))
	for _, t := range ts {
		out = append(out, AuditTarget(t))
	}
	b, _ := json.Marshal(out) // strings only: cannot fail
	return string(b)
}

// auditLine is the NDJSON form: the AuditEvent with details exactly as
// stored and hashed.
type auditLine struct {
	AuditEvent
	Details json.RawMessage `json:"details"`
}

func (h *auditAPI) export(ctx context.Context, in *exportAuditInput) (*huma.StreamResponse, error) {
	if err := h.authorize(ctx, CapAuditExport); err != nil {
		return nil, err
	}
	f, err := in.filter()
	if err != nil {
		return nil, err
	}
	format := in.Format
	if format == "" {
		format = "ndjson"
	}
	audit.SetDetail(ctx, "format", format)
	audit.SetDetail(ctx, "filters", in.describe())
	// Export up to the newest record at the start (the export's own record
	// is appended after it).
	head, err := h.svc.Records(ctx, domain.AuditFilter{Limit: 1})
	if err != nil {
		return nil, Internal(err)
	}
	var headSeq int64
	if len(head) == 1 {
		headSeq = head[0].Seq
	}
	name := "docker-manager-audit-" + h.deps.clock().Now().UTC().Format("20060102T150405Z") + "." + format
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		ctx := hctx.Context()
		ct := "application/x-ndjson"
		if format == "csv" {
			ct = "text/csv; charset=utf-8"
		}
		hctx.SetHeader("Content-Type", ct)
		hctx.SetHeader("Content-Disposition", `attachment; filename="`+name+`"`)
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetStatus(http.StatusOK)
		w := hctx.BodyWriter()
		flush := func() {
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		var cw *csv.Writer
		enc := json.NewEncoder(w)
		if format == "csv" {
			cw = csv.NewWriter(w)
			_ = cw.Write(auditCSVHeader)
		}
		fail := func(err error) {
			// Abort the response so the client sees a broken download
			// instead of a silently truncated one.
			logging.FromContext(ctx).Error("audit export failed", "error", err)
			audit.SetErrorClass(ctx, CodeInternal)
			panic(http.ErrAbortHandler)
		}
		var n int64
		q := f
		q.Ascending, q.Limit, q.BeforeSeq = true, exportBatch, headSeq+1
		for headSeq > 0 {
			recs, err := h.svc.Records(ctx, q)
			if err != nil {
				fail(err)
			}
			for _, r := range recs {
				if cw != nil {
					err = cw.Write(auditCSVRow(r))
				} else {
					err = enc.Encode(auditLine{AuditEvent: NewAuditEvent(r), Details: json.RawMessage(r.Details)})
				}
				if err != nil {
					fail(err)
				}
				n++
			}
			if cw != nil {
				cw.Flush()
				if err := cw.Error(); err != nil {
					fail(err)
				}
			}
			flush()
			audit.SetDetail(ctx, "exportedRecords", n)
			if len(recs) < exportBatch {
				break
			}
			q.AfterSeq = recs[len(recs)-1].Seq
		}
		if cw != nil {
			cw.Flush()
		}
		audit.SetDetail(ctx, "exportedRecords", n)
	}}, nil
}

func registerAudit(a huma.API, deps Deps) {
	h := &auditAPI{svc: deps.Audit, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusServiceUnavailable}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-audit-events", Method: http.MethodGet, Path: BasePath + "/audit",
			Summary: "List audit records",
			Description: "Audit records, newest first, with cursor pagination and filters (repeat a parameter to OR its values). " +
				"Requires audit.read, an instance-wide, all-or-nothing capability (records can reveal activity on resources the " +
				"reader cannot otherwise see); records are therefore not filtered per item. total is omitted.",
			Tags: []string{tagAudit}, Errors: errs,
		},
		Capability: CapAuditRead, Scope: ScopeInstance,
	}, h.list)

	eventSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(AuditEvent{}), true, "")
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "export-audit-events", Method: http.MethodGet, Path: BasePath + "/audit/exports",
			Summary: "Export audit records",
			Description: "Streams every record matching the filters, oldest first, as NDJSON (one AuditEvent per line, details " +
				"exactly as hashed) or CSV (formula-injection safe: cells starting with =, +, -, @, tab or CR are prefixed " +
				"with a quote). The export covers the records present when it starts and is itself audited. There is no " +
				"resume; repeat with a narrower time range. Requires audit.export.",
			Tags: []string{tagAudit}, Errors: errs,
			Responses: map[string]*huma.Response{
				"200": {
					Description: "Audit records",
					Content: map[string]*huma.MediaType{
						"application/x-ndjson": {Schema: eventSchema},
						"text/csv":             {Schema: &huma.Schema{Type: "string", Description: "Header row, then one row per record."}},
					},
				},
			},
		},
		Capability: CapAuditExport, Scope: ScopeInstance,
		Audit: AuditAlways,
	}, h.export)
}
