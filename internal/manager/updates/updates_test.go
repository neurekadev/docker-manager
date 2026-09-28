package updates_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/regclient"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// shop is the fixture stack: web (interpolated tag from .env, an env_file
// and an override file) depends on db with restart propagation; worker
// depends on db without it.
func (h *harness) shop() (domain.Stack, string, string) {
	h.t.Helper()
	web, db := h.ref("acme/web:1.4"), h.ref("acme/db:16")
	yaml := fmt.Sprintf(`services:
  db:
    image: %s
  web:
    image: %s/acme/web:${WEB_TAG}
    env_file: [web.env]
    depends_on:
      db:
        condition: service_started
        restart: true
`, db, h.reg.Host())
	st := h.deployStack("st-shop", "shop", yaml, map[string]string{
		".env": "WEB_TAG=1.4\n", "web.env": "API_KEY=never-touched\n", "compose.override.yaml": "services:\n  web:\n    labels:\n      team: shop\n",
	}, []stackService{
		{name: "db", image: db, running: true},
		{name: "web", image: web, running: true, deps: []lifecycle.Dependency{{Service: "db", Condition: lifecycle.ConditionStarted, Required: true, Restart: true}}},
	})
	return st, web, db
}

func (h *harness) connection(password string) domain.RegistryConnection {
	h.t.Helper()
	c, err := h.regs.Create(h.ctx, domain.RegistryConnectionInput{Name: "fake registry " + password, Host: h.reg.Host(), Username: "robot",
		Secret: password, RepositoryPattern: "acme/*"})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) private(repos ...string) {
	h.reg.Lock()
	defer h.reg.Unlock()
	for _, r := range repos {
		h.reg.Private[r] = true
	}
}

func updateCode(err error) string {
	var ue *domain.UpdateError
	if errors.As(err, &ue) {
		return ue.Code
	}
	return ""
}

