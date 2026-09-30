package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/cron"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Defaults returns the schedule defaults: the instance's default time zone
// and one editable expression per kind.
func (s *Service) Defaults(ctx context.Context) (domain.ScheduleDefaults, error) {
	return s.defaults(ctx, s.db)
}

func (s *Service) defaults(ctx context.Context, db bun.IDB) (domain.ScheduleDefaults, error) {
	tz, rev, at, err := store.ScheduleSettings(ctx, db)
	if err != nil {
		return domain.ScheduleDefaults{}, err
	}
	edited, err := store.ScheduleDefaultCrons(ctx, db)
	if err != nil {
		return domain.ScheduleDefaults{}, err
	}
	out := domain.ScheduleDefaults{TimeZone: tz, Revision: rev, UpdatedAt: at}
	for _, k := range s.Kinds() {
		d := domain.ScheduleDefault{Kind: k.Key, Label: k.Label, Cron: k.Suggested, Suggested: k.Suggested, CatchUp: k.CatchUp}
		if c, ok := edited[k.Key]; ok {
			d.Cron = c
		}
		out.Kinds = append(out.Kinds, d)
	}
	return out, nil
}

// Default returns the expression and time zone a new policy of kind starts
// with (policy owners prefill their create forms and requests without an
// explicit schedule from it). Changing defaults never changes existing
// policies.
func (s *Service) Default(ctx context.Context, kind string) (cronExpr, tz string, err error) {
	d, err := s.Defaults(ctx)
	if err != nil {
		return "", "", err
	}
	k, ok := d.Default(kind)
	if !ok {
		return "", "", fmt.Errorf("%w: %s", domain.ErrScheduleKindUnknown, kind)
	}
	return k.Cron, d.TimeZone, nil
}

// DefaultsProblem is one invalid field of a defaults change.
type DefaultsProblem struct {
	// Kind is empty for the time zone.
	Kind    string
	Problem *cron.ParseError
}

// DefaultsError lists the invalid fields of a defaults change (errors.Is
// domain.ErrScheduleInvalid).
type DefaultsError struct {
	Problems []DefaultsProblem
	// UnknownKinds are kinds that do not exist.
	UnknownKinds []string
}

func (e *DefaultsError) Error() string {
	return fmt.Sprintf("invalid schedule defaults (%d problems, unknown kinds %v)", len(e.Problems), e.UnknownKinds)
}

// Is matches domain.ErrScheduleInvalid.
func (e *DefaultsError) Is(target error) bool { return target == domain.ErrScheduleInvalid }

// UpdateDefaults changes defaults if they are still at revision
// (domain.ErrRevisionConflict otherwise). Expressions are normalized.
func (s *Service) UpdateDefaults(ctx context.Context, revision int64, p domain.ScheduleDefaultsPatch) (before, after domain.ScheduleDefaults, err error) {
	derr := &DefaultsError{}
	crons := map[string]string{}
	for kind, expr := range p.Crons {
		if _, ok := s.Kind(kind); !ok {
			derr.UnknownKinds = append(derr.UnknownKinds, kind)
			continue
		}
		spec, err := cron.Parse(expr)
		var pe *cron.ParseError
		if errors.As(err, &pe) {
			derr.Problems = append(derr.Problems, DefaultsProblem{Kind: kind, Problem: pe})
			continue
		}
		crons[kind] = spec.String()
	}
	if p.TimeZone != nil {
		if _, err := cron.LoadLocation(*p.TimeZone); err != nil {
			var pe *cron.ParseError
			if errors.As(err, &pe) {
				derr.Problems = append(derr.Problems, DefaultsProblem{Problem: pe})
			}
		}
	}
	if len(derr.Problems) > 0 || len(derr.UnknownKinds) > 0 {
		slices.Sort(derr.UnknownKinds)
		slices.SortFunc(derr.Problems, func(a, b DefaultsProblem) int {
			if a.Kind < b.Kind {
				return -1
			}
			if a.Kind > b.Kind {
				return 1
			}
			return 0
		})
		return before, after, derr
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if before, err = s.defaults(ctx, tx); err != nil {
			return err
		}
		if before.Revision != revision {
			return domain.ErrRevisionConflict
		}
		if err := store.UpdateScheduleDefaults(ctx, tx, revision, p.TimeZone, crons, s.now()); err != nil {
			return err
		}
		after, err = s.defaults(ctx, tx)
		return err
	})
	return before, after, err
}

