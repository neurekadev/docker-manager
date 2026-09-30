package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Environment, agent, credential and enrollment persistence (#3). The
// service in internal/manager/agents owns every state change and runs
// multi-statement changes in one transaction.

type environmentRow struct {
	bun.BaseModel `bun:"table:environments"`

	ID                     string     `bun:"id,pk"`
	Name                   string     `bun:"name,notnull"`
	ServiceAddress         string     `bun:"service_address,notnull"`
	EngineID               string     `bun:"engine_id,notnull"`
	InstallID              string     `bun:"install_id,notnull"`
	AgentID                *string    `bun:"agent_id"`
	Status                 string     `bun:"status,notnull"`
	Online                 int        `bun:"online,notnull"`
	ConnectionChangedAt    *time.Time `bun:"connection_changed_at,nullzero"`
	LastSeenAt             *time.Time `bun:"last_seen_at,nullzero"`
	AllowDuplicateEngineID int        `bun:"allow_duplicate_engine_id,notnull"`
	Revision               int64      `bun:"revision,notnull"`
	CreatedAt              time.Time  `bun:"created_at,notnull"`
	UpdatedAt              time.Time  `bun:"updated_at,notnull"`
	ArchivedAt             *time.Time `bun:"archived_at,nullzero"`
}

func fromEnvironment(e *domain.Environment) environmentRow {
	r := environmentRow{
		ID: e.ID, Name: e.Name, ServiceAddress: e.ServiceAddress, EngineID: e.EngineID, InstallID: e.InstallID,
		Status: string(e.Status), Online: b2i(e.Online), ConnectionChangedAt: utcPtr(e.ConnectionChangedAt),
		LastSeenAt: utcPtr(e.LastSeenAt), AllowDuplicateEngineID: b2i(e.AllowDuplicateEngineID), Revision: e.Revision,
		CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC(), ArchivedAt: utcPtr(e.ArchivedAt),
	}
	if e.AgentID != "" {
		id := e.AgentID
		r.AgentID = &id
	}
	return r
}

func (r environmentRow) toDomain() domain.Environment {
	e := domain.Environment{
		ID: r.ID, Name: r.Name, ServiceAddress: r.ServiceAddress, EngineID: r.EngineID, InstallID: r.InstallID,
		Status: domain.EnvironmentStatus(r.Status), Online: r.Online == 1, ConnectionChangedAt: utcPtr(r.ConnectionChangedAt),
		LastSeenAt: utcPtr(r.LastSeenAt), AllowDuplicateEngineID: r.AllowDuplicateEngineID == 1, Revision: r.Revision,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), ArchivedAt: utcPtr(r.ArchivedAt),
	}
	if r.AgentID != nil {
		e.AgentID = *r.AgentID
	}
	return e
}

