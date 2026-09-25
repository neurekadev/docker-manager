package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Request asks the engine to run a job.
type Request struct {
	Kind domain.JobKind
	// Principal is the authenticated caller for manual (user) and API-token
	// requests, or authz.Service() for scheduled jobs. It determines the
	// origin and is stored as audit metadata only.
	Principal authz.Principal
	// PolicyID links scheduled jobs to their policy (optional).
	PolicyID      string
	EnvironmentID string
	Targets       []domain.JobTarget
	// Input is the kind-specific input: a JSON object (json.RawMessage,
	// []byte) or any value that marshals to one. nil means {}.
	Input any
	// IdempotencyKey (optional): repeating a request with the same key and
	// the same input returns the existing job; a different input under the
	// same key is rejected with domain.ErrJobIdempotencyConflict.
	IdempotencyKey string
}

// MaxIdempotencyKeyLen bounds idempotency keys.
const MaxIdempotencyKeyLen = 128

// Enqueue validates, authorizes and stores a queued job. created is false
// when an existing job was returned for a repeated idempotency key.
func (e *Engine) Enqueue(ctx context.Context, req Request) (job domain.Job, created bool, err error) {
	spec, ok := jobspec.Lookup(req.Kind)
	if !ok {
		return domain.Job{}, false, fmt.Errorf("%w: %q", domain.ErrJobUnknownKind, req.Kind)
	}
	if !req.Principal.Valid() {
		return domain.Job{}, false, fmt.Errorf("%w: invalid principal", domain.ErrJobInvalid)
	}
	if len(req.IdempotencyKey) > MaxIdempotencyKeyLen {
		return domain.Job{}, false, fmt.Errorf("%w: idempotency key longer than %d bytes", domain.ErrJobInvalid, MaxIdempotencyKeyLen)
	}
	if spec.Executor == domain.ExecutorManager {
		e.mgrMu.Lock()
		_, ok := e.mgrExecs[spec.Kind]
		e.mgrMu.Unlock()
		if !ok {
			return domain.Job{}, false, fmt.Errorf("%w: %s", domain.ErrJobKindUnavailable, spec.Kind)
		}
	}
	input, err := canonicalInput(req.Input)
	if err != nil {
		return domain.Job{}, false, err
	}
	locks, err := spec.ComputeLocks(req.EnvironmentID, req.Targets)
	if err != nil {
		return domain.Job{}, false, err
	}
	caps, err := spec.Capabilities(req.Targets, input)
	if err != nil {
		return domain.Job{}, false, err
	}
	if d := e.authorize(ctx, req.Principal, caps, req.EnvironmentID, spec.AuthorizationTargets(req.Targets)); !d.Allowed {
		return domain.Job{}, false, fmt.Errorf("%w: %s", domain.ErrJobForbidden, d.Reason)
	}
	// Archived environments are hidden from operations (#34): users and
	// API tokens cannot start work there. Policy sources refuse their
	// scheduled runs themselves; internal follow-ups keep the offline rules.
	if req.EnvironmentID != "" && !req.Principal.IsService() {
		env, err := store.GetEnvironment(ctx, e.db, req.EnvironmentID)
		if err == nil && env.Status == domain.EnvironmentArchived {
			return domain.Job{}, false, fmt.Errorf("%w: %s", domain.ErrEnvironmentArchived, req.EnvironmentID)
		}
	}

	now := e.now()
	j := domain.Job{
		ID: ids.New(), Kind: spec.Kind, Executor: spec.Executor, Origin: originOf(req.Principal),
		InitiatorUserID: req.Principal.UserID, InitiatorTokenID: req.Principal.TokenID, PolicyID: req.PolicyID,
		RequestID:     protocol.RequestIDOrEmpty(logging.RequestID(ctx)),
		EnvironmentID: req.EnvironmentID, Targets: slices.Clone(req.Targets), Input: input,
		InputHash:      inputHash(spec.Kind, req.EnvironmentID, req.PolicyID, req.Targets, input),
		IdempotencyKey: req.IdempotencyKey, Attempt: 1, State: domain.JobQueued,
		Progress: domain.JobProgress{Percent: -1}, Locks: locks, CreatedAt: now, UpdatedAt: now,
	}
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if j.IdempotencyKey != "" {
			existing, found, err := store.FindJobByIdempotencyKey(ctx, tx, &j)
			if err != nil {
				return err
			}
			if found {
				if existing.InputHash != j.InputHash {
					return domain.ErrJobIdempotencyConflict
				}
				job = existing
				return nil
			}
		}
		if err := store.InsertJob(ctx, tx, &j); err != nil {
			if errors.Is(err, store.ErrIdempotencyKeyTaken) {
				return domain.ErrJobIdempotencyConflict
			}
			return err
		}
		if err := e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventState, State: domain.JobQueued,
			Message: "queued (" + string(j.Origin) + ")"}); err != nil {
			return err
		}
		if err := e.recordJob(ctx, tx, &j, audit.ActionJobQueued, audit.ActorFor(req.Principal), domain.AuditSuccess, "", nil); err != nil {
			return err
		}
		job, created = j, true
		return nil
	})
	if err != nil {
		return domain.Job{}, false, err
	}
	if created {
		if err := faultinject.Point(ctx, PointEnqueueCommitted); err != nil {
			return job, created, err
		}
		e.Wake()
	}
	return job, created, nil
}

