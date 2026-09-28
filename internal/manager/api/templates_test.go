package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/throttle"
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

func (f *fakeTemplates) Archive(ctx context.Context, id string, n int) (domain.TemplateVersion, []byte, error) {
	v, err := f.Version(ctx, id, n)
	return v, []byte("tar.gz-bytes"), err
}

func (f *fakeTemplates) Definition(ctx context.Context, id string, n int) (domain.TemplateVersion, []domain.TemplateFileContent, error) {
	v, err := f.Version(ctx, id, n)
	return v, []domain.TemplateFileContent{{Path: ".env", Content: []byte("PASSWORD=from-template")}}, err
}

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

func TestStackFromTemplateRoutes(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("sam", "creators").Group("creators", "allow stack.create @env:env-1").
		Member("uma", "users").Group("users", "allow stack.create @env:env-1", "allow template.use @template:t-a", "allow template.read @template:t-a").
		Member("rita", "nobody")
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: newFakeStacks(), Templates: newFakeTemplates(), InstanceID: "inst-self", Authorizer: pol,
		Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	h := authztest.Authenticate(withTestContext(t, mux, ""))

	create := func(user, templateID string) authztest.Response {
		return authztest.Do(t, h, user, authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/template-creations",
			Body: map[string]any{"environmentId": "env-1", "name": "cloud", "templateId": templateID, "version": 1}})
	}
	if r := create("sam", "t-a"); r.Status != http.StatusForbidden || !strings.Contains(string(r.Body), "template.use") {
		t.Errorf("without template.use: %d %s", r.Status, r.Body)
	}
	r := create("uma", "t-a")
	var out struct {
		Stack Stack `json:"stack"`
	}
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &out) != nil || out.Stack.ID != "st-tpl" {
		t.Fatalf("create from template: %d %s", r.Status, r.Body)
	}
	if r := create("olga", "missing"); r.Status != http.StatusNotFound {
		t.Errorf("unknown template: %d %s", r.Status, r.Body)
	}

	def := func(user string) authztest.Response {
		return authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a/versions/1/definition"})
	}
	if r := def("uma"); r.Status != http.StatusOK || !strings.Contains(string(r.Body), "from-template") {
		t.Errorf("definition with template.use: %d %s", r.Status, r.Body)
	}
	if r := def("sam"); r.Status != http.StatusNotFound {
		t.Errorf("definition of an invisible template: %d", r.Status)
	}

	// The icon map reaches every signed-in user; it names no template.
	r = authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-icons"})
	var icons TemplateIconMap
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &icons) != nil || icons.InstanceID != "inst-self" || len(icons.Items) != 2 ||
		icons.Items[0].InstanceID != "inst-self" || !strings.Contains(icons.Items[0].URL, "/icon?v=abc") {
		t.Fatalf("icon map: %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "icon map", r.Body, "Template t-a", "secret-ish")
}

func registryHandler(t *testing.T, disabled bool) (http.Handler, *fakeTemplates) {
	t.Helper()
	f := newFakeTemplates()
	pub := f.byID["t-a"]
	pub.Visibility, pub.Latest = domain.TemplatePublic, &domain.TemplateVersion{Number: 1, Label: "1.0.0"}
	f.byID["t-a"] = pub
	mux := http.NewServeMux()
	New(mux, Deps{Templates: f, InstanceID: "inst-self", Authorizer: authztest.New().Owner("olga"), Clock: testutil.FakeClock(),
		Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, TemplateRegistryDisabled: disabled,
		Deployment: DeploymentInfo{PublicURL: "https://dm.example.com"}})
	return authztest.Authenticate(withTestContext(t, mux, "")), f
}

// TestPublicRegistryListsOnlyPublishedPublicTemplates: without signing in,
// the registry shows public templates with a version, never private ones,
// and revalidates with If-None-Match.
func TestPublicRegistryListsOnlyPublishedPublicTemplates(t *testing.T) {
	h, _ := registryHandler(t, false)
	r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registry"})
	var idx TemplateRegistryIndex
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &idx) != nil {
		t.Fatalf("index: %d %s", r.Status, r.Body)
	}
	if idx.Format != TemplateRegistryFormat || idx.InstanceID != "inst-self" || idx.URL != "https://dm.example.com" ||
		len(idx.Templates) != 1 || idx.Templates[0].ID != "t-a" || len(idx.Templates[0].Versions) != 1 {
		t.Fatalf("index %+v", idx)
	}
	v := idx.Templates[0].Versions[0]
	if v.Archive.URL != "/api/v1/template-registry/templates/t-a/versions/1/archive" || idx.Templates[0].Icon == nil {
		t.Fatalf("version %+v icon %+v", v, idx.Templates[0].Icon)
	}
	authztest.AssertAbsent(t, "registry", r.Body, `"t-b"`)
	etag := r.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	again := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registry",
		Headers: map[string]string{"If-None-Match": etag}})
	if again.Status != http.StatusNotModified || len(again.Body) != 0 {
		t.Fatalf("revalidation: %d %s", again.Status, again.Body)
	}

	for path, want := range map[string]int{
		"/api/v1/template-registry/templates/t-a/icon":                http.StatusOK,
		"/api/v1/template-registry/templates/t-b/icon":                http.StatusNotFound,
		"/api/v1/template-registry/templates/t-a/versions/1/archive":  http.StatusOK,
		"/api/v1/template-registry/templates/t-b/versions/1/archive":  http.StatusNotFound,
		"/api/v1/template-registry/templates/nope/versions/1/archive": http.StatusNotFound,
	} {
		if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: path}); r.Status != want {
			t.Errorf("%s: %d, want %d", path, r.Status, want)
		}
	}
	r = authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registry/templates/t-a/versions/1/archive"})
	if r.Header.Get("Content-Type") != "application/gzip" || string(r.Body) != "tar.gz-bytes" {
		t.Errorf("archive: %v %q", r.Header, r.Body)
	}

	off, _ := registryHandler(t, true)
	if r := authztest.Do(t, off, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registry"}); r.Status != http.StatusNotFound {
		t.Errorf("disabled registry: %d", r.Status)
	}
}

