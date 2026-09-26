// Package cron is Docker Manager's one cron parser and next-run calculator (#13).
// Every user-configurable scheduled job (backups, repository verification,
// update checks and runs, prune, later kinds) uses it through the manager's
// scheduler (internal/manager/scheduler); nothing else parses cron.
//
// Syntax: standard five fields "minute hour day-of-month month
// day-of-week". Each field is a comma-separated list of "*", a value "N",
// a range "N-M", each optionally followed by "/step" ("*/15", "8-18/2",
// "5/10" = 5 to the field's maximum in steps of 10). Months accept JAN-DEC
// and days of week SUN-SAT (case-insensitive); day of week 7 is Sunday
// like 0. When both day-of-month and day-of-week are restricted (neither
// starts with "*"), a day matches if EITHER matches (Vixie cron); otherwise
// both must. Macros (@daily), seconds, years and the Quartz extensions
// L, W, # and ? are not supported. An expression that can never match
// (0 0 30 2 *) is rejected.
//
// Time zones and DST (docs/architecture/scheduler.md): expressions are
// evaluated on the wall clock of an explicit IANA time zone. A local time
// that does not exist because clocks jump forward (a DST gap) fires once, at
// the first instant after the gap; several skipped times collapse into that
// one run. A local time that occurs twice because clocks fall back fires
// once, at its first occurrence; the repeated hour's second pass has no
// runs. Occurrence.DST annotates both cases for previews.
//
// This package is in-house rather than robfig/cron: robfig evaluates by
// stepping local wall-clock fields, which skips runs inside a DST gap and
// can run a repeated local time twice, while Docker Manager promises the two
// rules above.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for every binary that evaluates schedules
)

// MaxExpressionLength bounds an expression.
const MaxExpressionLength = 256

// searchYears bounds the search for the next match. An expression that
// only matches February 29 needs up to eight years (2096 -> 2104).
const searchYears = 9

// Field names used in ParseError.
const (
	FieldExpression = "expression"
	FieldMinute     = "minute"
	FieldHour       = "hour"
	FieldDayOfMonth = "day-of-month"
	FieldMonth      = "month"
	FieldDayOfWeek  = "day-of-week"
	FieldTimeZone   = "timeZone"
)

// ParseError explains why an expression or time zone is invalid.
type ParseError struct {
	// Field is one of the Field* constants.
	Field string
	// Value is the offending field text (may be empty).
	Value string
	// Message is a plain-language explanation.
	Message string
}

func (e *ParseError) Error() string {
	if e.Field == FieldExpression || e.Field == FieldTimeZone {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// ErrInvalid is matched by every *ParseError (errors.Is).
var ErrInvalid = errors.New("invalid cron expression")

// Is reports ErrInvalid.
func (e *ParseError) Is(target error) bool { return target == ErrInvalid }

type fieldDef struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	months = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
		"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
	weekdays = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}
	fields   = [5]fieldDef{
		{FieldMinute, 0, 59, nil},
		{FieldHour, 0, 23, nil},
		{FieldDayOfMonth, 1, 31, nil},
		{FieldMonth, 1, 12, months},
		{FieldDayOfWeek, 0, 7, weekdays},
	}
)

// Schedule is a parsed expression. The zero value matches nothing; use Parse.
type Schedule struct {
	expr                          string
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// Parse parses a five-field expression. Errors are *ParseError.
func Parse(expr string) (*Schedule, error) {
	if len(expr) > MaxExpressionLength {
		return nil, &ParseError{Field: FieldExpression, Message: fmt.Sprintf("the expression is longer than %d characters", MaxExpressionLength)}
	}
	parts := strings.Fields(expr)
	if len(parts) == 1 && strings.HasPrefix(parts[0], "@") {
		return nil, &ParseError{Field: FieldExpression, Value: parts[0],
			Message: "macros such as " + parts[0] + " are not supported; use five fields (minute hour day-of-month month day-of-week), e.g. 0 2 * * * for daily at 02:00"}
	}
	if len(parts) != 5 {
		return nil, &ParseError{Field: FieldExpression, Value: expr,
			Message: fmt.Sprintf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))}
	}
	s := &Schedule{expr: strings.Join(parts, " ")}
	targets := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, p := range parts {
		bits, err := parseField(p, fields[i])
		if err != nil {
			return nil, err
		}
		*targets[i] = bits
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domStar = strings.HasPrefix(parts[2], "*")
	s.dowStar = strings.HasPrefix(parts[4], "*")
	if err := s.checkSatisfiable(); err != nil {
		return nil, err
	}
	return s, nil
}

