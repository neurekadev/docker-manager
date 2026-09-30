package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
)

// GET /search (#4, #22): every hit is filtered like the resource's own
// list route (#17) and shaped to identity and status only.

func searchFixture(t *testing.T, pol *authztest.Policy) *dockerFixture {
	t.Helper()
	f := newDockerFixture(t, pol)
	f.stacks["env-1"]["shop"] = "stack-shop"
	return f
}

func (f *dockerFixture) search(user string, query url.Values) (SearchResults, authztest.Response) {
	f.t.Helper()
	var out SearchResults
	r := f.get(user, "/api/v1/search?"+query.Encode(), &out)
	return out, r
}

type hitKey struct{ Type, Name, Env string }

func hitKeys(res SearchResults) []hitKey {
	out := make([]hitKey, 0, len(res.Items))
	for _, h := range res.Items {
		out = append(out, hitKey{h.Type, h.Name, h.EnvironmentID})
	}
	return out
}

func TestSearchOwnerFindsEverythingRankedExactThenPrefix(t *testing.T) {
	f := searchFixture(t, authztest.New().Owner("olga"))
	res, r := f.search("olga", url.Values{"q": {"shop"}})
	if r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	want := []hitKey{
		{SearchStack, "Shop", "env-1"}, // display name, exact match first
		{SearchContainer, "shop-db-1", "env-1"},
		{SearchContainer, "shop-web-1", "env-1"},
		{SearchVolume, "shop_data", "env-1"},
		{SearchNetwork, "shop_default", "env-1"},
	}
	if got := hitKeys(res); !slices.Equal(got, want) {
		t.Fatalf("hits = %+v, want %+v", got, want)
	}
	for _, h := range res.Items {
		if h.EnvironmentName != "NAS" {
			t.Fatalf("hit without environment name: %+v", h)
		}
		if h.Type != SearchStack && h.StackID != "stack-shop" {
			t.Fatalf("stack member without its stack: %+v", h)
		}
	}
	// The offline environment's Docker objects are missing.
	if len(res.Gaps) != 1 || res.Gaps[0] != (SearchGap{EnvironmentID: "env-secret", EnvironmentName: "Hidden", Reason: "offline"}) {
		t.Fatalf("gaps = %+v", res.Gaps)
	}

	// Environments and images match too; results are bounded by limit.
	res, _ = f.search("olga", url.Values{"q": {"nginx"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchImage, "nginx:1.27", "env-1"}, {SearchImage, "nginx:1.27", "env-2"}}) {
		t.Fatalf("image hits = %+v", got)
	}
	res, _ = f.search("olga", url.Values{"q": {"nas"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchEnvironment, "NAS", "env-1"}}) || res.Items[0].Status != "online" {
		t.Fatalf("environment hits = %+v", res.Items)
	}
	res, _ = f.search("olga", url.Values{"q": {"web"}, "limit": {"2"}})
	if len(res.Items) != 2 || res.Items[0].Name != "web" || res.Items[1].Name != "web" {
		t.Fatalf("limited hits = %+v", res.Items)
	}
}

func TestSearchRestrictedUserFindsNothing(t *testing.T) {
	f := searchFixture(t, authztest.New().Member("rita", "restricted"))
	for _, q := range []string{"web", "shop", "nas", "nginx", "db"} {
		res, r := f.search("rita", url.Values{"q": {q}})
		if r.Status != http.StatusOK || len(res.Items) != 0 || len(res.Gaps) != 0 {
			t.Fatalf("q=%s: %d %s", q, r.Status, r.Body)
		}
	}
	// No agent was asked on behalf of a user who sees no environment.
	if len(f.req.calls) != 0 {
		t.Fatalf("agent calls = %v", f.req.calls)
	}
}

