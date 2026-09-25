package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
)

// Update policies, candidates, quarantined digests and history (#20).

type windowDoc struct {
	Days  []int  `json:"days,omitempty"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type updatePolicyRow struct {
	bun.BaseModel `bun:"table:update_policies"`

	ID                 string    `bun:"id,pk"`
	EnvironmentID      string    `bun:"environment_id,notnull"`
	Name               string    `bun:"name,notnull"`
	NameKey            string    `bun:"name_key,notnull"`
	TargetType         string    `bun:"target_type,notnull"`
	TargetID           string    `bun:"target_id,notnull"`
	Services           string    `bun:"services,notnull"`
	ExcludeServices    string    `bun:"exclude_services,notnull"`
	CheckCron          string    `bun:"check_cron,notnull"`
	CheckTimeZone      string    `bun:"check_time_zone,notnull"`
	CheckEnabled       int       `bun:"check_enabled,notnull"`
	RunCron            string    `bun:"run_cron,notnull"`
	RunTimeZone        string    `bun:"run_time_zone,notnull"`
	RunEnabled         int       `bun:"run_enabled,notnull"`
	RunWindow          string    `bun:"run_window,notnull"`
	WaitTimeoutSeconds int       `bun:"wait_timeout_seconds,notnull"`
	Revision           int64     `bun:"revision,notnull"`
	CreatedAt          time.Time `bun:"created_at,notnull"`
	UpdatedAt          time.Time `bun:"updated_at,notnull"`
}

func fromUpdatePolicy(p *domain.UpdatePolicy) updatePolicyRow {
	window := ""
	if p.Window != nil {
		window = mustJSON(windowDoc{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End})
	}
	services, excluded := p.Services, p.ExcludeServices
	if services == nil {
		services = []string{}
	}
	if excluded == nil {
		excluded = []string{}
	}
	return updatePolicyRow{ID: p.ID, EnvironmentID: p.EnvironmentID, Name: p.Name, NameKey: NameKey(p.Name), TargetType: string(p.TargetType),
		TargetID: p.TargetID, Services: mustJSON(services), ExcludeServices: mustJSON(excluded), CheckCron: p.Check.Cron,
		CheckTimeZone: p.Check.TimeZone, CheckEnabled: b2i(p.Check.Enabled), RunCron: p.Run.Cron, RunTimeZone: p.Run.TimeZone,
		RunEnabled: b2i(p.Run.Enabled), RunWindow: window, WaitTimeoutSeconds: p.WaitTimeoutSeconds, Revision: p.Revision,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC()}
}

func (r updatePolicyRow) toDomain() domain.UpdatePolicy {
	p := domain.UpdatePolicy{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, TargetType: domain.UpdateTargetType(r.TargetType),
		TargetID: r.TargetID, Check: domain.UpdateSchedule{Cron: r.CheckCron, TimeZone: r.CheckTimeZone, Enabled: r.CheckEnabled == 1},
		Run:                domain.UpdateSchedule{Cron: r.RunCron, TimeZone: r.RunTimeZone, Enabled: r.RunEnabled == 1},
		WaitTimeoutSeconds: r.WaitTimeoutSeconds, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.Services), &p.Services)
	_ = json.Unmarshal([]byte(r.ExcludeServices), &p.ExcludeServices)
	if r.RunWindow != "" {
		var w windowDoc
		if json.Unmarshal([]byte(r.RunWindow), &w) == nil {
			p.Window = &domain.UpdateWindow{Days: w.Days, Start: w.Start, End: w.End}
		}
	}
	return p
}

func updatePolicyConflict(err error) error {
	switch {
	case uniqueViolation(err, "update_policies.environment_id, update_policies.target_type"):
		return domain.ErrUpdatePolicyTargetUsed
	case uniqueViolation(err, "update_policies.environment_id, update_policies.name_key"):
		return domain.ErrUpdatePolicyNameTaken
	}
	return nil
}

// InsertUpdatePolicy stores a new policy.
func InsertUpdatePolicy(ctx context.Context, db bun.IDB, p *domain.UpdatePolicy) error {
	row := fromUpdatePolicy(p)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if c := updatePolicyConflict(err); c != nil {
			return c
		}
		return fmt.Errorf("store: insert update policy: %w", err)
	}
	return nil
}

// UpdateUpdatePolicy writes p when the stored revision is expectRevision.
func UpdateUpdatePolicy(ctx context.Context, db bun.IDB, p *domain.UpdatePolicy, expectRevision int64) error {
	row := fromUpdatePolicy(p)
	res, err := db.NewUpdate().Model(&row).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if c := updatePolicyConflict(err); c != nil {
			return c
		}
		return fmt.Errorf("store: update update policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetUpdatePolicy(ctx, db, p.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// DeleteUpdatePolicy removes a policy (and its candidates, quarantine and
// history) when the revision matches.
func DeleteUpdatePolicy(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*updatePolicyRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete update policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetUpdatePolicy(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetUpdatePolicy returns a policy.
func GetUpdatePolicy(ctx context.Context, db bun.IDB, id string) (domain.UpdatePolicy, error) {
	var row updatePolicyRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UpdatePolicy{}, domain.ErrUpdatePolicyNotFound
	}
	if err != nil {
		return domain.UpdatePolicy{}, fmt.Errorf("store: get update policy: %w", err)
	}
	return row.toDomain(), nil
}

// ListUpdatePolicies returns policies in ID order (environment "" = all).
func ListUpdatePolicies(ctx context.Context, db bun.IDB, environmentID, afterID string, limit int) ([]domain.UpdatePolicy, error) {
	var rows []updatePolicyRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if environmentID != "" {
		q = q.Where("environment_id = ?", environmentID)
	}
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list update policies: %w", err)
	}
	out := make([]domain.UpdatePolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// UpdatePoliciesForTarget returns the policies of a target (stack ID or
// container name) in an environment ("" = any).
func UpdatePoliciesForTarget(ctx context.Context, db bun.IDB, environmentID string, typ domain.UpdateTargetType, targetID string) ([]domain.UpdatePolicy, error) {
	var rows []updatePolicyRow
	q := db.NewSelect().Model(&rows).Where("target_type = ?", string(typ)).Where("target_id = ?", targetID).Order("id ASC")
	if environmentID != "" {
		q = q.Where("environment_id = ?", environmentID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: update policies of a target: %w", err)
	}
	out := make([]domain.UpdatePolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

type updateCandidateRow struct {
	bun.BaseModel `bun:"table:update_candidates"`

	ID                   string     `bun:"id,pk"`
	PolicyID             string     `bun:"policy_id,notnull"`
	Service              string     `bun:"service,notnull"`
	Reference            string     `bun:"reference,notnull"`
	Registry             string     `bun:"registry,notnull"`
	Repository           string     `bun:"repository,notnull"`
	Tag                  string     `bun:"tag,notnull"`
	Platform             string     `bun:"platform,notnull"`
	RegistryConnectionID string     `bun:"registry_connection_id,notnull"`
	Eligible             int        `bun:"eligible,notnull"`
	Reason               string     `bun:"reason,notnull"`
	ReasonMessage        string     `bun:"reason_message,notnull"`
	NonVersionTag        int        `bun:"non_version_tag,notnull"`
	Status               string     `bun:"status,notnull"`
	AppliedDigest        string     `bun:"applied_digest,notnull"`
	AppliedImageID       string     `bun:"applied_image_id,notnull"`
	PreviousDigest       string     `bun:"previous_digest,notnull"`
	CandidateDigest      string     `bun:"candidate_digest,notnull"`
	CandidateIndexDigest string     `bun:"candidate_index_digest,notnull"`
	ErrorClass           string     `bun:"error_class,notnull"`
	ErrorMessage         string     `bun:"error_message,notnull"`
	RetryAfterSeconds    int        `bun:"retry_after_seconds,notnull"`
	CheckedAt            *time.Time `bun:"checked_at"`
	CheckJobID           string     `bun:"check_job_id,notnull"`
	SourceHashBefore     string     `bun:"source_hash_before,notnull"`
	SourceHashAfter      string     `bun:"source_hash_after,notnull"`
	UpdatedAt            time.Time  `bun:"updated_at,notnull"`
}

func fromUpdateCandidate(c *domain.UpdateCandidate) updateCandidateRow {
	return updateCandidateRow{ID: c.ID, PolicyID: c.PolicyID, Service: c.Service, Reference: c.Reference, Registry: c.Registry,
		Repository: c.Repository, Tag: c.Tag, Platform: c.Platform, RegistryConnectionID: c.RegistryConnectionID, Eligible: b2i(c.Eligible),
		Reason: c.Reason, ReasonMessage: c.ReasonMessage, NonVersionTag: b2i(c.NonVersionTag), Status: string(c.Status),
		AppliedDigest: c.AppliedDigest, AppliedImageID: c.AppliedImageID, PreviousDigest: c.PreviousDigest, CandidateDigest: c.CandidateDigest,
		CandidateIndexDigest: c.CandidateIndexDigest, ErrorClass: c.ErrorClass, ErrorMessage: c.ErrorMessage,
		RetryAfterSeconds: c.RetryAfterSeconds, CheckedAt: utcPtr(c.CheckedAt), CheckJobID: c.CheckJobID,
		SourceHashBefore: c.SourceHashBefore, SourceHashAfter: c.SourceHashAfter, UpdatedAt: c.UpdatedAt.UTC()}
}

func (r updateCandidateRow) toDomain() domain.UpdateCandidate {
	return domain.UpdateCandidate{ID: r.ID, PolicyID: r.PolicyID, Service: r.Service, Reference: r.Reference, Registry: r.Registry,
		Repository: r.Repository, Tag: r.Tag, Platform: r.Platform, RegistryConnectionID: r.RegistryConnectionID, Eligible: r.Eligible == 1,
		Reason: r.Reason, ReasonMessage: r.ReasonMessage, NonVersionTag: r.NonVersionTag == 1, Status: domain.UpdateCandidateStatus(r.Status),
		AppliedDigest: r.AppliedDigest, AppliedImageID: r.AppliedImageID, PreviousDigest: r.PreviousDigest, CandidateDigest: r.CandidateDigest,
		CandidateIndexDigest: r.CandidateIndexDigest, ErrorClass: r.ErrorClass, ErrorMessage: r.ErrorMessage,
		RetryAfterSeconds: r.RetryAfterSeconds, CheckedAt: utcPtr(r.CheckedAt), CheckJobID: r.CheckJobID,
		SourceHashBefore: r.SourceHashBefore, SourceHashAfter: r.SourceHashAfter, UpdatedAt: r.UpdatedAt.UTC()}
}

// UpsertUpdateCandidate stores a candidate by (policy, service); an
// existing row keeps its ID.
func UpsertUpdateCandidate(ctx context.Context, db bun.IDB, c *domain.UpdateCandidate) error {
	row := fromUpdateCandidate(c)
	_, err := db.NewInsert().Model(&row).
		On("CONFLICT (policy_id, service) DO UPDATE").
		Set("reference = EXCLUDED.reference, registry = EXCLUDED.registry, repository = EXCLUDED.repository, tag = EXCLUDED.tag").
		Set("platform = EXCLUDED.platform, registry_connection_id = EXCLUDED.registry_connection_id, eligible = EXCLUDED.eligible").
		Set("reason = EXCLUDED.reason, reason_message = EXCLUDED.reason_message, non_version_tag = EXCLUDED.non_version_tag").
		Set("status = EXCLUDED.status, applied_digest = EXCLUDED.applied_digest, applied_image_id = EXCLUDED.applied_image_id").
		Set("previous_digest = EXCLUDED.previous_digest, candidate_digest = EXCLUDED.candidate_digest").
		Set("candidate_index_digest = EXCLUDED.candidate_index_digest, error_class = EXCLUDED.error_class").
		Set("error_message = EXCLUDED.error_message, retry_after_seconds = EXCLUDED.retry_after_seconds").
		Set("checked_at = EXCLUDED.checked_at, check_job_id = EXCLUDED.check_job_id").
		Set("source_hash_before = EXCLUDED.source_hash_before, source_hash_after = EXCLUDED.source_hash_after").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: upsert update candidate: %w", err)
	}
	return nil
}

// UpdateCandidates returns a policy's candidates by service name.
func UpdateCandidates(ctx context.Context, db bun.IDB, policyID string) ([]domain.UpdateCandidate, error) {
	var rows []updateCandidateRow
	if err := db.NewSelect().Model(&rows).Where("policy_id = ?", policyID).Order("service ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list update candidates: %w", err)
	}
	out := make([]domain.UpdateCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// DeleteUpdateCandidatesExcept removes a policy's candidates whose service
// is not in keep.
func DeleteUpdateCandidatesExcept(ctx context.Context, db bun.IDB, policyID string, keep []string) error {
	q := db.NewDelete().Model((*updateCandidateRow)(nil)).Where("policy_id = ?", policyID)
	if len(keep) > 0 {
		q = q.Where("service NOT IN (?)", bun.List(keep))
	}
	if _, err := q.Exec(ctx); err != nil {
		return fmt.Errorf("store: delete update candidates: %w", err)
	}
	return nil
}

type updateQuarantineRow struct {
	bun.BaseModel `bun:"table:update_quarantine"`

	PolicyID   string    `bun:"policy_id,pk"`
	Service    string    `bun:"service,pk"`
	Digest     string    `bun:"digest,pk"`
	JobID      string    `bun:"job_id,notnull"`
	ErrorClass string    `bun:"error_class,notnull"`
	CreatedAt  time.Time `bun:"created_at,notnull"`
}

// QuarantineUpdateDigest records a failed candidate digest (idempotent).
func QuarantineUpdateDigest(ctx context.Context, db bun.IDB, q domain.UpdateQuarantine) error {
	row := updateQuarantineRow{PolicyID: q.PolicyID, Service: q.Service, Digest: q.Digest, JobID: q.JobID, ErrorClass: q.ErrorClass,
		CreatedAt: q.CreatedAt.UTC()}
	if _, err := db.NewInsert().Model(&row).On("CONFLICT DO NOTHING").Exec(ctx); err != nil {
		return fmt.Errorf("store: quarantine update digest: %w", err)
	}
	return nil
}

// UpdateQuarantine returns a policy's quarantined digests.
func UpdateQuarantine(ctx context.Context, db bun.IDB, policyID string) ([]domain.UpdateQuarantine, error) {
	var rows []updateQuarantineRow
	if err := db.NewSelect().Model(&rows).Where("policy_id = ?", policyID).Order("service ASC", "created_at ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list update quarantine: %w", err)
	}
	out := make([]domain.UpdateQuarantine, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.UpdateQuarantine{PolicyID: r.PolicyID, Service: r.Service, Digest: r.Digest, JobID: r.JobID,
			ErrorClass: r.ErrorClass, CreatedAt: r.CreatedAt.UTC()})
	}
	return out, nil
}

type updateHistoryRow struct {
	bun.BaseModel `bun:"table:update_history"`

	ID                   string    `bun:"id,pk"`
	PolicyID             string    `bun:"policy_id,notnull"`
	EnvironmentID        string    `bun:"environment_id,notnull"`
	TargetType           string    `bun:"target_type,notnull"`
	TargetID             string    `bun:"target_id,notnull"`
	Service              string    `bun:"service,notnull"`
	Reference            string    `bun:"reference,notnull"`
	RegistryConnectionID string    `bun:"registry_connection_id,notnull"`
	FromDigest           string    `bun:"from_digest,notnull"`
	ToDigest             string    `bun:"to_digest,notnull"`
	FromImageID          string    `bun:"from_image_id,notnull"`
	ToImageID            string    `bun:"to_image_id,notnull"`
	JobID                string    `bun:"job_id,notnull"`
	Outcome              string    `bun:"outcome,notnull"`
	ErrorClass           string    `bun:"error_class,notnull"`
	SourceHashBefore     string    `bun:"source_hash_before,notnull"`
	SourceHashAfter      string    `bun:"source_hash_after,notnull"`
	At                   time.Time `bun:"at,notnull"`
}

// InsertUpdateHistory records a history entry.
func InsertUpdateHistory(ctx context.Context, db bun.IDB, h domain.UpdateHistoryEntry) error {
	row := updateHistoryRow{ID: h.ID, PolicyID: h.PolicyID, EnvironmentID: h.EnvironmentID, TargetType: string(h.TargetType),
		TargetID: h.TargetID, Service: h.Service, Reference: h.Reference, RegistryConnectionID: h.RegistryConnectionID,
		FromDigest: h.FromDigest, ToDigest: h.ToDigest, FromImageID: h.FromImageID, ToImageID: h.ToImageID, JobID: h.JobID,
		Outcome: h.Outcome, ErrorClass: h.ErrorClass, SourceHashBefore: h.SourceHashBefore, SourceHashAfter: h.SourceHashAfter,
		At: h.At.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert update history: %w", err)
	}
	return nil
}

// UpdateHistory returns a policy's newest history entries first.
func UpdateHistory(ctx context.Context, db bun.IDB, policyID string, limit int) ([]domain.UpdateHistoryEntry, error) {
	var rows []updateHistoryRow
	q := db.NewSelect().Model(&rows).Where("policy_id = ?", policyID).Order("at DESC", "id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list update history: %w", err)
	}
	out := make([]domain.UpdateHistoryEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.UpdateHistoryEntry{ID: r.ID, PolicyID: r.PolicyID, EnvironmentID: r.EnvironmentID,
			TargetType: domain.UpdateTargetType(r.TargetType), TargetID: r.TargetID, Service: r.Service, Reference: r.Reference,
			RegistryConnectionID: r.RegistryConnectionID, FromDigest: r.FromDigest, ToDigest: r.ToDigest, FromImageID: r.FromImageID,
			ToImageID: r.ToImageID, JobID: r.JobID, Outcome: r.Outcome, ErrorClass: r.ErrorClass, SourceHashBefore: r.SourceHashBefore,
			SourceHashAfter: r.SourceHashAfter, At: r.At.UTC()})
	}
	return out, nil
}