// MustParse parses expr and panics on error (constants and tests).
func MustParse(expr string) *Schedule {
	s, err := Parse(expr)
	if err != nil {
		panic(err)
	}
	return s
}

// String returns the normalized expression (fields separated by one space).
func (s *Schedule) String() string { return s.expr }

func parseField(text string, f fieldDef) (uint64, error) {
	fail := func(format string, args ...any) error {
		return &ParseError{Field: f.name, Value: text, Message: fmt.Sprintf(format, args...)}
	}
	var bits uint64
	for _, item := range strings.Split(text, ",") {
		if item == "" {
			return 0, fail("empty list item in %q", text)
		}
		rng, stepText, hasStep := strings.Cut(item, "/")
		lo, hi := f.min, f.max
		switch rng {
		case "*":
		case "":
			return 0, fail("missing value before /%s", stepText)
		default:
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = value(a, f); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = value(b, f); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fail("range %s goes backwards (wrap-around ranges are not supported; use a list, e.g. 22-23,0-2)", rng)
				}
			} else if hasStep {
				hi = f.max // "5/10" = 5 to max in steps of 10
			}
		}
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fail("step %q must be a positive whole number", stepText)
			}
			if n > f.max-f.min+1 {
				return 0, fail("step %d is larger than the field's range %d-%d", n, f.min, f.max)
			}
			step = n
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v) //nolint:gosec // v is within 0..59
		}
	}
	return bits, nil
}