// PreviewRequest asks for the next runs of an expression.
type PreviewRequest struct {
	Cron string
	// TimeZone "" uses the instance default.
	TimeZone string
	// Kind (optional) adds its missed-run behavior to the notes.
	Kind string
	// From defaults to now; Count to 5 (at most 50).
	From  time.Time
	Count int
}

// Preview is the evaluation of an expression.
type Preview struct {
	Cron     string
	TimeZone string
	Kind     *Kind
	From     time.Time
	Runs     []Run
	// Notes explain DST, missed-run, restart and offline-agent behavior.
	Notes []string
}

// Run is one previewed run.
type Run struct {
	At       time.Time // in the preview's zone
	Nominal  string    // local wall-clock time the expression selected
	DST      cron.DST
	DSTNotes string
}

// Preview limits.
const (
	DefaultPreviewCount = 5
	MaxPreviewCount     = 50
)

// Preview evaluates an expression without saving or running anything.
// Invalid input returns *InvalidError (or ErrScheduleKindUnknown).
func (s *Service) Preview(ctx context.Context, req PreviewRequest) (Preview, error) {
	tz := req.TimeZone
	if tz == "" {
		d, err := s.Defaults(ctx)
		if err != nil {
			return Preview{}, err
		}
		tz = d.TimeZone
	}
	spec, loc, err := parseSpec(req.Cron, tz)
	if err != nil {
		return Preview{}, err
	}
	out := Preview{Cron: spec.String(), TimeZone: tz, From: req.From}
	if req.Kind != "" {
		k, ok := s.Kind(req.Kind)
		if !ok {
			return Preview{}, fmt.Errorf("%w: %s", domain.ErrScheduleKindUnknown, req.Kind)
		}
		out.Kind = &k
	}
	if out.From.IsZero() {
		out.From = s.now()
	}
	n := req.Count
	if n <= 0 {
		n = DefaultPreviewCount
	}
	n = min(n, MaxPreviewCount)
	var gap, repeated bool
	for _, o := range spec.NextN(out.From, loc, n) {
		r := Run{At: o.At.In(loc), Nominal: o.NominalString(), DST: o.DST}
		switch o.DST {
		case cron.DSTGap:
			gap = true
			r.DSTNotes = fmt.Sprintf("%s does not exist in %s (clocks move forward); the run starts at %s, the first instant after the gap",
				o.Nominal.Format("15:04 on 2006-01-02"), tz, r.At.Format("15:04 MST"))
		case cron.DSTRepeated:
			repeated = true
			r.DSTNotes = fmt.Sprintf("%s occurs twice in %s (clocks move back); the run starts once, at the first occurrence (%s)",
				o.Nominal.Format("15:04 on 2006-01-02"), tz, r.At.Format("15:04 MST"))
		}
		out.Runs = append(out.Runs, r)
	}
	out.Notes = notes(out.Kind, gap, repeated)
	return out, nil
}

func notes(k *Kind, gap, repeated bool) []string {
	out := []string{
		"Daylight saving time: a local time skipped when clocks move forward runs once at the first instant after the gap; " +
			"a local time that occurs twice when clocks move back runs once, at its first occurrence.",
	}
	if gap {
		out = append(out, "At least one of these runs falls into a daylight-saving gap (see its note).")
	}
	if repeated {
		out = append(out, "At least one of these runs falls into a repeated daylight-saving hour (see its note).")
	}
	switch {
	case k == nil:
		out = append(out, "Missed runs: when the manager was not running at a scheduled time, backups, repository verification "+
			"and update checks run once to catch up after it starts; prune and update runs skip missed runs. "+
			"Either way the schedule history records them.")
	case k.CatchUp == domain.CatchUpOnce:
		out = append(out, "Missed runs: when the manager was not running at a scheduled time, one catch-up run starts after it "+
			"starts again (never one per missed time); the history records the missed times.")
	default:
		out = append(out, "Missed runs: when the manager was not running at a scheduled time, the run is skipped and recorded "+
			"as missed; the next run happens at its scheduled time.")
	}
	return append(out,
		"Manager restarts never run a scheduled time twice: every run is recorded durably before its job is created.",
		"Overlaps: a run is skipped (and recorded) while the policy's previous run is still active.",
		"Offline environments: the run's job waits for the environment's agent up to the job kind's deadline, then fails "+
			"with agent_offline; the schedule history shows the job's result.",
		"Scheduled runs use Docker Manager's internal service identity, not the account that created the policy.",
	)
}
