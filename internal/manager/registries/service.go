// Package registries owns the manager's registry connections (#19):
// owner-administered, write-only credentials sealed with the
// secret-protection key, deterministic matching of image references to
// connections, connection tests and digest checks through the manager's
// registry client (internal/manager/regclient), and the per-dispatch
// resolution of a job's registry credentials for the agent.
//
// Secrets leave this package only as protocol.RegistryCredential values
// inside one job command (CommandSecrets) or as the in-memory credential of
// one registry request; never in a domain value, log, audit record, job or
// API response.
package registries

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/regclient"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Audit actions recorded outside HTTP requests.
const (
	// ActionUse: a job command or a manager-side check used a connection's
	// credential (details: registryConnectionId, secretVersion; never the
	// secret).
	ActionUse = "registry.use"
)

// Limits of stored values.
const (
	MaxNameLen     = 100
	MaxUsernameLen = 255
	MaxSecretLen   = 8192
	MinPriority    = -1000
	MaxPriority    = 1000
)

// OwnerGuard enforces owner-only administration (auth.Service).
type OwnerGuard interface {
	RequireOwner(ctx context.Context, recent bool) (string, error)
}

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	// Guard enforces owner-only administration; nil refuses it.
	Guard OwnerGuard
	// Audit records credential use (HTTP changes are audited by the API).
	Audit audit.Recorder
	// Client performs registry checks; nil creates one.
	Client *regclient.Client
	// ForgetResource removes permission rules naming a deleted connection
	// (permissions.Service.ForgetResource); optional.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
}

