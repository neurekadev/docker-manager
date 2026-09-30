package jobs_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/jobs/jobstest"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// auditedHarness is a harness whose engine records into a real audit log.
func auditedHarness(t *testing.T) (*harness, *audit.Log) {
	t.Helper()
	var log *audit.Log
	h := newHarness(t, func(o *jobs.Options) {
		if log == nil {
			l, err := audit.New(audit.Options{DB: o.DB, Clock: o.Clock, Logger: testutil.Logger(t)})
			if err != nil {
				t.Fatal(err)
			}
			log = l
		}
		o.Audit = log
	})
	return h, log
}

// targetsFor returns one target per target type the kind's lock rules use.
func targetsFor(s jobspec.Spec) []domain.JobTarget {
	var out []domain.JobTarget
	for _, r := range s.Locks {
		if r.Source != jobspec.FromTargets {
			continue
		}
		if s.RootScoped && r.TargetType == domain.TargetVolume {
			continue // file jobs act inside exactly one root (the stack here)
		}
		id := "t-" + string(r.TargetType)
		if r.TargetType == domain.TargetPath || r.TargetType == domain.TargetDestinationPath {
			id = "/data/" + string(r.TargetType)
		}
		t := domain.JobTarget{Type: r.TargetType, ID: id}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func jobRecords(t *testing.T, h *harness, log *audit.Log, jobID string) []domain.AuditRecord {
	t.Helper()
	recs, err := log.Records(h.ctx, domain.AuditFilter{JobID: jobID, Ascending: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func actions(recs []domain.AuditRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Action)
	}
	return out
}

// TestEveryJobKindEmitsLifecycleAuditRecords runs every kind of the
// catalog (a kind added later is covered automatically) from enqueue to
// success and requires job.queued, job.started and job.finished records
// with the job ID, kind, capability, origin and targets (#30 Done-when:
// every job kind emits records). The records are written by the engine
// itself (Enqueue and transition), not by executors.
func TestEveryJobKindEmitsLifecycleAuditRecords(t *testing.T) {
	kinds := 0
	for _, spec := range jobspec.Catalog() {
		t.Run(string(spec.Kind), func(t *testing.T) {
			h, log := auditedHarness(t)
			env := ""
			if spec.RequiresEnvironment() {
				env = "env-1"
				h.disp.Connect(env)
			}
			if spec.Executor == domain.ExecutorManager {
				if err := h.eng.RegisterManagerExecutor(sim(spec.Kind, &jobstest.Effects{})); err != nil {
					t.Fatal(err)
				}
			}
			j := h.enqueue(jobs.Request{Kind: spec.Kind, Principal: authz.Principal{Kind: authz.KindUser, UserID: "alice"},
				EnvironmentID: env, Targets: targetsFor(spec), Input: map[string]any{"note": "x", "chmod": map[string]any{"mode": "0644"}}})
			caps, err := spec.Capabilities(j.Targets, j.Input)
			if err != nil {
				t.Fatal(err)
			}
			h.dispatch()
			if spec.Executor == domain.ExecutorManager {
				h.eng.Wait()
			} else {
				h.completeAll(env)
			}
			h.wantState(j.ID, domain.JobSucceeded)

			recs := jobRecords(t, h, log, j.ID)
			if got := actions(recs); !slices.Equal(got, []string{audit.ActionJobQueued, audit.ActionJobStarted, audit.ActionJobFinished}) {
				t.Fatalf("actions %v", got)
			}
			for _, r := range recs {
				var d map[string]any
				if err := json.Unmarshal(r.Details, &d); err != nil {
					t.Fatal(err)
				}
				if r.JobID != j.ID || r.Actor != (domain.AuditActor{Kind: domain.AuditActorUser, UserID: "alice"}) ||
					r.Category != domain.AuditOperations || r.Outcome != domain.AuditSuccess || r.EnvironmentID != env ||
					d["kind"] != string(spec.Kind) || d["capability"] != strings.Join(caps, ",") || d["origin"] != "manual" ||
					r.Targets[0] != (domain.AuditTarget{Type: "job", ID: j.ID}) || len(r.Targets) != len(j.Targets)+1 ||
					strings.Contains(string(r.Details), `"note"`) {
					t.Fatalf("record %+v %v", r, d)
				}
			}
			if d := string(recs[2].Details); !strings.Contains(d, `"state":"succeeded"`) {
				t.Fatalf("finished details %s", d)
			}
			if rep, err := log.Verify(h.ctx); err != nil || !rep.OK {
				t.Fatalf("verify %+v %v", rep, err)
			}
		})
		kinds++
	}
	if kinds != len(jobspec.Kinds()) || kinds < 30 {
		t.Fatalf("checked %d kinds of %d", kinds, len(jobspec.Kinds()))
	}
}

func TestJobAuditOriginsCancellationAndFailures(t *testing.T) {
	h, log := auditedHarness(t)
	h.disp.Connect("env-1")

	// Scheduled work runs as the service identity; API tokens name the
	// token and its owner.
	sched := h.enqueue(jobs.Request{Kind: jobspec.StackUpdate, Principal: authz.Service(), PolicyID: "pol-1",
		EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s1")}})
	tok := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, Principal: authz.Principal{Kind: authz.KindAPIToken, UserID: "bob", TokenID: "tok-1"},
		EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s2")}})
	if r := jobRecords(t, h, log, sched.ID)[0]; r.Actor != audit.ServiceActor() || !strings.Contains(string(r.Details), `"policyId":"pol-1"`) ||
		!strings.Contains(string(r.Details), `"origin":"scheduled"`) {
		t.Fatalf("scheduled record %+v", r)
	}
	if r := jobRecords(t, h, log, tok.ID)[0]; r.Actor != (domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: "bob", TokenID: "tok-1"}) {
		t.Fatalf("token record %+v", r)
	}

	// Cancelling a waiting job: the canceller is the actor of the request
	// record; the job finishes cancelled on behalf of its initiator.
	waiting := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s2")}})
	ctx, err := authz.WithPrincipal(h.ctx, authz.Principal{Kind: authz.KindUser, UserID: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Cancel(ctx, waiting.ID); err != nil {
		t.Fatal(err)
	}
	recs := jobRecords(t, h, log, waiting.ID)
	if got := actions(recs); !slices.Equal(got, []string{audit.ActionJobQueued, audit.ActionJobCancelRequested, audit.ActionJobFinished}) {
		t.Fatalf("cancel actions %v", got)
	}
	if recs[1].Actor.UserID != "carol" || recs[2].Actor.UserID != "alice" || recs[2].Outcome != domain.AuditFailure ||
		recs[2].ErrorClass != domain.ErrorCancelled || !strings.Contains(string(recs[2].Details), `"state":"cancelled"`) {
		t.Fatalf("cancel records %+v", recs)
	}
	// A repeated cancellation request records nothing new.
	if _, err := h.eng.Cancel(ctx, waiting.ID); err == nil {
		t.Fatal("cancel of a finished job succeeded")
	}

	// Failed and partial outcomes carry the error class and the item list
	// (e.g. what a prune deleted), never item or error messages.
	c := canary.New()
	secret := c.New(canary.EnvValue, "env value in agent output")
	h.dispatch()
	cmds := h.commands("env-1")
	if len(cmds) != 2 {
		t.Fatalf("commands %d", len(cmds))
	}
	for i, cmd := range cmds {
		h.ack("env-1", cmd, protocol.AckPayload{Accepted: true})
		res := protocol.ResultPayload{Outcome: "failed", ErrorClass: domain.ErrorStepFailed, Message: "compose said DB_PASSWORD=" + secret}
		if i == 1 {
			res = protocol.ResultPayload{Outcome: "partial", Message: secret, Items: []protocol.ItemPayload{
				{Name: "image:old", Status: domain.ItemSucceeded, Message: secret}, {Name: "volume:data", Status: domain.ItemFailed, Message: secret}}}
		}
		h.result("env-1", cmd, res)
	}
	byJob := map[string][]domain.AuditRecord{sched.ID: jobRecords(t, h, log, sched.ID), tok.ID: jobRecords(t, h, log, tok.ID)}
	for id, recs := range byJob {
		last := recs[len(recs)-1]
		if last.Action != audit.ActionJobFinished || recs[1].Action != audit.ActionJobStarted {
			t.Fatalf("job %s actions %v", id, actions(recs))
		}
		switch last.Outcome {
		case domain.AuditFailure:
			if last.ErrorClass != domain.ErrorStepFailed {
				t.Fatalf("failed record %+v", last)
			}
		case domain.AuditPartial:
			if !strings.Contains(string(last.Details), `{"name":"image:old","status":"succeeded"}`) {
				t.Fatalf("partial record %s", last.Details)
			}
		default:
			t.Fatalf("outcome %+v", last)
		}
	}
	all, err := log.Records(h.ctx, domain.AuditFilter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	c.AssertClean(t, "job audit records", all)
	if rep, err := log.Verify(h.ctx); err != nil || !rep.OK {
		t.Fatalf("verify %+v %v", rep, err)
	}
}
