package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Scheduler persistence (#13). The scheduler (internal/manager/scheduler)
// owns every change; these functions only map rows.

type scheduleSettingsRow struct {
	bun.BaseModel `bun:"table:schedule_settings"`

	Singleton int       `bun:"singleton,pk"`
	TimeZone  string    `bun:"time_zone,notnull"`
	Revision  int64     `bun:"revision,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

type scheduleDefaultCronRow struct {
	bun.BaseModel `bun:"table:schedule_default_crons"`

	Kind      string    `bun:"kind,pk"`
	Cron      string    `bun:"cron,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

type scheduleRow struct {
	bun.BaseModel `bun:"table:schedules"`

	ID            string     `bun:"id,pk"`
	Kind          string     `bun:"kind,notnull"`
	PolicyID      string     `bun:"policy_id,notnull"`
	Name          string     `bun:"name,notnull"`
	EnvironmentID string     `bun:"environment_id,notnull"`
	Cron          string     `bun:"cron,notnull"`
	TimeZone      string     `bun:"time_zone,notnull"`
	Enabled       int        `bun:"enabled,notnull"`
	InvalidReason string     `bun:"invalid_reason,notnull"`
	CursorAt      time.Time  `bun:"cursor_at,notnull"`
	NextRunAt     *time.Time `bun:"next_run_at,nullzero"`
	CreatedAt     time.Time  `bun:"created_at,notnull"`
	UpdatedAt     time.Time  `bun:"updated_at,notnull"`
}

func (r scheduleRow) toDomain() domain.Schedule {
	return domain.Schedule{
		ID: r.ID, Kind: r.Kind, PolicyID: r.PolicyID, Name: r.Name, EnvironmentID: r.EnvironmentID, Cron: r.Cron,
		TimeZone: r.TimeZone, Enabled: r.Enabled == 1, InvalidReason: r.InvalidReason, Cursor: r.CursorAt.UTC(),
		NextRunAt: utcPtr(r.NextRunAt), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func fromSchedule(s *domain.Schedule) scheduleRow {
	return scheduleRow{
		ID: s.ID, Kind: s.Kind, PolicyID: s.PolicyID, Name: s.Name, EnvironmentID: s.EnvironmentID, Cron: s.Cron,
		TimeZone: s.TimeZone, Enabled: b2i(s.Enabled), InvalidReason: s.InvalidReason, CursorAt: s.Cursor.UTC(),
		NextRunAt: utcPtr(s.NextRunAt), CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
	}
}

type scheduleRunRow struct {
	bun.BaseModel `bun:"table:schedule_runs"`

	ID             string     `bun:"id,pk"`
	ScheduleID     string     `bun:"schedule_id,notnull"`
	ScheduledFor   time.Time  `bun:"scheduled_for,notnull"`
	IdempotencyKey string     `bun:"idempotency_key,notnull"`
	Outcome        string     `bun:"outcome,notnull"`
	CatchUp        int        `bun:"catch_up,notnull"`
	MissedCount    int        `bun:"missed_count,notnull"`
	MissedFrom     *time.Time `bun:"missed_from,nullzero"`
	Reason         string     `bun:"reason,notnull"`
	ErrorClass     string     `bun:"error_class,notnull"`
	CreatedAt      time.Time  `bun:"created_at,notnull"`
	UpdatedAt      time.Time  `bun:"updated_at,notnull"`
}

func (r scheduleRunRow) toDomain() domain.ScheduleRun {
	return domain.ScheduleRun{
		ID: r.ID, ScheduleID: r.ScheduleID, ScheduledFor: r.ScheduledFor.UTC(), IdempotencyKey: r.IdempotencyKey,
		Outcome: domain.ScheduleOutcome(r.Outcome), CatchUp: r.CatchUp == 1, MissedCount: r.MissedCount,
		MissedFrom: utcPtr(r.MissedFrom), Reason: r.Reason, ErrorClass: r.ErrorClass,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

type scheduleRunJobRow struct {
	bun.BaseModel `bun:"table:schedule_run_jobs"`

	JobID      string     `bun:"job_id,pk"`
	RunID      string     `bun:"run_id,notnull"`
	Position   int        `bun:"position,notnull"`
	Kind       string     `bun:"kind,notnull"`
	State      string     `bun:"state,notnull"`
	ErrorClass string     `bun:"error_class,notnull"`
	FinishedAt *time.Time `bun:"finished_at,nullzero"`
}

// ScheduleSettings returns the default time zone, the settings revision
// and when they last changed.
func ScheduleSettings(ctx context.Context, db bun.IDB) (tz string, revision int64, updatedAt time.Time, err error) {
	var row scheduleSettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return "", 0, time.Time{}, fmt.Errorf("store: read schedule settings: %w", err)
	}
	return row.TimeZone, row.Revision, row.UpdatedAt.UTC(), nil
}

// ScheduleDefaultCrons returns the edited default expressions by kind.
func ScheduleDefaultCrons(ctx context.Context, db bun.IDB) (map[string]string, error) {
	var rows []scheduleDefaultCronRow
	if err := db.NewSelect().Model(&rows).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: read schedule defaults: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Kind] = r.Cron
	}
	return out, nil
}

// UpdateScheduleDefaults applies a change if the settings are still at
// revision (domain.ErrRevisionConflict otherwise) and bumps the revision.
func UpdateScheduleDefaults(ctx context.Context, db bun.IDB, revision int64, tz *string, crons map[string]string, now time.Time) error {
	q := db.NewUpdate().Model((*scheduleSettingsRow)(nil)).
		Set("revision = revision + 1").Set("updated_at = ?", now.UTC()).
		Where("singleton = 1 AND revision = ?", revision)
	if tz != nil {
		q = q.Set("time_zone = ?", *tz)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update schedule settings: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionConflict
	}
	for kind, cron := range crons {
		row := scheduleDefaultCronRow{Kind: kind, Cron: cron, UpdatedAt: now.UTC()}
		if _, err := db.NewInsert().Model(&row).On("CONFLICT (kind) DO UPDATE").
			Set("cron = EXCLUDED.cron").Set("updated_at = EXCLUDED.updated_at").Exec(ctx); err != nil {
			return fmt.Errorf("store: update schedule default: %w", err)
		}
	}
	return nil
}

// Schedules returns schedules matching f in ID order (all of them when f
// is empty).
func Schedules(ctx context.Context, db bun.IDB, f domain.ScheduleFilter) ([]domain.Schedule, error) {
	var rows []scheduleRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.EnvironmentID != "" {
		q = q.Where("environment_id = ?", f.EnvironmentID)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", b2i(*f.Enabled))
	}
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	out := make([]domain.Schedule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetSchedule returns a schedule by ID (ok=false when absent).
func GetSchedule(ctx context.Context, db bun.IDB, id string) (domain.Schedule, bool, error) {
	var row scheduleRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, false, nil
	}
	if err != nil {
		return domain.Schedule{}, false, fmt.Errorf("store: get schedule: %w", err)
	}
	return row.toDomain(), true, nil
}

// ScheduleByPolicy returns the schedule of a policy (ok=false when absent).
func ScheduleByPolicy(ctx context.Context, db bun.IDB, kind, policyID string) (domain.Schedule, bool, error) {
	var row scheduleRow
	err := db.NewSelect().Model(&row).Where("kind = ? AND policy_id = ?", kind, policyID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, false, nil
	}
	if err != nil {
		return domain.Schedule{}, false, fmt.Errorf("store: get schedule: %w", err)
	}
	return row.toDomain(), true, nil
}

// InsertSchedule inserts a schedule.
func InsertSchedule(ctx context.Context, db bun.IDB, s *domain.Schedule) error {
	row := fromSchedule(s)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert schedule: %w", err)
	}
	return nil
}

// UpdateSchedule writes every mutable column of s.
func UpdateSchedule(ctx context.Context, db bun.IDB, s *domain.Schedule) error {
	row := fromSchedule(s)
	if _, err := db.NewUpdate().Model(&row).
		Column("name", "environment_id", "cron", "time_zone", "enabled", "invalid_reason", "cursor_at", "next_run_at", "updated_at").
		WherePK().Exec(ctx); err != nil {
		return fmt.Errorf("store: update schedule: %w", err)
	}
	return nil
}

// DeleteSchedule deletes a schedule and its run history.
func DeleteSchedule(ctx context.Context, db bun.IDB, id string) error {
	if _, err := db.NewRaw(`DELETE FROM schedule_run_jobs WHERE run_id IN (SELECT id FROM schedule_runs WHERE schedule_id = ?)`, id).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete schedule run jobs: %w", err)
	}
	if _, err := db.NewDelete().Model((*scheduleRunRow)(nil)).Where("schedule_id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete schedule runs: %w", err)
	}
	if _, err := db.NewDelete().Model((*scheduleRow)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete schedule: %w", err)
	}
	return nil
}

// ErrScheduleRunExists means the (schedule, instant) or idempotency key was
// already recorded.
var ErrScheduleRunExists = errors.New("store: schedule run already recorded")

// InsertScheduleRun records a run.
func InsertScheduleRun(ctx context.Context, db bun.IDB, r *domain.ScheduleRun) error {
	row := scheduleRunRow{
		ID: r.ID, ScheduleID: r.ScheduleID, ScheduledFor: r.ScheduledFor.UTC(), IdempotencyKey: r.IdempotencyKey,
		Outcome: string(r.Outcome), CatchUp: b2i(r.CatchUp), MissedCount: r.MissedCount, MissedFrom: utcPtr(r.MissedFrom),
		Reason: r.Reason, ErrorClass: r.ErrorClass, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "schedule_runs.") {
			return ErrScheduleRunExists
		}
		return fmt.Errorf("store: insert schedule run: %w", err)
	}
	return nil
}

