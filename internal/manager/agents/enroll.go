package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authsep"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// ParseIntent parses "new", "replace:<agentId>" or "reattach:<environmentId>".
func ParseIntent(s string) (domain.EnrollmentIntent, string, error) {
	kind, target, hasTarget := strings.Cut(s, ":")
	switch domain.EnrollmentIntent(kind) {
	case "", domain.IntentNew:
		if hasTarget {
			return "", "", errors.New("intent new takes no target")
		}
		return domain.IntentNew, "", nil
	case domain.IntentReplace, domain.IntentReattach:
		if !ids.Valid(target) {
			return "", "", fmt.Errorf("intent %s needs a target ID (%s:<id>)", kind, kind)
		}
		return domain.EnrollmentIntent(kind), target, nil
	}
	return "", "", errors.New("intent must be new, replace:<agentId> or reattach:<environmentId>")
}

// FormatIntent renders an enrollment's intent in the API form.
func FormatIntent(e domain.Enrollment) string {
	if e.Intent == domain.IntentNew || e.TargetID == "" {
		return string(domain.IntentNew)
	}
	return string(e.Intent) + ":" + e.TargetID
}

// CreateEnrollment creates a one-use enrollment token.
func (s *Service) CreateEnrollment(ctx context.Context, r domain.EnrollmentSpec) (domain.CreatedEnrollment, error) {
	if r.Intent == "" {
		r.Intent = domain.IntentNew
	}
	if r.TTL == 0 {
		r.TTL = DefaultEnrollmentTTL
	}
	if r.TTL < MinEnrollmentTTL || r.TTL > MaxEnrollmentTTL {
		return domain.CreatedEnrollment{}, inputErr("expiresInSeconds", "must be between %d and %d", int(MinEnrollmentTTL.Seconds()), int(MaxEnrollmentTTL.Seconds()))
	}
	if r.EnvironmentName != "" {
		if err := validName(r.EnvironmentName); err != nil {
			return domain.CreatedEnrollment{}, inputErr("environmentName", "%v", err)
		}
	}
	if r.AllowDuplicateEngineID && r.Intent != domain.IntentNew {
		return domain.CreatedEnrollment{}, inputErr("allowDuplicateEngineId", "only applies to intent new")
	}
	now := s.now()
	id := ids.New()
	minted, err := authsep.MintEnrollmentToken(id)
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	en := domain.Enrollment{
		ID: id, Verifier: minted.Verifier, Intent: r.Intent, TargetID: r.TargetID, EnvironmentName: r.EnvironmentName,
		AllowDuplicateEngineID: r.AllowDuplicateEngineID, CreatedBy: r.CreatedBy, CreatedAt: now, ExpiresAt: now.Add(r.TTL),
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		switch r.Intent {
		case domain.IntentNew:
			if r.TargetID != "" {
				return inputErr("intent", "intent new takes no target")
			}
		case domain.IntentReplace:
			a, err := store.GetAgent(ctx, tx, r.TargetID)
			if errors.Is(err, domain.ErrAgentNotFound) || (err == nil && a.Status != domain.AgentActive) {
				return inputErr("intent", "agent %s is not an active agent; only an active agent can be replaced", r.TargetID)
			}
			if err != nil {
				return err
			}
		case domain.IntentReattach:
			env, err := store.GetEnvironment(ctx, tx, r.TargetID)
			if errors.Is(err, domain.ErrEnvironmentNotFound) || (err == nil && !reattachable(env)) {
				return inputErr("intent", "environment %s is neither archived nor detached; only those can be re-attached", r.TargetID)
			}
			if err != nil {
				return err
			}
			if en.EnvironmentName == "" {
				en.EnvironmentName = env.Name
			}
		default:
			return inputErr("intent", "unknown intent %q", r.Intent)
		}
		if _, err := store.DeleteEnrollmentsBefore(ctx, tx, now.Add(-enrollmentRetention)); err != nil {
			return err
		}
		return store.InsertEnrollment(ctx, tx, &en)
	})
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	s.log.Info("agent enrollment created", "enrollment_id", en.ID, "intent", en.Intent, "target_id", en.TargetID,
		"expires_at", en.ExpiresAt, "created_by", en.CreatedBy)
	s.publish(events.Event{Type: events.EnrollmentCreated, ResourceType: events.ResourceEnrollment, ResourceID: en.ID})
	managerURL := ""
	if s.opts.PublicURL != nil {
		managerURL = s.opts.PublicURL.String()
	}
	return domain.CreatedEnrollment{Enrollment: en, Token: minted.Token, ManagerURL: managerURL,
		Install: InstallCommands(managerURL, s.opts.AgentImage, minted.Token, en.EnvironmentName)}, nil
}