// Service manages registry connections.
type Service struct {
	opts   Options
	db     *bun.DB
	client *regclient.Client
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil {
		return nil, errors.New("registries: DB and Keyring are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	client := opts.Client
	if client == nil {
		client = regclient.New(regclient.Options{Clock: opts.Clock, Logger: opts.Logger})
	}
	return &Service{opts: opts, db: opts.DB, client: client}, nil
}

func fieldErr(field, message string) error { return &domain.FieldError{Field: field, Message: message} }

func sealContext(id string) string { return "registry_connections/" + id + "/secret" }

func (s *Service) owner(ctx context.Context, recent bool) error {
	if s.opts.Guard == nil {
		return domain.ErrForbidden
	}
	_, err := s.opts.Guard.RequireOwner(ctx, recent)
	return err
}

// List returns connections in creation order (metadata only).
func (s *Service) List(ctx context.Context, afterID string, limit int) ([]domain.RegistryConnection, error) {
	return store.ListRegistryConnections(ctx, s.db, "", afterID, limit)
}

// Get returns one connection (metadata only).
func (s *Service) Get(ctx context.Context, id string) (domain.RegistryConnection, error) {
	return store.GetRegistryConnection(ctx, s.db, id)
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return "", fieldErr("name", fmt.Sprintf("must be 1 to %d characters", MaxNameLen))
	}
	return name, nil
}

func validSecret(secret string) error {
	if secret == "" || len(secret) > MaxSecretLen || strings.ContainsAny(secret, "\r\n") {
		return fieldErr("secret", fmt.Sprintf("must be 1 to %d bytes without line breaks", MaxSecretLen))
	}
	return nil
}

func validUsername(u string) error {
	if u == "" || len(u) > MaxUsernameLen || strings.ContainsAny(u, ":\r\n") {
		return fieldErr("username", fmt.Sprintf("must be 1 to %d characters without ':' or line breaks", MaxUsernameLen))
	}
	return nil
}

func (s *Service) validBinding(ctx context.Context, envID, stackID string) error {
	if envID != "" && stackID != "" {
		return fieldErr("stackId", "bind a connection to an environment or to a stack, not both")
	}
	if len(stackID) > 64 {
		return fieldErr("stackId", "too long")
	}
	if envID == "" {
		return nil
	}
	env, err := store.GetEnvironment(ctx, s.db, envID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) || (err == nil && env.Status != domain.EnvironmentActive) {
		return fieldErr("environmentId", "no such active environment")
	}
	return err
}

// Create stores a new connection (owner, recent step-up).
func (s *Service) Create(ctx context.Context, in domain.RegistryConnectionInput) (domain.RegistryConnection, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.RegistryConnection{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	host, err := imageref.NormalizeHost(in.Host)
	if err != nil {
		return domain.RegistryConnection{}, fieldErr("host", err.Error())
	}
	if in.CredentialType == "" {
		in.CredentialType = domain.RegistryCredentialToken
	}
	if !in.CredentialType.Valid() {
		return domain.RegistryConnection{}, fieldErr("credentialType", "must be password or token")
	}
	if err := validUsername(in.Username); err != nil {
		return domain.RegistryConnection{}, err
	}
	if err := validSecret(in.Secret); err != nil {
		return domain.RegistryConnection{}, err
	}
	pattern, err := imageref.ParsePattern(host, in.RepositoryPattern)
	if err != nil {
		return domain.RegistryConnection{}, fieldErr("repositoryPattern", err.Error())
	}
	if in.Priority < MinPriority || in.Priority > MaxPriority {
		return domain.RegistryConnection{}, fieldErr("priority", fmt.Sprintf("must be between %d and %d", MinPriority, MaxPriority))
	}
	if in.PlainHTTP && imageref.IsDockerHub(host) {
		return domain.RegistryConnection{}, fieldErr("plainHttp", "Docker Hub is HTTPS only")
	}
	if err := s.validBinding(ctx, in.EnvironmentID, in.StackID); err != nil {
		return domain.RegistryConnection{}, err
	}
	now := s.opts.Clock.Now().UTC()
	c := domain.RegistryConnection{
		ID: ids.New(), Name: name, Host: host, CredentialType: in.CredentialType, Username: in.Username,
		RepositoryPattern: string(pattern), EnvironmentID: in.EnvironmentID, StackID: in.StackID, Priority: in.Priority,
		PlainHTTP: in.PlainHTTP, Status: domain.RegistryConnectionActive, SecretVersion: 1, SecretUpdatedAt: now,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	sealed, err := s.opts.Keyring.Seal([]byte(in.Secret), sealContext(c.ID))
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	c.SecretFingerprint = s.opts.Keyring.Fingerprint([]byte(in.Secret), sealContext(c.ID))
	if err := store.InsertRegistryConnection(ctx, s.db, &c, sealed); err != nil {
		return domain.RegistryConnection{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "registry", ID: c.ID})
	audit.SetDetail(ctx, "host", c.Host)
	audit.SetDetail(ctx, "secretFingerprint", c.SecretFingerprint)
	return c, nil
}

// Update edits metadata or revokes the credential (owner, recent step-up).
func (s *Service) Update(ctx context.Context, id string, revision int64, p domain.RegistryConnectionPatch) (domain.RegistryConnection, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.RegistryConnection{}, err
	}
	cur, err := store.GetRegistryConnection(ctx, s.db, id)
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	if cur.Revision != revision {
		return domain.RegistryConnection{}, domain.ErrRevisionMismatch
	}
	next := cur
	if p.Name != nil {
		if next.Name, err = validName(*p.Name); err != nil {
			return domain.RegistryConnection{}, err
		}
	}
	if p.RepositoryPattern != nil {
		pattern, err := imageref.ParsePattern(cur.Host, *p.RepositoryPattern)
		if err != nil {
			return domain.RegistryConnection{}, fieldErr("repositoryPattern", err.Error())
		}
		next.RepositoryPattern = string(pattern)
	}
	if p.EnvironmentID != nil {
		next.EnvironmentID = *p.EnvironmentID
	}
	if p.StackID != nil {
		next.StackID = *p.StackID
	}
	if p.EnvironmentID != nil || p.StackID != nil {
		if err := s.validBinding(ctx, next.EnvironmentID, next.StackID); err != nil {
			return domain.RegistryConnection{}, err
		}
	}
	if p.Priority != nil {
		if *p.Priority < MinPriority || *p.Priority > MaxPriority {
			return domain.RegistryConnection{}, fieldErr("priority", fmt.Sprintf("must be between %d and %d", MinPriority, MaxPriority))
		}
		next.Priority = *p.Priority
	}
	if p.PlainHTTP != nil {
		if *p.PlainHTTP && imageref.IsDockerHub(cur.Host) {
			return domain.RegistryConnection{}, fieldErr("plainHttp", "Docker Hub is HTTPS only")
		}
		next.PlainHTTP = *p.PlainHTTP
	}
	revoke := false
	if p.Status != nil && *p.Status != cur.Status {
		if *p.Status != domain.RegistryConnectionRevoked {
			return domain.RegistryConnection{}, fieldErr("status", "only revoked can be set; rotate a new credential to re-activate the connection")
		}
		revoke = true
	}
	now := s.opts.Clock.Now().UTC()
	next.Revision, next.UpdatedAt = cur.Revision+1, now
	if revoke {
		// The sealed secret is erased; the connection keeps matching so
		// jobs fail visibly instead of pulling anonymously.
		next.Status, next.SecretFingerprint, next.RevokedAt = domain.RegistryConnectionRevoked, "", &now
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.UpdateRegistryConnection(ctx, tx, &next, revision); err != nil {
			return err
		}
		if !revoke {
			return nil
		}
		return store.ReplaceRegistrySecret(ctx, tx, &next, "", next.Revision)
	})
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	if revoke {
		s.client.Forget(cur.ID + "/")
	}
	audit.SetDiff(ctx, auditView(cur), auditView(next))
	return next, nil
}

// auditView is the audited metadata of a connection (no secret).
func auditView(c domain.RegistryConnection) map[string]any {
	return map[string]any{
		"name": c.Name, "host": c.Host, "credentialType": string(c.CredentialType), "username": c.Username,
		"repositoryPattern": c.RepositoryPattern, "environmentId": c.EnvironmentID, "stackId": c.StackID,
		"priority": c.Priority, "plainHttp": c.PlainHTTP, "status": string(c.Status),
		"secretFingerprint": c.SecretFingerprint, "secretVersion": c.SecretVersion,
	}
}

// Rotate replaces the credential (owner, recent step-up); a revoked
// connection becomes active again. Jobs dispatched afterwards use the new
// credential; commands already sent keep the old one for their attempt.
func (s *Service) Rotate(ctx context.Context, id string, revision int64, r domain.RegistryCredentialRotation) (domain.RegistryConnection, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.RegistryConnection{}, err
	}
	cur, err := store.GetRegistryConnection(ctx, s.db, id)
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	if cur.Revision != revision {
		return domain.RegistryConnection{}, domain.ErrRevisionMismatch
	}
	next := cur
	if r.Username != nil {
		if err := validUsername(*r.Username); err != nil {
			return domain.RegistryConnection{}, err
		}
		next.Username = *r.Username
	}
	if r.CredentialType != nil {
		if !r.CredentialType.Valid() {
			return domain.RegistryConnection{}, fieldErr("credentialType", "must be password or token")
		}
		next.CredentialType = *r.CredentialType
	}
	if err := validSecret(r.Secret); err != nil {
		return domain.RegistryConnection{}, err
	}
	sealed, err := s.opts.Keyring.Seal([]byte(r.Secret), sealContext(cur.ID))
	if err != nil {
		return domain.RegistryConnection{}, err
	}
	now := s.opts.Clock.Now().UTC()
	next.Status, next.RevokedAt = domain.RegistryConnectionActive, nil
	next.SecretFingerprint = s.opts.Keyring.Fingerprint([]byte(r.Secret), sealContext(cur.ID))
	next.SecretVersion, next.SecretUpdatedAt = cur.SecretVersion+1, now
	next.LastCheckAt, next.LastCheckResult = nil, ""
	next.Revision, next.UpdatedAt = cur.Revision+1, now
	if err := store.ReplaceRegistrySecret(ctx, s.db, &next, sealed, revision); err != nil {
		return domain.RegistryConnection{}, err
	}
	s.client.Forget(cur.ID + "/")
	audit.SetDiff(ctx, auditView(cur), auditView(next))
	return next, nil
}

