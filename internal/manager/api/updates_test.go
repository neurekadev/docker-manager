package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/policy"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeUpdates is an in-memory UpdateService for the HTTP mapping and
// shaping tests (the real service is tested in internal/manager/updates).
type fakeUpdates struct {
	mu       sync.Mutex
	policies map[string]domain.UpdatePolicy
	runErr   error
	runs     []updates.RunRequest
	checks   int
}

func (f *fakeUpdates) Get(_ context.Context, id string) (domain.UpdatePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[id]
	if !ok {
		return p, domain.ErrUpdatePolicyNotFound
	}
	return p, nil
}

func (f *fakeUpdates) List(_ context.Context, env, after string, _ int) ([]domain.UpdatePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.UpdatePolicy
	for _, id := range []string{"pol-1", "pol-2"} {
		if p, ok := f.policies[id]; ok && id > after && (env == "" || p.EnvironmentID == env) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeUpdates) Create(_ context.Context, np updates.NewPolicy) (domain.UpdatePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.policies {
		if p.TargetID == np.TargetID {
			return p, domain.ErrUpdatePolicyTargetUsed
		}
	}
	p := domain.UpdatePolicy{ID: "pol-2", EnvironmentID: np.EnvironmentID, Name: np.Name, TargetType: np.TargetType, TargetID: np.TargetID,
		Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}, Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"}, Revision: 1}
	f.policies[p.ID] = p
	return p, nil
}

func (f *fakeUpdates) Update(_ context.Context, id string, rev int64, patch domain.UpdatePolicyPatch) (domain.UpdatePolicy, domain.UpdatePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.policies[id]
	if cur.Revision != rev {
		return cur, cur, domain.ErrRevisionMismatch
	}
	next := cur
	if patch.Run != nil {
		next.Run = *patch.Run
	}
	next.Revision++
	f.policies[id] = next
	return cur, next, nil
}

func (f *fakeUpdates) Delete(_ context.Context, id string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.policies, id)
	return nil
}

func (f *fakeUpdates) Candidates(context.Context, string) ([]domain.UpdateCandidate, error) {
	published := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	return []domain.UpdateCandidate{{ID: "c-1", Service: "web", Reference: "nginx:1.27", Eligible: true, Status: domain.CandidateQuarantined,
		AppliedDigest: "sha256:old", CandidateDigest: "sha256:new", CandidatePublishedAt: &published}}, nil
}

func (f *fakeUpdates) Quarantine(context.Context, string) ([]domain.UpdateQuarantine, error) {
	return nil, nil
}

func (f *fakeUpdates) History(context.Context, string, int) ([]domain.UpdateHistoryEntry, error) {
	return nil, nil
}

func (f *fakeUpdates) ScheduleStatus(context.Context, string, string, int) (updates.ScheduleStatus, error) {
	return updates.ScheduleStatus{}, nil
}

func (f *fakeUpdates) StartCheck(_ context.Context, _ authz.Principal, id, _ string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return domain.Job{ID: "job-check", Kind: "update.check", State: domain.JobQueued, PolicyID: id}, nil
}

func (f *fakeUpdates) Preview(_ context.Context, id string, _ []string) (domain.UpdatePreview, error) {
	return domain.UpdatePreview{Policy: f.policies[id], Fingerprint: "fp-1", SourceDrift: true,
		SharedTag: []domain.UpdateSharedConsumer{{Reference: "nginx:1.27", StackID: "st-9", StackName: "blog", Service: "web"}}}, nil
}

func (f *fakeUpdates) Run(_ context.Context, r updates.RunRequest) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, r)
	if f.runErr != nil {
		return domain.Job{}, f.runErr
	}
	return domain.Job{ID: "job-run", Kind: "update.run", State: domain.JobQueued, PolicyID: r.PolicyID}, nil
}

func (f *fakeUpdates) ForTarget(_ context.Context, env string, typ domain.UpdateTargetType, id string) (*domain.UpdatePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.policies {
		if p.EnvironmentID == env && p.TargetType == typ && p.TargetID == id {
			return &p, nil
		}
	}
	return nil, nil
}

var _ UpdateService = (*fakeUpdates)(nil)

func updatesAPIFor(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeUpdates) {
	t.Helper()
	stacks := newFakeStacks()
	svc := &fakeUpdates{policies: map[string]domain.UpdatePolicy{
		"pol-1": {ID: "pol-1", EnvironmentID: "env-1", Name: "Shop updates", TargetType: domain.UpdateTargetStack, TargetID: "st-1",
			Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}, Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"}, Revision: 3},
	}}
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		switch ref.Type {
		case catalog.TypeStack:
			if st, ok := stacks.stacks[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: st.EnvironmentID}
			}
		case catalog.TypeUpdatePolicy:
			if p, err := svc.Get(context.Background(), ref.ID); err == nil {
				return policy.Location{Found: true, EnvironmentID: p.EnvironmentID, Parents: updates.Parents(p)}
			}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: stacks, Updates: svc, Agents: newFakeAgents(), Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), svc
}

