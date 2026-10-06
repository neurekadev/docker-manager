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

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
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

	// retryOf is the job a retry re-runs (Engine.Retry only).
	retryOf string
}

// MaxIdempotencyKeyLen bounds idempotency keys.
const MaxIdempotencyKeyLen = 128

// Enqueue validates, authorizes and stores a queued job. created is false
// when an existing job was returned for a repeated idempotency key.
func (e *Engine) Enqueue(ctx context.Context, req Request) (job domain.Job, created bool, err error) {
	j, spec, err := e.prepare(ctx, req)
	if err != nil {
		return domain.Job{}, false, err
	}
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		existing, found, err := e.insert(ctx, tx, &j, spec, req.Principal)
		if err != nil {
			return err
		}
		job, created = j, !found
		if found {
			job = existing
		}
		return nil
	})
	if err != nil {
		return domain.Job{}, false, err
	}
	if created {
		e.notify(job.ID)
		e.Wake()
	}
	return job, created, nil
}

// EnqueueAll validates, authorizes and stores reqs as queued jobs in one
// transaction: all of them or none, so a run that covers several
// environments starts everywhere or nowhere and the dispatcher never sees
// part of it (#247). Repeating a batch whose idempotency keys all match
// jobs stored before returns those jobs (created false); a batch that
// matches only some of them is domain.ErrJobIdempotencyConflict.
func (e *Engine) EnqueueAll(ctx context.Context, reqs []Request) (out []domain.Job, created bool, err error) {
	prepared := make([]domain.Job, len(reqs))
	specs := make([]jobspec.Spec, len(reqs))
	for i, req := range reqs {
		if prepared[i], specs[i], err = e.prepare(ctx, req); err != nil {
			return nil, false, err
		}
	}
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		out = make([]domain.Job, len(prepared))
		found := 0
		for i := range prepared {
			existing, ok, err := e.insert(ctx, tx, &prepared[i], specs[i], reqs[i].Principal)
			if err != nil {
				return err
			}
			out[i] = prepared[i]
			if ok {
				out[i] = existing
				found++
			}
		}
		if found != 0 && found != len(prepared) {
			return domain.ErrJobIdempotencyConflict
		}
		created = found == 0
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if created && len(out) > 0 {
		for _, j := range out {
			e.notify(j.ID)
		}
		e.Wake()
	}
	return out, created, nil
}

// prepare validates and authorizes req and builds its queued job (not
// stored yet).
func (e *Engine) prepare(ctx context.Context, req Request) (domain.Job, jobspec.Spec, error) {
	if e.opts.MoveLock.ReadOnly() {
		return domain.Job{}, jobspec.Spec{}, ErrManagerMoved
	}
	spec, ok := jobspec.Lookup(req.Kind)
	if !ok {
		return domain.Job{}, spec, fmt.Errorf("%w: %q", domain.ErrJobUnknownKind, req.Kind)
	}
	if !req.Principal.Valid() {
		return domain.Job{}, spec, fmt.Errorf("%w: invalid principal", domain.ErrJobInvalid)
	}
	if len(req.IdempotencyKey) > MaxIdempotencyKeyLen {
		return domain.Job{}, spec, fmt.Errorf("%w: idempotency key longer than %d bytes", domain.ErrJobInvalid, MaxIdempotencyKeyLen)
	}
	if spec.Executor == domain.ExecutorManager {
		e.mgrMu.Lock()
		_, ok := e.mgrExecs[spec.Kind]
		e.mgrMu.Unlock()
		if !ok {
			return domain.Job{}, spec, fmt.Errorf("%w: %s", domain.ErrJobKindUnavailable, spec.Kind)
		}
	}
	input, err := canonicalInput(req.Input)
	if err != nil {
		return domain.Job{}, spec, err
	}
	locks, err := spec.ComputeLocks(req.EnvironmentID, req.Targets)
	if err != nil {
		return domain.Job{}, spec, err
	}
	caps, err := spec.Capabilities(req.Targets, input)
	if err != nil {
		return domain.Job{}, spec, err
	}
	if d := e.authorize(ctx, req.Principal, caps, req.EnvironmentID, spec.AuthorizationTargets(req.Targets)); !d.Allowed {
		return domain.Job{}, spec, fmt.Errorf("%w: %s", domain.ErrJobForbidden, d.Reason)
	}
	// Archived environments are hidden from operations (#34): users and
	// API tokens cannot start work there. Policy sources refuse their
	// scheduled runs themselves; internal follow-ups keep the offline rules.
	if req.EnvironmentID != "" && !req.Principal.IsService() {
		env, err := store.GetEnvironment(ctx, e.db, req.EnvironmentID)
		if err == nil && env.Status == domain.EnvironmentArchived {
			return domain.Job{}, spec, fmt.Errorf("%w: %s", domain.ErrEnvironmentArchived, req.EnvironmentID)
		}
	}

	now := e.now()
	return domain.Job{
		ID: ids.New(), Kind: spec.Kind, Executor: spec.Executor, Origin: originOf(req.Principal),
		InitiatorUserID: req.Principal.UserID, InitiatorTokenID: req.Principal.TokenID, PolicyID: req.PolicyID,
		RetryOf:       req.retryOf,
		RequestID:     protocol.RequestIDOrEmpty(logging.RequestID(ctx)),
		EnvironmentID: req.EnvironmentID, Targets: slices.Clone(req.Targets), Input: input,
		InputHash:      inputHash(spec.Kind, req.EnvironmentID, req.PolicyID, req.Targets, input),
		IdempotencyKey: req.IdempotencyKey, Attempt: 1, State: domain.JobQueued,
		Progress: domain.JobProgress{Percent: -1}, Locks: locks, CreatedAt: now, UpdatedAt: now,
	}, spec, nil
}

