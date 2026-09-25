package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseValid(t *testing.T) {
	for expr, want := range map[string]string{
		"0 2 * * *":              "0 2 * * *",
		"  0   3  *  * 0 ":       "0 3 * * 0",
		"*/15 8-18/2 1,15 * 1-5": "*/15 8-18/2 1,15 * 1-5",
		"0 0 * JAN-mar sun":      "0 0 * JAN-mar sun",
		"5/10 * * * 7":           "5/10 * * * 7",
		"0 0 29 2 *":             "0 0 29 2 *",
		"0 0 31 2 1":             "0 0 31 2 1", // OR rule: Mondays in February
	} {
		s, err := Parse(expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", expr, err)
			continue
		}
		if s.String() != want {
			t.Errorf("Parse(%q).String() = %q, want %q", expr, s.String(), want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for expr, field := range map[string]string{
		"":                                      FieldExpression,
		"* * * *":                               FieldExpression,
		"* * * * * *":                           FieldExpression,
		"@daily":                                FieldExpression,
		"60 * * * *":                            FieldMinute,
		"-1 * * * *":                            FieldMinute,
		"* 24 * * *":                            FieldHour,
		"* * 0 * *":                             FieldDayOfMonth,
		"* * 32 * *":                            FieldDayOfMonth,
		"* * * 13 *":                            FieldMonth,
		"* * * FOO *":                           FieldMonth,
		"* * * * 8":                             FieldDayOfWeek,
		"* * * * MON-SUNDAY":                    FieldDayOfWeek,
		"*/0 * * * *":                           FieldMinute,
		"*/61 * * * *":                          FieldMinute,
		"*/x * * * *":                           FieldMinute,
		"5-1 * * * *":                           FieldMinute,
		"1,,2 * * * *":                          FieldMinute,
		"/5 * * * *":                            FieldMinute,
		"* * L * *":                             FieldDayOfMonth,
		"* * * * 1#2":                           FieldDayOfWeek,
		"* * ? * *":                             FieldDayOfMonth,
		"0 0 30 2 *":                            FieldDayOfMonth, // never matches
		"0 0 31 4,6,9,11 *":                     FieldDayOfMonth,
		strings.Repeat("1,", 200) + "1 * * * *": FieldExpression,
	} {
		_, err := Parse(expr)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("Parse(%q) = %v, want a *ParseError", expr, err)
			continue
		}
		if pe.Field != field || pe.Message == "" || !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error field %q (%v), want %q", expr, pe.Field, err, field)
		}
	}
}

func TestLoadLocation(t *testing.T) {
	for _, ok := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Kolkata"} {
		if _, err := LoadLocation(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", "../etc/passwd", "Europe/Berlin/.."} {
		var pe *ParseError
		if _, err := LoadLocation(bad); !errors.As(err, &pe) || pe.Field != FieldTimeZone {
			t.Errorf("LoadLocation(%q) = %v", bad, err)
		}
	}
}

func runs(t *testing.T, expr, zone, after string, n int) []Occurrence {
	t.Helper()
	return MustParse(expr).NextN(utc(after), mustLoc(t, zone), n)
}

func checkRuns(t *testing.T, got []Occurrence, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d runs, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		at, dst, _ := strings.Cut(w, " ")
		if !got[i].At.Equal(utc(at)) || string(got[i].DST) != dst {
			t.Errorf("run %d = %s (%s, dst %q), want %s dst %q", i, got[i].At.UTC().Format(time.RFC3339),
				got[i].At.Format(time.RFC3339), got[i].DST, at, dst)
		}
	}
}

func TestNextBasics(t *testing.T) {
	checkRuns(t, runs(t, "0 2 * * *", "UTC", "2026-01-01T02:00:00Z", 2),
		"2026-01-02T02:00:00Z", "2026-01-03T02:00:00Z")
	// Strictly after: a run exactly at `after` is not returned again.
	checkRuns(t, runs(t, "*/20 * * * *", "UTC", "2026-01-01T00:00:00Z", 3),
		"2026-01-01T00:20:00Z", "2026-01-01T00:40:00Z", "2026-01-01T01:00:00Z")
	// Weekly Sunday 03:00 (2026-01-04 is a Sunday), 7 == 0.
	checkRuns(t, runs(t, "0 3 * * 7", "UTC", "2026-01-01T00:00:00Z", 2),
		"2026-01-04T03:00:00Z", "2026-01-11T03:00:00Z")
	// Day-of-month OR day-of-week when both are restricted: the 13th or Fridays.
	checkRuns(t, runs(t, "0 0 13 * 5", "UTC", "2026-02-01T00:00:00Z", 3),
		"2026-02-06T00:00:00Z", "2026-02-13T00:00:00Z", "2026-02-20T00:00:00Z")
	// Leap day only.
	checkRuns(t, runs(t, "0 0 29 2 *", "UTC", "2026-01-01T00:00:00Z", 2),
		"2028-02-29T00:00:00Z", "2032-02-29T00:00:00Z")
	// Zone offset (India, no DST).
	checkRuns(t, runs(t, "30 9 * * *", "Asia/Kolkata", "2026-01-01T00:00:00Z", 1), "2026-01-01T04:00:00Z")
}