// Delete removes a connection (owner, recent step-up). Queued jobs naming
// it fail at dispatch with credential_unavailable.
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	if err := s.owner(ctx, true); err != nil {
		return err
	}
	cur, err := store.GetRegistryConnection(ctx, s.db, id)
	if err != nil {
		return err
	}
	if err := store.DeleteRegistryConnection(ctx, s.db, id, revision); err != nil {
		return err
	}
	s.client.Forget(id + "/")
	audit.SetDetail(ctx, "host", cur.Host)
	if s.opts.ForgetResource != nil {
		if _, err := s.opts.ForgetResource(ctx, authz.ResourceRef{Type: "registry", ID: id}); err != nil {
			s.opts.Logger.Warn("could not remove the permission rules of a deleted registry connection", "registry_connection_id", id, "error", err)
		}
	}
	return nil
}

// credential opens a connection's secret for one use.
func (s *Service) credential(ctx context.Context, id string) (domain.RegistryConnection, *regclient.Credential, int, error) {
	c, err := store.GetRegistryConnection(ctx, s.db, id)
	if err != nil {
		return domain.RegistryConnection{}, nil, 0, err
	}
	if !c.Active() {
		return c, nil, 0, domain.ErrRegistryConnectionRevoked
	}
	sealed, version, err := store.RegistrySecret(ctx, s.db, id)
	if err != nil {
		return c, nil, 0, err
	}
	if sealed == "" {
		return c, nil, 0, domain.ErrRegistryConnectionRevoked
	}
	pt, err := s.opts.Keyring.Open(sealed, sealContext(id))
	if err != nil {
		return c, nil, 0, fmt.Errorf("registries: open the credential of %s: %w", id, err)
	}
	return c, &regclient.Credential{Username: c.Username, Secret: logging.Secret(pt)}, version, nil
}