// reattachable: archived, or active but detached (its agent was removed).
func reattachable(env domain.Environment) bool {
	return env.Status == domain.EnvironmentArchived || env.AgentID == ""
}

// ListEnrollments returns enrollments newest first (before beforeID).
func (s *Service) ListEnrollments(ctx context.Context, beforeID string, limit int) ([]domain.Enrollment, error) {
	return store.ListEnrollments(ctx, s.db, beforeID, limit)
}

// GetEnrollment returns one enrollment.
func (s *Service) GetEnrollment(ctx context.Context, id string) (domain.Enrollment, error) {
	return store.GetEnrollment(ctx, s.db, id)
}

// RevokeEnrollment revokes an unused enrollment. Revoking an already
// revoked, used or expired enrollment changes nothing.
func (s *Service) RevokeEnrollment(ctx context.Context, id string) (domain.Enrollment, error) {
	var en domain.Enrollment
	changed := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if en, err = store.GetEnrollment(ctx, tx, id); err != nil {
			return err
		}
		if en.State(s.now()) != domain.EnrollmentPending {
			return nil
		}
		now := s.now()
		en.RevokedAt = &now
		changed = true
		return store.UpdateEnrollment(ctx, tx, &en)
	})
	if err == nil && changed {
		s.log.Info("agent enrollment revoked", "enrollment_id", id)
		s.publish(events.Event{Type: events.EnrollmentRevoked, ResourceType: events.ResourceEnrollment, ResourceID: id})
	}
	return en, err
}

// VersionError is an agent version outside the manager's window.
type VersionError struct{ Err error }

func (e *VersionError) Error() string { return e.Err.Error() }
func (e *VersionError) Unwrap() error { return e.Err }

// enrollPlan is the outcome of an enrollment decision.
type enrollPlan struct {
	env        domain.Environment
	newEnv     bool
	reattached bool
	replaced   *domain.Agent
	rejection  *domain.EnrollmentRejection
}

