package backup

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Retention (#10) follows restic's keep policies, computed by Docker Manager so
// the preview and the execution are the same decision: the executor
// forgets exactly the snapshot IDs Plan returns (restic forget <ids>),
// then prunes. On top of restic's rules a minimum recovery floor always
// keeps the newest MinKeep snapshots of each item, and the newest snapshot
// of an item is never removed.

// RetentionRules configure retention per policy.
type RetentionRules struct {
	Last    int `json:"last,omitempty"`
	Hourly  int `json:"hourly,omitempty"`
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
	Yearly  int `json:"yearly,omitempty"`
	// WithinDays keeps every snapshot taken within this many days of the
	// item's newest snapshot.
	WithinDays int `json:"withinDays,omitempty"`
	// MinKeep is the minimum recovery floor: at least this many of the
	// newest snapshots of each item are kept whatever the rules say.
	MinKeep int `json:"minKeep"`
}

// MaxRetentionCount bounds each rule.
const MaxRetentionCount = 10000

// Empty reports whether no rule is set (retention keeps everything).
func (r RetentionRules) Empty() bool {
	return r.Last == 0 && r.Hourly == 0 && r.Daily == 0 && r.Weekly == 0 && r.Monthly == 0 && r.Yearly == 0 && r.WithinDays == 0
}

// Validate checks the rules. Field names match the JSON keys.
func (r RetentionRules) Validate() map[string]string {
	errs := map[string]string{}
	check := func(name string, v int) {
		if v < 0 || v > MaxRetentionCount {
			errs[name] = fmt.Sprintf("must be between 0 and %d", MaxRetentionCount)
		}
	}
	check("last", r.Last)
	check("hourly", r.Hourly)
	check("daily", r.Daily)
	check("weekly", r.Weekly)
	check("monthly", r.Monthly)
	check("yearly", r.Yearly)
	check("withinDays", r.WithinDays)
	check("minKeep", r.MinKeep)
	if !r.Empty() && r.MinKeep < 1 {
		errs["minKeep"] = "must be at least 1 when retention rules are set (the minimum recovery floor)"
	}
	return errs
}

// RetentionSnapshot is a snapshot considered by retention.
type RetentionSnapshot struct {
	ID   string
	Time time.Time
	// Item groups snapshots (ItemOf the tags); each item is judged alone.
	Item string
}

// RetentionDecision is the fate of one snapshot.
type RetentionDecision struct {
	ID   string
	Time time.Time
	Item string
	Keep bool
	// Reasons lists the rules that keep it (last, hourly, daily, weekly,
	// monthly, yearly, within, floor, newest).
	Reasons []string
}

// RetentionPlan is the result of Plan.
type RetentionPlan struct {
	Decisions []RetentionDecision
}

// Remove returns the IDs to forget.
func (p RetentionPlan) Remove() []string {
	var out []string
	for _, d := range p.Decisions {
		if !d.Keep {
			out = append(out, d.ID)
		}
	}
	return out
}

// Kept counts kept snapshots.
func (p RetentionPlan) Kept() int {
	n := 0
	for _, d := range p.Decisions {
		if d.Keep {
			n++
		}
	}
	return n
}

// Plan applies rules to snapshots. Periods are evaluated in loc (the
// policy's time zone). Empty rules keep everything.
func Plan(rules RetentionRules, snaps []RetentionSnapshot, loc *time.Location) RetentionPlan {
	if loc == nil {
		loc = time.UTC
	}
	groups := map[string][]RetentionSnapshot{}
	for _, s := range snaps {
		groups[s.Item] = append(groups[s.Item], s)
	}
	items := make([]string, 0, len(groups))
	for k := range groups {
		items = append(items, k)
	}
	slices.Sort(items)
	var out RetentionPlan
	for _, item := range items {
		out.Decisions = append(out.Decisions, planGroup(rules, groups[item], loc)...)
	}
	return out
}

type bucketRule struct {
	name  string
	count int
	key   func(t time.Time) string
}

func planGroup(rules RetentionRules, snaps []RetentionSnapshot, loc *time.Location) []RetentionDecision {
	sorted := slices.Clone(snaps)
	// Newest first; ties by ID for determinism.
	slices.SortFunc(sorted, func(a, b RetentionSnapshot) int {
		if c := b.Time.Compare(a.Time); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	ds := make([]RetentionDecision, len(sorted))
	for i, s := range sorted {
		ds[i] = RetentionDecision{ID: s.ID, Time: s.Time, Item: s.Item}
	}
	if len(ds) == 0 {
		return nil
	}
	keep := func(i int, reason string) {
		ds[i].Keep = true
		if !slices.Contains(ds[i].Reasons, reason) {
			ds[i].Reasons = append(ds[i].Reasons, reason)
		}
	}
	if rules.Empty() {
		for i := range ds {
			keep(i, "no_rules")
		}
		return ds
	}
	for i := 0; i < len(ds) && i < rules.Last; i++ {
		keep(i, "last")
	}
	buckets := []bucketRule{
		{"hourly", rules.Hourly, func(t time.Time) string { return t.In(loc).Format("2006-01-02T15") }},
		{"daily", rules.Daily, func(t time.Time) string { return t.In(loc).Format("2006-01-02") }},
		{"weekly", rules.Weekly, func(t time.Time) string {
			y, w := t.In(loc).ISOWeek()
			return fmt.Sprintf("%04d-W%02d", y, w)
		}},
		{"monthly", rules.Monthly, func(t time.Time) string { return t.In(loc).Format("2006-01") }},
		{"yearly", rules.Yearly, func(t time.Time) string { return t.In(loc).Format("2006") }},
	}
	for _, b := range buckets {
		if b.count <= 0 {
			continue
		}
		last, n := "", 0
		for i := range ds {
			if n >= b.count {
				break
			}
			k := b.key(ds[i].Time)
			if k == last {
				continue
			}
			last = k
			keep(i, b.name)
			n++
		}
	}
	if rules.WithinDays > 0 {
		limit := ds[0].Time.Add(-time.Duration(rules.WithinDays) * 24 * time.Hour)
		for i := range ds {
			if !ds[i].Time.Before(limit) {
				keep(i, "within")
			}
		}
	}
	// The minimum recovery floor and the newest snapshot.
	keep(0, "newest")
	for i := 0; i < len(ds) && i < rules.MinKeep; i++ {
		if !ds[i].Keep {
			keep(i, "floor")
		}
	}
	return ds
}
