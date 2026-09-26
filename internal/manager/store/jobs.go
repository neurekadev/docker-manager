package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Job persistence (#26). The engine (internal/manager/jobs) owns all state
// changes; these functions only map rows. Callers run multi-statement
// changes inside one transaction (bun.Tx implements bun.IDB).

type jobRow struct {
	bun.BaseModel `bun:"table:jobs"`

	ID               string     `bun:"id,pk"`
	Kind             string     `bun:"kind,notnull"`
	Executor         string     `bun:"executor,notnull"`
	Origin           string     `bun:"origin,notnull"`
	InitiatorUserID  string     `bun:"initiator_user_id,nullzero"`
	InitiatorTokenID string     `bun:"initiator_token_id,nullzero"`
	RequestID        string     `bun:"request_id,notnull"`
	PolicyID         string     `bun:"policy_id,nullzero"`
	EnvironmentID    string     `bun:"environment_id,nullzero"`
	Targets          string     `bun:"targets,notnull"`
	Input            string     `bun:"input,notnull"`
	InputHash        string     `bun:"input_hash,notnull"`
	IdempotencyScope string     `bun:"idempotency_scope,nullzero"`
	IdempotencyKey   string     `bun:"idempotency_key,nullzero"`
	Attempt          int        `bun:"attempt,notnull"`
	State            string     `bun:"state,notnull"`
	ProgressPercent  int        `bun:"progress_percent,notnull"`
	ProgressStep     string     `bun:"progress_step,notnull"`
	ProgressMessage  string     `bun:"progress_message,notnull"`
	Items            string     `bun:"items,notnull"`
	ErrorClass       string     `bun:"error_class,notnull"`
	ErrorMessage     string     `bun:"error_message,notnull"`
	Recovery         string     `bun:"recovery,notnull"`
	BlockedBy        string     `bun:"blocked_by,notnull"`
	BlockedReason    string     `bun:"blocked_reason,notnull"`
	Locks            string     `bun:"locks,notnull"`
	FencingToken     int64      `bun:"fencing_token,notnull"`
	CancelRequested  int        `bun:"cancel_requested,notnull"`
	CurrentStep      string     `bun:"current_step,notnull"`
	StepInFlight     int        `bun:"step_in_flight,notnull"`
	CompletedSteps   string     `bun:"completed_steps,notnull"`
	Compensations    string     `bun:"compensations,notnull"`
	Resumes          int        `bun:"resumes,notnull"`
	ResumeOutput     string     `bun:"resume_output,notnull"`
	LastEventSeq     int64      `bun:"last_event_seq,notnull"`
	CreatedAt        time.Time  `bun:"created_at,notnull"`
	UpdatedAt        time.Time  `bun:"updated_at,notnull"`
	DispatchedAt     *time.Time `bun:"dispatched_at,nullzero"`
	StartedAt        *time.Time `bun:"started_at,nullzero"`
	FinishedAt       *time.Time `bun:"finished_at,nullzero"`
}

// JSON column encodings (database format, not API).
type targetJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Env  string `json:"env,omitempty"`
}

type lockJSON struct {
	Scope string `json:"scope"`
	Env   string `json:"env,omitempty"`
	Name  string `json:"name,omitempty"`
	Mode  string `json:"mode"`
}

