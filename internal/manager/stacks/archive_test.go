package stacks_test

import (
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// TestArchiveStack (#313): a stack created from an archive is reserved
// without files, records its committed files as its first observed
// revision under its own name, and is forgotten when its import did not
// keep them.
func TestArchiveStack(t *testing.T) {
	h := newHarness(t)
	r := domain.StackFromArchive{EnvironmentID: env, Name: "web", DisplayName: "Web", Meta: domain.DisplayMeta{Description: "Our site"},
		Links: []domain.Link{{Label: "Docs", URL: "https://web.example/docs"}, {Label: "Bad", URL: "javascript:alert(1)"}}}
	st, err := h.svc.ReserveArchiveStack(h.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StackUndeployed || st.Dir != "web" || st.Root != domain.StackRootStacks || st.Observed != nil || len(st.Links) != 1 {
		t.Fatalf("reserved %+v", st)
	}
	if _, err := h.svc.ReserveArchiveStack(h.ctx, r); !errors.Is(err, domain.ErrStackNameTaken) {
		t.Fatalf("a second reservation: %v", err)
	}
	if err := h.svc.CheckArchiveName(h.ctx, env, "web", st.ID); err != nil {
		t.Fatalf("the stack's own name: %v", err)
	}
	if _, err := h.svc.ReserveArchiveStack(h.ctx, domain.StackFromArchive{EnvironmentID: env, Name: "Not Valid"}); err == nil {
		t.Fatal("an invalid name was reserved")
	}

	// The job committed the archive's project directory.
	h.write("services:\n  app:\n    image: nginx:1.27\n", "web", "compose.yaml")
	h.write("TAG=1\n", "web", ".env")
	rec, err := h.svc.RecordArchiveStack(h.ctx, st.ID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Observed == nil || len(rec.Services) != 1 || rec.Services[0].Name != "app" || rec.Status != domain.StackUndeployed || rec.Applied != nil {
		t.Fatalf("recorded %+v", rec)
	}
	revs, err := h.svc.Revisions(h.ctx, st.ID, 0, 10)
	if err != nil || len(revs) != 1 || revs[0].Source != domain.RevisionExternal {
		t.Fatalf("revisions %+v, %v", revs, err)
	}

	if err := h.svc.ForgetArchiveStack(h.ctx, h.db, st.ID, domain.Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Get(h.ctx, st.ID); !errors.Is(err, domain.ErrStackNotFound) {
		t.Fatalf("after forgetting: %v", err)
	}
	if err := h.svc.ForgetArchiveStack(h.ctx, h.db, st.ID, domain.Job{ID: "job-1"}); err != nil {
		t.Fatalf("forgetting twice: %v", err)
	}
}

// TestArchiveStackPinnedName: files that pin another project name are not
// recorded.
func TestArchiveStackPinnedName(t *testing.T) {
	h := newHarness(t)
	st, err := h.svc.ReserveArchiveStack(h.ctx, domain.StackFromArchive{EnvironmentID: env, Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	h.write("name: original\nservices:\n  app:\n    image: nginx:1.27\n", "copy", "compose.yaml")
	if _, err := h.svc.RecordArchiveStack(h.ctx, st.ID, alice); stackErrCode(err) != domain.StackErrInvalidDefinition {
		t.Fatalf("err = %v", err)
	}
	if got := h.get(st.ID); got.Observed != nil {
		t.Fatalf("a revision was recorded: %+v", got.Observed)
	}
}
