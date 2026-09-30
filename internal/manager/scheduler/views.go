package scheduler

import (
	"context"
	"time"

	"github.com/neurekadev/docker-manager/internal/cron"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// List returns schedules matching f in ID order (the cross-policy view;
// callers filter by the policies' read capability).
func (s *Service) List(ctx context.Context, f domain.ScheduleFilter) ([]domain.Schedule, error) {
	return store.Schedules(ctx, s.db, f)
}

// Runs returns a schedule's newest runs (history), newest first.
func (s *Service) Runs(ctx context.Context, scheduleID string, limit int) ([]domain.ScheduleRun, error) {
	if limit <= 0 {
		limit = 10
	}
	return store.ScheduleRuns(ctx, s.db, scheduleID, min(limit, s.opts.History))
}

// Status returns a policy's schedule state and its newest runs, for policy
// DTOs (next run, last outcome). ok is false before the scheduler first
// synchronized the policy.
func (s *Service) Status(ctx context.Context, kind, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error) {
	sc, ok, err := store.ScheduleByPolicy(ctx, s.db, kind, policyID)
	if err != nil || !ok {
		return domain.Schedule{}, nil, ok, err
	}
	rs, err := s.Runs(ctx, sc.ID, runs)
	return sc, rs, true, err
}

// NextRun annotates a schedule's next run (local wall clock, DST).
func (s *Service) NextRun(sc domain.Schedule) (Run, bool) {
	if sc.NextRunAt == nil {
		return Run{}, false
	}
	spec, loc, err := parseSpec(sc.Cron, sc.TimeZone)
	if err != nil {
		return Run{}, false
	}
	o, ok := spec.Next(sc.NextRunAt.Add(-time.Nanosecond), loc)
	if !ok || !o.At.Equal(*sc.NextRunAt) {
		return Run{At: sc.NextRunAt.In(loc), Nominal: sc.NextRunAt.In(loc).Format("2006-01-02T15:04")}, true
	}
	r := Run{At: o.At.In(loc), Nominal: o.NominalString(), DST: o.DST}
	switch o.DST {
	case cron.DSTGap:
		r.DSTNotes = "the local time does not exist (clocks move forward); the run starts at the first instant after the gap"
	case cron.DSTRepeated:
		r.DSTNotes = "the local time occurs twice (clocks move back); the run starts once, at the first occurrence"
	}
	return r, true
}