// A version-tagged Compose service updates when the same tag points to a
// new host-platform digest (authenticated through a registry connection,
// #19), and is not recreated while the digest is unchanged. The Compose
// file, the override file, .env and the env_file stay byte-for-byte
// identical; another stack sharing the tag keeps its container.
func TestTaggedServiceFollowsItsDigest(t *testing.T) {
	h := newHarness(t)
	conn := h.connection(h.reg.Password)
	h.private("acme/web", "acme/db")
	d1 := h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, web, _ := h.shop()
	blog := h.deployStack("st-blog", "blog", "services:\n  web:\n    image: "+web+"\n", nil, []stackService{{name: "web", image: web, running: true}})
	before := h.snapshot("shop")
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	if p.Check.Enabled || p.Run.Enabled || p.Check.Cron != "0 3 * * *" || p.Run.Cron != "0 4 * * *" {
		t.Fatalf("new policy schedules %+v %+v: defaults, disabled", p.Check, p.Run)
	}

	// Unchanged digest: up to date, a run has nothing to do (no pull).
	if j := h.check(p); j.State != domain.JobSucceeded {
		t.Fatalf("check %s %s: %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	c := h.candidates(p)
	if c["web"].Status != domain.CandidateUpToDate || c["db"].Status != domain.CandidateUpToDate || c["web"].RegistryConnectionID != conn.ID ||
		c["web"].AppliedDigest != d1 || c["web"].Platform != "linux/amd64" || c["web"].Reference != web || c["web"].Tag != "1.4" {
		t.Fatalf("candidates %+v", c)
	}
	if c["web"].SourceHashBefore != st.Applied.Hash || c["web"].SourceHashAfter != st.Applied.Hash {
		t.Errorf("source hashes %s %s, want %s", c["web"].SourceHashBefore, c["web"].SourceHashAfter, st.Applied.Hash)
	}
	pullsBefore := pulls(h.engine)
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrNoCandidates {
		t.Fatalf("run with an unchanged digest: %v", err)
	}
	if pulls(h.engine) != pullsBefore || h.updateRuns() != 0 {
		t.Fatal("an unchanged digest pulled or enqueued a run")
	}

	// The same tag names a new build.
	d2 := h.publish("acme/web", "1.4", " v2")
	h.clk.Advance(2 * time.Minute) // past the check cache
	h.check(p)
	c = h.candidates(p)
	if c["web"].Status != domain.CandidateAvailable || c["web"].CandidateDigest != d2 || c["db"].Status != domain.CandidateUpToDate {
		t.Fatalf("after the tag moved: %+v", c)
	}
	pv, err := h.svc.Preview(h.ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Items) != 1 || pv.Items[0].Candidate.Service != "web" || !pv.Items[0].Running || pv.SourceDrift || pv.Fingerprint == "" {
		t.Fatalf("preview %+v", pv)
	}
	if len(pv.SharedTag) != 1 || pv.SharedTag[0].StackID != blog.ID || pv.SharedTag[0].Service != "web" {
		t.Fatalf("shared-tag consumers %+v", pv.SharedTag)
	}
	if len(pv.Dependencies) != 2 {
		t.Errorf("dependencies %+v", pv.Dependencies)
	}

	j, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID, Fingerprint: pv.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if j = h.finish(j); j.State != domain.JobSucceeded {
		t.Fatalf("run %s %s: %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	c = h.candidates(p)
	if c["web"].Status != domain.CandidateUpToDate || c["web"].AppliedDigest != d2 || c["web"].PreviousDigest != d1 {
		t.Fatalf("after the run: %+v", c["web"])
	}
	hist, _ := h.svc.History(h.ctx, p.ID, 10)
	if len(hist) != 1 || hist[0].Outcome != domain.UpdateOutcomeUpdated || hist[0].FromDigest != d1 || hist[0].ToDigest != d2 ||
		hist[0].RegistryConnectionID != conn.ID || hist[0].SourceHashBefore != st.Applied.Hash || hist[0].SourceHashAfter != st.Applied.Hash {
		t.Fatalf("history %+v", hist)
	}
	stNow, _ := h.stacks.Get(h.ctx, st.ID)
	for _, img := range stNow.Images {
		if img.Service == "web" && img.Digest != d2 {
			t.Errorf("stack baseline %+v", img)
		}
	}
	// The pull authenticated with the connection's account.
	auths := h.engine.PullAuths()
	if a := auths[len(auths)-1]; a == nil || a.Username != "robot" {
		t.Errorf("pull auth %+v", a)
	}
	shopWeb, _ := h.engine.Container("shop-web-1")
	blogWeb, _ := h.engine.Container("blog-web-1")
	if shopWeb.Details.ImageID == blogWeb.Details.ImageID || !shopWeb.Details.State.Running {
		t.Errorf("shop web %s running=%v, blog web %s: the opted-in stack updates, the other keeps its container",
			shopWeb.Details.ImageID, shopWeb.Details.State.Running, blogWeb.Details.ImageID)
	}
	h.assertSame(before, h.snapshot("shop"))
	// A stale preview is refused.
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID, Fingerprint: pv.Fingerprint}); updateCode(err) != domain.UpdateErrPreviewStale {
		t.Fatalf("stale preview: %v", err)
	}
}

// A failed update is reported, its candidate digest quarantined (no retry
// loop) and audited; the source files are untouched; a newer digest
// becomes a new candidate.
func TestFailedUpdateIsQuarantined(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, web, _ := h.shop()
	before := h.snapshot("shop")
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	bad := h.publish("acme/web", "1.4", " broken")
	// The new image never becomes healthy.
	if _, err := h.engine.PullImage(h.ctx, web, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	h.engine.SetStartHealth(web, "unhealthy")
	h.check(p)
	if c := h.candidates(p); c["web"].Status != domain.CandidateAvailable || c["web"].CandidateDigest != bad {
		t.Fatalf("candidates %+v", c)
	}
	j := h.run(p)
	if j.State != domain.JobFailed || j.ErrorClass != protocol.UpdateClassUnhealthy || !strings.Contains(j.Recovery, "no automatic rollback") {
		t.Fatalf("run %s %s %q", j.State, j.ErrorClass, j.Recovery)
	}
	c := h.candidates(p)
	if c["web"].Status != domain.CandidateQuarantined || !strings.Contains(c["web"].ErrorMessage, "pin the previous digest") {
		t.Fatalf("after the failure: %+v", c["web"])
	}
	qs, _ := h.svc.Quarantine(h.ctx, p.ID)
	if len(qs) != 1 || qs[0].Digest != bad || qs[0].JobID != j.ID {
		t.Fatalf("quarantine %+v", qs)
	}
	recs, err := h.audit.Records(h.ctx, domain.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(recs, func(r domain.AuditRecord) bool { return r.Action == updates.ActionQuarantine && r.JobID == j.ID }) {
		t.Error("the quarantine is not audited")
	}
	h.assertSame(before, h.snapshot("shop"))

	// No retry loop: the next check keeps it quarantined and neither a
	// manual nor a scheduled run applies it.
	h.clk.Advance(2 * time.Minute)
	h.check(p)
	if c := h.candidates(p); c["web"].Status != domain.CandidateQuarantined {
		t.Fatalf("recheck: %+v", c["web"])
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrNoCandidates {
		t.Fatalf("run of a quarantined digest: %v", err)
	}
	if reqs, err := h.svc.RunSource().Jobs(h.ctx, scheduler.Due{PolicyID: p.ID}); err != nil || len(reqs) != 0 {
		t.Fatalf("scheduled run of a quarantined digest: %v %v", reqs, err)
	}
	// A newer digest is a new candidate.
	fixed := h.publish("acme/web", "1.4", " fixed")
	h.clk.Advance(2 * time.Minute)
	h.check(p)
	if c := h.candidates(p); c["web"].Status != domain.CandidateAvailable || c["web"].CandidateDigest != fixed {
		t.Fatalf("new digest: %+v", c["web"])
	}
}

// The registry index changes but the host-platform manifest does not: no
// update (and no pull); another platform's stack sees its change.
func TestIndexChangeWithTheSamePlatformManifestIsNoUpdate(t *testing.T) {
	h := newHarness(t)
	put := func(salt string) string {
		return h.reg.Put("acme/multi", "m"+salt, regclient.MediaOCIManifest, []byte(protocolManifest+salt))
	}
	index := func(amd, arm string) string {
		return `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
			`{"digest":"` + amd + `","platform":{"os":"linux","architecture":"amd64"}},` +
			`{"digest":"` + arm + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}}]}`
	}
	amdA, armA := put("a"), put("b")
	idx1 := h.reg.Put("acme/multi", "2", regclient.MediaOCIIndex, []byte(index(amdA, armA)))
	ref := h.ref("acme/multi:2")
	h.engine.Publish(ref, idx1) // the Engine records the index digest
	amdStack := h.deployStack("st-amd", "amd", "services:\n  app:\n    image: "+ref+"\n", nil, []stackService{{name: "app", image: ref, running: true}})
	armStack := h.deployStack("st-arm", "arm", "services:\n  app:\n    image: "+ref+"\n", nil, []stackService{{name: "app", image: ref, running: true}})
	armStack.Images[0].Platform = "linux/arm64/v8"
	h.stacks.put(armStack)
	pAmd := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: amdStack.ID})
	pArm := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: armStack.ID})
	h.check(pAmd)
	h.check(pArm)
	if a, b := h.candidates(pAmd)["app"], h.candidates(pArm)["app"]; a.Status != domain.CandidateUpToDate || b.Status != domain.CandidateUpToDate {
		t.Fatalf("applied index digest: %+v %+v", a, b)
	}
	// Only the arm64 manifest changes: the index digest changes too.
	armB := put("c")
	idx2 := h.reg.Put("acme/multi", "2", regclient.MediaOCIIndex, []byte(index(amdA, armB)))
	h.clk.Advance(2 * time.Minute)
	h.check(pAmd)
	h.check(pArm)
	a, b := h.candidates(pAmd)["app"], h.candidates(pArm)["app"]
	if a.Status != domain.CandidateUpToDate {
		t.Fatalf("amd64: the index changed, its platform manifest did not: %+v", a)
	}
	if b.Status != domain.CandidateAvailable || b.CandidateDigest != armB || b.CandidateIndexDigest != idx2 {
		t.Fatalf("arm64: %+v", b)
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: pAmd.ID}); updateCode(err) != domain.UpdateErrNoCandidates {
		t.Fatalf("amd64 run: %v", err)
	}
	// Now the amd64 manifest changes as well.
	amdB := put("d")
	idx3 := h.reg.Put("acme/multi", "2", regclient.MediaOCIIndex, []byte(index(amdB, armB)))
	h.clk.Advance(2 * time.Minute)
	h.check(pAmd)
	if a := h.candidates(pAmd)["app"]; a.Status != domain.CandidateAvailable || a.CandidateDigest != amdB || a.CandidateIndexDigest != idx3 {
		t.Fatalf("amd64 after its manifest changed: %+v", a)
	}
}

const protocolManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:c"},"layers":[]}`

// 401 and 429 are recorded on the candidate with their class and retry
// guidance; they never lead to a pull, and the rate-limit cooldown stops
// repeated registry requests.
func TestRegistryRefusalsNeverPull(t *testing.T) {
	h := newHarness(t)
	h.private("acme/web")
	h.connection("wrong-password")
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	h.publish("acme/web", "1.4", " v2")
	pullsBefore := pulls(h.engine)
	h.check(p)
	c := h.candidates(p)
	if c["web"].Status != domain.CandidateCheckFailed || c["web"].ErrorClass != regclient.ClassUnauthorized {
		t.Fatalf("401: %+v", c["web"])
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrNoCandidates {
		t.Fatalf("run after 401: %v", err)
	}
	if reqs, err := h.svc.RunSource().Jobs(h.ctx, scheduler.Due{PolicyID: p.ID}); err != nil || len(reqs) != 0 {
		t.Fatalf("scheduled run after 401: %v %v", reqs, err)
	}

	// 429 with a long Retry-After on the public db image.
	h.clk.Advance(2 * time.Minute)
	h.reg.FailNext(429, map[string]string{"Retry-After": "3600"}, 1)
	h.check(p)
	c = h.candidates(p)
	if c["db"].Status != domain.CandidateCheckFailed || c["db"].ErrorClass != regclient.ClassRateLimited || c["db"].RetryAfterSeconds != 3600 {
		t.Fatalf("429: %+v", c["db"])
	}
	hits, _, _ := h.reg.Counts()
	h.clk.Advance(10 * time.Second)
	h.check(p)
	after, _, _ := h.reg.Counts()
	if c := h.candidates(p); c["db"].ErrorClass != regclient.ClassRateLimited {
		t.Fatalf("cooldown: %+v", c["db"])
	}
	// Only the 401 web checks reached the registry; db stays in cooldown.
	if after-hits > 2 {
		t.Errorf("registry requests during the cooldown: %d", after-hits)
	}
	if pulls(h.engine) != pullsBefore || h.updateRuns() != 0 {
		t.Fatalf("pulled %d times / %d runs after 401/429", pulls(h.engine)-pullsBefore, h.updateRuns())
	}
}

// A run whose pull is refused (401/429 on the agent) fails without
// quarantine; the candidate needs a new successful check before another
// run pulls again.
func TestRefusedPullNeedsANewCheck(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	h.publish("acme/web", "1.4", " v2")
	h.check(p)
	h.engine.Fail("image.pull", enginefake.Err("image.pull", engine.CodeUnauthorized, "unauthorized: authentication required"))
	j := h.run(p)
	if j.State != domain.JobFailed || j.ErrorClass != string(engine.CodeUnauthorized) {
		t.Fatalf("run %s %s", j.State, j.ErrorClass)
	}
	c := h.candidates(p)
	if c["web"].Status != domain.CandidateRunFailed || c["web"].ErrorClass != "unauthorized" {
		t.Fatalf("candidate %+v", c["web"])
	}
	if qs, _ := h.svc.Quarantine(h.ctx, p.ID); len(qs) != 0 {
		t.Fatal("a refused pull quarantined the digest")
	}
	pullsBefore := pulls(h.engine)
	if reqs, err := h.svc.RunSource().Jobs(h.ctx, scheduler.Due{PolicyID: p.ID}); err != nil || len(reqs) != 0 {
		t.Fatalf("scheduled run right after a refused pull: %v %v", reqs, err)
	}
	if pulls(h.engine) != pullsBefore {
		t.Fatal("pulled again")
	}
	h.clk.Advance(2 * time.Minute)
	h.check(p)
	if c := h.candidates(p); c["web"].Status != domain.CandidateAvailable {
		t.Fatalf("after a new check: %+v", c["web"])
	}
}

// Undeployed edits: the check reports them unchanged on disk; a run is
// refused (an update never deploys an edit or writes a file).
func TestUndeployedEditsRefuseTheRun(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	h.publish("acme/web", "1.4", " v2")
	h.check(p)
	st.Observed = &domain.RevisionRef{ID: "rev-2", Seq: 2, Hash: strings.Repeat("e", 64)}
	h.stacks.put(st)
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrSourceDrift {
		t.Fatalf("run with undeployed changes: %v", err)
	}
	var rej *scheduler.Rejection
	if _, err := h.svc.RunSource().Jobs(h.ctx, scheduler.Due{PolicyID: p.ID}); !errors.As(err, &rej) || rej.Class != updates.RejectSourceDrift {
		t.Fatalf("scheduled run with undeployed changes: %v", err)
	}
	if pv, _ := h.svc.Preview(h.ctx, p.ID, nil); !pv.SourceDrift {
		t.Error("the preview does not show the drift")
	}
}

// No automatic check or update happens before the user enables the
// policy's schedules; enabled, the scheduled run applies the update inside
// the window only.
func TestNothingHappensBeforeAPolicyIsEnabled(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	h.publish("acme/web", "1.4", " v2")
	// A day passes (03:00 checks, 04:00 runs): nothing is enqueued.
	for range 24 * 4 {
		h.clk.Advance(15 * time.Minute)
		if err := h.sched.Tick(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	js, _ := h.eng.List(h.ctx, domain.JobFilter{Limit: 100})
	if len(js) != 0 {
		t.Fatalf("jobs before the policy was enabled: %d", len(js))
	}
	if err := h.svc.RunSource().Validate(h.ctx, p.ID); err == nil {
		t.Fatal("a disabled run schedule validated")
	}
	if err := h.svc.CheckSource().Validate(h.ctx, p.ID); err == nil {
		t.Fatal("a disabled check schedule validated")
	}
	// Enable both, with a run window 04:00-05:00 UTC.
	on := func(s domain.UpdateSchedule) *domain.UpdateSchedule { s.Enabled = true; return &s }
	_, p, err := h.svc.Update(h.ctx, p.ID, p.Revision, domain.UpdatePolicyPatch{Check: on(p.Check), Run: on(p.Run),
		Window: &domain.UpdateWindow{Start: "04:00", End: "05:00"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 24 * 4 {
		h.clk.Advance(15 * time.Minute)
		if err := h.sched.Tick(h.ctx); err != nil {
			t.Fatal(err)
		}
		h.dispatch()
		h.eng.Wait()
		h.playAgent()
	}
	checks, _ := h.eng.List(h.ctx, domain.JobFilter{Kinds: []domain.JobKind{"update.check"}, Limit: 100})
	runs, _ := h.eng.List(h.ctx, domain.JobFilter{Kinds: []domain.JobKind{"update.run"}, Limit: 100})
	if len(checks) != 1 || checks[0].Origin != domain.OriginScheduled || len(runs) != 1 || runs[0].State != domain.JobSucceeded {
		t.Fatalf("scheduled checks %d, runs %d (%v)", len(checks), len(runs), runs)
	}
	if c := h.candidates(p); c["web"].Status != domain.CandidateUpToDate {
		t.Fatalf("after the scheduled run: %+v", c["web"])
	}
	// Outside the window a scheduled run is refused.
	p.Window = &domain.UpdateWindow{Start: "01:00", End: "02:00"}
	if updates.InWindow(p, time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)) {
		t.Fatal("04:00 is inside 01:00-02:00")
	}
}

func TestUpdateWindows(t *testing.T) {
	p := domain.UpdatePolicy{Run: domain.UpdateSchedule{TimeZone: "Europe/Berlin"}}
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	p.Window = &domain.UpdateWindow{Start: "23:00", End: "02:00", Days: []int{6}} // Saturday night
	for ts, want := range map[string]bool{
		"2026-09-26T21:30:00Z": true,  // Sat 23:30 Berlin
		"2026-09-26T23:30:00Z": true,  // Sun 01:30 Berlin, window started Saturday
		"2026-09-27T00:30:00Z": false, // Sun 02:30
		"2026-09-27T21:30:00Z": false, // Sun 23:30: not a window day
		"2026-09-26T20:30:00Z": false, // Sat 22:30
	} {
		if got := updates.InWindow(p, at(ts)); got != want {
			t.Errorf("%s: %v, want %v", ts, got, want)
		}
	}
	p.Window = nil
	if !updates.InWindow(p, at("2026-09-26T12:00:00Z")) {
		t.Error("no window means always")
	}
}

// A managed standalone container updates from its saved specification;
// unmanaged and Docker Manager's own containers cannot be targets.
func TestStandaloneContainerPolicy(t *testing.T) {
	h := newHarness(t)
	ref := h.ref("acme/api:3")
	old := h.publish("acme/api", "3", "")
	if _, err := h.engine.PullImage(h.ctx, ref, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	id := h.res.standalone(h.ctx, protocol.ContainerSpec{Name: "api", Image: ref, Env: []string{"TOKEN=keep"}, RestartPolicy: "always"}, true)
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetContainer, TargetID: "api"})
	h.check(p)
	if c := h.candidates(p)["api"]; c.Status != domain.CandidateUpToDate || c.AppliedDigest != old || c.Platform != "linux/amd64" {
		t.Fatalf("candidate %+v", c)
	}
	d2 := h.publish("acme/api", "3", " v2")
	h.clk.Advance(2 * time.Minute)
	h.check(p)
	if c := h.candidates(p)["api"]; c.Status != domain.CandidateAvailable || c.CandidateDigest != d2 {
		t.Fatalf("candidate %+v", c)
	}
	j := h.run(p)
	if j.State != domain.JobSucceeded {
		t.Fatalf("run %s %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	c, ok := h.engine.Container("api")
	if !ok || c.Details.ID == id || !c.Details.State.Running || c.Details.Image != ref || !slices.Equal(c.Env, []string{"TOKEN=keep"}) {
		t.Fatalf("recreated container %+v env %v", c.Details, c.Env)
	}
	if got := h.candidates(p)["api"]; got.Status != domain.CandidateUpToDate || got.AppliedDigest != d2 || got.PreviousDigest != old {
		t.Fatalf("after the run: %+v", got)
	}

	// Unmanaged and protected containers are refused as targets.
	h.engine.AddContainer(engine.ContainerSpec{Name: "manual", Image: ref}, true)
	if _, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "manual", TargetType: domain.UpdateTargetContainer,
		TargetID: "manual"}); updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("unmanaged container: %v", err)
	}
	h.engine.AddContainer(engine.ContainerSpec{Name: "docker-agent", Image: ref, Labels: map[string]string{protocol.LabelRole: "agent"}}, true)
	if _, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "agent", TargetType: domain.UpdateTargetContainer,
		TargetID: "docker-agent"}); updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("Docker Manager's own container: %v", err)
	}
	// One policy per target.
	if _, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "again", TargetType: domain.UpdateTargetContainer,
		TargetID: "api"}); !errors.Is(err, domain.ErrUpdatePolicyTargetUsed) {
		t.Fatalf("second policy: %v", err)
	}
}

