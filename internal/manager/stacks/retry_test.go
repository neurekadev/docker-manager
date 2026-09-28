package stacks_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// TestRetryDeployUsesTheStackAsItIsNow: a retried deploy keeps the
// original's options but takes the stack's current reference and registry
// connections, and becomes the stack's last job; a deleted stack refuses
// the retry.
func TestRetryDeployUsesTheStackAsItIsNow(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	orig, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{Services: []string{"web"}},
		domain.StackDeployOptions{Pull: "always", ForceRecreate: true})
	if err != nil {
		t.Fatal(err)
	}
	h.finishWith("failed", protocol.StackJobOutput{})
	if j := h.job(orig.ID); j.State != domain.JobFailed {
		t.Fatalf("deploy %s", j.State)
	}
	var before protocol.StackJobInput
	_ = json.Unmarshal(orig.Input, &before)
	if len(before.RegistryConnections) != 0 {
		t.Fatalf("registry connections before %v", before.RegistryConnections)
	}

	// A registry connection now applies to the stack's db image.
	h.regs.byHost["registry.example:5000/"] = "reg-1"
	nj, created, err := h.eng.Retry(h.ctx, alice, orig.ID, "")
	if err != nil || !created {
		t.Fatalf("retry: %v", err)
	}
	var in protocol.StackJobInput
	if err := json.Unmarshal(nj.Input, &in); err != nil {
		t.Fatal(err)
	}
	if nj.Kind != jobspec.StackDeploy || nj.RetryOf != orig.ID || in.StackID != st.ID || in.Stack.ProjectName != "shop" ||
		!slices.Equal(in.Services, []string{"web"}) || in.Pull != "always" || !in.ForceRecreate ||
		!slices.Equal(in.RegistryConnections, []string{"reg-1"}) {
		t.Fatalf("retry %s of %s input %+v", nj.Kind, nj.RetryOf, in)
	}
	if got := h.get(st.ID); got.LastJobID != nj.ID || got.LastJobKind != jobspec.StackDeploy {
		t.Fatalf("last job %s %s, want the retry %s", got.LastJobID, got.LastJobKind, nj.ID)
	}

	// The stack is gone: nothing to retry.
	h.finishWith("failed", protocol.StackJobOutput{})
	if err := store.DeleteStack(h.ctx, h.db, st.ID); err != nil {
		t.Fatal(err)
	}
	var nr *domain.JobNotRetryableError
	if _, _, err := h.eng.Retry(h.ctx, alice, nj.ID, ""); !errors.As(err, &nr) || nr.Reason != domain.RetryRefusedUnavailable {
		t.Fatalf("retry of a deleted stack's deploy: %v", err)
	}
}