func TestUpdatePolicyRoutes(t *testing.T) {
	h, svc := updatesAPIFor(t, authztest.New().Owner("olga"))
	do := func(c authztest.Call) authztest.Response { return authztest.Do(t, h, "olga", c) }

	r := do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-policies/pol-1"})
	var p UpdatePolicy
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"3"` || json.Unmarshal(r.Body, &p) != nil || p.View != "full" ||
		p.RunSchedule == nil || p.RunSchedule.Enabled || p.Summary == nil || p.Summary.Quarantined != 1 {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	// The target by the name users know (the stack's display name).
	if p.TargetName != "Shop" {
		t.Fatalf("target name: %q", p.TargetName)
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		path := "/api/v1/update-policies/pol-1"
		if method == http.MethodPost {
			path = "/api/v1/update-policies"
		}
		if r := do(authztest.Call{Method: method, Path: path}); r.Status != http.StatusMethodNotAllowed {
			t.Fatalf("legacy policy mutation %s: %d", method, r.Status)
		}
	}
	if r := do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies/pol-1/checks"}); r.Status != http.StatusAccepted || svc.checks != 1 {
		t.Fatalf("check: %d %s", r.Status, r.Body)
	}
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies/pol-1/previews", Body: map[string]any{}})
	var pv UpdatePreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &pv) != nil || !pv.SourceDrift || len(pv.SharedTag) != 1 || len(pv.Notes) == 0 {
		t.Fatalf("preview: %d %s", r.Status, r.Body)
	}
	r = do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-policies/pol-1/candidates"})
	var page Page[UpdateCandidate]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil || len(page.Items) != 1 ||
		!strings.Contains(page.Items[0].Guidance, "docker.io/library/nginx@sha256:old") {
		t.Fatalf("candidates: %d %s", r.Status, r.Body)
	}
	if !strings.Contains(string(r.Body), `"publishedAt":"2026-09-20T08:30:00Z"`) {
		t.Fatalf("candidate publish time: %s", r.Body)
	}
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies/pol-1/runs", Headers: map[string]string{"Idempotency-Key": "k1"},
		Body: map[string]any{"previewFingerprint": "fp-1"}})
	if r.Status != http.StatusAccepted || len(svc.runs) != 1 || svc.runs[0].IdempotencyKey != "k1" || svc.runs[0].Fingerprint != "fp-1" {
		t.Fatalf("run: %d %s %+v", r.Status, r.Body, svc.runs)
	}
	for code, err := range map[string]error{
		CodeNoUpdateCandidates: &domain.UpdateError{Code: domain.UpdateErrNoCandidates, Message: "nothing"},
		CodeUpdateSourceDrift:  &domain.UpdateError{Code: domain.UpdateErrSourceDrift, Message: "deploy first"},
		CodeUpdatePreviewStale: &domain.UpdateError{Code: domain.UpdateErrPreviewStale, Message: "again"},
	} {
		svc.runErr = err
		r := do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies/pol-1/runs", Body: map[string]any{}})
		if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), `"`+code+`"`) {
			t.Errorf("%s: %d %s", code, r.Status, r.Body)
		}
	}
}

func TestUpdatePolicyAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("rae", "readers").Group("readers", "allow update_policy.read @update_policy:pol-1").
		Member("ron", "runners").Group("runners", "allow update.run @stack:st-1").
		Member("nia", "nobody")
	h, svc := updatesAPIFor(t, pol)
	run := authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies/pol-1/runs", Body: map[string]any{}}
	get := authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-policies/pol-1"}
	// Read access shows the policy in full but cannot run it.
	var p UpdatePolicy
	r := authztest.Do(t, h, "rae", get)
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || p.View != "full" {
		t.Fatalf("reader get: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "rae", run); r.Status != http.StatusForbidden {
		t.Fatalf("reader run: %d", r.Status)
	}
	// update.run on the stack covers its policy (minimal view).
	p = UpdatePolicy{}
	r = authztest.Do(t, h, "ron", get)
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || p.View != "minimal" || p.RunSchedule != nil || p.Target.ID != "st-1" {
		t.Fatalf("runner get: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "ron", run); r.Status != http.StatusAccepted || len(svc.runs) != 1 {
		t.Fatalf("runner run: %d %s", r.Status, r.Body)
	}
	// No capability: the policy does not exist for them.
	if r := authztest.Do(t, h, "nia", get); r.Status != http.StatusNotFound {
		t.Fatalf("stranger get: %d", r.Status)
	}
	r = authztest.Do(t, h, "nia", authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-policies"})
	var page Page[UpdatePolicy]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil || len(page.Items) != 0 {
		t.Fatalf("stranger list: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "rae", authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-policies"}); r.Status != http.StatusMethodNotAllowed {
		t.Fatalf("legacy create: %d", r.Status)
	}
}

func TestStackImageStatusShowsUpdateState(t *testing.T) {
	h, _ := updatesAPIFor(t, authztest.New().Owner("olga"))
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/image-status"})
	var out struct {
		Images []StackImageStatus `json:"images"`
	}
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &out) != nil || len(out.Images) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if s := out.Images[0]; s.PolicyID != "pol-1" || s.Update != "quarantined" || s.CandidateDigest != "sha256:new" || !s.Eligible {
		t.Fatalf("%+v", s)
	}
}
