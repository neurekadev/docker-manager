// Package gitcreds owns the manager's Git credentials (#33): HTTPS
// username + token for private Git build contexts, handled like registry
// connections (#19): shared instance resources administered by the owner,
// sealed with the secret-protection key, write-only (keyed fingerprint
// only), resolved into one agent command at dispatch and never persisted
// on agents. Connection tests list the refs of a repository with an
// in-process ls-remote (internal/gitremote), no git CLI.
package gitcreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/gitremote"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// ActionUse audits a job's or test's use of a credential (IDs only).
const ActionUse = "git_credential.use"

// Limits.
const (
	MaxNameLen     = 100
	MaxUsernameLen = 255
	MaxSecretLen   = 8192
	MaxPrefixLen   = 255
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
	Guard   OwnerGuard
	Audit   audit.Recorder
	// HTTP is the client of connection tests (default: 60 s timeout).
	HTTP *http.Client
	// ForgetResource removes permission rules naming a deleted credential.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
}

// Service manages Git credentials.
type Service struct {
	opts Options
	db   *bun.DB
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil {
		return nil, errors.New("gitcreds: DB and Keyring are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{opts: opts, db: opts.DB}, nil
}

func fieldErr(field, message string) error { return &domain.FieldError{Field: field, Message: message} }

func sealContext(id string) string { return "git_credentials/" + id + "/secret" }

func (s *Service) owner(ctx context.Context, recent bool) error {
	if s.opts.Guard == nil {
		return domain.ErrForbidden
	}
	_, err := s.opts.Guard.RequireOwner(ctx, recent)
	return err
}

// List returns credentials in creation order (metadata only).
func (s *Service) List(ctx context.Context, afterID string, limit int) ([]domain.GitCredential, error) {
	return store.ListGitCredentials(ctx, s.db, "", afterID, limit)
}

// Get returns one credential (metadata only).
func (s *Service) Get(ctx context.Context, id string) (domain.GitCredential, error) {
	return store.GetGitCredential(ctx, s.db, id)
}

// NormalizeHost validates a Git host (host[:port]).
func NormalizeHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h = strings.TrimRight(h, "/")
	r, err := gitremote.ParseURL("https://" + h + "/x")
	if err != nil || r.Host() != h || strings.Contains(h, "/") {
		return "", errors.New("the host must be host or host:port")
	}
	return h, nil
}

// NormalizePrefix normalizes a repository path prefix ("acme/team").
func NormalizePrefix(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if len(p) > MaxPrefixLen || strings.ContainsAny(p, " \\#?:@*") || strings.Contains("/"+p+"/", "/../") || strings.Contains("/"+p+"/", "/./") {
		return "", errors.New("the path prefix must be repository path segments like acme or acme/team")
	}
	return p, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return "", fieldErr("name", fmt.Sprintf("must be 1 to %d characters", MaxNameLen))
	}
	return name, nil
}

func validUsername(u string) error {
	if u == "" || len(u) > MaxUsernameLen || strings.ContainsAny(u, ":\r\n") {
		return fieldErr("username", fmt.Sprintf("must be 1 to %d characters without ':' or line breaks", MaxUsernameLen))
	}
	return nil
}

func validSecret(v string) error {
	if v == "" || len(v) > MaxSecretLen || strings.ContainsAny(v, "\r\n") {
		return fieldErr("secret", fmt.Sprintf("must be 1 to %d bytes without line breaks", MaxSecretLen))
	}
	return nil
}

// Create stores a credential (owner, recent step-up).
func (s *Service) Create(ctx context.Context, in domain.GitCredentialInput) (domain.GitCredential, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.GitCredential{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return domain.GitCredential{}, err
	}
	host, err := NormalizeHost(in.Host)
	if err != nil {
		return domain.GitCredential{}, fieldErr("host", err.Error())
	}
	prefix, err := NormalizePrefix(in.PathPrefix)
	if err != nil {
		return domain.GitCredential{}, fieldErr("pathPrefix", err.Error())
	}
	if err := validUsername(in.Username); err != nil {
		return domain.GitCredential{}, err
	}
	if err := validSecret(in.Secret); err != nil {
		return domain.GitCredential{}, err
	}
	now := s.opts.Clock.Now().UTC()
	c := domain.GitCredential{ID: ids.New(), Name: name, Host: host, PathPrefix: prefix, Username: in.Username, PlainHTTP: in.PlainHTTP,
		Status: domain.RegistryConnectionActive, SecretVersion: 1, SecretUpdatedAt: now, Revision: 1, CreatedAt: now, UpdatedAt: now}
	sealed, err := s.opts.Keyring.Seal([]byte(in.Secret), sealContext(c.ID))
	if err != nil {
		return domain.GitCredential{}, err
	}
	c.SecretFingerprint = s.opts.Keyring.Fingerprint([]byte(in.Secret), sealContext(c.ID))
	if err := store.InsertGitCredential(ctx, s.db, &c, sealed); err != nil {
		return domain.GitCredential{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "git_credential", ID: c.ID})
	audit.SetDetail(ctx, "host", c.Host)
	return c, nil
}