func value(text string, f fieldDef) (int, error) {
	if text == "" {
		return 0, &ParseError{Field: f.name, Value: text, Message: "missing value"}
	}
	if f.names != nil {
		if v, ok := f.names[strings.ToUpper(text)]; ok {
			return v, nil
		}
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			msg := fmt.Sprintf("%q is not a number", text)
			switch {
			case strings.ContainsAny(text, "LW#?"):
				msg = fmt.Sprintf("%q uses an unsupported extension (L, W, # and ? are not standard five-field cron)", text)
			case f.names != nil:
				msg = fmt.Sprintf("%q is neither a number nor a known name", text)
			}
			return 0, &ParseError{Field: f.name, Value: text, Message: msg}
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil || v < f.min || v > f.max {
		return 0, &ParseError{Field: f.name, Value: text, Message: fmt.Sprintf("%s is outside %d-%d", text, f.min, f.max)}
	}
	return v, nil
}

// daysIn is the longest length of each month (February in a leap year).
var daysIn = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// checkSatisfiable rejects expressions that never match, e.g. 0 0 30 2 *.
// Only the day-of-month/month combination can be impossible: with a
// restricted day of week the OR rule always leaves a match.
func (s *Schedule) checkSatisfiable() error {
	if s.domStar || !s.dowStar {
		return nil
	}
	for m := 1; m <= 12; m++ {
		if s.month&(1<<uint(m)) == 0 {
			continue
		}
		for d := 1; d <= daysIn[m]; d++ {
			if s.dom&(1<<uint(d)) != 0 {
				return nil
			}
		}
	}
	return &ParseError{Field: FieldDayOfMonth, Message: "the expression never matches: none of the selected days exists in the selected months"}
}

func (s *Schedule) dayMatches(c time.Time) bool {
	dom := s.dom&(1<<uint(c.Day())) != 0
	dow := s.dow&(1<<uint(c.Weekday())) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// nextCivil returns the first civil (wall-clock) minute >= c that matches.
// Civil times are represented as UTC times without zone meaning, so the
// arithmetic never sees a DST transition.
func (s *Schedule) nextCivil(c time.Time) (time.Time, bool) {
	limit := c.AddDate(searchYears, 0, 0)
	for !c.After(limit) {
		switch {
		case s.month&(1<<uint(c.Month())) == 0:
			c = time.Date(c.Year(), c.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(c):
			c = time.Date(c.Year(), c.Month(), c.Day()+1, 0, 0, 0, 0, time.UTC)
		case s.hour&(1<<uint(c.Hour())) == 0:
			c = time.Date(c.Year(), c.Month(), c.Day(), c.Hour()+1, 0, 0, 0, time.UTC)
		case s.minute&(1<<uint(c.Minute())) == 0:
			c = c.Add(time.Minute)
		default:
			return c, true
		}
	}
	return time.Time{}, false
}

// DST says how a run relates to a daylight-saving transition.
type DST string

// DST annotations.
const (
	// DSTNone: the local time exists exactly once.
	DSTNone DST = ""
	// DSTGap: the local time does not exist (clocks jumped forward); the
	// run fires once at the first instant after the gap.
	DSTGap DST = "gap"
	// DSTRepeated: the local time occurs twice (clocks fell back); the run
	// fires once, at the first occurrence.
	DSTRepeated DST = "repeated"
)

// Occurrence is one run time.
type Occurrence struct {
	// At is the instant the run fires (in the schedule's location).
	At time.Time
	// Nominal is the local wall-clock time the expression selected, as a
	// civil time in UTC (only Year..Minute are meaningful). It differs from
	// At's wall clock only for DSTGap.
	Nominal time.Time
	DST     DST
}

// NominalString formats Nominal as "2006-01-02T15:04".
func (o Occurrence) NominalString() string { return o.Nominal.Format("2006-01-02T15:04") }

// Next returns the first run strictly after `after`, evaluated in loc.
// ok is false only when no run exists within the search horizon (never
// for a parsed expression in practice).
func (s *Schedule) Next(after time.Time, loc *time.Location) (Occurrence, bool) {
	if loc == nil {
		loc = time.UTC
	}
	c := civil(after.In(loc)).Truncate(time.Minute)
	for {
		m, ok := s.nextCivil(c)
		if !ok {
			return Occurrence{}, false
		}
		at, dst := resolve(m, loc)
		// Runs are monotonic in civil order (first occurrences and gap
		// ends never go backwards), so the first civil match whose run is
		// after `after` is the next run.
		if at.After(after) {
			return Occurrence{At: at, Nominal: m, DST: dst}, true
		}
		c = m.Add(time.Minute)
	}
}

// NextN returns up to n runs after `after`.
func (s *Schedule) NextN(after time.Time, loc *time.Location, n int) []Occurrence {
	out := make([]Occurrence, 0, n)
	for len(out) < n {
		o, ok := s.Next(after, loc)
		if !ok {
			break
		}
		out = append(out, o)
		after = o.At
	}
	return out
}

// civil returns t's wall clock as a zone-less civil time (UTC).
func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// resolve maps a civil minute to the instant it fires in loc.
func resolve(c time.Time, loc *time.Location) (time.Time, DST) {
	wall := time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), 0, 0, loc)
	offsets := map[int]bool{}
	add := func(t time.Time) {
		_, off := t.Zone()
		offsets[off] = true
	}
	add(wall)
	start, end := wall.ZoneBounds()
	if !start.IsZero() {
		add(start.Add(-time.Second))
	}
	if !end.IsZero() {
		add(end)
	}
	var valid []time.Time
	for off := range offsets {
		u := time.Unix(c.Unix()-int64(off), 0).In(loc)
		if civil(u).Equal(c) {
			valid = append(valid, u)
		}
	}
	switch len(valid) {
	case 0:
	case 1:
		return valid[0], DSTNone
	default:
		first := valid[0]
		for _, v := range valid[1:] {
			if v.Before(first) {
				first = v
			}
		}
		return first, DSTRepeated
	}
	// A gap: fire at the transition that skipped c (the first instant after
	// the gap).
	var best time.Time
	for off := range offsets {
		u := time.Unix(c.Unix()-int64(off), 0).In(loc)
		for _, t := range []time.Time{u, wall} {
			s, _ := t.ZoneBounds()
			if s.IsZero() {
				continue
			}
			if civil(s.Add(-time.Nanosecond)).Before(c) && civil(s).After(c) && (best.IsZero() || s.Before(best)) {
				best = s
			}
		}
	}
	if best.IsZero() {
		return wall, DSTGap // unreachable for real zone data; stay monotonic-safe
	}
	return best.In(loc), DSTGap
}

// LoadLocation loads an IANA time zone for schedules. "UTC" is accepted;
// empty names and "Local" are rejected (a schedule's zone is always
// explicit). Errors are *ParseError on FieldTimeZone.
func LoadLocation(name string) (*time.Location, error) {
	switch {
	case name == "":
		return nil, &ParseError{Field: FieldTimeZone, Message: "a time zone is required (an IANA name such as Europe/Berlin or UTC)"}
	case name == "Local" || len(name) > 64:
		return nil, &ParseError{Field: FieldTimeZone, Value: name, Message: fmt.Sprintf("%q is not an IANA time zone name", name)}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, &ParseError{Field: FieldTimeZone, Value: name, Message: fmt.Sprintf("unknown time zone %q (use an IANA name such as Europe/Berlin or UTC)", name)}
	}
	return loc, nil
}