// Test is the outcome of a connection test.
type Test struct {
	Reference string
	OK        bool
	// ErrorClass and Message describe a failure (regclient classes).
	ErrorClass     string
	Message        string
	RetryAfter     time.Duration
	Digest         string
	PlatformDigest string
	MediaType      string
	CheckedAt      time.Time
}

// ConnectionTest checks a connection against one image reference with a
// fresh manifest request (owner). The reference must be on the
// connection's host and match its repository matcher.
func (s *Service) ConnectionTest(ctx context.Context, id, reference, platform string) (Test, error) {
	if err := s.owner(ctx, false); err != nil {
		return Test{}, err
	}
	ref, err := imageref.Parse(reference)
	if err != nil {
		return Test{}, fieldErr("imageReference", err.Error())
	}
	cur, err := store.GetRegistryConnection(ctx, s.db, id)
	if err != nil {
		return Test{}, err
	}
	if ref.Host != cur.Host {
		return Test{}, fieldErr("imageReference", "the image is on "+ref.Host+", not on this connection's host "+cur.Host)
	}
	if ok, _ := imageref.Pattern(cur.RepositoryPattern).Match(ref.Repository); !ok {
		return Test{}, fieldErr("imageReference", "the repository does not match this connection's repository matcher "+cur.RepositoryPattern)
	}
	if platform != "" {
		if _, err := regclient.ParsePlatform(platform); err != nil {
			return Test{}, fieldErr("platform", err.Error())
		}
	}
	c, cred, version, err := s.credential(ctx, id)
	if err != nil {
		return Test{}, err
	}
	res, rerr := s.client.Resolve(ctx, regclient.Request{Ref: ref, Platform: platform, Credential: cred,
		CredentialKey: credentialKey(c.ID, version), PlainHTTP: c.PlainHTTP, Fresh: true})
	out := Test{Reference: ref.String(), CheckedAt: s.opts.Clock.Now().UTC()}
	if rerr != nil {
		if ctx.Err() != nil {
			return Test{}, ctx.Err()
		}
		out.ErrorClass, out.Message = classOf(rerr), messageOf(rerr)
		var re *regclient.Error
		if errors.As(rerr, &re) && re.RetryAfter > 0 {
			out.RetryAfter = re.RetryAfter
		}
	} else {
		out.OK, out.Digest, out.PlatformDigest, out.MediaType = true, res.Digest, res.PlatformDigest, res.MediaType
	}
	result := domain.RegistryCheckOK
	if !out.OK {
		result = out.ErrorClass
	}
	if err := store.RecordRegistryCheck(ctx, s.db, c.ID, out.CheckedAt, result, out.OK); err != nil {
		return Test{}, err
	}
	audit.SetDetail(ctx, "result", result)
	audit.SetDetail(ctx, "reference", out.Reference)
	return out, nil
}