// FinishScheduleRun sets a run's outcome, reason and error class.
func FinishScheduleRun(ctx context.Context, db bun.IDB, id string, outcome domain.ScheduleOutcome, reason, class string, now time.Time) error {
	if _, err := db.NewUpdate().Model((*scheduleRunRow)(nil)).
		Set("outcome = ?", string(outcome)).Set("reason = ?", reason).Set("error_class = ?", class).Set("updated_at = ?", now.UTC()).
		Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: update schedule run: %w", err)
	}
	return nil
}

// AddScheduleRunJob links a job to a run (idempotent per job).
func AddScheduleRunJob(ctx context.Context, db bun.IDB, runID string, position int, j domain.Job) error {
	row := scheduleRunJobRow{JobID: j.ID, RunID: runID, Position: position, Kind: string(j.Kind), State: string(j.State),
		ErrorClass: j.ErrorClass, FinishedAt: utcPtr(j.FinishedAt)}
	if _, err := db.NewInsert().Model(&row).On("CONFLICT (job_id) DO NOTHING").Exec(ctx); err != nil {
		return fmt.Errorf("store: link schedule run job: %w", err)
	}
	return nil
}

// SetScheduleRunJobState records a linked job's terminal state (no-op for
// jobs no run enqueued).
func SetScheduleRunJobState(ctx context.Context, db bun.IDB, j domain.Job) error {
	if _, err := db.NewUpdate().Model((*scheduleRunJobRow)(nil)).
		Set("state = ?", string(j.State)).Set("error_class = ?", j.ErrorClass).Set("finished_at = ?", utcPtr(j.FinishedAt)).
		Where("job_id = ?", j.ID).Exec(ctx); err != nil {
		return fmt.Errorf("store: update schedule run job: %w", err)
	}
	return nil
}