func TestPublicRegistryIsRateLimited(t *testing.T) {
	h := &registryAPI{svc: newFakeTemplates(), limit: throttle.New(throttle.Limit{Every: time.Hour, Burst: 2}, testutil.FakeClock(), 10)}
	ctx := testutil.Context(t)
	for i := range 2 {
		if err := h.allow(ctx, h.limit); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	var e *Error
	if err := h.allow(ctx, h.limit); !errors.As(err, &e) || e.GetStatus() != http.StatusTooManyRequests {
		t.Fatalf("third request: %v", err)
	}
}

// fakeTemplateRegistries is the registry side of the template service.
type fakeTemplateRegistries struct {
	added []string
}

func (f *fakeTemplateRegistries) Registries(context.Context) ([]domain.TemplateRegistry, error) {
	return []domain.TemplateRegistry{{InstanceID: "remote-1", URL: "https://friend.example", Name: "Friend", Status: domain.TemplateRegistryOK, Templates: 1}}, nil
}

func (f *fakeTemplateRegistries) Registry(_ context.Context, id string) (domain.TemplateRegistry, error) {
	if id != "remote-1" {
		return domain.TemplateRegistry{}, domain.ErrTemplateRegistryNotFound
	}
	return domain.TemplateRegistry{InstanceID: "remote-1", Name: "Friend"}, nil
}

func (f *fakeTemplateRegistries) AddRegistry(_ context.Context, rawURL, _ string) (domain.TemplateRegistry, error) {
	f.added = append(f.added, rawURL)
	return domain.TemplateRegistry{InstanceID: "remote-2", URL: rawURL, Name: "New", Status: domain.TemplateRegistryOK}, nil
}

func (f *fakeTemplateRegistries) RemoveRegistry(context.Context, string) error { return nil }

func (f *fakeTemplateRegistries) SyncRegistry(ctx context.Context, id string) (domain.TemplateRegistry, error) {
	return f.Registry(ctx, id)
}

func (f *fakeTemplateRegistries) RegistryTemplates(context.Context, string) ([]domain.RegistryTemplate, error) {
	return []domain.RegistryTemplate{{RegistryID: "remote-1", TemplateID: "rt-1", Name: "Remote cloud", Tags: []string{"cloud"}, IconSHA256: "abc",
		Versions: []domain.RegistryTemplateVersion{{Number: 3, Label: "3.0.0"}}}}, nil
}

func (f *fakeTemplateRegistries) RegistryTemplate(_ context.Context, reg, id string) (domain.RegistryTemplate, error) {
	ts, _ := f.RegistryTemplates(context.Background(), reg)
	for _, t := range ts {
		if t.TemplateID == id {
			return t, nil
		}
	}
	return domain.RegistryTemplate{}, domain.ErrTemplateNotFound
}

func (f *fakeTemplateRegistries) RegistryIcon(context.Context, string, string) (domain.TemplateIcon, []byte, error) {
	return domain.TemplateIcon{MediaType: "image/svg+xml", SHA256: "abc"}, []byte("<svg/>"), nil
}

func (f *fakeTemplateRegistries) RegistryDefinition(context.Context, string, string, int) (domain.RegistryTemplateVersion, []domain.TemplateFileContent, error) {
	return domain.RegistryTemplateVersion{Number: 3, Label: "3.0.0"}, []domain.TemplateFileContent{{Path: "compose.yaml", Content: []byte("services: {}")}}, nil
}

var _ TemplateRegistryService = (*fakeTemplateRegistries)(nil)

func TestTemplateRegistryAndCatalogRoutes(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("rea", "readers").Group("readers", "allow template.read @all").
		Member("una", "users").Group("users", "allow template.read @all", "allow template.use @all").
		Member("rita", "nobody")
	regs := &fakeTemplateRegistries{}
	mux := http.NewServeMux()
	New(mux, Deps{Templates: newFakeTemplates(), TemplateRegistries: regs, InstanceID: "inst-self", Authorizer: pol,
		Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}, Builds: emptyBuilds{},
		Deployment: DeploymentInfo{PublicURL: "https://me.example"}})
	h := authztest.Authenticate(withTestContext(t, mux, ""))

	r := authztest.Do(t, h, "rea", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registries"})
	var list Page[TemplateRegistryInfo]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &list) != nil || len(list.Items) != 2 || !list.Items[0].Own ||
		list.Items[0].Removable || list.Items[0].URL != "https://me.example" || list.Items[1].InstanceID != "remote-1" {
		t.Fatalf("registries: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registries"}); r.Status != http.StatusForbidden {
		t.Errorf("registries without template.read: %d", r.Status)
	}
	// Only the owner adds registries.
	add := authztest.Call{Method: http.MethodPost, Path: "/api/v1/template-registries", Body: map[string]any{"url": "https://friend2.example"}}
	if r := authztest.Do(t, h, "una", add); r.Status != http.StatusForbidden {
		t.Errorf("add as a user: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "olga", add); r.Status != http.StatusCreated || len(regs.added) != 1 {
		t.Errorf("add as the owner: %d %s", r.Status, r.Body)
	}

	// The catalog merges this instance's templates and registry templates.
	r = authztest.Do(t, h, "una", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-catalog"})
	var cat Page[TemplateCatalogItem]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &cat) != nil || len(cat.Items) != 3 {
		t.Fatalf("catalog: %d %s", r.Status, r.Body)
	}
	var remote *TemplateCatalogItem
	for i := range cat.Items {
		if !cat.Items[i].Own {
			remote = &cat.Items[i]
		}
	}
	if remote == nil || remote.InstanceID != "remote-1" || remote.RegistryName != "Friend" || remote.IconURL == "" ||
		!slices.Contains(remote.Actions, "template.use") {
		t.Fatalf("remote item %+v", remote)
	}
	r = authztest.Do(t, h, "rea", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-catalog?registry=remote-1&tag=cloud"})
	if json.Unmarshal(r.Body, &cat) != nil || len(cat.Items) != 1 || slices.Contains(cat.Items[0].Actions, "template.use") {
		t.Fatalf("filtered catalog for a reader: %s", r.Body)
	}
	def := authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-catalog/remote-1/rt-1/versions/3/definition"}
	if r := authztest.Do(t, h, "rea", def); r.Status != http.StatusForbidden {
		t.Errorf("definition without template.use: %d", r.Status)
	}
	if r := authztest.Do(t, h, "una", def); r.Status != http.StatusOK || !strings.Contains(string(r.Body), "compose.yaml") {
		t.Errorf("definition: %d %s", r.Status, r.Body)
	}
	// Icons of registry templates are in the icon map for everyone.
	r = authztest.Do(t, h, "rita", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-icons"})
	if !strings.Contains(string(r.Body), "/api/v1/template-registries/remote-1/templates/rt-1/icon?v=abc") {
		t.Errorf("icon map lacks the registry icon: %s", r.Body)
	}
}