func credentialKey(id string, version int) string { return fmt.Sprintf("%s/%d", id, version) }

func classOf(err error) string {
	if c := regclient.ClassOf(err); c != "" {
		return c
	}
	return regclient.ClassUnavailable
}

func messageOf(err error) string {
	var re *regclient.Error
	if errors.As(err, &re) {
		return re.Message
	}
	return "the registry check failed"
}

// Preview matches a reference without enforcing a usable selection: it
// reports ambiguity (tied candidate IDs) and revoked selections instead of
// failing, for POST /registries/matches.
func (s *Service) Preview(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, []string, error) {
	ref, err := imageref.Parse(req.Reference)
	if err != nil {
		return domain.RegistrySelection{}, nil, fieldErr("imageReference", err.Error())
	}
	conns, err := store.ListRegistryConnections(ctx, s.db, ref.Host, "", 0)
	if err != nil {
		return domain.RegistrySelection{}, nil, err
	}
	if req.ConnectionID != "" {
		if _, err := store.GetRegistryConnection(ctx, s.db, req.ConnectionID); err != nil {
			return domain.RegistrySelection{}, nil, err
		}
	}
	return Match(conns, ref, req)
}

// Select is the resolver pulls, deployments, builds and updates use (#6,
// #7, #20, #33): it returns the connection an image reference uses, or an
// anonymous selection when none applies. Errors:
// *domain.AmbiguousRegistryError (explicit selection required),
// domain.ErrRegistryConnectionMismatch (explicit connection not a
// candidate), domain.ErrRegistryConnectionNotFound,
// domain.ErrRegistryConnectionRevoked (no anonymous fallback), a
// *FieldError for an invalid reference. Put the selected ID into the job
// input (jobspec.CredentialRefs); the credential is resolved at dispatch.
func (s *Service) Select(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error) {
	sel, tied, err := s.Preview(ctx, req)
	if err != nil {
		return sel, err
	}
	if len(tied) > 0 {
		return sel, &domain.AmbiguousRegistryError{Host: sel.Host, CandidateIDs: tied}
	}
	if sel.Selected != nil && !sel.Selected.Active() {
		return sel, domain.ErrRegistryConnectionRevoked
	}
	return sel, nil
}

// CheckRequest is a manager-side digest check (#20).
type CheckRequest struct {
	domain.RegistrySelectRequest
	// Platform selects the host platform's manifest ("linux/amd64").
	Platform string
	// JobID and EnvironmentID attribute the credential use in the audit.
	JobID string
}

// CheckResult is a digest check outcome.
type CheckResult struct {
	Selection domain.RegistrySelection
	Result    regclient.Result
}