// PendingScheduleRuns returns runs still being enqueued, oldest first.
func PendingScheduleRuns(ctx context.Context, db bun.IDB) ([]domain.ScheduleRun, error) {
	var rows []scheduleRunRow
	if err := db.NewSelect().Model(&rows).Where("outcome = ?", string(domain.RunPending)).
		Order("scheduled_for ASC", "id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: pending schedule runs: %w", err)
	}
	out := make([]domain.ScheduleRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// LaterScheduleRunExists reports whether the schedule has a run for an
// instant after t.
func LaterScheduleRunExists(ctx context.Context, db bun.IDB, scheduleID string, t time.Time) (bool, error) {
	n, err := db.NewSelect().Model((*scheduleRunRow)(nil)).Where("schedule_id = ? AND scheduled_for > ?", scheduleID, t.UTC()).Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: count schedule runs: %w", err)
	}
	return n > 0, nil
}

// ScheduleRuns returns a schedule's newest runs (at most limit) with their
// jobs; a job's live state comes from the jobs table while it exists.
func ScheduleRuns(ctx context.Context, db bun.IDB, scheduleID string, limit int) ([]domain.ScheduleRun, error) {
	var rows []scheduleRunRow
	if err := db.NewSelect().Model(&rows).Where("schedule_id = ?", scheduleID).
		Order("scheduled_for DESC", "id DESC").Limit(limit).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: schedule runs: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	var jobs []struct {
		JobID        string         `bun:"job_id"`
		RunID        string         `bun:"run_id"`
		Position     int            `bun:"position"`
		Kind         string         `bun:"kind"`
		State        string         `bun:"state"`
		ErrorClass   string         `bun:"error_class"`
		FinishedAt   *time.Time     `bun:"finished_at"`
		LiveState    sql.NullString `bun:"live_state"`
		LiveClass    sql.NullString `bun:"live_class"`
		LiveBlocked  sql.NullString `bun:"live_blocked"`
		LiveFinished *time.Time     `bun:"live_finished"`
	}
	if err := db.NewRaw(`SELECT rj.job_id, rj.run_id, rj.position, rj.kind, rj.state, rj.error_class, rj.finished_at,
			j.state AS live_state, j.error_class AS live_class, j.blocked_reason AS live_blocked, j.finished_at AS live_finished
		FROM schedule_run_jobs rj LEFT JOIN jobs j ON j.id = rj.job_id
		WHERE rj.run_id IN (?) ORDER BY rj.run_id, rj.position`, bun.List(ids)).Scan(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("store: schedule run jobs: %w", err)
	}
	byRun := map[string][]domain.ScheduleRunJob{}
	for _, j := range jobs {
		rj := domain.ScheduleRunJob{JobID: j.JobID, Position: j.Position, Kind: domain.JobKind(j.Kind), State: domain.JobState(j.State),
			ErrorClass: j.ErrorClass, FinishedAt: utcPtr(j.FinishedAt)}
		if j.LiveState.Valid {
			rj.State, rj.ErrorClass, rj.BlockedReason = domain.JobState(j.LiveState.String), j.LiveClass.String, j.LiveBlocked.String
			rj.FinishedAt = utcPtr(j.LiveFinished)
		}
		byRun[j.RunID] = append(byRun[j.RunID], rj)
	}
	out := make([]domain.ScheduleRun, 0, len(rows))
	for _, r := range rows {
		run := r.toDomain()
		run.Jobs = byRun[r.ID]
		out = append(out, run)
	}
	return out, nil
}

