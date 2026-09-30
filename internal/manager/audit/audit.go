// Package audit is Docker Manager's append-only, hash-chained audit trail (#30):
// who did what to which resource, from where, and with what result, for
// every security-relevant and mutating action.
//
// Recording:
//   - HTTP: api.Register audits every non-GET operation by construction
//     (and GET operations that opt in). Handlers enrich the record through
//     the request context: AddTarget, SetDetail, SetDiff, SetAction (for
//     selector capabilities), SetActor/SetPrincipal (sign-in), SetJob.
//   - Jobs: the job engine records job.queued / job.started /
//     job.cancel_requested / job.finished for every kind inside its own
//     transactions (RecordTx).
//   - Everything else (sign-in failures outside a route, agent enrollment
//     on /agent/v1, the owner-recovery CLI, scheduled work, retention)
//     calls Record(ctx, domain.AuditEvent) or (*Log).Record.
//
// Every record passes the redaction layer (redact.go): secret values,
// tokens, file contents and .env values are never stored, whatever the
// caller passes. Records are chained: hash = SHA-256 over the previous
// record's hash and the canonical record (chain.go). There is no update or
// delete API; the only deletion is the retention purge (retention.go),
// which is itself audited and moves the chain anchor so the chain stays
// verifiable from the new oldest record (VerifyChain).
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Defaults.
const (
	// DefaultRetention keeps records for 365 days.
	DefaultRetention = 365 * 24 * time.Hour
	// DefaultMaxBytes caps the canonical size of retained records (1 GiB).
	DefaultMaxBytes int64 = 1 << 30
	// DefaultPurgeInterval runs the retention purge hourly.
	DefaultPurgeInterval = time.Hour
	// DefaultPurgeBatch bounds the records deleted per purge transaction.
	DefaultPurgeBatch = 5000
)

// Recorder records audit events. *Log implements it; tests use fakes.
type Recorder interface {
	Record(ctx context.Context, ev domain.AuditEvent) error
}

// TxRecorder records events inside the caller's transaction (the job
// engine uses it so a job state change and its audit record commit
// together).
type TxRecorder interface {
	RecordTx(ctx context.Context, db bun.IDB, ev domain.AuditEvent) error
}

// Options configures a Log.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	// Mirror, when set, receives every stored record as a structured log
	// line (DOCKER_MANAGER_AUDIT_LOG_MIRROR). Off (nil) by default.
	Mirror *slog.Logger
	// Retention deletes records older than this (DefaultRetention).
	Retention time.Duration
	// MaxBytes caps the retained records' canonical size (DefaultMaxBytes);
	// the purge trims the oldest records to 90% of it.
	MaxBytes int64
	// PurgeInterval is how often Run purges (DefaultPurgeInterval).
	PurgeInterval time.Duration
	// PurgeBatch bounds the records deleted per transaction.
	PurgeBatch int
}

// Log is the audit trail. Create it with New.
type Log struct {
	db     *bun.DB
	clock  clock.Clock
	logger *slog.Logger
	mirror *slog.Logger
	opts   Options
}

// New returns a Log.
func New(o Options) (*Log, error) {
	if o.DB == nil {
		return nil, errors.New("audit: DB is required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.PurgeInterval <= 0 {
		o.PurgeInterval = DefaultPurgeInterval
	}
	if o.PurgeBatch <= 0 {
		o.PurgeBatch = DefaultPurgeBatch
	}
	return &Log{db: o.DB, clock: o.Clock, logger: o.Logger, mirror: o.Mirror, opts: o}, nil
}

// ErrInvalidEvent reports a malformed event (a programming error: unknown
// category/outcome/actor kind or a malformed action key).
var ErrInvalidEvent = errors.New("audit: invalid event")

// Record appends ev in its own transaction.
func (l *Log) Record(ctx context.Context, ev domain.AuditEvent) error {
	return l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return l.RecordTx(ctx, tx, ev)
	})
}