// New York springs forward on 2026-03-08 (02:00 EST -> 03:00 EDT) and falls
// back on 2026-11-01 (02:00 EDT -> 01:00 EST).
func TestDSTNewYork(t *testing.T) {
	// Daily 02:30: the skipped local time fires once at the first instant
	// after the gap (03:00 EDT = 07:00Z).
	checkRuns(t, runs(t, "30 2 * * *", "America/New_York", "2026-03-07T00:00:00Z", 3),
		"2026-03-07T07:30:00Z", "2026-03-08T07:00:00Z gap", "2026-03-09T06:30:00Z")
	// Every 15 minutes across the gap: 02:00-02:45 collapse into one run at
	// 03:00 EDT, which is also the 03:00 run itself.
	checkRuns(t, runs(t, "*/15 * * * *", "America/New_York", "2026-03-08T06:30:00Z", 4),
		"2026-03-08T06:45:00Z", "2026-03-08T07:00:00Z gap", "2026-03-08T07:15:00Z", "2026-03-08T07:30:00Z")
	// Daily 01:30 on fall-back day: 01:30 occurs twice; it fires once, at
	// the first occurrence (EDT, 05:30Z), not at 01:30 EST (06:30Z).
	checkRuns(t, runs(t, "30 1 * * *", "America/New_York", "2026-10-31T12:00:00Z", 2),
		"2026-11-01T05:30:00Z repeated", "2026-11-02T06:30:00Z")
	// Every 30 minutes: the repeated hour's second pass has no runs.
	checkRuns(t, runs(t, "*/30 * * * *", "America/New_York", "2026-11-01T04:00:00Z", 5),
		"2026-11-01T04:30:00Z", "2026-11-01T05:00:00Z repeated", "2026-11-01T05:30:00Z repeated",
		"2026-11-01T07:00:00Z", "2026-11-01T07:30:00Z")
	// Evaluating from inside the second pass does not re-run the repeated
	// local time.
	checkRuns(t, runs(t, "30 1 * * *", "America/New_York", "2026-11-01T06:10:00Z", 1), "2026-11-02T06:30:00Z")
	// Daily 02:30 on fall-back day is unambiguous (02:30 EST exists once).
	checkRuns(t, runs(t, "30 2 * * *", "America/New_York", "2026-11-01T00:00:00Z", 1), "2026-11-01T07:30:00Z")
}

// Berlin springs forward on 2026-03-29 (02:00 CET -> 03:00 CEST) and falls
// back on 2026-10-25 (03:00 CEST -> 02:00 CET).
func TestDSTBerlin(t *testing.T) {
	checkRuns(t, runs(t, "30 2 * * *", "Europe/Berlin", "2026-03-28T00:00:00Z", 3),
		"2026-03-28T01:30:00Z", "2026-03-29T01:00:00Z gap", "2026-03-30T00:30:00Z")
	checkRuns(t, runs(t, "0 2 * * *", "Europe/Berlin", "2026-03-28T12:00:00Z", 1), "2026-03-29T01:00:00Z gap")
	// Hourly across the gap: 01:00 CET, then 03:00 CEST (02:00 collapsed), 04:00.
	checkRuns(t, runs(t, "0 * * * *", "Europe/Berlin", "2026-03-28T23:30:00Z", 3),
		"2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z gap", "2026-03-29T02:00:00Z")
	checkRuns(t, runs(t, "30 2 * * *", "Europe/Berlin", "2026-10-24T12:00:00Z", 2),
		"2026-10-25T00:30:00Z repeated", "2026-10-26T01:30:00Z")
	checkRuns(t, runs(t, "0 * * * *", "Europe/Berlin", "2026-10-24T23:30:00Z", 4),
		"2026-10-25T00:00:00Z repeated", "2026-10-25T02:00:00Z", "2026-10-25T03:00:00Z", "2026-10-25T04:00:00Z")
}

// A daily schedule runs exactly once per local day in every zone, across
// several years of transitions (including zones whose DST starts at
// midnight or shifts by 30 minutes), and runs strictly increase.
func TestDailyRunsOncePerLocalDay(t *testing.T) {
	zones := []string{"America/New_York", "Europe/Berlin", "America/Santiago", "America/Havana",
		"Australia/Lord_Howe", "Asia/Tehran", "Pacific/Apia", "UTC"}
	exprs := []string{"0 0 * * *", "30 2 * * *", "0 1 * * *", "59 23 * * *"}
	for _, zone := range zones {
		loc := mustLoc(t, zone)
		for _, expr := range exprs {
			s := MustParse(expr)
			start := utc("2024-01-01T00:00:00Z")
			got := s.NextN(start, loc, 3*366)
			days := map[string]int{}
			prev := start
			for _, o := range got {
				if !o.At.After(prev) {
					t.Fatalf("%s %q: run %s not after %s", zone, expr, o.At, prev)
				}
				prev = o.At
				days[o.Nominal.Format("2006-01-02")]++
				if o.DST == DSTNone && !civil(o.At.In(loc)).Equal(o.Nominal) {
					t.Fatalf("%s %q: run %s does not show nominal %s", zone, expr, o.At.In(loc), o.Nominal)
				}
			}
			for d, n := range days {
				if n != 1 {
					t.Errorf("%s %q: %d runs on %s", zone, expr, n, d)
				}
			}
			first, last := got[0].Nominal, got[len(got)-1].Nominal
			if want := int(last.Sub(first).Hours()/24) + 1; len(days) != want {
				t.Errorf("%s %q: %d days with runs between %s and %s, want %d (a day was skipped)", zone, expr, len(days), first, last, want)
			}
		}
	}
}