func auditView(c domain.GitCredential) map[string]any {
	return map[string]any{"name": c.Name, "host": c.Host, "pathPrefix": c.PathPrefix, "username": c.Username, "plainHttp": c.PlainHTTP,
		"status": string(c.Status), "secretFingerprint": c.SecretFingerprint, "secretVersion": c.SecretVersion}
}

// Update edits, rotates (Secret) or revokes a credential (owner, recent
// step-up). A new secret re-activates a revoked credential.
func (s *Service) Update(ctx context.Context, id string, revision int64, p domain.GitCredentialPatch) (domain.GitCredential, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.GitCredential{}, err
	}
	cur, err := store.GetGitCredential(ctx, s.db, id)
	if err != nil {
		return domain.GitCredential{}, err
	}
	if cur.Revision != revision {
		return domain.GitCredential{}, domain.ErrRevisionMismatch
	}
	next := cur
	if p.Name != nil {
		if next.Name, err = validName(*p.Name); err != nil {
			return domain.GitCredential{}, err
		}
	}
	if p.PathPrefix != nil {
		if next.PathPrefix, err = NormalizePrefix(*p.PathPrefix); err != nil {
			return domain.GitCredential{}, fieldErr("pathPrefix", err.Error())
		}
	}
	if p.Username != nil {
		if err := validUsername(*p.Username); err != nil {
			return domain.GitCredential{}, err
		}
		next.Username = *p.Username
	}
	if p.PlainHTTP != nil {
		next.PlainHTTP = *p.PlainHTTP
	}
	now := s.opts.Clock.Now().UTC()
	sealed, keep := "", true
	switch {
	case p.Secret != nil && p.Status != nil && *p.Status == domain.RegistryConnectionRevoked:
		return domain.GitCredential{}, fieldErr("status", "revoke or set a new secret, not both")
	case p.Secret != nil:
		if err := validSecret(*p.Secret); err != nil {
			return domain.GitCredential{}, err
		}
		if sealed, err = s.opts.Keyring.Seal([]byte(*p.Secret), sealContext(id)); err != nil {
			return domain.GitCredential{}, err
		}
		keep = false
		next.Status, next.RevokedAt = domain.RegistryConnectionActive, nil
		next.SecretFingerprint = s.opts.Keyring.Fingerprint([]byte(*p.Secret), sealContext(id))
		next.SecretVersion, next.SecretUpdatedAt = cur.SecretVersion+1, now
		next.LastCheckAt, next.LastCheckResult = nil, ""
	case p.Status != nil && *p.Status != cur.Status:
		if *p.Status != domain.RegistryConnectionRevoked {
			return domain.GitCredential{}, fieldErr("status", "only revoked can be set; set a new secret to re-activate the credential")
		}
		keep = false
		next.Status, next.SecretFingerprint, next.RevokedAt = domain.RegistryConnectionRevoked, "", &now
	}
	next.Revision, next.UpdatedAt = cur.Revision+1, now
	if err := store.UpdateGitCredential(ctx, s.db, &next, sealed, keep, revision); err != nil {
		return domain.GitCredential{}, err
	}
	audit.SetDiff(ctx, auditView(cur), auditView(next))
	return next, nil
}

// Delete removes a credential (owner, recent step-up).
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	if err := s.owner(ctx, true); err != nil {
		return err
	}
	if err := store.DeleteGitCredential(ctx, s.db, id, revision); err != nil {
		return err
	}
	if s.opts.ForgetResource != nil {
		if _, err := s.opts.ForgetResource(ctx, authz.ResourceRef{Type: "git_credential", ID: id}); err != nil {
			s.opts.Logger.Warn("could not remove the permission rules of a deleted Git credential", "git_credential_id", id, "error", err)
		}
	}
	return nil
}

