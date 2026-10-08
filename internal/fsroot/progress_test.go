package fsroot

import (
	"strings"
	"testing"
	"time"
)

type sent struct {
	percent int
	message string
}

func newTestProgress(now *time.Time) (*jobProgress, *[]sent) {
	var out []sent
	p := &jobProgress{
		send: func(percent int, message string) { out = append(out, sent{percent, message}) },
		now:  func() time.Time { return *now },
	}
	return p, &out
}

func TestJobProgressReportsMembersAndBytesAtMostEveryInterval(t *testing.T) {
	now := time.Unix(1000, 0)
	p, out := newTestProgress(&now)
	p.total, p.size = 4, 1000
	p.next("config/app.yml")
	p.add(100) // within the interval: not sent
	now = now.Add(progressEvery)
	p.add(150)
	now = now.Add(progressEvery)
	p.next("data/db.sqlite")
	want := []sent{
		{0, "1 of 4 · config/app.yml"},
		{25, "1 of 4 · config/app.yml"},
		{25, "2 of 4 · data/db.sqlite"},
	}
	if len(*out) != len(want) {
		t.Fatalf("sent %+v, want %+v", *out, want)
	}
	for i := range want {
		if (*out)[i] != want[i] {
			t.Fatalf("report %d: %+v, want %+v", i, (*out)[i], want[i])
		}
	}
}

func TestJobProgressNeverReports100AndFallsBackToMembers(t *testing.T) {
	now := time.Unix(1000, 0)
	p, out := newTestProgress(&now)
	p.size = 10
	p.next("a")
	now = now.Add(progressEvery)
	p.add(20) // more read than the size (a zip's directory): capped
	if got := (*out)[len(*out)-1]; got.percent != 99 || got.message != "a" {
		t.Fatalf("capped report %+v", got)
	}

	p, out = newTestProgress(&now)
	p.total = 4
	p.next("a")
	now = now.Add(progressEvery)
	p.next("b")
	now = now.Add(progressEvery)
	p.next("c")
	if got := (*out)[2]; got.percent != 50 || got.message != "3 of 4 · c" {
		t.Fatalf("by members %+v", got)
	}
}

func TestJobProgressNilIsANoOp(t *testing.T) {
	var p *jobProgress
	p.next("a")
	p.add(10)
	if n, err := p.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

func TestProgressNameKeepsTheEndAndDropsControlCharacters(t *testing.T) {
	if got := progressName("a\nb\x7f"); got != "a?b?" {
		t.Fatalf("control characters: %q", got)
	}
	long := strings.Repeat("d/", 200) + "file.txt"
	got := progressName(long)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "file.txt") || len(got) > maxProgressName+len("…") {
		t.Fatalf("long name: %q", got)
	}
}

func TestJobProgressWaitsForTheFirstMember(t *testing.T) {
	now := time.Unix(1000, 0)
	p, out := newTestProgress(&now)
	p.total, p.size = 2, 100
	p.add(4) // the format sniff
	p.next("a")
	if len(*out) != 1 || (*out)[0] != (sent{4, "1 of 2 · a"}) {
		t.Fatalf("sent %+v", *out)
	}
}
