package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// seedAdmin adds the automation and administration data of the #22 admin
// screens through the public API, as the owner: update candidates for the
// Silo policy, a maintenance policy for homelab (schedules off, as every
// policy starts) and backups.
func (s *seeder) seedAdmin(ctx context.Context, owner *apiClient) error {
	hl := s.envs["homelab"]
	var stacks struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if _, err := owner.do(ctx, http.MethodGet, "/api/v1/stacks?environmentId="+hl, nil, &stacks); err != nil {
		return err
	}
	silo := ""
	for _, st := range stacks.Items {
		if st.Name == "silo" {
			silo = st.ID
		}
	}
	if silo == "" {
		return fmt.Errorf("stack silo not seeded")
	}
	// The update policy "Silo images" is created by seedAccountsAndJobs
	// (its weekly check); exclude silo-db, add a weekday window and record
	// an earlier check's result.
	var pols struct {
		Items []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		} `json:"items"`
	}
	if _, err := owner.do(ctx, http.MethodGet, "/api/v1/update-policies?environmentId="+hl, nil, &pols); err != nil {
		return err
	}
	var pol struct {
		ID       string
		Revision int64
	}
	for _, p := range pols.Items {
		if p.Name == "Silo images" {
			pol.ID, pol.Revision = p.ID, p.Revision
		}
	}
	if pol.ID == "" {
		return fmt.Errorf("update policy Silo images not seeded")
	}
	if _, err := owner.do(ctx, http.MethodPatch, "/api/v1/update-policies/"+pol.ID, map[string]any{
		"excludeServices": []string{"silo-db"},
		"window":          map[string]any{"days": []int{1, 2, 3, 4, 5}, "start": "01:00", "end": "05:00"},
	}, nil, "If-Match", fmt.Sprintf("%q", fmt.Sprint(pol.Revision))); err != nil {
		return err
	}
	if err := s.seedUpdateCandidates(ctx, pol.ID, hl, silo); err != nil {
		return err
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/maintenance-policies", map[string]any{
		"environmentId": hl, "name": "Weekly cleanup", "description": "Old stopped containers and dangling images",
		"rules": []map[string]any{
			{"category": "stopped_containers", "enabled": true, "minAgeHours": 720},
			{"category": "dangling_images", "enabled": true, "minAgeHours": 720},
		},
	}, nil); err != nil {
		return err
	}
	return s.seedBackups(ctx, owner, hl, silo)
}