type itemJSON struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type compensationJSON struct {
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args,omitempty"`
	Released bool            `json:"released,omitempty"`
	Done     bool            `json:"done,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("store: marshal: %v", err)) // only plain structs are marshaled
	}
	return string(b)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func fromJob(j *domain.Job) jobRow {
	targets := make([]targetJSON, 0, len(j.Targets))
	for _, t := range j.Targets {
		targets = append(targets, targetJSON{Type: string(t.Type), ID: t.ID, Env: t.EnvironmentID})
	}
	locks := make([]lockJSON, 0, len(j.Locks))
	for _, l := range j.Locks {
		locks = append(locks, lockJSON{Scope: string(l.Scope), Env: l.EnvironmentID, Name: l.Name, Mode: string(l.Mode)})
	}
	items := make([]itemJSON, 0, len(j.Items))
	for _, it := range j.Items {
		items = append(items, itemJSON(it))
	}
	comps := make([]compensationJSON, 0, len(j.Compensations))
	for _, c := range j.Compensations {
		comps = append(comps, compensationJSON{Name: c.Name, Args: c.Args, Released: c.Released, Done: c.Done, Error: c.Error})
	}
	completed := j.CompletedSteps
	if completed == nil {
		completed = []string{}
	}
	input := string(j.Input)
	if input == "" {
		input = "{}"
	}
	scope := ""
	if j.IdempotencyKey != "" {
		scope = idempotencyScope(j)
	}
	return jobRow{
		ID: j.ID, Kind: string(j.Kind), Executor: string(j.Executor), Origin: string(j.Origin),
		InitiatorUserID: j.InitiatorUserID, InitiatorTokenID: j.InitiatorTokenID, PolicyID: j.PolicyID, RequestID: j.RequestID,
		EnvironmentID: j.EnvironmentID, Targets: mustJSON(targets), Input: input, InputHash: j.InputHash,
		IdempotencyScope: scope, IdempotencyKey: j.IdempotencyKey, Attempt: j.Attempt, State: string(j.State),
		ProgressPercent: j.Progress.Percent, ProgressStep: j.Progress.Step, ProgressMessage: j.Progress.Message,
		Items: mustJSON(items), ErrorClass: j.ErrorClass, ErrorMessage: j.ErrorMessage, Recovery: j.Recovery,
		BlockedBy: j.BlockedBy, BlockedReason: j.BlockedReason, Locks: mustJSON(locks),
		FencingToken: int64(j.FencingToken), CancelRequested: b2i(j.CancelRequested), //nolint:gosec // tokens stay far below 2^63
		CurrentStep: j.CurrentStep, StepInFlight: b2i(j.StepInFlight), CompletedSteps: mustJSON(completed),
		Compensations: mustJSON(comps), Resumes: j.Resumes, ResumeOutput: string(j.ResumeOutput), LastEventSeq: j.LastEventSeq,
		CreatedAt: j.CreatedAt.UTC(), UpdatedAt: j.UpdatedAt.UTC(),
		DispatchedAt: utcPtr(j.DispatchedAt), StartedAt: utcPtr(j.StartedAt), FinishedAt: utcPtr(j.FinishedAt),
	}
}

func nilIfEmpty(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}

// idempotencyScope keys idempotency per initiating principal (or the
// service identity for scheduled jobs), so keys never collide across users.
func idempotencyScope(j *domain.Job) string {
	switch {
	case j.InitiatorTokenID != "":
		return "token:" + j.InitiatorTokenID
	case j.InitiatorUserID != "":
		return "user:" + j.InitiatorUserID
	}
	return "service"
}

func (r *jobRow) toDomain() (domain.Job, error) {
	j := domain.Job{
		ID: r.ID, Kind: domain.JobKind(r.Kind), Executor: domain.JobExecutor(r.Executor), Origin: domain.JobOrigin(r.Origin),
		InitiatorUserID: r.InitiatorUserID, InitiatorTokenID: r.InitiatorTokenID, PolicyID: r.PolicyID, RequestID: r.RequestID,
		EnvironmentID: r.EnvironmentID, Input: []byte(r.Input), InputHash: r.InputHash, IdempotencyKey: r.IdempotencyKey,
		Attempt: r.Attempt, State: domain.JobState(r.State),
		Progress:   domain.JobProgress{Percent: r.ProgressPercent, Step: r.ProgressStep, Message: r.ProgressMessage},
		ErrorClass: r.ErrorClass, ErrorMessage: r.ErrorMessage, Recovery: r.Recovery,
		BlockedBy: r.BlockedBy, BlockedReason: r.BlockedReason, FencingToken: uint64(r.FencingToken), //nolint:gosec // never negative (CHECK)
		CancelRequested: r.CancelRequested == 1, CurrentStep: r.CurrentStep, StepInFlight: r.StepInFlight == 1,
		Resumes: r.Resumes, LastEventSeq: r.LastEventSeq,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		ResumeOutput: nilIfEmpty(r.ResumeOutput),
		DispatchedAt: utcPtr(r.DispatchedAt), StartedAt: utcPtr(r.StartedAt), FinishedAt: utcPtr(r.FinishedAt),
	}
	var targets []targetJSON
	var locks []lockJSON
	var items []itemJSON
	var comps []compensationJSON
	if err := errors.Join(
		json.Unmarshal([]byte(r.Targets), &targets),
		json.Unmarshal([]byte(r.Locks), &locks),
		json.Unmarshal([]byte(r.Items), &items),
		json.Unmarshal([]byte(r.Compensations), &comps),
		json.Unmarshal([]byte(r.CompletedSteps), &j.CompletedSteps),
	); err != nil {
		return domain.Job{}, fmt.Errorf("store: decode job %s: %w", r.ID, err)
	}
	for _, t := range targets {
		j.Targets = append(j.Targets, domain.JobTarget{Type: domain.TargetType(t.Type), ID: t.ID, EnvironmentID: t.Env})
	}
	for _, l := range locks {
		j.Locks = append(j.Locks, domain.JobLock{Scope: domain.LockScope(l.Scope), EnvironmentID: l.Env, Name: l.Name, Mode: domain.LockMode(l.Mode)})
	}
	for _, it := range items {
		j.Items = append(j.Items, domain.JobItem(it))
	}
	for _, c := range comps {
		j.Compensations = append(j.Compensations, domain.JobCompensation{Name: c.Name, Args: c.Args, Released: c.Released, Done: c.Done, Error: c.Error})
	}
	return j, nil
}

// ErrIdempotencyKeyTaken is returned by InsertJob when the (scope, key)
// pair already exists.
var ErrIdempotencyKeyTaken = errors.New("store: idempotency key already used")

// InsertJob inserts a new job and its target index rows.
func InsertJob(ctx context.Context, db bun.IDB, j *domain.Job) error {
	row := fromJob(j)
	if row.IdempotencyKey != "" {
		n, err := db.NewSelect().Model((*jobRow)(nil)).
			Where("idempotency_scope = ? AND idempotency_key = ?", row.IdempotencyScope, row.IdempotencyKey).Count(ctx)
		if err != nil {
			return fmt.Errorf("store: check idempotency key: %w", err)
		}
		if n > 0 {
			return ErrIdempotencyKeyTaken
		}
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert job: %w", err)
	}
	seen := map[[3]string]bool{}
	for _, t := range j.Targets {
		env := t.EnvironmentID
		if env == "" {
			env = j.EnvironmentID
		}
		k := [3]string{string(t.Type), env, t.ID}
		if seen[k] {
			continue
		}
		seen[k] = true
		if _, err := db.NewRaw(`INSERT INTO job_targets (job_id, type, environment_id, target_id) VALUES (?, ?, ?, ?)`,
			j.ID, string(t.Type), env, t.ID).Exec(ctx); err != nil {
			return fmt.Errorf("store: insert job target: %w", err)
		}
	}
	return nil
}

// GetJob returns a job or domain.ErrJobNotFound.
func GetJob(ctx context.Context, db bun.IDB, id string) (domain.Job, error) {
	var row jobRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, domain.ErrJobNotFound
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("store: get job: %w", err)
	}
	return row.toDomain()
}

// FindJobByIdempotencyKey returns the job created with this key by the same
// principal scope as j (see idempotencyScope).
func FindJobByIdempotencyKey(ctx context.Context, db bun.IDB, j *domain.Job) (domain.Job, bool, error) {
	var row jobRow
	err := db.NewSelect().Model(&row).
		Where("idempotency_scope = ? AND idempotency_key = ?", idempotencyScope(j), j.IdempotencyKey).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("store: find job by idempotency key: %w", err)
	}
	out, err := row.toDomain()
	return out, err == nil, err
}

// UpdateJob writes every mutable column of j (not last_event_seq, which
// AppendJobEvent owns).
func UpdateJob(ctx context.Context, db bun.IDB, j *domain.Job) error {
	row := fromJob(j)
	res, err := db.NewUpdate().Model(&row).
		Column("attempt", "state", "progress_percent", "progress_step", "progress_message", "items",
			"error_class", "error_message", "recovery", "blocked_by", "blocked_reason", "locks", "fencing_token",
			"cancel_requested", "current_step", "step_in_flight", "completed_steps", "compensations", "resumes", "resume_output",
			"updated_at", "dispatched_at", "started_at", "finished_at").
		WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update job: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrJobNotFound
	}
	return nil
}

// ListJobs returns jobs matching q, newest first.
func ListJobs(ctx context.Context, db bun.IDB, q domain.JobFilter) ([]domain.Job, error) {
	var rows []jobRow
	sel := db.NewSelect().Model(&rows).OrderExpr("id DESC")
	if len(q.States) > 0 {
		sel = sel.Where("state IN (?)", bun.List(q.States))
	}
	if len(q.Kinds) > 0 {
		sel = sel.Where("kind IN (?)", bun.List(q.Kinds))
	}
	if len(q.Origins) > 0 {
		sel = sel.Where("origin IN (?)", bun.List(q.Origins))
	}
	if q.EnvironmentID != "" {
		sel = sel.Where("(environment_id = ? OR id IN (SELECT job_id FROM job_targets WHERE environment_id = ?))", q.EnvironmentID, q.EnvironmentID)
	}
	if q.Target != nil {
		if q.Target.EnvironmentID != "" {
			sel = sel.Where("id IN (SELECT job_id FROM job_targets WHERE type = ? AND target_id = ? AND environment_id = ?)",
				string(q.Target.Type), q.Target.ID, q.Target.EnvironmentID)
		} else {
			sel = sel.Where("id IN (SELECT job_id FROM job_targets WHERE type = ? AND target_id = ?)", string(q.Target.Type), q.Target.ID)
		}
	}
	if q.BeforeID != "" {
		sel = sel.Where("id < ?", q.BeforeID)
	}
	if q.Limit > 0 {
		sel = sel.Limit(q.Limit)
	}
	if err := sel.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return rowsToJobs(rows)
}

// JobsInStates returns all jobs in the given states, oldest first (FIFO).
func JobsInStates(ctx context.Context, db bun.IDB, states ...domain.JobState) ([]domain.Job, error) {
	var rows []jobRow
	if err := db.NewSelect().Model(&rows).Where("state IN (?)", bun.List(states)).OrderExpr("id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: jobs in states: %w", err)
	}
	return rowsToJobs(rows)
}

func rowsToJobs(rows []jobRow) ([]domain.Job, error) {
	out := make([]domain.Job, 0, len(rows))
	for i := range rows {
		j, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

type jobEventRow struct {
	bun.BaseModel `bun:"table:job_events"`

	JobID   string    `bun:"job_id,pk"`
	Seq     int64     `bun:"seq,pk"`
	At      time.Time `bun:"at,notnull"`
	Type    string    `bun:"type,notnull"`
	State   string    `bun:"state,notnull"`
	Message string    `bun:"message,notnull"`
	Percent int       `bun:"percent,notnull"`
	Step    string    `bun:"step,notnull"`
	Item    *string   `bun:"item"`
}

// AppendJobEvent assigns ev the job's next sequence number, stores it and
// trims the job's log to the newest maxEvents entries (0 = unbounded).
func AppendJobEvent(ctx context.Context, db bun.IDB, ev *domain.JobEvent, maxEvents int) error {
	var seq int64
	if err := db.NewRaw(`UPDATE jobs SET last_event_seq = last_event_seq + 1 WHERE id = ? RETURNING last_event_seq`, ev.JobID).
		Scan(ctx, &seq); err != nil {
		return fmt.Errorf("store: next job event seq: %w", err)
	}
	ev.Seq = seq
	row := jobEventRow{JobID: ev.JobID, Seq: seq, At: ev.At.UTC(), Type: ev.Type, State: string(ev.State),
		Message: ev.Message, Percent: ev.Percent, Step: ev.Step}
	if ev.Item != nil {
		s := mustJSON(itemJSON(*ev.Item))
		row.Item = &s
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert job event: %w", err)
	}
	if maxEvents > 0 && seq > int64(maxEvents) {
		if _, err := db.NewDelete().Model((*jobEventRow)(nil)).
			Where("job_id = ? AND seq <= ?", ev.JobID, seq-int64(maxEvents)).Exec(ctx); err != nil {
			return fmt.Errorf("store: trim job events: %w", err)
		}
	}
	return nil
}

// JobEvents returns up to limit events of a job with seq > afterSeq.
func JobEvents(ctx context.Context, db bun.IDB, jobID string, afterSeq int64, limit int) ([]domain.JobEvent, error) {
	var rows []jobEventRow
	sel := db.NewSelect().Model(&rows).Where("job_id = ? AND seq > ?", jobID, afterSeq).OrderExpr("seq ASC")
	if limit > 0 {
		sel = sel.Limit(limit)
	}
	if err := sel.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: job events: %w", err)
	}
	out := make([]domain.JobEvent, 0, len(rows))
	for _, r := range rows {
		ev := domain.JobEvent{JobID: r.JobID, Seq: r.Seq, At: r.At.UTC(), Type: r.Type, State: domain.JobState(r.State),
			Message: r.Message, Percent: r.Percent, Step: r.Step}
		if r.Item != nil {
			var it itemJSON
			if err := json.Unmarshal([]byte(*r.Item), &it); err != nil {
				return nil, fmt.Errorf("store: decode job event item: %w", err)
			}
			item := domain.JobItem(it)
			ev.Item = &item
		}
		out = append(out, ev)
	}
	return out, nil
}

// HeldLock is a lock held by an active job.
type HeldLock struct {
	JobID string
	Lock  domain.JobLock
}

type jobLockRow struct {
	bun.BaseModel `bun:"table:job_locks"`

	JobID         string    `bun:"job_id,pk"`
	Scope         string    `bun:"scope,pk"`
	EnvironmentID string    `bun:"environment_id,pk"`
	Name          string    `bun:"name,pk"`
	Mode          string    `bun:"mode,notnull"`
	AcquiredAt    time.Time `bun:"acquired_at,notnull"`
}

// HeldLocks returns every held lock, ordered by (job, scope, env, name).
func HeldLocks(ctx context.Context, db bun.IDB) ([]HeldLock, error) {
	var rows []jobLockRow
	if err := db.NewSelect().Model(&rows).OrderExpr("job_id, scope, environment_id, name").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: held locks: %w", err)
	}
	out := make([]HeldLock, 0, len(rows))
	for _, r := range rows {
		out = append(out, HeldLock{JobID: r.JobID, Lock: domain.JobLock{Scope: domain.LockScope(r.Scope),
			EnvironmentID: r.EnvironmentID, Name: r.Name, Mode: domain.LockMode(r.Mode)}})
	}
	return out, nil
}

// InsertJobLock records one held lock.
func InsertJobLock(ctx context.Context, db bun.IDB, jobID string, l domain.JobLock, at time.Time) error {
	row := jobLockRow{JobID: jobID, Scope: string(l.Scope), EnvironmentID: l.EnvironmentID, Name: l.Name, Mode: string(l.Mode), AcquiredAt: at.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert job lock: %w", err)
	}
	return nil
}

// DeleteJobLocks releases every lock held by a job.
func DeleteJobLocks(ctx context.Context, db bun.IDB, jobID string) error {
	if _, err := db.NewDelete().Model((*jobLockRow)(nil)).Where("job_id = ?", jobID).Exec(ctx); err != nil {
		return fmt.Errorf("store: release job locks: %w", err)
	}
	return nil
}

// NextFencingToken increments and returns the environment's persisted
// fencing counter (first token: 1).
func NextFencingToken(ctx context.Context, db bun.IDB, environmentID string) (uint64, error) {
	var tok int64
	err := db.NewRaw(`INSERT INTO job_fencing (environment_id, last_token) VALUES (?, 1)
		ON CONFLICT (environment_id) DO UPDATE SET last_token = last_token + 1 RETURNING last_token`, environmentID).Scan(ctx, &tok)
	if err != nil {
		return 0, fmt.Errorf("store: next fencing token: %w", err)
	}
	return uint64(tok), nil //nolint:gosec // CHECK last_token >= 0
}

// RaiseFencingFloor makes sure the environment's counter is at least floor
// (e.g. the agent's high-water mark after the manager database was restored).
func RaiseFencingFloor(ctx context.Context, db bun.IDB, environmentID string, floor uint64) error {
	if floor == 0 {
		return nil
	}
	_, err := db.NewRaw(`INSERT INTO job_fencing (environment_id, last_token) VALUES (?, ?)
		ON CONFLICT (environment_id) DO UPDATE SET last_token = max(last_token, excluded.last_token)`,
		environmentID, int64(floor)).Exec(ctx) //nolint:gosec // agent tokens originate from this counter
	if err != nil {
		return fmt.Errorf("store: raise fencing floor: %w", err)
	}
	return nil
}

// DeleteFinishedJobs removes terminal jobs (with their events, targets and
// locks) finished before olderThan, and then all but the newest keep
// terminal jobs (keep <= 0: no count bound). It returns the number deleted.
func DeleteFinishedJobs(ctx context.Context, db bun.IDB, olderThan time.Time, keep int) (int, error) {
	res, err := db.NewDelete().Model((*jobRow)(nil)).
		Where("finished_at IS NOT NULL AND finished_at < ?", olderThan.UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: delete expired jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	if keep > 0 {
		res, err = db.NewRaw(`DELETE FROM jobs WHERE finished_at IS NOT NULL AND id NOT IN
			(SELECT id FROM jobs WHERE finished_at IS NOT NULL ORDER BY finished_at DESC, id DESC LIMIT ?)`, keep).Exec(ctx)
		if err != nil {
			return int(n), fmt.Errorf("store: delete surplus jobs: %w", err)
		}
		m, _ := res.RowsAffected()
		n += m
	}
	return int(n), nil
}

// JobCount is the number of jobs of one kind in one state.
type JobCount struct {
	Kind  domain.JobKind
	State domain.JobState
	Count int
}

// CountJobs counts jobs per kind and state (diagnostics, #34), sorted.
func CountJobs(ctx context.Context, db bun.IDB) ([]JobCount, error) {
	var rows []struct {
		Kind  string `bun:"kind"`
		State string `bun:"state"`
		Count int    `bun:"n"`
	}
	if err := db.NewRaw(`SELECT kind, state, count(*) AS n FROM jobs GROUP BY kind, state ORDER BY kind, state`).Scan(ctx, &rows); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: count jobs: %w", err)
	}
	out := make([]JobCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, JobCount{Kind: domain.JobKind(r.Kind), State: domain.JobState(r.State), Count: r.Count})
	}
	return out, nil
}