// credential opens a credential's token for one use.
func (s *Service) credential(ctx context.Context, id string) (domain.GitCredential, string, int, error) {
	c, err := store.GetGitCredential(ctx, s.db, id)
	if err != nil {
		return c, "", 0, err
	}
	sealed, version, err := store.GitSecret(ctx, s.db, id)
	if err != nil {
		return c, "", 0, err
	}
	if !c.Active() || sealed == "" {
		return c, "", 0, domain.ErrGitCredentialRevoked
	}
	pt, err := s.opts.Keyring.Open(sealed, sealContext(id))
	if err != nil {
		return c, "", 0, fmt.Errorf("gitcreds: open the credential of %s: %w", id, err)
	}
	return c, string(pt), version, nil
}

// repoPath is the repository path used for prefix matching ("acme/app.git").
func repoPath(r gitremote.Repo) string { return strings.Trim(r.Path(), "/") }

// matches reports whether c applies to repo and how specific it is.
func matches(c domain.GitCredential, repo gitremote.Repo) (bool, int) {
	if c.Host != repo.Host() {
		return false, 0
	}
	if c.PathPrefix == "" {
		return true, 0
	}
	p := repoPath(repo)
	if p == c.PathPrefix || strings.HasPrefix(p, c.PathPrefix+"/") {
		return true, strings.Count(c.PathPrefix, "/") + 1
	}
	return false, 0
}

// Select returns the credential a repository uses: the explicit one (it
// must apply to the repository), else the most specific matching one
// (a tie is *domain.AmbiguousGitCredentialError), else nil (anonymous). A
// revoked selection is domain.ErrGitCredentialRevoked: never anonymous.
func (s *Service) Select(ctx context.Context, repo gitremote.Repo, explicitID string) (*domain.GitCredential, error) {
	if explicitID != "" {
		c, err := store.GetGitCredential(ctx, s.db, explicitID)
		if err != nil {
			return nil, err
		}
		if ok, _ := matches(c, repo); !ok {
			return nil, domain.ErrGitCredentialMismatch
		}
		if !c.Active() {
			return nil, domain.ErrGitCredentialRevoked
		}
		return &c, nil
	}
	all, err := store.ListGitCredentials(ctx, s.db, repo.Host(), "", 0)
	if err != nil {
		return nil, err
	}
	type cand struct {
		c    domain.GitCredential
		spec int
	}
	var cands []cand
	for _, c := range all {
		if ok, spec := matches(c, repo); ok {
			cands = append(cands, cand{c, spec})
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].spec != cands[j].spec {
			return cands[i].spec > cands[j].spec
		}
		return cands[i].c.ID < cands[j].c.ID
	})
	var tied []string
	for _, c := range cands {
		if c.spec == cands[0].spec {
			tied = append(tied, c.c.ID)
		}
	}
	if len(tied) > 1 {
		return nil, &domain.AmbiguousGitCredentialError{Host: repo.Host(), CredentialIDs: tied}
	}
	c := cands[0].c
	if !c.Active() {
		return nil, domain.ErrGitCredentialRevoked
	}
	return &c, nil
}

// Test is the outcome of a connection test.
type Test struct {
	Repository string
	OK         bool
	ErrorClass string
	Message    string
	// Head is HEAD's target; Commit the commit of Ref (or HEAD).
	Head      string
	Ref       string
	Commit    string
	RefCount  int
	CheckedAt time.Time
}