func TestSearchMetricsOnlyUserSeesOnlyTheGrantedContainerMinimally(t *testing.T) {
	f := searchFixture(t, authztest.Only("mia", "allow container.metrics.read @container:env-1/web"))
	res, r := f.search("mia", url.Values{"q": {"web"}})
	if r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchContainer, "web", "env-1"}}) {
		t.Fatalf("hits = %+v", got)
	}
	// Identity and status only: no image, labels, ports or actions.
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Body, &raw); err != nil {
		t.Fatal(err)
	}
	for k := range raw.Items[0] {
		if !slices.Contains([]string{"type", "id", "name", "environmentId", "environmentName", "status"}, k) {
			t.Fatalf("hit carries %q: %s", k, r.Body)
		}
	}
	// The hidden environment env-2 is never searched nor reported.
	for _, c := range f.req.calls {
		if c[:6] == "env-2:" {
			t.Fatalf("searched a hidden environment: %v", f.req.calls)
		}
	}
	res, _ = f.search("mia", url.Values{"q": {"cloud"}})
	if len(res.Items) != 0 {
		t.Fatalf("hidden environment found: %+v", res.Items)
	}
}

func TestSearchStackReadShowsTheStackAndServicesButNotItsContainers(t *testing.T) {
	f := searchFixture(t, authztest.Only("sam", "allow stack.read @stack:stack-shop"))
	res, _ := f.search("sam", url.Values{"q": {"shop"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchStack, "Shop", "env-1"}}) {
		t.Fatalf("hits = %+v", got)
	}
	// The environment is named only when the caller may see it.
	wantName := ""
	if r := f.get("sam", "/api/v1/environments/env-1", nil); r.Status == http.StatusOK {
		wantName = "NAS"
	}
	if res.Items[0].EnvironmentName != wantName {
		t.Fatalf("environment name %q, want %q", res.Items[0].EnvironmentName, wantName)
	}
	res, _ = f.search("sam", url.Values{"q": {"we"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchService, "web", "env-1"}}) || res.Items[0].ID != "stack-shop/web" {
		t.Fatalf("service hits = %+v", res.Items)
	}
}

func TestSearchReportsOfflineEnvironmentsAsGaps(t *testing.T) {
	f := searchFixture(t, authztest.New().Owner("olga"))
	f.req.offline["env-2"] = true
	res, _ := f.search("olga", url.Values{"q": {"web"}})
	if got := hitKeys(res); !slices.Contains(got, hitKey{SearchContainer, "web", "env-1"}) || slices.Contains(got, hitKey{SearchContainer, "web", "env-2"}) {
		t.Fatalf("hits = %+v", got)
	}
	// env-2's agent went away while it was reported online; env-secret is offline.
	if len(res.Gaps) != 2 || res.Gaps[0] != (SearchGap{EnvironmentID: "env-2", EnvironmentName: "Cloud", Reason: "offline"}) ||
		res.Gaps[1].EnvironmentID != "env-secret" {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
}

func TestSearchShortQueriesAndFilters(t *testing.T) {
	f := searchFixture(t, authztest.New().Owner("olga"))
	// One character: the manager's records only, no agent request.
	res, _ := f.search("olga", url.Values{"q": {"s"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchStack, "Shop", "env-1"}, {SearchEnvironment, "NAS", "env-1"}}) {
		t.Fatalf("hits = %+v", got)
	}
	if len(f.req.calls) != 0 {
		t.Fatalf("agent calls for a one-character query: %v", f.req.calls)
	}
	res, _ = f.search("olga", url.Values{"q": {"web"}, "types": {"container"}, "environmentId": {"env-2"}})
	if got := hitKeys(res); !slices.Equal(got, []hitKey{{SearchContainer, "web", "env-2"}}) {
		t.Fatalf("filtered hits = %+v", got)
	}
	if _, r := f.search("olga", url.Values{"q": {"web"}, "types": {"secrets"}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown type: %d %s", r.Status, r.Body)
	}
	if _, r := f.search("olga", url.Values{}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("missing q: %d %s", r.Status, r.Body)
	}
	if r := f.do("", authztest.Call{Method: http.MethodGet, Path: "/api/v1/search?q=web"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", r.Status, r.Body)
	}
}
