package audit

import (
	"container/heap"
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Anonymous failures (#180). Anyone who can reach the manager can make it
// write audit records: every call of a public API operation (sign-in,
// setup, invitation and reset redemption) and every refused /agent/v1
// request is recorded with an anonymous actor, and the size cap
// (Options.MaxBytes) purges the oldest records. So that a flood cannot
// push real history out of the trail (or keep the database busy), records
// with an anonymous actor and an outcome other than success are budgeted
// per window: AnonPerClient per client (an IPv4 address or an IPv6 /64)
// and AnonTotal overall. What the budget leaves out is counted and
// written as one summary record per action, outcome and error class
// (details "suppressed": how many, "clients": from how many clients,
// counted up to maxAnonSummaryClients, "topClients": the clients that
// sent the most, with their counts) once the window is over: by the next
// Record after it, or by the next Purge at the latest. Summaries a failed
// transaction could not write are kept for the next one.
const (
	// AnonWindow is the budget period.
	AnonWindow = time.Minute
	// AnonPerClient bounds the anonymous failures recorded per client and
	// window.
	AnonPerClient = 10
	// AnonTotal bounds the anonymous failures recorded per window from all
	// clients together.
	AnonTotal = 30

	// maxAnonClients bounds the per-client table; past it new clients are
	// over budget for the rest of the window.
	maxAnonClients = 10000
	// maxAnonSummaryClients bounds the clients a summary counts records of.
	maxAnonSummaryClients = 1000
	// anonTopClients is how many clients a summary names.
	anonTopClients = 10
)

// anonKey groups the records a summary stands for.
type anonKey struct {
	action, operationID string
	outcome             domain.AuditOutcome
	errorClass          string
}

// anonSummary counts the records of one key left out. It counts them per
// client for at most maxAnonSummaryClients clients; past that a new client
// replaces the one with the lowest count and takes over its count
// (Space-Saving, the lowest found in O(log n) through a min-heap), so the
// clients sending the most stay in it however many others there are.
type anonSummary struct {
	count   int
	clients map[string]*clientCount
	low     countHeap // lowest count first
}

type clientCount struct {
	client string
	n      int
	index  int // in low
}

func newAnonSummary() *anonSummary { return &anonSummary{clients: map[string]*clientCount{}} }

// add counts n records of client.
func (s *anonSummary) add(client string, n int) {
	s.count += n
	if c, ok := s.clients[client]; ok {
		c.n += n
		heap.Fix(&s.low, c.index)
		return
	}
	if len(s.clients) >= maxAnonSummaryClients {
		c := s.low[0]
		delete(s.clients, c.client)
		c.client, c.n = client, c.n+n
		s.clients[client] = c
		heap.Fix(&s.low, 0)
		return
	}
	c := &clientCount{client: client, n: n}
	s.clients[client] = c
	heap.Push(&s.low, c)
}

// merge adds the counts of o.
func (s *anonSummary) merge(o *anonSummary) {
	for _, c := range o.clients {
		s.add(c.client, c.n)
	}
	s.count += o.count - o.counted()
}

// counted is the sum of the per-client counts.
func (s *anonSummary) counted() int {
	n := 0
	for _, c := range s.clients {
		n += c.n
	}
	return n
}

// topClients are the anonTopClients clients with the most records, most
// first: the summary keeps who sent the flood.
func (s *anonSummary) topClients() []map[string]any {
	all := make([]clientCount, 0, len(s.clients))
	for _, c := range s.clients {
		all = append(all, *c)
	}
	slices.SortFunc(all, func(a, b clientCount) int {
		if a.n != b.n {
			return b.n - a.n
		}
		return strings.Compare(a.client, b.client)
	})
	out := []map[string]any{}
	for _, c := range all[:min(len(all), anonTopClients)] {
		out = append(out, map[string]any{"client": c.client, "count": c.n})
	}
	return out
}

// countHeap orders client counts lowest first (container/heap).
type countHeap []*clientCount

func (h countHeap) Len() int           { return len(h) }
func (h countHeap) Less(i, j int) bool { return h[i].n < h[j].n }
func (h countHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *countHeap) Push(x any) {
	c := x.(*clientCount)
	c.index = len(*h)
	*h = append(*h, c)
}
func (h *countHeap) Pop() any {
	old := *h
	c := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return c
}

// anonGate is the budget of anonymous failures for the current window and
// the summaries of what it left out.
type anonGate struct {
	mu      sync.Mutex
	start   time.Time
	total   int
	clients map[string]int
	// left are the records left out in the current window; due are those
	// of ended windows, not written yet.
	left, due map[anonKey]*anonSummary
}

// putDue merges summaries into due; the caller holds g.mu.
func (g *anonGate) putDue(sums map[anonKey]*anonSummary) {
	for k, s := range sums {
		if g.due == nil {
			g.due = map[anonKey]*anonSummary{}
		}
		if d, ok := g.due[k]; ok {
			d.merge(s)
		} else {
			g.due[k] = s
		}
	}
}

// restore gives back summaries takeDue returned that could not be written
// (their transaction failed), so the next write tries again.
func (g *anonGate) restore(sums map[anonKey]*anonSummary) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.putDue(sums)
}

// anonymousFailure reports whether rec is subject to the budget: an
// anonymous request (it has a client IP; internal work recorded without an
// actor has none) that did not succeed.
func anonymousFailure(rec *domain.AuditRecord) bool {
	return rec.Actor.Kind == domain.AuditActorAnonymous && rec.Outcome != domain.AuditSuccess && rec.ClientIP != ""
}

// roll starts a new window when the current one is over; the caller holds
// g.mu.
func (g *anonGate) roll(now time.Time) {
	if !g.start.IsZero() && now.Sub(g.start) < AnonWindow {
		return
	}
	g.putDue(g.left)
	g.start, g.total, g.clients, g.left = now, 0, map[string]int{}, nil
}

// admit decides whether rec is written. Records outside the budget are
// counted for their window's summary instead.
func (g *anonGate) admit(rec *domain.AuditRecord, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roll(now)
	client := anonClient(rec.ClientIP)
	n, known := g.clients[client]
	if g.total < AnonTotal && n < AnonPerClient && (known || len(g.clients) < maxAnonClients) {
		g.total++
		g.clients[client] = n + 1
		return true
	}
	k := anonKey{action: rec.Action, operationID: rec.OperationID, outcome: rec.Outcome, errorClass: rec.ErrorClass}
	if g.left == nil {
		g.left = map[anonKey]*anonSummary{}
	}
	s, ok := g.left[k]
	if !ok {
		s = newAnonSummary()
		g.left[k] = s
	}
	s.add(client, 1)
	return false
}

// takeDue returns the summaries of ended windows and forgets them.
func (g *anonGate) takeDue(now time.Time) map[anonKey]*anonSummary {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roll(now)
	due := g.due
	g.due = nil
	return due
}

// anonClient groups a client address like the sign-in throttle: IPv4 by
// address, IPv6 by /64.
func anonClient(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "unknown"
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// writeAnonSummaries appends one summary record per due key in tx and
// returns what it took from the gate, for the caller to give back
// (anonGate.restore) when the transaction does not commit. The records
// carry no client, request or target of their own (they stand for many
// requests); their details name the clients that sent the most.
func (l *Log) writeAnonSummaries(ctx context.Context, tx bun.IDB) (map[anonKey]*anonSummary, error) {
	due := l.anon.takeDue(l.clock.Now())
	for k, s := range due {
		ev := domain.AuditEvent{
			Action: k.action, OperationID: k.operationID, Outcome: k.outcome, ErrorClass: k.errorClass,
			Actor:   domain.AuditActor{Kind: domain.AuditActorAnonymous},
			Details: map[string]any{"suppressed": s.count, "clients": len(s.clients), "topClients": s.topClients()},
		}
		// A fresh context: the summary must not take the client IP or the
		// request ID of the request that happens to write it.
		rec, err := l.normalize(context.Background(), ev)
		if err != nil {
			return due, err
		}
		if err := l.appendRecord(ctx, tx, rec); err != nil {
			return due, err
		}
	}
	return due, nil
}

// inTxWithAnonSummaries runs fn in a transaction after writing the due
// summaries, and gives the summaries back when the transaction fails.
func (l *Log) inTxWithAnonSummaries(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	var due map[anonKey]*anonSummary
	err := l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if due, err = l.writeAnonSummaries(ctx, tx); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
	if err != nil {
		l.anon.restore(due)
	}
	return err
}
