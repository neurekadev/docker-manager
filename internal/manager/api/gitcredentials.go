package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/gitcreds"
)

// Git credentials (#33): owner-administered HTTPS Git credentials for
// private build contexts, handled like registry connections (#19).

const tagGitCredentials = "Git credentials"

// CapGitCredentialRead shows Git credential metadata (never secrets).
const CapGitCredentialRead Capability = "git_credential.read" //nolint:gosec // G101: a capability key, not a credential

const capGitCredentialManage = "git_credential.manage" //nolint:gosec // G101: a capability key, not a credential

// GitCredentialService is the Git credential service as seen by the API
// (implemented by *gitcreds.Service).
type GitCredentialService interface {
	List(ctx context.Context, afterID string, limit int) ([]domain.GitCredential, error)
	Get(ctx context.Context, id string) (domain.GitCredential, error)
	Create(ctx context.Context, in domain.GitCredentialInput) (domain.GitCredential, error)
	Update(ctx context.Context, id string, revision int64, p domain.GitCredentialPatch) (domain.GitCredential, error)
	Delete(ctx context.Context, id string, revision int64) error
	ConnectionTest(ctx context.Context, id, repository, ref string) (gitcreds.Test, error)
}

// GitCredential is a stored Git credential; the token is write-only.
// Shaping (#17): git_credential.read shows it in full, other capabilities
// on it only id, name, host and status.
type GitCredential struct {
	ID         string          `json:"id"`
	Name       string          `json:"name" example:"GitHub (acme builds)"`
	Host       string          `json:"host" example:"github.com" doc:"host[:port] the credential is sent to."`
	Status     string          `json:"status" enum:"active,revoked"`
	View       string          `json:"view" enum:"minimal,full"`
	Actions    []string        `json:"actions"`
	PathPrefix string          `json:"pathPrefix,omitempty" example:"acme" doc:"Only repositories below this path (full view); empty: every repository on the host."`
	Username   string          `json:"username,omitempty" doc:"Full view."`
	PlainHTTP  bool            `json:"plainHttp,omitempty" doc:"May be sent to http:// repositories (full view)."`
	Secret     *RegistrySecret `json:"secret,omitempty" doc:"Token metadata (full view); the token itself is never returned."`
	LastUsedAt *time.Time      `json:"lastUsedAt,omitempty"`
	LastCheck  *RegistryCheck  `json:"lastCheck,omitempty"`
	RevokedAt  *time.Time      `json:"revokedAt,omitempty"`
	Revision   int64           `json:"revision,omitempty"`
	CreatedAt  time.Time       `json:"createdAt,omitzero"`
	UpdatedAt  time.Time       `json:"updatedAt,omitzero"`
}

func gitCredentialResource(id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeGitCredential, ID: id, Parents: []authz.ResourceRef{}}
}

func newGitCredential(c domain.GitCredential, v authz.View) GitCredential {
	out := GitCredential{ID: c.ID, Name: c.Name, Host: c.Host, Status: string(c.Status), View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	out.PathPrefix, out.Username, out.PlainHTTP = c.PathPrefix, c.Username, c.PlainHTTP
	out.Secret = &RegistrySecret{Set: c.Active(), Fingerprint: c.SecretFingerprint, Version: c.SecretVersion, UpdatedAt: c.SecretUpdatedAt}
	out.LastUsedAt, out.RevokedAt = c.LastUsedAt, c.RevokedAt
	if c.LastCheckAt != nil {
		out.LastCheck = &RegistryCheck{At: *c.LastCheckAt, Result: c.LastCheckResult}
	}
	out.Revision, out.CreatedAt, out.UpdatedAt = c.Revision, c.CreatedAt, c.UpdatedAt
	return out
}

func gitCredentialError(err error) error {
	var amb *domain.AmbiguousGitCredentialError
	switch {
	case errors.As(err, &amb):
		return Conflict(CodeAmbiguousGitCredential, err.Error())
	case errors.Is(err, domain.ErrGitCredentialNotFound):
		return NotFound("Git credential not found")
	case errors.Is(err, domain.ErrGitCredentialNameTaken):
		return Conflict(CodeGitCredentialNameTaken, "another Git credential already uses this name")
	case errors.Is(err, domain.ErrGitCredentialRevoked):
		return Conflict(CodeGitCredentialRevoked, "the Git credential is revoked; set a new token or select another credential")
	case errors.Is(err, domain.ErrGitCredentialMismatch):
		return Invalid("the selected Git credential does not apply to this repository",
			Field("body.gitCredentialId", "not for this repository's host or path"))
	}
	return registryError(err)
}

type gitCredentialsAPI struct {
	svc   GitCredentialService
	authz authz.Authorizer
}

func (h *gitCredentialsAPI) service() (GitCredentialService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "Git credentials are not available")
	}
	return h.svc, nil
}

func (h *gitCredentialsAPI) admin(ctx context.Context) (GitCredentialService, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(capGitCredentialManage, authz.Instance()).Allowed {
		return nil, Forbidden("only the instance owner may administer Git credentials")
	}
	return svc, nil
}

type gitCredentialIDInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Git credential ID."`
}

type gitCredentialOutput struct {
	ETagHeader
	Body GitCredential
}

type gitCredentialListOutput struct{ Body Page[GitCredential] }

type createGitCredentialInput struct {
	Body struct {
		Name       string `json:"name" minLength:"1" maxLength:"100"`
		Host       string `json:"host" minLength:"1" maxLength:"255" example:"github.com" doc:"host or host:port."`
		PathPrefix string `json:"pathPrefix,omitempty" maxLength:"255" example:"acme"`
		Username   string `json:"username" minLength:"1" maxLength:"255" doc:"For GitHub/GitLab tokens any non-empty name works (e.g. x-access-token, oauth2)."`
		Secret     string `json:"secret" minLength:"1" maxLength:"8192" writeOnly:"true" doc:"Access token (read-only repository scope recommended). Write-only."`
		PlainHTTP  bool   `json:"plainHttp,omitempty"`
	}
}

type updateGitCredentialInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Git credential ID."`
	IfMatchParam
	Body struct {
		Name       *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		PathPrefix *string `json:"pathPrefix,omitempty" maxLength:"255"`
		Username   *string `json:"username,omitempty" minLength:"1" maxLength:"255"`
		Secret     *string `json:"secret,omitempty" minLength:"1" maxLength:"8192" writeOnly:"true" doc:"A new token (rotation; re-activates a revoked credential). Write-only."`
		PlainHTTP  *bool   `json:"plainHttp,omitempty"`
		Status     *string `json:"status,omitempty" enum:"revoked" doc:"revoked erases the stored token."`
	}
}

type deleteGitCredentialInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Git credential ID."`
	IfMatchParam
}

type gitCredentialTestInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Git credential ID."`
	Body         struct {
		RepositoryURL string `json:"repositoryUrl" minLength:"1" maxLength:"2048" example:"https://github.com/acme/app.git"`
		Ref           string `json:"ref,omitempty" maxLength:"255" doc:"Also resolve this branch, tag or ref (default HEAD)."`
	}
}