// ConnectionTest lists the refs of a repository with the credential
// (owner): the equivalent of git ls-remote, from the manager.
func (s *Service) ConnectionTest(ctx context.Context, id, repository, ref string) (Test, error) {
	if err := s.owner(ctx, false); err != nil {
		return Test{}, err
	}
	repo, err := gitremote.ParseURL(repository)
	if err != nil {
		return Test{}, fieldErr("repositoryUrl", err.Error())
	}
	cur, err := store.GetGitCredential(ctx, s.db, id)
	if err != nil {
		return Test{}, err
	}
	if ok, _ := matches(cur, repo); !ok {
		return Test{}, fieldErr("repositoryUrl", "the repository is not on this credential's host "+cur.Host+
			func() string {
				if cur.PathPrefix != "" {
					return " below " + cur.PathPrefix
				}
				return ""
			}())
	}
	if !repo.Secure() && !cur.PlainHTTP {
		return Test{}, fieldErr("repositoryUrl", "the repository uses plain HTTP; allow plain HTTP on the credential or use https")
	}
	c, token, _, err := s.credential(ctx, id)
	if err != nil {
		return Test{}, err
	}
	out := Test{Repository: repo.String(), CheckedAt: s.opts.Clock.Now().UTC()}
	refs, lerr := gitremote.LsRemote(ctx, repo, gitremote.Options{HTTP: s.opts.HTTP, AllowPlainHTTP: c.PlainHTTP,
		Credential: &gitremote.Credential{Username: c.Username, Token: logging.Secret(token)}})
	if lerr == nil {
		out.Head, out.RefCount = refs.Head, len(refs.Commits)
		out.Commit, out.Ref, lerr = refs.Resolve(ref)
	}
	if lerr != nil {
		if ctx.Err() != nil {
			return Test{}, ctx.Err()
		}
		out.ErrorClass = gitremote.ClassOf(lerr)
		if out.ErrorClass == "" {
			out.ErrorClass = gitremote.ClassUnavailable
		}
		var ge *gitremote.Error
		if errors.As(lerr, &ge) {
			out.Message = ge.Message
		}
	} else {
		out.OK = true
	}
	result := domain.RegistryCheckOK
	if !out.OK {
		result = out.ErrorClass
	}
	if err := store.RecordGitCheck(ctx, s.db, c.ID, out.CheckedAt, result, out.OK); err != nil {
		return Test{}, err
	}
	audit.SetDetail(ctx, "result", result)
	return out, nil
}

func (s *Service) recordUse(ctx context.Context, c domain.GitCredential, j *domain.Job, class string) {
	if s.opts.Audit == nil {
		return
	}
	outcome := domain.AuditSuccess
	if class != "" {
		outcome = domain.AuditFailure
	}
	if err := s.opts.Audit.Record(ctx, domain.AuditEvent{
		Category: domain.AuditCredentials, Action: ActionUse, Actor: audit.ServiceActor(), EnvironmentID: j.EnvironmentID, JobID: j.ID,
		Targets: []domain.AuditTarget{{Type: "git_credential", ID: c.ID}}, Outcome: outcome, ErrorClass: class,
		Details: map[string]any{"gitCredentialId": c.ID, "secretVersion": c.SecretVersion, "host": c.Host},
	}); err != nil {
		s.opts.Logger.Error("could not audit a Git credential use", "git_credential_id", c.ID, "error", err)
	}
}

// CredentialsError names an unusable credential (never a secret).
type CredentialsError struct {
	CredentialID string
	Reason       string
}

func (e *CredentialsError) Error() string { return "Git credential " + e.CredentialID + " " + e.Reason }

// CommandSecrets resolves the Git credentials a job's input names into the
// credentials of one agent command (every dispatch; audited).
func (s *Service) CommandSecrets(ctx context.Context, j *domain.Job) ([]protocol.GitCredential, error) {
	refs, err := jobspec.CredentialRefsOf(j.Input)
	if err != nil {
		return nil, err
	}
	var out []protocol.GitCredential
	seen := map[string]bool{}
	for _, id := range refs.GitCredentials {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, token, version, err := s.credential(ctx, id)
		switch {
		case errors.Is(err, domain.ErrGitCredentialNotFound):
			return nil, &CredentialsError{id, "was deleted"}
		case errors.Is(err, domain.ErrGitCredentialRevoked):
			s.recordUse(ctx, c, j, "git_credential_revoked")
			return nil, &CredentialsError{id, "is revoked"}
		case err != nil:
			s.opts.Logger.Error("could not open a Git credential", "git_credential_id", id, "error", err)
			return nil, &CredentialsError{id, "cannot be decrypted (check the secret-protection key)"}
		}
		c.SecretVersion = version
		out = append(out, protocol.GitCredential{CredentialID: c.ID, Host: c.Host, Username: c.Username, Secret: token, PlainHTTP: c.PlainHTTP})
		s.recordUse(ctx, c, j, "")
	}
	return out, nil
}