// Ineligible services are visible with their reason and never checked.
func TestIneligibleServicesAreVisible(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	web := h.ref("acme/web:1.4")
	st := h.deployStack("st-mix", "mix", "services:\n  web:\n    image: "+web+"\n", nil, []stackService{{name: "web", image: web, running: true}})
	pinned := h.ref("acme/web") + "@" + st.Images[0].Digest
	st.Services = append(st.Services,
		domain.StackServiceDef{Name: "pinned", Image: pinned},
		domain.StackServiceDef{Name: "built", Image: "mix-built", Build: true},
		domain.StackServiceDef{Name: "untagged", Image: h.ref("acme/web")},
		domain.StackServiceDef{Name: "always", Image: web, PullPolicy: "always"},
		domain.StackServiceDef{Name: "excluded", Image: web},
		domain.StackServiceDef{Name: "latest", Image: h.ref("acme/web:latest")},
	)
	h.stacks.put(st)
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID, ExcludeServices: []string{"excluded"}})
	h.check(p)
	c := h.candidates(p)
	for svc, reason := range map[string]string{"pinned": domain.UpdateReasonDigestPinned, "built": domain.UpdateReasonBuildOnly,
		"untagged": domain.UpdateReasonUntagged, "always": domain.UpdateReasonPullPolicy, "excluded": domain.UpdateReasonExcluded,
		"latest": domain.UpdateReasonNotDeployed} {
		if c[svc].Status != domain.CandidateIneligible || c[svc].Reason != reason || c[svc].ReasonMessage == "" {
			t.Errorf("%s: %+v, want %s", svc, c[svc], reason)
		}
	}
	if !c["latest"].NonVersionTag || c["web"].NonVersionTag || c["web"].Status != domain.CandidateUpToDate {
		t.Errorf("latest %+v web %+v", c["latest"], c["web"])
	}
}

