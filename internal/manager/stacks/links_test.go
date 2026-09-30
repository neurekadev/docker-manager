package stacks_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
)

// TestStackLinksAreDetails: links come with the creation, are replaced by a
// details edit (a new revision, a stack.updated event), survive the store
// and keep their order; invalid ones are refused naming the field and
// change nothing.
func TestStackLinksAreDetails(t *testing.T) {
	h := newHarness(t)
	st, v, err := h.svc.Create(h.ctx, alice, domain.StackCreate{
		StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "shop", Files: files(shopYAML, "")},
		Links:           []domain.Link{{Label: " Docs ", URL: "https://docs.example.com/shop"}},
	})
	if err != nil {
		t.Fatalf("create: %v (validation %+v)", err, v)
	}
	if got := h.get(st.ID).Links; len(got) != 1 || got[0] != (domain.Link{Label: "Docs", URL: "https://docs.example.com/shop"}) {
		t.Fatalf("stored links after create %+v", got)
	}

	sub := h.bus.Subscribe(0, func(e events.Event) bool { return e.Type == events.StackUpdated })
	defer sub.Close()
	links := []domain.Link{{URL: "https://git.example.com/shop"}, {Label: "Website", URL: "https://shop.example.com/?ref=dm"}}
	up, err := h.svc.Update(h.ctx, st.ID, st.Revision, domain.StackPatch{Links: &links})
	if err != nil {
		t.Fatal(err)
	}
	if up.Revision != st.Revision+1 {
		t.Errorf("revision %d, want %d", up.Revision, st.Revision+1)
	}
	got := h.get(st.ID)
	if len(got.Links) != 2 || got.Links[0].URL != "https://git.example.com/shop" || got.Links[1].Label != "Website" {
		t.Fatalf("stored links after update %+v", got.Links)
	}
	if e := <-sub.C(); e.ResourceID != st.ID || e.Attributes["change"] != "metadata" {
		t.Errorf("event %+v", e)
	}

	// Other details leave the links alone.
	name := "Shop"
	if up, err = h.svc.Update(h.ctx, st.ID, up.Revision, domain.StackPatch{DisplayName: &name}); err != nil || len(up.Links) != 2 {
		t.Fatalf("display name edit: %v %+v", err, up.Links)
	}

	bad := []domain.Link{{URL: "https://ok.example"}, {URL: "javascript:alert(1)"}}
	_, err = h.svc.Update(h.ctx, st.ID, up.Revision, domain.StackPatch{Links: &bad})
	var in *domain.InputError
	if !errors.As(err, &in) || in.Field != "links[1].url" {
		t.Fatalf("invalid link: %v", err)
	}
	if got := h.get(st.ID); got.Revision != up.Revision || len(got.Links) != 2 {
		t.Errorf("a refused edit changed the stack: rev %d links %+v", got.Revision, got.Links)
	}

	none := []domain.Link{}
	if up, err = h.svc.Update(h.ctx, st.ID, up.Revision, domain.StackPatch{Links: &none}); err != nil || len(h.get(st.ID).Links) != 0 {
		t.Fatalf("clear: %v %+v", err, up)
	}
}

func TestCreateRefusesInvalidLinksBeforeWriting(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.svc.Create(h.ctx, alice, domain.StackCreate{
		StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "shop", Files: files(shopYAML, "")},
		Links:           []domain.Link{{Label: strings.Repeat("a", domain.MaxLinkLabel+1), URL: "https://docs.example.com"}},
	})
	var in *domain.InputError
	if !errors.As(err, &in) || in.Field != "links[0].label" {
		t.Fatalf("long label: %v", err)
	}
	if _, err := os.Stat(h.path("shop")); !os.IsNotExist(err) {
		t.Errorf("a refused creation wrote the project directory: %v", err)
	}
}
