package resources

import (
	"sync"

	"github.com/neurekadev/dockyard/internal/agent/engine"
)

// pullProgress aggregates the per-layer messages of a pull into one
// percentage and reports it only when it grew by at least progressStep, so
// a pull of many layers does not flood the job's event log (#26 bounds it
// anyway).
type pullProgress struct {
	mu     sync.Mutex
	report func(percent int, message string)
	layers map[string][2]int64
	order  []string
	last   int
}

const progressStep = 5

func newPullProgress(report func(int, string)) *pullProgress {
	return &pullProgress{report: report, layers: map[string][2]int64{}, last: -1}
}

func (p *pullProgress) add(ev engine.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.ID == "" {
		return
	}
	cur, seen := p.layers[ev.ID]
	if !seen {
		p.order = append(p.order, ev.ID)
	}
	switch ev.Status {
	case "Download complete", "Pull complete", "Already exists":
		if cur[1] == 0 {
			cur[1] = 1
		}
		cur[0] = cur[1]
	default:
		if ev.Total > 0 {
			cur = [2]int64{min(ev.Current, ev.Total), ev.Total}
		}
	}
	p.layers[ev.ID] = cur
	var done, total int64
	for _, id := range p.order {
		l := p.layers[id]
		done, total = done+l[0], total+l[1]
	}
	if total == 0 {
		return
	}
	percent := int(done * 100 / total)
	if percent >= 100 {
		percent = 99 // 100 is reported when the pull has finished
	}
	if percent-p.last < progressStep && p.last >= 0 {
		return
	}
	p.last = percent
	p.report(percent, "pulling layers")
}