// seedBackups creates a local backup repository on the manager (the
// Recovery Key is generated, confirmed and printed), a second repository
// on homelab still awaiting its key confirmation, a manager-state policy
// that completes and a nightly policy whose Silo member cannot be written
// (the manager's local repository does not serve homelab): a partial set.
func (s *seeder) seedBackups(ctx context.Context, owner *apiClient, hl, silo string) error {
	var created struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
		RecoveryKey struct {
			Key string `json:"key"`
		} `json:"recoveryKey"`
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-repositories", map[string]any{
		"name": "Manager disk", "kind": "local", "executor": "manager", "path": s.backups + "/manager",
	}, &created); err != nil {
		return err
	}
	s.recoveryKey = created.RecoveryKey.Key
	var confirmed struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-repositories/"+created.Repository.ID+"/recovery-confirmations",
		map[string]any{"recoveryKey": s.recoveryKey, "backedUp": true}, &confirmed); err != nil {
		return err
	}
	for _, j := range confirmed.Jobs {
		if err := s.waitJob(ctx, owner, j.ID); err != nil {
			return err
		}
	}
	// homelab's own repository: confirmed, but the devstack agents run no
	// restic, so its jobs fail (the nightly set stays partial).
	var hlRepo struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-repositories", map[string]any{
		"name": "Homelab disk", "kind": "local", "executor": hl, "path": s.backups + "/homelab",
	}, &hlRepo); err != nil {
		return err
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-repositories/"+hlRepo.Repository.ID+"/recovery-confirmations",
		map[string]any{"recoveryKey": s.recoveryKey, "backedUp": true}, &confirmed); err != nil {
		return err
	}
	for _, j := range confirmed.Jobs {
		if err := s.waitJob(ctx, owner, j.ID); err != nil {
			return err
		}
	}
	// A repository whose key confirmation is still open.
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-repositories", map[string]any{
		"name": "NAS disk", "kind": "local", "executor": s.envs["nas"], "path": s.backups + "/nas",
	}, nil); err != nil {
		return err
	}
	for _, p := range []map[string]any{
		{"name": "Manager state", "repositoryId": created.Repository.ID, "includeManagerState": true,
			"retention": map[string]any{"daily": 7, "weekly": 4, "minKeep": 3}},
		{"name": "Nightly", "repositoryId": created.Repository.ID, "includeManagerState": true,
			"environmentRepositories": map[string]string{hl: hlRepo.Repository.ID},
			"stacks":                  []map[string]any{{"stackId": silo, "volumeExclude": []string{"silo_cache"}}},
			"retention":               map[string]any{"daily": 14, "monthly": 6, "minKeep": 3, "afterBackup": true}},
	} {
		var pol struct {
			ID string `json:"id"`
		}
		if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-policies", p, &pol); err != nil {
			return err
		}
		var run struct {
			Jobs []struct {
				ID string `json:"id"`
			} `json:"jobs"`
		}
		if _, err := owner.do(ctx, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", map[string]any{}, &run,
			"Idempotency-Key", "devstack-backup-"+pol.ID); err != nil {
			return err
		}
		for _, j := range run.Jobs {
			if err := s.waitJob(ctx, owner, j.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// digest returns a stable fake sha256 digest for the devstack.
func digest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedUpdateCandidates records the result of an earlier digest check of
// Silo (the devstack has no registry): an update available on a
// latest-style tag, a quarantined digest after a failed update, an
// up-to-date service, an excluded one and a failed check, plus history.
func (s *seeder) seedUpdateCandidates(ctx context.Context, policyID, env, stackID string) error {
	db := s.m.DB()
	now := time.Now().UTC().Truncate(time.Second)
	checked := now.Add(-47 * time.Minute)
	cands := []domain.UpdateCandidate{
		{Service: "silo-web", Reference: "ghcr.io/silo/web:latest", Registry: "ghcr.io", Repository: "silo/web", Tag: "latest",
			Platform: "linux/amd64", Eligible: true, NonVersionTag: true, Status: domain.CandidateAvailable,
			ReasonMessage: "latest is not a version: the image behind it can change meaning (for example a new major release).",
			AppliedDigest: digest("silo-web-1"), PreviousDigest: digest("silo-web-0"), CandidateDigest: digest("silo-web-2"),
			CandidateIndexDigest: digest("silo-web-2-index")},
		{Service: "silo-api", Reference: "ghcr.io/silo/api:2.4", Registry: "ghcr.io", Repository: "silo/api", Tag: "2.4",
			Platform: "linux/amd64", Eligible: true, Status: domain.CandidateQuarantined, AppliedDigest: digest("silo-api-1"),
			PreviousDigest: digest("silo-api-0"), CandidateDigest: digest("silo-api-2"), ErrorClass: "health_check_failed",
			ErrorMessage: "silo-api did not become healthy within 120 s"},
		{Service: "silo-redis", Reference: "redis:7-alpine", Registry: "docker.io", Repository: "library/redis", Tag: "7-alpine",
			Platform: "linux/amd64", Eligible: true, Status: domain.CandidateUpToDate, AppliedDigest: digest("redis-7"),
			CandidateDigest: digest("redis-7")},
		{Service: "silo-db", Reference: "postgres:16", Registry: "docker.io", Repository: "library/postgres", Tag: "16",
			Status: domain.CandidateIneligible, Reason: "excluded", ReasonMessage: "The service is not opted in by this policy."},
		{Service: "silo-worker", Reference: "ghcr.io/silo/worker:latest", Registry: "ghcr.io", Repository: "silo/worker", Tag: "latest",
			Platform: "linux/amd64", Eligible: true, NonVersionTag: true, Status: domain.CandidateCheckFailed,
			AppliedDigest: digest("silo-worker-1"), ErrorClass: "unauthorized", ErrorMessage: "ghcr.io answered 401 Unauthorized"},
	}
	for i := range cands {
		c := &cands[i]
		c.ID, c.PolicyID, c.CheckedAt, c.UpdatedAt = ids.New(), policyID, &checked, checked
		if err := store.UpsertUpdateCandidate(ctx, db, c); err != nil {
			return err
		}
	}
	if err := store.QuarantineUpdateDigest(ctx, db, domain.UpdateQuarantine{PolicyID: policyID, Service: "silo-api",
		Digest: digest("silo-api-2"), ErrorClass: "health_check_failed", CreatedAt: now.Add(-26 * time.Hour)}); err != nil {
		return err
	}
	for _, h := range []domain.UpdateHistoryEntry{
		{Service: "silo-api", Reference: "ghcr.io/silo/api:2.4", FromDigest: digest("silo-api-1"), ToDigest: digest("silo-api-2"),
			Outcome: domain.UpdateOutcomeFailed, ErrorClass: "health_check_failed", At: now.Add(-26 * time.Hour)},
		{Service: "silo-web", Reference: "ghcr.io/silo/web:latest", FromDigest: digest("silo-web-0"), ToDigest: digest("silo-web-1"),
			Outcome: domain.UpdateOutcomeUpdated, At: now.Add(-9 * 24 * time.Hour)},
		{Service: "silo-worker", Reference: "ghcr.io/silo/worker:latest", FromDigest: digest("silo-worker-0"),
			ToDigest: digest("silo-worker-1"), Outcome: domain.UpdateOutcomeKeptStopped, At: now.Add(-9 * 24 * time.Hour)},
	} {
		h.ID, h.PolicyID, h.EnvironmentID, h.TargetType, h.TargetID = ids.New(), policyID, env, domain.UpdateTargetStack, stackID
		if err := store.InsertUpdateHistory(ctx, db, h); err != nil {
			return err
		}
	}
	return nil
}