// ScheduleRunOfJob returns the schedule and run that enqueued a job
// (ok=false for jobs no schedule enqueued).
func ScheduleRunOfJob(ctx context.Context, db bun.IDB, jobID string) (domain.Schedule, string, bool, error) {
	var ref struct {
		RunID      string `bun:"run_id"`
		ScheduleID string `bun:"schedule_id"`
	}
	err := db.NewRaw(`SELECT r.id AS run_id, r.schedule_id FROM schedule_run_jobs rj JOIN schedule_runs r ON r.id = rj.run_id
		WHERE rj.job_id = ?`, jobID).Scan(ctx, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, "", false, nil
	}
	if err != nil {
		return domain.Schedule{}, "", false, fmt.Errorf("store: schedule run of job: %w", err)
	}
	s, ok, err := GetSchedule(ctx, db, ref.ScheduleID)
	return s, ref.RunID, ok, err
}

// ActivePolicyJob returns a non-terminal job of the policy among kinds
// ("" when there is none).
func ActivePolicyJob(ctx context.Context, db bun.IDB, policyID string, kinds []domain.JobKind, exclude []string) (string, error) {
	var terminal []string
	for _, s := range domain.JobStates() {
		if s.Terminal() {
			terminal = append(terminal, string(s))
		}
	}
	ks := make([]string, 0, len(kinds))
	for _, k := range kinds {
		ks = append(ks, string(k))
	}
	if len(ks) == 0 {
		return "", nil
	}
	var rows []jobRow
	q := db.NewSelect().Model(&rows).Column("id").
		Where("policy_id = ? AND kind IN (?) AND state NOT IN (?)", policyID, bun.List(ks), bun.List(terminal)).
		Order("id ASC").Limit(len(exclude) + 1)
	if err := q.Scan(ctx); err != nil {
		return "", fmt.Errorf("store: active policy jobs: %w", err)
	}
	for _, r := range rows {
		if !slices.Contains(exclude, r.ID) {
			return r.ID, nil
		}
	}
	return "", nil
}

// PruneScheduleRuns keeps the newest keep runs of a schedule and never
// deletes pending runs.
func PruneScheduleRuns(ctx context.Context, db bun.IDB, scheduleID string, keep int) error {
	sub := db.NewSelect().Model((*scheduleRunRow)(nil)).Column("id").Where("schedule_id = ?", scheduleID).
		Order("scheduled_for DESC", "id DESC").Limit(keep)
	if _, err := db.NewRaw(`DELETE FROM schedule_run_jobs WHERE run_id IN (
			SELECT id FROM schedule_runs WHERE schedule_id = ? AND outcome <> 'pending' AND id NOT IN (?))`, scheduleID, sub).Exec(ctx); err != nil {
		return fmt.Errorf("store: prune schedule run jobs: %w", err)
	}
	if _, err := db.NewRaw(`DELETE FROM schedule_runs WHERE schedule_id = ? AND outcome <> 'pending' AND id NOT IN (?)`, scheduleID, sub).Exec(ctx); err != nil {
		return fmt.Errorf("store: prune schedule runs: %w", err)
	}
	return nil
}