// RecordTx appends ev inside the caller's transaction. The manager's
// database has a single connection: never call Record (or anything else
// using the root DB) while holding a transaction; use RecordTx with it.
func (l *Log) RecordTx(ctx context.Context, db bun.IDB, ev domain.AuditEvent) error {
	rec, err := l.normalize(ctx, ev)
	if err != nil {
		return err
	}
	chain, err := store.GetAuditChain(ctx, db)
	if err != nil {
		return err
	}
	rec.Seq, rec.PrevHash = chain.HeadSeq+1, chain.HeadHash
	canon := Canonical(&rec)
	rec.Hash = ChainHash(rec.PrevHash, canon)
	if err := store.AppendAuditRecord(ctx, db, &rec, int64(len(canon))); err != nil {
		return err
	}
	l.mirrorRecord(ctx, &rec)
	return nil
}

// Records lists stored records (API list/export).
func (l *Log) Records(ctx context.Context, f domain.AuditFilter) ([]domain.AuditRecord, error) {
	return store.ListAuditRecords(ctx, l.db, f)
}

// normalize fills defaults from ctx, validates and redacts ev.
func (l *Log) normalize(ctx context.Context, ev domain.AuditEvent) (domain.AuditRecord, error) {
	at := ev.At
	if at.IsZero() {
		at = l.clock.Now()
	}
	if !actionRE.MatchString(ev.Action) {
		return domain.AuditRecord{}, fmt.Errorf("%w: action %q must be a dotted key like stack.deploy", ErrInvalidEvent, ev.Action)
	}
	cat := ev.Category
	if cat == "" {
		cat = CategoryFor(ev.Action, ev.OperationID)
	}
	if !cat.Valid() {
		return domain.AuditRecord{}, fmt.Errorf("%w: category %q", ErrInvalidEvent, cat)
	}
	outcome := ev.Outcome
	if outcome == "" {
		outcome = domain.AuditSuccess
	}
	if !outcome.Valid() {
		return domain.AuditRecord{}, fmt.Errorf("%w: outcome %q", ErrInvalidEvent, outcome)
	}
	actor := ev.Actor
	if actor.Kind == "" {
		actor = ActorFromContext(ctx)
	}
	actor, err := cleanActor(actor)
	if err != nil {
		return domain.AuditRecord{}, err
	}
	clientIP := ev.ClientIP
	if clientIP == "" {
		if ip := requestinfo.ClientIP(ctx); ip.IsValid() {
			clientIP = ip.String()
		}
	}
	if a, err := netip.ParseAddr(clientIP); err == nil {
		clientIP = a.Unmap().String()
	} else {
		clientIP = ""
	}
	requestID := ev.RequestID
	if requestID == "" {
		requestID = logging.RequestID(ctx)
	}
	if !requestIDRE.MatchString(requestID) {
		requestID = ""
	}
	errorClass := ev.ErrorClass
	if errorClass != "" && !errorClassRE.MatchString(errorClass) {
		errorClass = "unknown"
	}
	details, err := CanonicalDetails(ev.Details)
	if err != nil {
		return domain.AuditRecord{}, err
	}
	return domain.AuditRecord{
		ID: ids.New(), At: at.UTC().Truncate(time.Microsecond), Category: cat, Action: ev.Action,
		OperationID: cleanID(ev.OperationID), Actor: actor, ClientIP: clientIP, UserAgent: cleanUserAgent(ev.UserAgent),
		EnvironmentID: cleanID(ev.EnvironmentID), Targets: cleanTargets(ev.Targets), Outcome: outcome,
		ErrorClass: errorClass, JobID: cleanID(ev.JobID), RequestID: requestID, Details: details,
	}, nil
}

func cleanActor(a domain.AuditActor) (domain.AuditActor, error) {
	a = domain.AuditActor{Kind: a.Kind, UserID: cleanID(a.UserID), TokenID: cleanID(a.TokenID), AgentID: cleanID(a.AgentID)}
	switch a.Kind {
	case domain.AuditActorUser:
		if a.UserID == "" {
			return a, fmt.Errorf("%w: user actor without user ID", ErrInvalidEvent)
		}
		a.TokenID, a.AgentID = "", ""
	case domain.AuditActorAPIToken:
		if a.UserID == "" || a.TokenID == "" {
			return a, fmt.Errorf("%w: api_token actor needs the token ID and its owner's user ID", ErrInvalidEvent)
		}
		a.AgentID = ""
	case domain.AuditActorAgent:
		if a.AgentID == "" {
			return a, fmt.Errorf("%w: agent actor without agent ID", ErrInvalidEvent)
		}
		a.UserID, a.TokenID = "", ""
	case domain.AuditActorService, domain.AuditActorAnonymous:
		a.UserID, a.TokenID, a.AgentID = "", "", ""
	default:
		return a, fmt.Errorf("%w: actor kind %q", ErrInvalidEvent, a.Kind)
	}
	return a, nil
}

