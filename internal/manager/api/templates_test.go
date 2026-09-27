package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeTemplates keeps templates in memory.
type fakeTemplates struct {
	mu   sync.Mutex
	byID map[string]domain.Template
	icon []byte
}

func newFakeTemplates() *fakeTemplates {
	f := &fakeTemplates{byID: map[string]domain.Template{}, icon: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}
	for _, id := range []string{"t-a", "t-b"} {
		f.byID[id] = domain.Template{ID: id, Name: "Template " + id, Description: "secret-ish description", Tags: []string{"web"},
			Visibility: domain.TemplatePrivate, Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch,
			Icon: &domain.TemplateIcon{MediaType: "image/svg+xml", SHA256: "abc", Size: 10, UpdatedAt: testutil.Epoch}}
	}
	return f
}

func (f *fakeTemplates) List(_ context.Context, after string, limit int) ([]domain.Template, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Template
	for _, id := range []string{"t-a", "t-b", "t-new"} {
		if t, ok := f.byID[id]; ok && id > after {
			out = append(out, t)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeTemplates) Get(_ context.Context, id string) (domain.Template, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.byID[id]
	if !ok {
		return t, domain.ErrTemplateNotFound
	}
	return t, nil
}

func (f *fakeTemplates) Create(_ context.Context, in domain.TemplateInput, _ string) (domain.Template, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := domain.Template{ID: "t-new", Name: in.Name, Visibility: domain.TemplatePrivate, Revision: 1, Tags: []string{}}
	f.byID[t.ID] = t
	return t, nil
}

func (f *fakeTemplates) Update(ctx context.Context, id string, _ int64, p domain.TemplatePatch) (domain.Template, error) {
	t, err := f.Get(ctx, id)
	if p.Name != nil {
		t.Name = *p.Name
	}
	return t, err
}

func (f *fakeTemplates) SetVisibility(ctx context.Context, id string, _ int64, v domain.TemplateVisibility, ack bool) (domain.Template, error) {
	if v == domain.TemplatePublic && !ack {
		return domain.Template{}, domain.ErrTemplatePublicAckRequired
	}
	t, err := f.Get(ctx, id)
	t.Visibility = v
	return t, err
}

func (f *fakeTemplates) Delete(context.Context, string, int64) error { return nil }

func (f *fakeTemplates) Icon(ctx context.Context, id string) (domain.TemplateIcon, []byte, error) {
	t, err := f.Get(ctx, id)
	if err != nil {
		return domain.TemplateIcon{}, nil, err
	}
	return *t.Icon, f.icon, nil
}

func (f *fakeTemplates) SetIcon(ctx context.Context, id string, data []byte) (domain.Template, error) {
	if len(data) > domain.MaxTemplateIcon {
		return domain.Template{}, &domain.TemplateIconError{TooLarge: true, Message: "icons are limited to 256 KiB"}
	}
	if !strings.HasPrefix(string(data), "<svg") {
		return domain.Template{}, &domain.TemplateIconError{Message: "use a PNG, JPEG, GIF, WebP or SVG image"}
	}
	return f.Get(ctx, id)
}

func (f *fakeTemplates) RemoveIcon(ctx context.Context, id string) (domain.Template, error) {
	return f.Get(ctx, id)
}

func (f *fakeTemplates) Versions(context.Context, string) ([]domain.TemplateVersion, error) {
	return []domain.TemplateVersion{{Number: 1, Label: "1.0.0", Definition: []domain.TemplateFile{{Path: "compose.yaml", Size: 10}}}}, nil
}

func (f *fakeTemplates) Version(_ context.Context, _ string, n int) (domain.TemplateVersion, error) {
	if n != 1 {
		return domain.TemplateVersion{}, domain.ErrTemplateVersionNotFound
	}
	return domain.TemplateVersion{Number: 1, Label: "1.0.0"}, nil
}

func (f *fakeTemplates) Publish(_ context.Context, id, label, _ string, _ bool, _ string) (domain.TemplateVersion, error) {
	if id == "t-b" {
		return domain.TemplateVersion{}, &domain.TemplateDefinitionError{Message: "the template needs a compose.yaml"}
	}
	return domain.TemplateVersion{Number: 2, Label: label}, nil
}

func (f *fakeTemplates) DeleteVersion(context.Context, string, int) error { return nil }

var _ TemplateService = (*fakeTemplates)(nil)

func templatesHandler(t *testing.T, pol *authztest.Policy) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Templates: newFakeTemplates()})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