// GitCredentialTest is the outcome of a connection test (an ls-remote).
type GitCredentialTest struct {
	Repository string    `json:"repository"`
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"errorClass,omitempty" enum:"unauthorized,forbidden,not_found,rate_limited,git_unavailable,invalid_response,ref_not_found,invalid_git_url"`
	Message    string    `json:"message,omitempty" doc:"Explanation of the failure (never contains credentials)."`
	Head       string    `json:"head,omitempty" example:"refs/heads/main"`
	Ref        string    `json:"ref,omitempty" doc:"The full ref resolved."`
	Commit     string    `json:"commit,omitempty" doc:"The commit the ref (or HEAD) points to."`
	RefCount   int       `json:"refCount"`
	CheckedAt  time.Time `json:"checkedAt"`
}

type gitCredentialTestOutput struct{ Body GitCredentialTest }

func gitETag(c GitCredential) ETagHeader {
	if c.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(c.Revision)}
}

func ownerGitView() authz.View {
	return authz.View{Level: authz.Full, Actions: []string{string(CapGitCredentialRead)}}
}

func (h *gitCredentialsAPI) list(ctx context.Context, in *struct{ PageParams }) (*gitCredentialListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("git-credentials")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.GitCredential]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.GitCredential, error) {
			return svc.List(ctx, afterID, n)
		},
		Position: func(r domain.GitCredential) string { return r.ID },
		Visible:  func(r domain.GitCredential) bool { return authz.ViewOf(c, gitCredentialResource(r.ID)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]GitCredential, 0, len(items))
	for _, r := range items {
		out = append(out, newGitCredential(r, authz.ViewOf(c, gitCredentialResource(r.ID))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &gitCredentialListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *gitCredentialsAPI) get(ctx context.Context, in *gitCredentialIDInput) (*gitCredentialOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	r, err := svc.Get(ctx, in.CredentialID)
	if err != nil {
		return nil, gitCredentialError(err)
	}
	v := authz.ViewOf(c, gitCredentialResource(r.ID))
	if !v.Visible() {
		return nil, NotFound("Git credential not found")
	}
	body := newGitCredential(r, v)
	return &gitCredentialOutput{ETagHeader: gitETag(body), Body: body}, nil
}

func (h *gitCredentialsAPI) create(ctx context.Context, in *createGitCredentialInput) (*gitCredentialOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	r, err := svc.Create(ctx, domain.GitCredentialInput{Name: b.Name, Host: b.Host, PathPrefix: b.PathPrefix, Username: b.Username,
		Secret: b.Secret, PlainHTTP: b.PlainHTTP})
	if err != nil {
		return nil, gitCredentialError(err)
	}
	body := newGitCredential(r, ownerGitView())
	return &gitCredentialOutput{ETagHeader: gitETag(body), Body: body}, nil
}

func (h *gitCredentialsAPI) current(ctx context.Context, svc GitCredentialService, id string, im IfMatchParam) (domain.GitCredential, error) {
	r, err := svc.Get(ctx, id)
	if err != nil {
		return r, gitCredentialError(err)
	}
	return r, im.CheckIfMatch(RevisionETag(r.Revision))
}

func (h *gitCredentialsAPI) staleOr(ctx context.Context, svc GitCredentialService, id string, err error) error {
	if !errors.Is(err, domain.ErrRevisionMismatch) {
		return gitCredentialError(err)
	}
	cur, gerr := svc.Get(ctx, id)
	if gerr != nil {
		return gitCredentialError(gerr)
	}
	return stale(cur.Revision)
}

func (h *gitCredentialsAPI) update(ctx context.Context, in *updateGitCredentialInput) (*gitCredentialOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.CredentialID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	b := in.Body
	p := domain.GitCredentialPatch{Name: b.Name, PathPrefix: b.PathPrefix, Username: b.Username, Secret: b.Secret, PlainHTTP: b.PlainHTTP}
	if b.Status != nil {
		st := domain.RegistryConnectionStatus(*b.Status)
		p.Status = &st
	}
	r, err := svc.Update(ctx, cur.ID, cur.Revision, p)
	if err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	body := newGitCredential(r, ownerGitView())
	return &gitCredentialOutput{ETagHeader: gitETag(body), Body: body}, nil
}

func (h *gitCredentialsAPI) remove(ctx context.Context, in *deleteGitCredentialInput) (*struct{}, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.CredentialID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, cur.ID, cur.Revision); err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	return nil, nil
}

func (h *gitCredentialsAPI) test(ctx context.Context, in *gitCredentialTestInput) (*gitCredentialTestOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	t, err := svc.ConnectionTest(ctx, in.CredentialID, in.Body.RepositoryURL, in.Body.Ref)
	if err != nil {
		return nil, gitCredentialError(err)
	}
	return &gitCredentialTestOutput{Body: GitCredentialTest{Repository: t.Repository, OK: t.OK, ErrorClass: t.ErrorClass, Message: t.Message,
		Head: t.Head, Ref: t.Ref, Commit: t.Commit, RefCount: t.RefCount, CheckedAt: t.CheckedAt}}, nil
}

func registerGitCredentials(a huma.API, deps Deps) {
	h := &gitCredentialsAPI{svc: deps.GitCredentials, authz: authz.OrDenyAll(deps.Authorizer)}
	stepUp := " Requires a recent step-up (403 step_up_required)."
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-git-credentials", Method: http.MethodGet, Path: BasePath + "/git-credentials",
			Summary: "List Git credentials",
			Description: "Git credentials in creation order, filtered per item (#17): git_credential.read shows one in full, other " +
				"capabilities on it only id, name, host and status. Tokens are write-only and never returned.",
			Tags: []string{tagGitCredentials}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapGitCredentialRead, Scope: ScopeResource,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-git-credential", Method: http.MethodPost, Path: BasePath + "/git-credentials",
			Summary: "Add a Git credential", DefaultStatus: http.StatusCreated,
			Description: "Stores an HTTPS Git credential (username + access token) for private build contexts, sealed with the manager's " +
				"secret-protection key; the token is write-only (fingerprint only). Builds send it to the agent for that job only, as a " +
				"BuildKit session secret. 409 git_credential_name_taken." + stepUp + " " + ownerOnly,
			Tags: []string{tagGitCredentials}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.create)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-git-credential", Method: http.MethodGet, Path: BasePath + "/git-credentials/{credentialId}",
			Summary: "Get a Git credential", Description: "Never contains the token.",
			Tags: []string{tagGitCredentials}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapGitCredentialRead, Scope: ScopeResource,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-git-credential", Method: http.MethodPatch, Path: BasePath + "/git-credentials/{credentialId}",
			Summary: "Update a Git credential",
			Description: "Edits the name, path prefix, username or plain-HTTP flag, rotates the token (secret) or revokes it " +
				"(status revoked). Jobs dispatched afterwards use the new token. Requires If-Match." + stepUp + " " + ownerOnly,
			Tags: []string{tagGitCredentials}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.update)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-git-credential", Method: http.MethodDelete, Path: BasePath + "/git-credentials/{credentialId}",
			Summary: "Delete a Git credential", DefaultStatus: http.StatusNoContent,
			Description: "Queued builds that name it fail at dispatch with credential_unavailable. Requires If-Match." + stepUp + " " + ownerOnly,
			Tags:        []string{tagGitCredentials}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.remove)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-git-credential-connection-test", Method: http.MethodPost,
			Path:    BasePath + "/git-credentials/{credentialId}/connection-tests",
			Summary: "Test a Git credential", DefaultStatus: http.StatusOK,
			Description: "Lists the refs of a repository with the credential from the manager (an in-process git ls-remote over " +
				"HTTP(S), no git CLI) and resolves a ref. A failure is reported in the body (ok false, errorClass). The repository " +
				"must be on the credential's host and path prefix (422). 409 git_credential_revoked. " + ownerOnly,
			Tags: []string{tagGitCredentials}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.test)
}
