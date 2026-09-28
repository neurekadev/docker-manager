package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// Links of stacks and templates: shown in full views only, edited with the
// details (422 naming the field for invalid links), carried by the public
// registry and the catalog.

const docsURL = "https://docs.example.com/shop?lang=en"

// errorFields returns the field names of an error response.
func errorFields(t *testing.T, body []byte) []string {
	t.Helper()
	var e struct {
		Details []struct {
			Field string `json:"field"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	var out []string
	for _, d := range e.Details {
		out = append(out, d.Field)
	}
	return out
}

func TestStackLinksFollowTheView(t *testing.T) {
	pol := authztest.Only("sam", "allow stack.read @stack:st-1")
	h, svc := stacksAPIFor(t, pol)
	st := svc.stacks["st-1"]
	st.Links = []domain.Link{{Label: "Docs", URL: docsURL}, {URL: "https://git.example.com/shop"}}
	svc.stacks["st-1"] = st

	r := authztest.Do(t, h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	var got Stack
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	if len(got.Links) != 2 || got.Links[0] != (WebLink{Label: "Docs", URL: docsURL}) || got.Links[1].Label != "" {
		t.Errorf("full view links %+v", got.Links)
	}

	// A stack without links leaves the field out.
	st.Links = nil
	svc.stacks["st-1"] = st
	r = authztest.Do(t, h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	authztest.AssertAbsent(t, "stack without links", r.Body, `"links"`)
}

func TestMinimalStackHasNoLinks(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("dev", "allow stack.deploy @stack:st-1"))
	st := svc.stacks["st-1"]
	st.Links = []domain.Link{{Label: "Docs", URL: docsURL}}
	svc.stacks["st-1"] = st
	r := authztest.Do(t, h, "dev", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	if r.Status != http.StatusOK {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "minimal stack", r.Body, `"links"`, "docs.example.com")
}

func TestStackLinksEditedWithTheDetails(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("mo", "allow stack.read @stack:st-1", "allow stack.manage @stack:st-1"))
	patch := func(rev string, links any) authztest.Response {
		return authztest.Do(t, h, "mo", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/stacks/st-1",
			Headers: map[string]string{"If-Match": rev}, Body: map[string]any{"links": links}})
	}

	r := patch(`"4"`, []map[string]any{{"label": " Docs ", "url": docsURL}, {"url": "https://git.example.com/shop"}})
	var got Stack
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	if len(got.Links) != 2 || got.Links[0].Label != "Docs" || got.Revision != 5 || r.Header.Get("ETag") != `"5"` {
		t.Fatalf("patched %+v (ETag %s)", got, r.Header.Get("ETag"))
	}
	if stored := svc.stacks["st-1"].Links; len(stored) != 2 || stored[1].URL != "https://git.example.com/shop" {
		t.Errorf("stored %+v", stored)
	}

	// Invalid links name their field; nothing changes.
	for name, tc := range map[string]struct {
		links []map[string]any
		field string
	}{
		"javascript":  {[]map[string]any{{"url": "https://ok.example"}, {"url": "javascript:alert(1)"}}, "body.links[1].url"},
		"credentials": {[]map[string]any{{"url": "https://me:pw@example.com"}}, "body.links[0].url"},
		"duplicate":   {[]map[string]any{{"url": docsURL}, {"url": docsURL}}, "body.links[1].url"},
	} {
		r := patch(`"5"`, tc.links)
		if r.Status != http.StatusUnprocessableEntity || !slices.Contains(errorFields(t, r.Body), tc.field) {
			t.Errorf("%s: %d %s, want 422 on %s", name, r.Status, r.Body, tc.field)
		}
	}
	// The schema bounds the list, the label and the URL before the service.
	many := make([]map[string]any, 11)
	for i := range many {
		many[i] = map[string]any{"url": "https://example.com/" + strings.Repeat("x", i+1)}
	}
	for name, links := range map[string]any{
		"11 links":   many,
		"long label": []map[string]any{{"label": strings.Repeat("a", 61), "url": docsURL}},
		"long url":   []map[string]any{{"url": "https://example.com/" + strings.Repeat("a", 2048)}},
		"no url":     []map[string]any{{"label": "Docs"}},
	} {
		if r := patch(`"5"`, links); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if svc.stacks["st-1"].Revision != 5 || len(svc.stacks["st-1"].Links) != 2 {
		t.Errorf("a refused edit changed the stack: %+v", svc.stacks["st-1"])
	}

	// An empty list removes them.
	r = patch(`"5"`, []map[string]any{})
	if r.Status != http.StatusOK || len(svc.stacks["st-1"].Links) != 0 {
		t.Fatalf("clear: %d %s", r.Status, r.Body)
	}
}

func TestTemplateLinksFollowTheViewAndValidate(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("ed", "editors").Group("editors", "allow template.files.read @template:t-a")
	mux := http.NewServeMux()
	f := newFakeTemplates()
	tm := f.byID["t-a"]
	tm.Links = []domain.Link{{Label: "Website", URL: "https://nextcloud.example"}}
	f.byID["t-a"] = tm
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Templates: f, Clock: testutil.FakeClock()})
	h := authztest.Authenticate(withTestContext(t, mux, ""))

	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a"})
	var got Template
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil || len(got.Links) != 1 || got.Links[0].Label != "Website" {
		t.Fatalf("full template: %d %s", r.Status, r.Body)
	}
	r = authztest.Do(t, h, "ed", authztest.Call{Method: http.MethodGet, Path: "/api/v1/templates/t-a"})
	authztest.AssertAbsent(t, "minimal template", r.Body, `"links"`, "nextcloud.example")

	patch := func(links any) authztest.Response {
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/templates/t-a",
			Headers: map[string]string{"If-Match": `"1"`}, Body: map[string]any{"links": links}})
	}
	r = patch([]map[string]any{{"label": "Docs", "url": docsURL}})
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil || len(got.Links) != 1 || got.Links[0].URL != docsURL {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	r = patch([]map[string]any{{"url": "file:///etc/passwd"}})
	if r.Status != http.StatusUnprocessableEntity || !slices.Contains(errorFields(t, r.Body), "body.links[0].url") {
		t.Errorf("file URL: %d %s", r.Status, r.Body)
	}
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/templates",
		Body: map[string]any{"name": "New", "links": []map[string]any{{"label": strings.Repeat("a", 61), "url": docsURL}}}})
	if r.Status != http.StatusUnprocessableEntity {
		t.Errorf("create with a long label: %d %s", r.Status, r.Body)
	}
}

func TestRegistryAndCatalogCarryLinks(t *testing.T) {
	h, f := registryHandler(t, false)
	pub := f.byID["t-a"]
	pub.Links = []domain.Link{{Label: "Docs", URL: docsURL}}
	f.byID["t-a"] = pub
	r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-registry"})
	var idx TemplateRegistryIndex
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &idx) != nil || len(idx.Templates) != 1 ||
		len(idx.Templates[0].Links) != 1 || idx.Templates[0].Links[0].URL != docsURL {
		t.Fatalf("registry index: %d %s", r.Status, r.Body)
	}

	pol := authztest.New().Owner("olga")
	mux := http.NewServeMux()
	New(mux, Deps{Templates: newFakeTemplates(), TemplateRegistries: &fakeTemplateRegistries{}, InstanceID: "inst-self", Authorizer: pol,
		Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	ch := authztest.Authenticate(withTestContext(t, mux, ""))
	r = authztest.Do(t, ch, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/template-catalog/remote-1/rt-1"})
	var item TemplateCatalogItem
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &item) != nil || len(item.Links) != 1 ||
		item.Links[0] != (WebLink{Label: "Guide", URL: "https://friend.example/guide"}) {
		t.Fatalf("remote catalog item: %d %s", r.Status, r.Body)
	}
}