// Enroll exchanges an enrollment token for an agent credential. Every
// token problem (unknown, wrong secret, expired, revoked, used) is
// domain.ErrEnrollmentInvalid. A refused enrollment (duplicate Engine,
// Engine ID collision, archived environment, wrong Engine for a replace or
// re-attach) is a *domain.EnrollConflict; it is recorded on the enrollment
// for the owner and does not consume the token.
func (s *Service) Enroll(ctx context.Context, token string, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	id, secret, ok := authsep.ParseEnrollmentToken(token)
	if !ok {
		s.record(ctx, domain.AuditEvent{Action: AuditEnroll, Actor: anonymous(), Outcome: domain.AuditDenied, ErrorClass: "unauthenticated"})
		return protocol.EnrollResponse{}, domain.ErrEnrollmentInvalid
	}
	enrollTarget := []domain.AuditTarget{{Type: "agent_enrollment", ID: id}}
	versionStatus, err := protocol.CheckAgentVersion(s.opts.ManagerVersion, req.AgentVersion)
	if err != nil {
		s.record(ctx, domain.AuditEvent{Action: AuditEnroll, Actor: anonymous(), Outcome: domain.AuditFailure,
			ErrorClass: "version_unsupported", Targets: enrollTarget, Details: map[string]any{"agent_version": req.AgentVersion}})
		return protocol.EnrollResponse{}, &VersionError{Err: err}
	}
	var (
		plan     enrollPlan
		agent    domain.Agent
		minted   authsep.Minted
		enrolled domain.Enrollment
	)
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := s.now()
		en, err := store.GetEnrollment(ctx, tx, id)
		if errors.Is(err, domain.ErrEnrollmentNotFound) {
			return domain.ErrEnrollmentInvalid
		}
		if err != nil {
			return err
		}
		if !authsep.VerifierMatches(en.Verifier, secret) || en.State(now) != domain.EnrollmentPending {
			return domain.ErrEnrollmentInvalid
		}
		enrolled = en
		plan, err = s.planEnrollment(ctx, tx, en, req)
		if err != nil {
			return err
		}
		if plan.rejection != nil {
			plan.rejection.At = now
			en.Rejection = plan.rejection
			return store.UpdateEnrollment(ctx, tx, &en) // commit the record, not the enrollment
		}

		agent = domain.Agent{
			ID: ids.New(), EnvironmentID: plan.env.ID, EnrollmentID: en.ID, InstallID: req.InstallID, EngineID: req.Engine.ID,
			Hostname: sanitizeName255(req.Hostname), Version: req.AgentVersion, VersionStatus: versionStatus,
			Status: domain.AgentActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if minted, err = authsep.MintAgentCredential(ids.New()); err != nil {
			return err
		}
		if r := plan.replaced; r != nil {
			if err := s.revokeAgentTx(ctx, tx, r, "replaced by agent "+agent.ID, now); err != nil {
				return err
			}
		}
		env := &plan.env
		env.AgentID, env.EngineID, env.InstallID = agent.ID, req.Engine.ID, req.InstallID
		env.Online, env.UpdatedAt = false, now
		if plan.reattached {
			env.Status, env.ArchivedAt = domain.EnvironmentActive, nil
		}
		if plan.newEnv {
			env.Revision, env.CreatedAt = 1, now
			if err := store.InsertEnvironment(ctx, tx, env); err != nil {
				return err
			}
		} else {
			env.Revision++
			if err := store.UpdateEnvironment(ctx, tx, env, 0); err != nil {
				return err
			}
		}
		if err := store.InsertAgent(ctx, tx, &agent); err != nil {
			return err
		}
		if err := store.InsertCredential(ctx, tx, &domain.AgentCredential{ID: minted.ID, AgentID: agent.ID, Verifier: minted.Verifier,
			State: domain.CredentialActive, CreatedAt: now, ActivatedAt: &now}); err != nil {
			return err
		}
		used, err := store.ConsumeEnrollment(ctx, tx, en.ID, agent.ID, now)
		if err != nil {
			return err
		}
		if !used {
			return domain.ErrEnrollmentInvalid // consumed concurrently
		}
		details := map[string]any{"intent": string(en.Intent), "engine_id": req.Engine.ID, "install_id": req.InstallID,
			"agent_version": req.AgentVersion, "reattached": plan.reattached}
		targets := append(agentTargets(agent.ID, env.ID), enrollTarget...)
		if r := plan.replaced; r != nil {
			details["replaced_agent_id"] = r.ID
			targets = append(targets, domain.AuditTarget{Type: "agent", ID: r.ID, EnvironmentID: env.ID})
		}
		return s.recordTx(ctx, tx, domain.AuditEvent{Action: AuditEnroll, Actor: audit.AgentActor(agent.ID),
			EnvironmentID: env.ID, Targets: targets, Details: details})
	})
	if errors.Is(err, domain.ErrEnrollmentInvalid) {
		s.record(ctx, domain.AuditEvent{Action: AuditEnroll, Actor: anonymous(), Outcome: domain.AuditDenied,
			ErrorClass: "unauthenticated", Targets: enrollTarget})
	}
	if err != nil {
		return protocol.EnrollResponse{}, err
	}
	if rj := plan.rejection; rj != nil {
		s.record(ctx, domain.AuditEvent{Action: AuditEnroll, Actor: anonymous(), Outcome: domain.AuditFailure, ErrorClass: rj.Code,
			EnvironmentID: rj.ConflictEnvironmentID, Targets: append(agentTargets(rj.ConflictAgentID, rj.ConflictEnvironmentID), enrollTarget...),
			Details: map[string]any{"engine_id": rj.EngineID, "install_id": rj.InstallID}})
		s.log.Warn("agent enrollment refused", "enrollment_id", enrolled.ID, "code", rj.Code, "engine_id", rj.EngineID,
			"install_id", rj.InstallID, "hostname", rj.Hostname, "conflict_agent_id", rj.ConflictAgentID,
			"conflict_environment_id", rj.ConflictEnvironmentID)
		s.publish(events.Event{Type: events.EnrollmentRejected, ResourceType: events.ResourceEnrollment, ResourceID: enrolled.ID,
			EnvironmentID: rj.ConflictEnvironmentID, Attributes: map[string]string{"code": rj.Code}})
		return protocol.EnrollResponse{}, &domain.EnrollConflict{Rejection: *rj}
	}
	env := plan.env
	s.log.Info("agent enrolled", "agent_id", agent.ID, "environment_id", env.ID, "enrollment_id", enrolled.ID,
		"intent", enrolled.Intent, "engine_id", agent.EngineID, "install_id", agent.InstallID, "agent_version", agent.Version,
		"version_status", versionStatus)
	evs := []events.Event{
		{Type: events.EnrollmentUsed, ResourceType: events.ResourceEnrollment, ResourceID: enrolled.ID},
		{Type: events.AgentEnrolled, ResourceType: events.ResourceAgent, ResourceID: agent.ID, EnvironmentID: env.ID, Revision: agent.Revision},
	}
	switch {
	case plan.newEnv:
		evs = append(evs, events.Event{Type: events.EnvironmentCreated, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision})
	case plan.reattached:
		evs = append(evs, events.Event{Type: events.EnvironmentReattached, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision})
	default:
		evs = append(evs, events.Event{Type: events.EnvironmentUpdated, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision})
	}
	if r := plan.replaced; r != nil {
		evs = append(evs, events.Event{Type: events.AgentRevoked, ResourceType: events.ResourceAgent, ResourceID: r.ID, EnvironmentID: r.EnvironmentID})
		s.log.Info("agent replaced; its credential is revoked", "agent_id", r.ID, "replaced_by", agent.ID)
		s.hub.kick(r.ID, protocol.CloseRevoked, "replaced by a newly enrolled agent")
	}
	s.publish(evs...)
	return protocol.EnrollResponse{AgentID: agent.ID, EnvironmentID: env.ID, EnvironmentName: env.Name,
		Credential: minted.Token, SessionPath: protocol.SessionPath, Reattached: plan.reattached}, nil
}