func originOf(p authz.Principal) domain.JobOrigin {
	switch p.Kind {
	case authz.KindService:
		return domain.OriginScheduled
	case authz.KindAPIToken:
		return domain.OriginAPIToken
	}
	return domain.OriginManual
}

// principalOf reconstructs the initiating principal of a stored job.
func principalOf(j *domain.Job) authz.Principal {
	switch j.Origin {
	case domain.OriginAPIToken:
		return authz.Principal{Kind: authz.KindAPIToken, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	case domain.OriginManual:
		return authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID}
	}
	return authz.Service()
}

// authorize checks the kind's capabilities (jobspec.Spec.Capabilities) on
// every target (the operation's full effect, #17; file paths are covered
// by their stack or volume root, authz.TargetResources) except the kind's
// lock-only targets (jobspec.Spec.AuthorizationTargets, migrations #35). The service
// identity runs scheduled work and is not subject to user grants.
func (e *Engine) authorize(ctx context.Context, p authz.Principal, caps []string, env string, targets []domain.JobTarget) authz.Decision {
	if p.IsService() {
		return authz.Allow("manager service identity")
	}
	c := authz.For(ctx, e.opts.Authorizer, p)
	for _, r := range authz.TargetResources(env, targets) {
		for _, capability := range caps {
			if d := c.Can(capability, r); !d.Allowed {
				if d.Reason == "" {
					d.Reason = "denied"
				}
				return d
			}
		}
	}
	return authz.Allow("granted")
}

// canonicalInput normalizes the input to a canonical JSON object (sorted
// keys, numbers preserved) so equal inputs hash equally.
func canonicalInput(in any) ([]byte, error) {
	var raw []byte
	switch v := in.(type) {
	case nil:
		raw = []byte("{}")
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("%w: input: %v", domain.ErrJobInvalid, err)
		}
		raw = b
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > MaxInputSize {
		return nil, fmt.Errorf("%w: input larger than %d bytes", domain.ErrJobInvalid, MaxInputSize)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: input must be a JSON object", domain.ErrJobInvalid)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after input", domain.ErrJobInvalid)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: input: %v", domain.ErrJobInvalid, err)
	}
	return out, nil
}

// inputHash identifies a request for idempotency: kind, environment, policy,
// the sorted target set and the canonical input.
func inputHash(kind domain.JobKind, env, policy string, targets []domain.JobTarget, input []byte) string {
	keys := make([]string, 0, len(targets))
	for _, t := range targets {
		keys = append(keys, strings.Join([]string{string(t.Type), t.EnvironmentID, t.ID}, "\x00"))
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	b, _ := json.Marshal(struct {
		Kind    string          `json:"kind"`
		Env     string          `json:"env"`
		Policy  string          `json:"policy"`
		Targets []string        `json:"targets"`
		Input   json.RawMessage `json:"input"`
	}{string(kind), env, policy, keys, input})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