// insert stores the prepared job j in tx with its queued event and audit
// record, or returns the job already stored under its idempotency key
// (found; a different input under the key is
// domain.ErrJobIdempotencyConflict).
func (e *Engine) insert(ctx context.Context, tx bun.Tx, j *domain.Job, spec jobspec.Spec, p authz.Principal) (existing domain.Job, found bool, err error) {
	if j.IdempotencyKey != "" {
		existing, found, err := store.FindJobByIdempotencyKey(ctx, tx, j)
		if err != nil {
			return domain.Job{}, false, err
		}
		if found {
			if existing.InputHash != j.InputHash || existing.RetryOf != j.RetryOf {
				return domain.Job{}, false, domain.ErrJobIdempotencyConflict
			}
			return existing, true, nil
		}
	}
	if spec.StartsContainers {
		if err := restoreInProgress(ctx, tx, j.Locks); err != nil {
			return domain.Job{}, false, err
		}
	}
	if err := store.InsertJob(ctx, tx, j); err != nil {
		if errors.Is(err, store.ErrIdempotencyKeyTaken) {
			return domain.Job{}, false, domain.ErrJobIdempotencyConflict
		}
		return domain.Job{}, false, err
	}
	if err := e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventState, State: domain.JobQueued,
		Message: "queued (" + string(j.Origin) + ")"}); err != nil {
		return domain.Job{}, false, err
	}
	if err := e.recordJob(ctx, tx, j, audit.ActionJobQueued, audit.ActorFor(p), domain.AuditSuccess, "", nil); err != nil {
		return domain.Job{}, false, err
	}
	return domain.Job{}, false, nil
}

// restoreInProgress refuses a kind that starts containers while a restore
// that has not ended holds or waits for a lock conflicting with locks
// (#10): the restore starts the previously running containers itself.
func restoreInProgress(ctx context.Context, db bun.IDB, locks []domain.JobLock) error {
	active, err := store.ListJobs(ctx, db, domain.JobFilter{Kinds: []domain.JobKind{jobspec.RestoreRun},
		States: []domain.JobState{domain.JobQueued, domain.JobBlocked, domain.JobDispatched, domain.JobRunning, domain.JobCancelling}})
	if err != nil {
		return err
	}
	for _, r := range active {
		if _, _, ok := jobspec.FirstConflict(locks, r.Locks); ok {
			return fmt.Errorf("%w (job %s)", domain.ErrRestoreInProgress, r.ID)
		}
	}
	return nil
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
		return nil, fmt.Errorf("%w: input larger than %s", domain.ErrJobInvalid, humanize.Bytes(MaxInputSize))
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
