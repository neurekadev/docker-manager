package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
)

// anonFail records a failed anonymous sign-in from ip.
func (f *fixture) anonFail(ip string) {
	f.t.Helper()
	ctx := requestinfo.With(f.ctx, requestinfo.Info{ClientIP: netip.MustParseAddr(ip)})
	if err := f.log.Record(ctx, domain.AuditEvent{Action: "auth.sign_in", OperationID: "create-auth-session",
		Actor: domain.AuditActor{Kind: domain.AuditActorAnonymous}, Outcome: domain.AuditDenied, ErrorClass: "invalid_credentials"}); err != nil {
		f.t.Fatal(err)
	}
}

// summary is a summary record with its counts.
type summary struct {
	domain.AuditRecord
	Suppressed, Clients int
	TopClients          []topClient
}

type topClient struct {
	Client string `json:"client"`
	Count  int    `json:"count"`
}

// summaries returns the summary records (details "suppressed").
func summaries(t *testing.T, recs []domain.AuditRecord) []summary {
	t.Helper()
	var out []summary
	for _, r := range recs {
		var d struct {
			Suppressed *int        `json:"suppressed"`
			Clients    int         `json:"clients"`
			TopClients []topClient `json:"topClients"`
		}
		if err := json.Unmarshal(r.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d.Suppressed != nil {
			out = append(out, summary{AuditRecord: r, Suppressed: *d.Suppressed, Clients: d.Clients, TopClients: d.TopClients})
		}
	}
	return out
}

// TestAnonymousFailuresPerClientBudget: one client's anonymous failures
// are written up to the budget; the rest are counted and summarized once
// the window is over, by the next record.
func TestAnonymousFailuresPerClientBudget(t *testing.T) {
	f := newFixture(t)
	for range audit.AnonPerClient + 5 {
		f.anonFail("203.0.113.7")
	}
	if n := len(f.all()); n != audit.AnonPerClient {
		t.Fatalf("%d records, want the per-client budget %d", n, audit.AnonPerClient)
	}
	// Another client still has its own budget.
	f.anonFail("203.0.113.8")
	if n := len(f.all()); n != audit.AnonPerClient+1 {
		t.Fatalf("another client's failure was not recorded (%d records)", n)
	}

	f.clk.Advance(audit.AnonWindow)
	f.record(domain.AuditEvent{Action: "stack.deploy", Actor: audit.ServiceActor()})
	sums := summaries(t, f.all())
	if len(sums) != 1 {
		t.Fatalf("summaries %+v", sums)
	}
	s := sums[0]
	if s.Action != "auth.sign_in" || s.OperationID != "create-auth-session" || s.Outcome != domain.AuditDenied ||
		s.ErrorClass != "invalid_credentials" || s.Actor.Kind != domain.AuditActorAnonymous || s.ClientIP != "" {
		t.Fatalf("summary %+v", s)
	}
	if s.Suppressed != 5 || s.Clients != 1 || len(s.TopClients) != 1 || s.TopClients[0] != (topClient{"203.0.113.7", 5}) {
		t.Fatalf("summary counts %d from %d clients (%+v), want 5 from 203.0.113.7", s.Suppressed, s.Clients, s.TopClients)
	}
	// The new window has a fresh budget.
	f.anonFail("203.0.113.7")
	if n := len(f.all()); n != audit.AnonPerClient+1+2+1 {
		t.Fatalf("%d records after the window", n)
	}
	if rep := f.verify(); !rep.OK {
		t.Fatalf("chain %+v", rep)
	}
}

// TestAnonymousFailuresTotalBudget: failures spread over many clients
// stop at the total budget; IPv6 clients count per /64; Purge writes the
// summary when no record follows the window.
func TestAnonymousFailuresTotalBudget(t *testing.T) {
	f := newFixture(t)
	for i := range audit.AnonTotal + 20 {
		f.anonFail(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if n := len(f.all()); n != audit.AnonTotal {
		t.Fatalf("%d records, want the total budget %d", n, audit.AnonTotal)
	}
	f.clk.Advance(audit.AnonWindow)
	for i := range audit.AnonPerClient + 3 { // one /64, many addresses
		f.anonFail(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	f.clk.Advance(audit.AnonWindow)
	if _, err := f.log.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	var suppressed, clients int
	for _, s := range summaries(t, f.all()) {
		suppressed += s.Suppressed
		clients += s.Clients
	}
	if suppressed != 20+3 || clients != 20+1 {
		t.Fatalf("summaries count %d suppressed from %d clients, want 23 from 21", suppressed, clients)
	}
}

// TestBudgetSparesOtherRecords: successes, authenticated actors and
// internal work without a client are never budgeted.
func TestBudgetSparesOtherRecords(t *testing.T) {
	f := newFixture(t)
	ctx := requestinfo.With(f.ctx, requestinfo.Info{ClientIP: netip.MustParseAddr("203.0.113.7")})
	for range audit.AnonTotal + 5 {
		for _, ev := range []domain.AuditEvent{
			{Action: "auth.sign_in", Actor: domain.AuditActor{Kind: domain.AuditActorAnonymous}},
			{Action: "stack.deploy", Actor: domain.AuditActor{Kind: domain.AuditActorUser, UserID: "u-1"}, Outcome: domain.AuditDenied},
		} {
			if err := f.log.Record(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		f.record(domain.AuditEvent{Action: "backup.run", Actor: domain.AuditActor{Kind: domain.AuditActorAnonymous}, Outcome: domain.AuditError})
		f.clk.Advance(time.Second)
	}
	if n := len(f.all()); n != 3*(audit.AnonTotal+5) {
		t.Fatalf("%d records, want every one", n)
	}
}

// TestSummariesSurviveAFailedTransaction: summaries taken for a write
// whose transaction rolls back are written by the next one.
func TestSummariesSurviveAFailedTransaction(t *testing.T) {
	f := newFixture(t)
	for range audit.AnonPerClient + 2 {
		f.anonFail("203.0.113.7")
	}
	f.clk.Advance(audit.AnonWindow)
	// An invalid event fails its transaction after the summary was written in it.
	if err := f.log.Record(f.ctx, domain.AuditEvent{Action: "Not A Key"}); !errors.Is(err, audit.ErrInvalidEvent) {
		t.Fatalf("invalid event: %v", err)
	}
	if n := len(summaries(t, f.all())); n != 0 {
		t.Fatalf("%d summaries after the rollback", n)
	}
	f.record(domain.AuditEvent{Action: "stack.deploy", Actor: audit.ServiceActor()})
	sums := summaries(t, f.all())
	if len(sums) != 1 || sums[0].Suppressed != 2 {
		t.Fatalf("summaries after the next record: %+v", sums)
	}
}

// TestSummaryNamesTheHeaviestClients: an attacker cannot hide among many
// throwaway addresses: the clients sending the most stay in the summary.
func TestSummaryNamesTheHeaviestClients(t *testing.T) {
	f := newFixture(t)
	for i := range audit.AnonTotal { // spend the total budget
		f.anonFail(fmt.Sprintf("198.51.100.%d", i+1))
	}
	for i := range 1200 { // more throwaway clients than a summary counts
		f.anonFail(fmt.Sprintf("2001:db8:%x::1", i+1))
	}
	for range 7 {
		f.anonFail("203.0.113.7")
	}
	f.clk.Advance(audit.AnonWindow)
	f.record(domain.AuditEvent{Action: "stack.deploy", Actor: audit.ServiceActor()})
	sums := summaries(t, f.all())
	if len(sums) != 1 || sums[0].Suppressed != 1207 || len(sums[0].TopClients) == 0 || sums[0].TopClients[0].Client != "203.0.113.7" {
		t.Fatalf("summary %+v", sums)
	}
}