type agentRow struct {
	bun.BaseModel `bun:"table:agents"`

	ID              string     `bun:"id,pk"`
	EnvironmentID   string     `bun:"environment_id,notnull"`
	EnrollmentID    string     `bun:"enrollment_id,notnull"`
	InstallID       string     `bun:"install_id,notnull"`
	EngineID        string     `bun:"engine_id,notnull"`
	Hostname        string     `bun:"hostname,notnull"`
	Label           string     `bun:"label,notnull"`
	Version         string     `bun:"version,notnull"`
	VersionStatus   string     `bun:"version_status,notnull"`
	Status          string     `bun:"status,notnull"`
	RevokedReason   string     `bun:"revoked_reason,notnull"`
	Capabilities    string     `bun:"capabilities,notnull"`
	CapabilitiesAt  *time.Time `bun:"capabilities_at,nullzero"`
	SessionID       string     `bun:"session_id,notnull"`
	Revision        int64      `bun:"revision,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
	LastConnectedAt *time.Time `bun:"last_connected_at,nullzero"`
	LastSeenAt      *time.Time `bun:"last_seen_at,nullzero"`
	RevokedAt       *time.Time `bun:"revoked_at,nullzero"`
}

func fromAgent(a *domain.Agent) agentRow {
	return agentRow{
		ID: a.ID, EnvironmentID: a.EnvironmentID, EnrollmentID: a.EnrollmentID, InstallID: a.InstallID, EngineID: a.EngineID,
		Hostname: a.Hostname, Label: a.Label, Version: a.Version, VersionStatus: a.VersionStatus, Status: string(a.Status),
		RevokedReason: a.RevokedReason, Capabilities: a.Capabilities, CapabilitiesAt: utcPtr(a.CapabilitiesAt),
		SessionID: a.SessionID, Revision: a.Revision, CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
		LastConnectedAt: utcPtr(a.LastConnectedAt), LastSeenAt: utcPtr(a.LastSeenAt), RevokedAt: utcPtr(a.RevokedAt),
	}
}

func (r agentRow) toDomain() domain.Agent {
	return domain.Agent{
		ID: r.ID, EnvironmentID: r.EnvironmentID, EnrollmentID: r.EnrollmentID, InstallID: r.InstallID, EngineID: r.EngineID,
		Hostname: r.Hostname, Label: r.Label, Version: r.Version, VersionStatus: r.VersionStatus, Status: domain.AgentStatus(r.Status),
		RevokedReason: r.RevokedReason, Capabilities: r.Capabilities, CapabilitiesAt: utcPtr(r.CapabilitiesAt),
		SessionID: r.SessionID, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		LastConnectedAt: utcPtr(r.LastConnectedAt), LastSeenAt: utcPtr(r.LastSeenAt), RevokedAt: utcPtr(r.RevokedAt),
	}
}

type credentialRow struct {
	bun.BaseModel `bun:"table:agent_credentials"`

	ID          string     `bun:"id,pk"`
	AgentID     string     `bun:"agent_id,notnull"`
	Verifier    string     `bun:"verifier,notnull"`
	State       string     `bun:"state,notnull"`
	Sealed      string     `bun:"sealed,notnull"`
	CreatedAt   time.Time  `bun:"created_at,notnull"`
	ActivatedAt *time.Time `bun:"activated_at,nullzero"`
	RevokedAt   *time.Time `bun:"revoked_at,nullzero"`
}

func (r credentialRow) toDomain() domain.AgentCredential {
	return domain.AgentCredential{ID: r.ID, AgentID: r.AgentID, Verifier: r.Verifier, State: domain.CredentialState(r.State),
		Sealed: r.Sealed, CreatedAt: r.CreatedAt.UTC(), ActivatedAt: utcPtr(r.ActivatedAt), RevokedAt: utcPtr(r.RevokedAt)}
}

type enrollmentRow struct {
	bun.BaseModel `bun:"table:agent_enrollments"`

	ID                     string     `bun:"id,pk"`
	Verifier               string     `bun:"verifier,notnull"`
	Intent                 string     `bun:"intent,notnull"`
	TargetID               string     `bun:"target_id,notnull"`
	EnvironmentName        string     `bun:"environment_name,notnull"`
	AllowDuplicateEngineID int        `bun:"allow_duplicate_engine_id,notnull"`
	CreatedBy              string     `bun:"created_by,notnull"`
	CreatedAt              time.Time  `bun:"created_at,notnull"`
	ExpiresAt              time.Time  `bun:"expires_at,notnull"`
	UsedAt                 *time.Time `bun:"used_at,nullzero"`
	RevokedAt              *time.Time `bun:"revoked_at,nullzero"`
	AgentID                string     `bun:"agent_id,notnull"`
	Rejection              string     `bun:"rejection,notnull"`
}

// rejectionJSON is the database encoding of an enrollment rejection.
type rejectionJSON struct {
	Code                  string    `json:"code"`
	Message               string    `json:"message"`
	At                    time.Time `json:"at"`
	EngineID              string    `json:"engineId,omitempty"`
	InstallID             string    `json:"installId,omitempty"`
	Hostname              string    `json:"hostname,omitempty"`
	ConflictAgentID       string    `json:"conflictAgentId,omitempty"`
	ConflictEnvironmentID string    `json:"conflictEnvironmentId,omitempty"`
}

func fromEnrollment(e *domain.Enrollment) enrollmentRow {
	r := enrollmentRow{
		ID: e.ID, Verifier: e.Verifier, Intent: string(e.Intent), TargetID: e.TargetID, EnvironmentName: e.EnvironmentName,
		AllowDuplicateEngineID: b2i(e.AllowDuplicateEngineID), CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt.UTC(),
		ExpiresAt: e.ExpiresAt.UTC(), UsedAt: utcPtr(e.UsedAt), RevokedAt: utcPtr(e.RevokedAt), AgentID: e.AgentID,
	}
	if rj := e.Rejection; rj != nil {
		r.Rejection = mustJSON(rejectionJSON{Code: rj.Code, Message: rj.Message, At: rj.At.UTC(), EngineID: rj.EngineID,
			InstallID: rj.InstallID, Hostname: rj.Hostname, ConflictAgentID: rj.ConflictAgentID, ConflictEnvironmentID: rj.ConflictEnvironmentID})
	}
	return r
}

func (r enrollmentRow) toDomain() (domain.Enrollment, error) {
	e := domain.Enrollment{
		ID: r.ID, Verifier: r.Verifier, Intent: domain.EnrollmentIntent(r.Intent), TargetID: r.TargetID,
		EnvironmentName: r.EnvironmentName, AllowDuplicateEngineID: r.AllowDuplicateEngineID == 1, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(), UsedAt: utcPtr(r.UsedAt), RevokedAt: utcPtr(r.RevokedAt),
		AgentID: r.AgentID,
	}
	if r.Rejection != "" {
		var rj rejectionJSON
		if err := json.Unmarshal([]byte(r.Rejection), &rj); err != nil {
			return e, fmt.Errorf("store: enrollment %s rejection: %w", r.ID, err)
		}
		e.Rejection = &domain.EnrollmentRejection{Code: rj.Code, Message: rj.Message, At: rj.At.UTC(), EngineID: rj.EngineID,
			InstallID: rj.InstallID, Hostname: rj.Hostname, ConflictAgentID: rj.ConflictAgentID, ConflictEnvironmentID: rj.ConflictEnvironmentID}
	}
	return e, nil
}

// --- environments ---

// InsertEnvironment stores a new environment.
func InsertEnvironment(ctx context.Context, db bun.IDB, e *domain.Environment) error {
	row := fromEnvironment(e)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert environment: %w", err)
	}
	return nil
}

// UpdateEnvironment writes every column of e. With expectRevision > 0 it
// only updates when the stored revision still equals it
// (domain.ErrRevisionMismatch otherwise).
func UpdateEnvironment(ctx context.Context, db bun.IDB, e *domain.Environment, expectRevision int64) error {
	row := fromEnvironment(e)
	q := db.NewUpdate().Model(&row).WherePK()
	if expectRevision > 0 {
		q = q.Where("revision = ?", expectRevision)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update environment: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if expectRevision > 0 {
			return domain.ErrRevisionMismatch
		}
		return domain.ErrEnvironmentNotFound
	}
	return nil
}

// GetEnvironment returns one environment.
func GetEnvironment(ctx context.Context, db bun.IDB, id string) (domain.Environment, error) {
	var row environmentRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Environment{}, domain.ErrEnvironmentNotFound
	}
	if err != nil {
		return domain.Environment{}, fmt.Errorf("store: get environment: %w", err)
	}
	return row.toDomain(), nil
}

// ListEnvironments returns environments matching f in ID order.
func ListEnvironments(ctx context.Context, db bun.IDB, f domain.EnvironmentFilter) ([]domain.Environment, error) {
	var rows []environmentRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if len(f.Statuses) > 0 {
		q = q.Where("status IN (?)", bun.List(f.Statuses))
	}
	if f.EngineID != "" {
		q = q.Where("engine_id = ?", f.EngineID)
	}
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list environments: %w", err)
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// SetEnvironmentsOffline marks every online environment offline (manager
// start: no session survives a restart). It returns the affected IDs.
func SetEnvironmentsOffline(ctx context.Context, db bun.IDB, now time.Time) ([]string, error) {
	var ids []string
	if err := db.NewSelect().Model((*environmentRow)(nil)).Column("id").Where("online = 1").Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("store: list online environments: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err := db.NewUpdate().Model((*environmentRow)(nil)).
		Set("online = 0").Set("connection_changed_at = ?", now.UTC()).Set("updated_at = ?", now.UTC()).
		Where("online = 1").Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: mark environments offline: %w", err)
	}
	return ids, nil
}

// --- agents ---

// InsertAgent stores a new agent.
func InsertAgent(ctx context.Context, db bun.IDB, a *domain.Agent) error {
	row := fromAgent(a)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert agent: %w", err)
	}
	return nil
}

// UpdateAgent writes every column of a (compare-and-swap on the revision
// when expectRevision > 0).
func UpdateAgent(ctx context.Context, db bun.IDB, a *domain.Agent, expectRevision int64) error {
	row := fromAgent(a)
	q := db.NewUpdate().Model(&row).WherePK()
	if expectRevision > 0 {
		q = q.Where("revision = ?", expectRevision)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update agent: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if expectRevision > 0 {
			return domain.ErrRevisionMismatch
		}
		return domain.ErrAgentNotFound
	}
	return nil
}

// TouchLastSeen records that an agent's live session was seen at at: the
// agent's last_seen_at while sessionID is still its session, and its
// environment's while that agent (with that session) is still the
// environment's agent. Times only move forward, and revision and
// updated_at stay as they are (being seen is not an edit). It reports
// whether sessionID is still the agent's session.
func TouchLastSeen(ctx context.Context, db bun.IDB, agentID, sessionID, environmentID string, at time.Time) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	at = at.UTC()
	n, err := db.NewSelect().Model((*agentRow)(nil)).Where("id = ? AND session_id = ?", agentID, sessionID).Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: find agent session: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if _, err := db.NewUpdate().Model((*agentRow)(nil)).Set("last_seen_at = ?", at).
		Where("id = ? AND session_id = ?", agentID, sessionID).
		Where("(last_seen_at IS NULL OR last_seen_at < ?)", at).Exec(ctx); err != nil {
		return false, fmt.Errorf("store: touch agent last seen: %w", err)
	}
	if _, err := db.NewUpdate().Model((*environmentRow)(nil)).Set("last_seen_at = ?", at).
		Where("id = ? AND agent_id = ?", environmentID, agentID).
		Where("(last_seen_at IS NULL OR last_seen_at < ?)", at).Exec(ctx); err != nil {
		return false, fmt.Errorf("store: touch environment last seen: %w", err)
	}
	return true, nil
}

// GetAgent returns one agent; RotationPending is filled in.
func GetAgent(ctx context.Context, db bun.IDB, id string) (domain.Agent, error) {
	var row agentRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, domain.ErrAgentNotFound
	}
	if err != nil {
		return domain.Agent{}, fmt.Errorf("store: get agent: %w", err)
	}
	a := row.toDomain()
	n, err := db.NewSelect().Model((*credentialRow)(nil)).Where("agent_id = ? AND state = ?", id, domain.CredentialPending).Count(ctx)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("store: count pending credentials: %w", err)
	}
	a.RotationPending = n > 0
	return a, nil
}

// ListAgents returns agents matching f, newest first.
func ListAgents(ctx context.Context, db bun.IDB, f domain.AgentFilter) ([]domain.Agent, error) {
	var rows []agentRow
	q := db.NewSelect().Model(&rows).Order("id DESC")
	if f.EnvironmentID != "" {
		q = q.Where("environment_id = ?", f.EnvironmentID)
	}
	if f.EngineID != "" {
		q = q.Where("engine_id = ?", f.EngineID)
	}
	if len(f.Statuses) > 0 {
		q = q.Where("status IN (?)", bun.List(f.Statuses))
	}
	if f.BeforeID != "" {
		q = q.Where("id < ?", f.BeforeID)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	var pending []string
	if len(rows) > 0 {
		agentIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			agentIDs = append(agentIDs, r.ID)
		}
		if err := db.NewSelect().Model((*credentialRow)(nil)).Column("agent_id").
			Where("state = ? AND agent_id IN (?)", domain.CredentialPending, bun.List(agentIDs)).Scan(ctx, &pending); err != nil {
			return nil, fmt.Errorf("store: list pending credentials: %w", err)
		}
	}
	out := make([]domain.Agent, 0, len(rows))
	for _, r := range rows {
		a := r.toDomain()
		for _, p := range pending {
			if p == a.ID {
				a.RotationPending = true
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// --- credentials ---

// InsertCredential stores a credential verifier.
func InsertCredential(ctx context.Context, db bun.IDB, c *domain.AgentCredential) error {
	row := credentialRow{ID: c.ID, AgentID: c.AgentID, Verifier: c.Verifier, State: string(c.State), Sealed: c.Sealed,
		CreatedAt: c.CreatedAt.UTC(), ActivatedAt: utcPtr(c.ActivatedAt), RevokedAt: utcPtr(c.RevokedAt)}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert credential: %w", err)
	}
	return nil
}

// GetCredential returns one credential.
func GetCredential(ctx context.Context, db bun.IDB, id string) (domain.AgentCredential, error) {
	var row credentialRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCredential{}, domain.ErrCredentialInvalid
	}
	if err != nil {
		return domain.AgentCredential{}, fmt.Errorf("store: get credential: %w", err)
	}
	return row.toDomain(), nil
}

// PendingCredential returns the agent's pending rotation credential, if any.
func PendingCredential(ctx context.Context, db bun.IDB, agentID string) (domain.AgentCredential, bool, error) {
	var row credentialRow
	err := db.NewSelect().Model(&row).Where("agent_id = ? AND state = ?", agentID, domain.CredentialPending).
		Order("id DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCredential{}, false, nil
	}
	if err != nil {
		return domain.AgentCredential{}, false, fmt.Errorf("store: pending credential: %w", err)
	}
	return row.toDomain(), true, nil
}

// ActivateCredential makes credential id the agent's only valid one: it
// becomes active (its sealed copy is dropped) and every other active or
// pending credential of the agent is revoked. It reports whether id was
// still pending or active.
func ActivateCredential(ctx context.Context, db bun.IDB, agentID, id string, now time.Time) (bool, error) {
	res, err := db.NewUpdate().Model((*credentialRow)(nil)).
		Set("state = ?", domain.CredentialActive).Set("sealed = ''").Set("activated_at = COALESCE(activated_at, ?)", now.UTC()).
		Where("id = ? AND agent_id = ? AND state IN (?)", id, agentID, bun.List([]domain.CredentialState{domain.CredentialPending, domain.CredentialActive})).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: activate credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	_, err = db.NewUpdate().Model((*credentialRow)(nil)).
		Set("state = ?", domain.CredentialRevoked).Set("sealed = ''").Set("revoked_at = ?", now.UTC()).
		Where("agent_id = ? AND id <> ? AND state IN (?)", agentID, id, bun.List([]domain.CredentialState{domain.CredentialPending, domain.CredentialActive})).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: revoke superseded credentials: %w", err)
	}
	return true, nil
}

// RevokeCredentials revokes every active or pending credential of an agent
// (and, with states, only those states).
func RevokeCredentials(ctx context.Context, db bun.IDB, agentID string, now time.Time, states ...domain.CredentialState) error {
	if len(states) == 0 {
		states = []domain.CredentialState{domain.CredentialActive, domain.CredentialPending}
	}
	_, err := db.NewUpdate().Model((*credentialRow)(nil)).
		Set("state = ?", domain.CredentialRevoked).Set("sealed = ''").Set("revoked_at = ?", now.UTC()).
		Where("agent_id = ? AND state IN (?)", agentID, bun.List(states)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: revoke credentials: %w", err)
	}
	return nil
}

// --- enrollments ---

// InsertEnrollment stores a new enrollment (verifier only).
func InsertEnrollment(ctx context.Context, db bun.IDB, e *domain.Enrollment) error {
	row := fromEnrollment(e)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert enrollment: %w", err)
	}
	return nil
}

// UpdateEnrollment writes every column of e.
func UpdateEnrollment(ctx context.Context, db bun.IDB, e *domain.Enrollment) error {
	row := fromEnrollment(e)
	res, err := db.NewUpdate().Model(&row).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update enrollment: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrEnrollmentNotFound
	}
	return nil
}

// ConsumeEnrollment marks a pending enrollment used by agentID. It reports
// false when the enrollment was used, revoked or expired meanwhile (the
// compare-and-set makes one-use atomic).
func ConsumeEnrollment(ctx context.Context, db bun.IDB, id, agentID string, now time.Time) (bool, error) {
	res, err := db.NewUpdate().Model((*enrollmentRow)(nil)).
		Set("used_at = ?", now.UTC()).Set("agent_id = ?", agentID).
		Where("id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", id, now.UTC()).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: consume enrollment: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetEnrollment returns one enrollment.
func GetEnrollment(ctx context.Context, db bun.IDB, id string) (domain.Enrollment, error) {
	var row enrollmentRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Enrollment{}, domain.ErrEnrollmentNotFound
	}
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("store: get enrollment: %w", err)
	}
	return row.toDomain()
}

// ListEnrollments returns enrollments newest first, before beforeID.
func ListEnrollments(ctx context.Context, db bun.IDB, beforeID string, limit int) ([]domain.Enrollment, error) {
	var rows []enrollmentRow
	q := db.NewSelect().Model(&rows).Order("id DESC")
	if beforeID != "" {
		q = q.Where("id < ?", beforeID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list enrollments: %w", err)
	}
	out := make([]domain.Enrollment, 0, len(rows))
	for _, r := range rows {
		e, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// DeleteEnrollmentsBefore deletes enrollments that expired before cutoff
// (used, revoked and unused alike); their verifiers are useless by then.
func DeleteEnrollmentsBefore(ctx context.Context, db bun.IDB, cutoff time.Time) (int64, error) {
	res, err := db.NewDelete().Model((*enrollmentRow)(nil)).Where("expires_at < ?", cutoff.UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: delete old enrollments: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
