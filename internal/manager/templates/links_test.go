package templates

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const secretishURL = "https://docs.example.com/app?token=query-canary"

// auditJSON is what a request's audit draft would record.
func auditJSON(t *testing.T, d *audit.Draft) string {
	t.Helper()
	_, _, ev := d.Snapshot()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTemplateLinksAreStoredInOrderAndNeverAudited: links are trimmed and
// kept in order, replaced by an edit (an empty list removes them), refused
// with the field of the first problem, and the audit trail records their
// number, never a URL.
func TestTemplateLinksAreStoredInOrderAndNeverAudited(t *testing.T) {
	f := newFixture(t)
	d := audit.NewDraft("template.create", nil)
	ctx := audit.WithDraft(f.ctx, d)
	tm, err := f.svc.Create(ctx, domain.TemplateInput{Name: "App", Links: []domain.Link{
		{Label: " Docs ", URL: secretishURL}, {URL: "https://git.example.com/app"},
	}}, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.Links) != 2 || tm.Links[0] != (domain.Link{Label: "Docs", URL: secretishURL}) || tm.Links[1].URL != "https://git.example.com/app" {
		t.Fatalf("created links %+v", tm.Links)
	}
	if got := auditJSON(t, d); strings.Contains(got, "query-canary") || !strings.Contains(got, `"linkCount":2`) {
		t.Errorf("create audit %s", got)
	}

	d = audit.NewDraft("template.manage", nil)
	ctx = audit.WithDraft(f.ctx, d)
	links := []domain.Link{{Label: "Website", URL: "https://app.example.com/?ref=query-canary"}}
	up, err := f.svc.Update(ctx, tm.ID, tm.Revision, domain.TemplatePatch{Links: &links})
	if err != nil || len(up.Links) != 1 || up.Links[0].Label != "Website" || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if got := auditJSON(t, d); strings.Contains(got, "query-canary") || !strings.Contains(got, `"links":2`) || !strings.Contains(got, `"links":1`) {
		t.Errorf("update audit %s", got)
	}

	// Other edits keep the links.
	name := "App 2"
	if up, err = f.svc.Update(f.ctx, tm.ID, up.Revision, domain.TemplatePatch{Name: &name}); err != nil || len(up.Links) != 1 {
		t.Fatalf("rename = %+v, %v", up, err)
	}

	var fe *domain.FieldError
	bad := []domain.Link{{URL: "https://ok.example"}, {URL: "data:text/html,hi"}}
	if _, err := f.svc.Update(f.ctx, tm.ID, up.Revision, domain.TemplatePatch{Links: &bad}); !errors.As(err, &fe) || fe.Field != "links[1].url" {
		t.Fatalf("invalid link: %v", err)
	}
	if _, err := f.svc.Create(f.ctx, domain.TemplateInput{Name: "Other", Links: []domain.Link{{URL: "https://u:p@example.com"}}}, ""); !errors.As(err, &fe) ||
		fe.Field != "links[0].url" {
		t.Fatalf("credentials on create: %v", err)
	}

	none := []domain.Link{}
	if up, err = f.svc.Update(f.ctx, tm.ID, up.Revision, domain.TemplatePatch{Links: &none}); err != nil || len(up.Links) != 0 {
		t.Fatalf("clear = %+v, %v", up, err)
	}
}

// TestRegistryDropsInvalidRemoteLinks: another instance's links are kept
// only when valid; invalid ones are dropped without refusing the template
// or the registry.
func TestRegistryDropsInvalidRemoteLinks(t *testing.T) {
	remote := newFakeRegistry(t)
	remote.set(func(r *fakeRegistry) {
		r.index.Templates[0].Links = []RegistryLink{
			{Label: "Docs", URL: "https://friend.example/docs"},
			{Label: "Evil", URL: "javascript:alert(1)"},
			{Label: "Creds", URL: "https://user:pw@friend.example/"},
			{Label: strings.Repeat("x", 200), URL: "https://friend.example/long-label"},
			{Label: "Again", URL: "https://friend.example/docs"},
			{URL: " https://git.friend.example/app "},
		}
	})
	c := NewRegistryClient(remote.srv.Client())
	idx, _, _, err := c.FetchIndex(testutil.Context(t), remote.srv.URL, "")
	if err != nil || len(idx.Templates) != 1 {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	want := []RegistryLink{{Label: "Docs", URL: "https://friend.example/docs"}, {URL: "https://git.friend.example/app"}}
	if got := idx.Templates[0].Links; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("validated links %+v, want %+v", got, want)
	}

	f := newFixture(t, func(o *Options) { o.InstanceID, o.HTTPClient = "self-1", remote.srv.Client() })
	if _, err := f.svc.AddRegistry(f.ctx, remote.srv.URL, "owner"); err != nil {
		t.Fatal(err)
	}
	rt, err := f.svc.RegistryTemplate(f.ctx, "remote-1", "tpl-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Links) != 2 || rt.Links[0] != (domain.Link{Label: "Docs", URL: "https://friend.example/docs"}) {
		t.Fatalf("cached links %+v", rt.Links)
	}

	// A registry without links (an older instance) lists none.
	remote.set(func(r *fakeRegistry) { r.index.Templates[0].Links = nil })
	if _, err := f.svc.SyncRegistry(f.ctx, "remote-1"); err != nil {
		t.Fatal(err)
	}
	if rt, _ := f.svc.RegistryTemplate(f.ctx, "remote-1", "tpl-a"); len(rt.Links) != 0 {
		t.Fatalf("links after the remote removed them %+v", rt.Links)
	}
}

// TestRegistryLinksOfTheWrongShapeAreLeftOut: a registry whose links are
// not a list, or whose entries are not links, still decodes.
func TestRegistryLinksOfTheWrongShapeAreLeftOut(t *testing.T) {
	for raw, want := range map[string]int{
		`{"id":"a","links":"https://example.com"}`:                                       0,
		`{"id":"a","links":{"url":"https://example.com"}}`:                               0,
		`{"id":"a","links":[{"url":5},"x",{"label":"Docs","url":"https://ok.example"}]}`: 1,
		`{"id":"a"}`: 0,
	} {
		var e RegistryEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if len(e.Links) != want {
			t.Errorf("%s: %d links %+v, want %d", raw, len(e.Links), e.Links, want)
		}
	}
}