// Check resolves the digest of a reference with the connection it matches
// (cached and deduplicated per registry/repository/tag/platform/credential,
// rate limits honored). A registry failure returns a *regclient.Error; an
// authentication failure never retries anonymously.
func (s *Service) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	sel, err := s.Select(ctx, req.RegistrySelectRequest)
	if err != nil {
		return CheckResult{Selection: sel}, err
	}
	ref, err := imageref.Parse(req.Reference)
	if err != nil {
		return CheckResult{Selection: sel}, fieldErr("imageReference", err.Error())
	}
	rq := regclient.Request{Ref: ref, Platform: req.Platform}
	var conn domain.RegistryConnection
	if sel.Selected != nil {
		c, cred, version, err := s.credential(ctx, sel.Selected.ID)
		if err != nil {
			return CheckResult{Selection: sel}, err
		}
		conn = c
		rq.Credential, rq.CredentialKey, rq.PlainHTTP = cred, credentialKey(c.ID, version), c.PlainHTTP
	}
	res, rerr := s.client.Resolve(ctx, rq)
	if sel.Selected != nil && !res.Cached && ctx.Err() == nil {
		result := domain.RegistryCheckOK
		if rerr != nil {
			result = classOf(rerr)
		}
		if err := store.RecordRegistryCheck(ctx, s.db, conn.ID, s.opts.Clock.Now(), result, rerr == nil); err != nil {
			s.opts.Logger.Warn("could not record a registry check", "registry_connection_id", conn.ID, "error", err)
		}
		s.recordUse(ctx, conn, req.JobID, req.EnvironmentID, "digest_check", errorClass(rerr))
	}
	return CheckResult{Selection: sel, Result: res}, rerr
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	return classOf(err)
}

// recordUse audits one use of a connection's credential.
func (s *Service) recordUse(ctx context.Context, c domain.RegistryConnection, jobID, envID, purpose, class string) {
	if s.opts.Audit == nil {
		return
	}
	outcome := domain.AuditSuccess
	if class != "" {
		outcome = domain.AuditFailure
	}
	err := s.opts.Audit.Record(ctx, domain.AuditEvent{
		Category: domain.AuditCredentials, Action: ActionUse, Actor: audit.ServiceActor(), EnvironmentID: envID, JobID: jobID,
		Targets: []domain.AuditTarget{{Type: "registry", ID: c.ID}}, Outcome: outcome, ErrorClass: class,
		Details: map[string]any{"registryConnectionId": c.ID, "secretVersion": c.SecretVersion, "host": c.Host, "purpose": purpose},
	})
	if err != nil {
		s.opts.Logger.Error("could not audit a registry credential use", "registry_connection_id", c.ID, "error", err)
	}
}

// CredentialsError is returned by CommandSecrets when a referenced
// connection cannot be used; its message names the connection, never a
// secret.
type CredentialsError struct {
	ConnectionID string
	Reason       string
}

func (e *CredentialsError) Error() string {
	return "registry connection " + e.ConnectionID + " " + e.Reason
}

// CommandSecrets resolves the registry connections a job's input names
// (jobspec.CredentialRefs) into the credentials of one agent command. It
// runs at every dispatch, so a rotation applies to every later attempt,
// and audits each use.
func (s *Service) CommandSecrets(ctx context.Context, j *domain.Job) ([]protocol.RegistryCredential, error) {
	refs, err := jobspec.CredentialRefsOf(j.Input)
	if err != nil {
		return nil, err
	}
	var out []protocol.RegistryCredential
	seen := map[string]bool{}
	for _, id := range refs.RegistryConnections {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, cred, version, err := s.credential(ctx, id)
		switch {
		case errors.Is(err, domain.ErrRegistryConnectionNotFound):
			return nil, &CredentialsError{id, "was deleted"}
		case errors.Is(err, domain.ErrRegistryConnectionRevoked):
			s.recordUse(ctx, c, j.ID, j.EnvironmentID, "job", "registry_connection_revoked")
			return nil, &CredentialsError{id, "is revoked"}
		case err != nil:
			s.opts.Logger.Error("could not open a registry credential", "registry_connection_id", id, "error", err)
			return nil, &CredentialsError{id, "cannot be decrypted (check the secret-protection key)"}
		}
		c.SecretVersion = version
		out = append(out, protocol.RegistryCredential{ConnectionID: c.ID, Host: c.Host, ServerAddress: imageref.ServerAddress(c.Host),
			Username: cred.Username, Secret: string(cred.Secret)})
		s.recordUse(ctx, c, j.ID, j.EnvironmentID, "job", "")
	}
	return out, nil
}