// planEnrollment decides what an enrollment does, or why it is refused.
func (s *Service) planEnrollment(ctx context.Context, tx bun.IDB, en domain.Enrollment, req protocol.EnrollRequest) (enrollPlan, error) {
	reject := func(code, msg string, agentID, envID string) (enrollPlan, error) {
		return enrollPlan{rejection: &domain.EnrollmentRejection{Code: code, Message: msg, EngineID: req.Engine.ID,
			InstallID: req.InstallID, Hostname: sanitizeName255(req.Hostname), ConflictAgentID: agentID, ConflictEnvironmentID: envID}}, nil
	}
	active, err := store.ListAgents(ctx, tx, domain.AgentFilter{EngineID: req.Engine.ID, Statuses: []domain.AgentStatus{domain.AgentActive}})
	if err != nil {
		return enrollPlan{}, err
	}
	envsForEngine, err := store.ListEnvironments(ctx, tx, domain.EnvironmentFilter{EngineID: req.Engine.ID})
	if err != nil {
		return enrollPlan{}, err
	}
	switch en.Intent {
	case domain.IntentReplace:
		target, err := store.GetAgent(ctx, tx, en.TargetID)
		if errors.Is(err, domain.ErrAgentNotFound) || (err == nil && target.Status != domain.AgentActive) {
			return reject(domain.ConflictTargetUnavailable, "the agent this enrollment was meant to replace is no longer active; create a new enrollment", en.TargetID, "")
		}
		if err != nil {
			return enrollPlan{}, err
		}
		if target.EngineID != req.Engine.ID {
			return reject(domain.ConflictEngineMismatch, fmt.Sprintf("this agent controls Docker Engine %s, but the agent it should replace controls %s; "+
				"replace only works for the same Engine", req.Engine.ID, target.EngineID), target.ID, target.EnvironmentID)
		}
		env, err := store.GetEnvironment(ctx, tx, target.EnvironmentID)
		if err != nil {
			return enrollPlan{}, err
		}
		if env.Status != domain.EnvironmentActive {
			return reject(domain.ConflictTargetUnavailable, "the environment of the agent to replace is archived; create a reattach enrollment", target.ID, env.ID)
		}
		return enrollPlan{env: env, replaced: &target}, nil

	case domain.IntentReattach:
		env, err := store.GetEnvironment(ctx, tx, en.TargetID)
		if errors.Is(err, domain.ErrEnvironmentNotFound) || (err == nil && !reattachable(env)) {
			return reject(domain.ConflictTargetUnavailable, "the environment to re-attach is no longer archived or detached; create a new enrollment", "", en.TargetID)
		}
		if err != nil {
			return enrollPlan{}, err
		}
		if env.EngineID != req.Engine.ID {
			return reject(domain.ConflictEngineMismatch, fmt.Sprintf("environment %q belongs to Docker Engine %s, but this agent controls %s; "+
				"re-attach only works for the same Engine", env.Name, env.EngineID, req.Engine.ID), "", env.ID)
		}
		for _, a := range active {
			if a.EnvironmentID != env.ID {
				return reject(domain.ConflictEngineAlreadyEnrolled, fmt.Sprintf("Docker Engine %s is already controlled by agent %s; remove it first", req.Engine.ID, a.ID), a.ID, a.EnvironmentID)
			}
		}
		return enrollPlan{env: env, reattached: env.Status == domain.EnvironmentArchived}, nil
	}

	// Intent new.
	for _, a := range active {
		if en.AllowDuplicateEngineID && a.InstallID != req.InstallID {
			continue // the owner declared a distinct host sharing the Engine ID
		}
		env, err := store.GetEnvironment(ctx, tx, a.EnvironmentID)
		if err != nil {
			return enrollPlan{}, err
		}
		if a.InstallID == req.InstallID || strings.EqualFold(a.Hostname, sanitizeName255(req.Hostname)) {
			return reject(domain.ConflictEngineAlreadyEnrolled, fmt.Sprintf("Docker Engine %s already has an active agent (%s, environment %q); "+
				"v1 allows one agent per Engine. To move to this agent, create an enrollment with intent replace:%s", req.Engine.ID, a.ID, env.Name, a.ID), a.ID, env.ID)
		}
		return reject(domain.ConflictEngineIdentity, fmt.Sprintf("Docker Engine ID %s is already enrolled from host %q (agent %s, environment %q), and this host is %q. "+
			"If this is the same Engine, create an enrollment with intent replace:%s. If this host is a cloned machine, regenerate its Engine ID "+
			"(remove /var/lib/docker/engine-id and restart Docker) or create an enrollment that allows the duplicate Engine ID",
			req.Engine.ID, a.Hostname, a.ID, env.Name, sanitizeName255(req.Hostname), a.ID), a.ID, env.ID)
	}
	if !en.AllowDuplicateEngineID {
		for _, env := range envsForEngine {
			switch {
			case env.Status == domain.EnvironmentArchived:
				return reject(domain.ConflictEnvironmentArchived, fmt.Sprintf("Docker Engine %s belongs to the archived environment %q; "+
					"create an enrollment with intent reattach:%s to re-attach it", req.Engine.ID, env.Name, env.ID), "", env.ID)
			case env.AgentID == "":
				return reject(domain.ConflictEnvironmentDetached, fmt.Sprintf("Docker Engine %s belongs to environment %q, whose agent was removed; "+
					"create an enrollment with intent reattach:%s to attach this agent", req.Engine.ID, env.Name, env.ID), "", env.ID)
			}
		}
	}
	name := en.EnvironmentName
	if name == "" {
		name = sanitizeName(req.EnvironmentName)
	}
	if name == "" {
		name = sanitizeName(req.Hostname)
	}
	if name == "" {
		name = "Environment " + shortID(req.Engine.ID)
	}
	return enrollPlan{newEnv: true, env: domain.Environment{ID: ids.New(), Name: name, Status: domain.EnvironmentActive,
		AllowDuplicateEngineID: en.AllowDuplicateEngineID}}, nil
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// sanitizeName255 keeps an agent-reported host name printable and bounded.
func sanitizeName255(s string) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) > protocol.MaxHostname {
		s = s[:protocol.MaxHostname]
	}
	return strings.TrimSpace(strings.ToValidUTF8(s, ""))
}

// revokeAgentTx revokes an agent and its credentials and detaches it from
// its environment (the caller re-attaches a replacement if any).
func (s *Service) revokeAgentTx(ctx context.Context, tx bun.IDB, a *domain.Agent, reason string, now time.Time) error {
	a.Status, a.RevokedAt, a.RevokedReason = domain.AgentRevoked, &now, reason
	a.SessionID, a.UpdatedAt = "", now
	a.Revision++
	if err := store.UpdateAgent(ctx, tx, a, 0); err != nil {
		return err
	}
	return store.RevokeCredentials(ctx, tx, a.ID, now)
}
