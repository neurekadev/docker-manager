package audit

import (
	"context"
	"net/netip"
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
// counted up to maxAnonSummaryClients) once the window is over: by the
// next Record after it, or by the next Purge at the latest.
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
	// maxAnonSummaryClients bounds the distinct clients a summary counts.
	maxAnonSummaryClients = 1000
)

// anonKey groups the records a summary stands for.
type anonKey struct {
	action, operationID string
	outcome             domain.AuditOutcome
	errorClass          string
}

type anonSummary struct {
	count   int
	clients map[string]struct{}
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
	for k, s := range g.left {
		if g.due == nil {
			g.due = map[anonKey]*anonSummary{}
		}
		if d, ok := g.due[k]; ok {
			d.count += s.count
			for c := range s.clients {
				if len(d.clients) < maxAnonSummaryClients {
					d.clients[c] = struct{}{}
				}
			}
		} else {
			g.due[k] = s
		}
	}
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
		s = &anonSummary{clients: map[string]struct{}{}}
		g.left[k] = s
	}
	s.count++
	if len(s.clients) < maxAnonSummaryClients {
		s.clients[client] = struct{}{}
	}
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

// writeAnonSummaries appends one summary record per due key in tx. The
// records carry no client, request or target of their own: they stand for
// many requests.
func (l *Log) writeAnonSummaries(ctx context.Context, tx bun.IDB) error {
	for k, s := range l.anon.takeDue(l.clock.Now()) {
		ev := domain.AuditEvent{
			Action: k.action, OperationID: k.operationID, Outcome: k.outcome, ErrorClass: k.errorClass,
			Actor:   domain.AuditActor{Kind: domain.AuditActorAnonymous},
			Details: map[string]any{"suppressed": s.count, "clients": len(s.clients)},
		}
		// A fresh context: the summary must not take the client IP or the
		// request ID of the request that happens to write it.
		rec, err := l.normalize(context.Background(), ev)
		if err != nil {
			return err
		}
		if err := l.appendRecord(ctx, tx, rec); err != nil {
			return err
		}
	}
	return nil
}