// ActorFor converts an authorization principal into an audit actor.
func ActorFor(p authz.Principal) domain.AuditActor {
	switch p.Kind {
	case authz.KindUser:
		return domain.AuditActor{Kind: domain.AuditActorUser, UserID: p.UserID}
	case authz.KindAPIToken:
		return domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: p.UserID, TokenID: p.TokenID}
	case authz.KindService:
		return ServiceActor()
	}
	return domain.AuditActor{Kind: domain.AuditActorAnonymous}
}

// ServiceActor is the manager's internal service identity (scheduled work,
// retention, recovery).
func ServiceActor() domain.AuditActor { return domain.AuditActor{Kind: domain.AuditActorService} }

// AgentActor is an agent.
func AgentActor(agentID string) domain.AuditActor {
	return domain.AuditActor{Kind: domain.AuditActorAgent, AgentID: agentID}
}

// ActorFromContext is the request principal as an actor, or anonymous.
func ActorFromContext(ctx context.Context) domain.AuditActor {
	if p, ok := authz.PrincipalFrom(ctx); ok {
		return ActorFor(p)
	}
	return domain.AuditActor{Kind: domain.AuditActorAnonymous}
}

type recorderKey struct{}

// WithRecorder returns ctx carrying r for Record. The API installs the
// manager's Log into every request context; background workers get it from
// their constructor.
func WithRecorder(ctx context.Context, r Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

// RecorderFrom returns the recorder carried by ctx.
func RecorderFrom(ctx context.Context) (Recorder, bool) {
	r, ok := ctx.Value(recorderKey{}).(Recorder)
	return r, ok && r != nil
}

// ErrNoRecorder means ctx carries no recorder.
var ErrNoRecorder = errors.New("audit: no recorder in context")

// Record appends ev with the recorder carried by ctx (for events outside
// the automatic HTTP and job records: sign-in failures, agent enrollment,
// the owner-recovery CLI, scheduled work). Actor, client IP and request ID
// default from ctx.
func Record(ctx context.Context, ev domain.AuditEvent) error {
	r, ok := RecorderFrom(ctx)
	if !ok {
		return ErrNoRecorder
	}
	return r.Record(ctx, ev)
}

// mirrorRecord writes the stored (redacted) record to the mirror logger.
func (l *Log) mirrorRecord(ctx context.Context, rec *domain.AuditRecord) {
	if l.mirror == nil {
		return
	}
	attrs := []slog.Attr{
		slog.Int64("audit_seq", rec.Seq), slog.String("audit_id", rec.ID),
		slog.String("at", store.FormatAuditTime(rec.At)), slog.String("category", string(rec.Category)),
		slog.String("action", rec.Action), slog.String("actor_kind", string(rec.Actor.Kind)),
		slog.String("outcome", string(rec.Outcome)), slog.String("hash", rec.Hash),
	}
	for _, kv := range [][2]string{
		{"operation_id", rec.OperationID}, {"actor_user_id", rec.Actor.UserID}, {"actor_token_id", rec.Actor.TokenID},
		{"actor_agent_id", rec.Actor.AgentID}, {"client_ip", rec.ClientIP}, {"user_agent", rec.UserAgent},
		{"environment_id", rec.EnvironmentID}, {"error_class", rec.ErrorClass}, {"job_id", rec.JobID},
		{"request_id", rec.RequestID},
	} {
		if kv[1] != "" {
			attrs = append(attrs, slog.String(kv[0], kv[1]))
		}
	}
	attrs = append(attrs, slog.String("targets", store.MarshalAuditTargets(rec.Targets)), slog.String("details", string(rec.Details)))
	l.mirror.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "audit", attrs...)
}