// A new candidate shows when its image was created (the image config's
// created time, display only). The time is read once per digest, and a
// check never fails because it is unavailable.
func TestCandidatePublishTime(t *testing.T) {
	h := newHarness(t)
	h.publishImage("acme/web", "1.4", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	h.check(p)
	if c := h.candidates(p); c["web"].Status != domain.CandidateUpToDate || c["web"].CandidatePublishedAt != nil {
		t.Fatalf("up to date: %+v", c["web"])
	}

	published := time.Date(2026, 9, 22, 7, 45, 0, 0, time.UTC)
	d2 := h.publishImage("acme/web", "1.4", published, " v2")
	h.publish("acme/db", "16", " db v2") // its manifest names no config blob
	h.clk.Advance(2 * time.Minute)       // past the check cache
	if j := h.check(p); j.State != domain.JobSucceeded {
		t.Fatalf("check %s %s: %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	c := h.candidates(p)
	if c["web"].Status != domain.CandidateAvailable || c["web"].CandidateDigest != d2 || c["web"].CandidatePublishedAt == nil ||
		!c["web"].CandidatePublishedAt.Equal(published) {
		t.Fatalf("new web image: %+v", c["web"])
	}
	if c["db"].Status != domain.CandidateAvailable || c["db"].CandidatePublishedAt != nil {
		t.Fatalf("new db image without a creation time: %+v", c["db"])
	}

	// The next check reuses the recorded time (no blob request).
	blobs := h.reg.BlobCount()
	h.clk.Advance(2 * time.Minute)
	h.check(p)
	if c := h.candidates(p); c["web"].CandidatePublishedAt == nil || !c["web"].CandidatePublishedAt.Equal(published) {
		t.Fatalf("second check: %+v", c["web"])
	}
	if n := h.reg.BlobCount(); n != blobs {
		t.Fatalf("blob requests on the second check: %d", n-blobs)
	}
}