func TestTemplateRoutesShapeAndAuthorize(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("mia", "readers").Group("readers", "allow template.read @template:t-a").
		Member("ed", "editors").Group("editors", "allow template.files.read @template:t-a", "allow template.files.write @template:t-a").
		Member("pat", "publishers").Group("publishers", "allow template.read @all", "allow template.publish @template:t-a").
		Member("cara", "creators").Group("creators", "allow template.create @all").
		Member("rita", "nobody")
	h := templatesHandler(t, pol)

	list := func(user string) []string {
		r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates"})
		var page Page[Template]
		if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil {
			t.Fatalf("list as %s: %d %s", user, r.Status, r.Body)
		}
		var out []string
		for _, tm := range page.Items {
			out = append(out, tm.ID+":"+tm.View)
		}
		return out
	}
	if got := list("olga"); !slices.Equal(got, []string{"t-a:full", "t-b:full"}) {
		t.Errorf("owner lists %v", got)
	}
	if got := list("mia"); !slices.Equal(got, []string{"t-a:full"}) {
		t.Errorf("reader lists %v", got)
	}
	if got := list("ed"); !slices.Equal(got, []string{"t-a:minimal"}) {
		t.Errorf("file editor lists %v", got)
	}
	if got := list("rita"); len(got) != 0 {
		t.Errorf("nobody lists %v", got)
	}
	r := authztest.Do(t, h, "ed", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a"})
	authztest.AssertAbsent(t, "minimal template", r.Body, "secret-ish", `"tags"`)
	if !strings.Contains(string(r.Body), `"icon"`) {
		t.Errorf("the minimal view lacks the icon: %s", r.Body)
	}
	if r := authztest.Do(t, h, "mia", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-b"}); r.Status != http.StatusNotFound {
		t.Errorf("invisible template: %d", r.Status)
	}
	// Visible but not granted: 403.
	if r := authztest.Do(t, h, "mia", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/templates/t-a",
		Headers: map[string]string{"If-Match": `"1"`}, Body: map[string]any{"name": "x"}}); r.Status != http.StatusForbidden {
		t.Errorf("update without template.manage: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates", Body: map[string]any{"name": "x"}}); r.Status != http.StatusForbidden {
		t.Errorf("create without template.create: %d", r.Status)
	}
	r = authztest.Do(t, h, "cara", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates", Body: map[string]any{"name": "New"}})
	var created Template
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &created) != nil || created.View != "full" || created.Name != "New" {
		t.Errorf("create as creator: %d %s", r.Status, r.Body)
	}
	// The draft's file routes follow template.files.* on the template.
	if r := authztest.Do(t, h, "mia", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a/files"}); r.Status != http.StatusForbidden {
		t.Errorf("draft listing without template.files.read: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "ed", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-b/files"}); r.Status != http.StatusNotFound {
		t.Errorf("draft listing of an invisible template: %d %s", r.Status, r.Body)
	}
	// Publishing maps definition refusals.
	if r := authztest.Do(t, h, "pat", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates/t-a/versions", Body: map[string]any{"label": "2.0"}}); r.Status != http.StatusCreated {
		t.Errorf("publish: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "pat", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates/t-b/versions", Body: map[string]any{"label": "2.0"}}); r.Status != http.StatusForbidden {
		t.Errorf("publish without template.publish there: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates/t-b/versions", Body: map[string]any{"label": "2.0"}}); r.Status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(r.Body), CodeTemplateDefinitionInvalid) {
		t.Errorf("invalid draft: %d %s", r.Status, r.Body)
	}
}

func TestTemplateVisibilityNeedsAcknowledgement(t *testing.T) {
	h := templatesHandler(t, authztest.New().Owner("olga"))
	call := func(ack bool) authztest.Response {
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: "/api/v1/templates/t-a/visibility",
			Headers: map[string]string{"If-Match": `"1"`}, Body: map[string]any{"visibility": "public", "acknowledgePublic": ack}})
	}
	if r := call(false); r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), CodeTemplatePublicAckRequired) {
		t.Fatalf("without acknowledgement: %d %s", r.Status, r.Body)
	}
	if r := call(true); r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"visibility":"public"`) {
		t.Fatalf("with acknowledgement: %d %s", r.Status, r.Body)
	}
}

func TestTemplateIconsAreSandboxedAndCacheable(t *testing.T) {
	pol := authztest.New().Owner("olga").Member("rita", "nobody")
	h := templatesHandler(t, pol)
	// Any signed-in user loads icons (stacks show their template's icon).
	r := authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a/icon?v=abc"})
	if r.Status != http.StatusOK || r.Header.Get("Content-Type") != "image/svg+xml" || r.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("icon: %d %v", r.Status, r.Header)
	}
	r = authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a/icon?v=old"})
	if strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("a stale version is cached forever: %v", r.Header)
	}
	if r := authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/missing/icon"}); r.Status != http.StatusNotFound {
		t.Errorf("missing icon: %d", r.Status)
	}
	put := func(data string) authztest.Response {
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: "/api/v1/templates/t-a/icon", Body: map[string]any{"data": []byte(data)}})
	}
	if r := put("<svg/>"); r.Status != http.StatusOK {
		t.Errorf("set icon: %d %s", r.Status, r.Body)
	}
	if r := put("GIF-not-really"); r.Status != http.StatusUnsupportedMediaType || !strings.Contains(string(r.Body), CodeTemplateIconUnsupported) {
		t.Errorf("unsupported icon: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodPut, Path: "/api/v1/templates/t-a/icon", Body: map[string]any{"data": []byte("<svg/>")}}); r.Status != http.StatusNotFound {
		t.Errorf("set icon of an invisible template: %d", r.Status)
	}
}
