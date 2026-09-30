package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/registries"
)

// Registry connections (#19): owner-administered, write-only registry
// credentials shared by the instance. The flows live in
// internal/manager/registries; this file is the transport contract.

const tagRegistries = "Registries"

// CapRegistryRead shows registry connection metadata (never credentials).
const CapRegistryRead Capability = "registry.read"

// RegistryService is the registry connection service as seen by the API
// (implemented by *registries.Service). Management methods enforce
// owner-only access and step-up from the caller's session.
type RegistryService interface {
	List(ctx context.Context, afterID string, limit int) ([]domain.RegistryConnection, error)
	Get(ctx context.Context, id string) (domain.RegistryConnection, error)
	Create(ctx context.Context, in domain.RegistryConnectionInput) (domain.RegistryConnection, error)
	Update(ctx context.Context, id string, revision int64, p domain.RegistryConnectionPatch) (domain.RegistryConnection, error)
	Rotate(ctx context.Context, id string, revision int64, r domain.RegistryCredentialRotation) (domain.RegistryConnection, error)
	Delete(ctx context.Context, id string, revision int64) error
	ConnectionTest(ctx context.Context, id, reference, platform string) (registries.Test, error)
	Preview(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, []string, error)
}

// RegistrySecret describes the stored credential without revealing it.
type RegistrySecret struct {
	Set         bool      `json:"set" doc:"A credential is stored (false once revoked)."`
	Fingerprint string    `json:"fingerprint,omitempty" example:"fp_3f2a9c0d1e4b5a67" doc:"Keyed fingerprint of the secret: changes when the secret changes, cannot be reversed or brute-forced without the manager's key."`
	Version     int       `json:"version" doc:"Increases with every rotation."`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// RegistryCheck is the outcome of the last connection test or use.
type RegistryCheck struct {
	At     time.Time `json:"at"`
	Result string    `json:"result" example:"ok" doc:"ok, or an error class: unauthorized, forbidden, not_found, rate_limited, registry_unavailable, platform_not_found, invalid_response."`
}

// RegistryConnection is a stored registry connection. The secret is
// write-only: responses carry only its fingerprint and version.
//
// Shaping (#17): registry.read shows the connection in full; any other
// capability on it shows id, name, host and status (view "minimal").
type RegistryConnection struct {
	ID                string          `json:"id"`
	Name              string          `json:"name" example:"GHCR (acme pull token)"`
	Host              string          `json:"host" example:"ghcr.io" doc:"Normalized registry host: docker.io for every Docker Hub alias, host:port for self-hosted registries."`
	Status            string          `json:"status" enum:"active,revoked" doc:"A revoked connection has no credential; it still matches its images so jobs fail visibly instead of pulling anonymously."`
	View              string          `json:"view" enum:"minimal,full"`
	Actions           []string        `json:"actions"`
	CredentialType    string          `json:"credentialType,omitempty" enum:"password,token" doc:"Full view. Least-privilege pull tokens are recommended."`
	Username          string          `json:"username,omitempty" doc:"Full view."`
	RepositoryPattern string          `json:"repositoryPattern,omitempty" example:"acme/*" doc:"Exact repository or namespace/* (full view); empty matches every repository on the host."`
	EnvironmentID     string          `json:"environmentId,omitempty" doc:"Bound to this environment (full view)."`
	StackID           string          `json:"stackId,omitempty" doc:"Bound to this stack (full view)."`
	Priority          int             `json:"priority,omitempty" doc:"Tie-breaker between equally specific connections, higher wins (full view)."`
	PlainHTTP         bool            `json:"plainHttp,omitempty" doc:"Manager-side checks use plain HTTP (self-hosted registries only; full view)."`
	Secret            *RegistrySecret `json:"secret,omitempty" doc:"Full view."`
	LastUsedAt        *time.Time      `json:"lastUsedAt,omitempty" doc:"Last successful use (full view)."`
	LastCheck         *RegistryCheck  `json:"lastCheck,omitempty" doc:"Last test or use (full view)."`
	RevokedAt         *time.Time      `json:"revokedAt,omitempty"`
	Revision          int64           `json:"revision,omitempty" doc:"Edit revision (the ETag; full view)."`
	CreatedAt         time.Time       `json:"createdAt,omitzero"`
	UpdatedAt         time.Time       `json:"updatedAt,omitzero"`
}

func registryResource(id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeRegistry, ID: id, Parents: []authz.ResourceRef{}}
}

func newRegistryConnection(c domain.RegistryConnection, v authz.View) RegistryConnection {
	out := RegistryConnection{ID: c.ID, Name: c.Name, Host: c.Host, Status: string(c.Status), View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	out.CredentialType, out.Username, out.RepositoryPattern = string(c.CredentialType), c.Username, c.RepositoryPattern
	out.EnvironmentID, out.StackID, out.Priority, out.PlainHTTP = c.EnvironmentID, c.StackID, c.Priority, c.PlainHTTP
	out.Secret = &RegistrySecret{Set: c.Active(), Fingerprint: c.SecretFingerprint, Version: c.SecretVersion, UpdatedAt: c.SecretUpdatedAt}
	out.LastUsedAt, out.RevokedAt = c.LastUsedAt, c.RevokedAt
	if c.LastCheckAt != nil {
		out.LastCheck = &RegistryCheck{At: *c.LastCheckAt, Result: c.LastCheckResult}
	}
	out.Revision, out.CreatedAt, out.UpdatedAt = c.Revision, c.CreatedAt, c.UpdatedAt
	return out
}

type registriesAPI struct {
	svc   RegistryService
	authz authz.Authorizer
}

func (h *registriesAPI) service() (RegistryService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "registry connections are not available")
	}
	return h.svc, nil
}

// admin returns the service for an owner-only operation. The owner check
// runs before any lookup, so other callers cannot probe which connection
// IDs exist; the service enforces it again (with step-up).
func (h *registriesAPI) admin(ctx context.Context) (RegistryService, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(capRegistryManage, authz.Instance()).Allowed {
		return nil, Forbidden("only the instance owner may administer registry connections")
	}
	return svc, nil
}

// capRegistryManage is the owner-only catalog key of credential
// administration (only the owner holds it).
const capRegistryManage = "registry.manage"

func registryError(err error) error {
	var amb *domain.AmbiguousRegistryError
	switch {
	case errors.As(err, &amb):
		return Conflict(CodeAmbiguousRegistryConnection, err.Error())
	case errors.Is(err, domain.ErrRegistryConnectionNotFound):
		return NotFound("registry connection not found")
	case errors.Is(err, domain.ErrRegistryConnectionNameTaken):
		return Conflict(CodeRegistryNameTaken, "another registry connection already uses this name")
	case errors.Is(err, domain.ErrRegistryConnectionRevoked):
		return Conflict(CodeRegistryConnectionRevoked, "the registry connection is revoked; rotate a new credential into it or select another one")
	case errors.Is(err, domain.ErrRegistryConnectionMismatch):
		return Invalid("the selected registry connection does not apply to this image",
			Field("body.registryId", "not a candidate for this image (host, repository matcher or binding differ)"))
	}
	return identityError(err)
}

// --- inputs and outputs ---

type registryIDInput struct {
	RegistryID string `path:"registryId" maxLength:"64" doc:"Registry connection ID."`
}

type registryOutput struct {
	ETagHeader
	Body RegistryConnection
}

type registryListOutput struct{ Body Page[RegistryConnection] }

type createRegistryInput struct {
	Body struct {
		Name              string `json:"name" minLength:"1" maxLength:"100" example:"GHCR (acme pull token)"`
		Host              string `json:"host" minLength:"1" maxLength:"255" example:"ghcr.io" doc:"Registry host (host or host:port); Docker Hub aliases (docker.io, index.docker.io, registry-1.docker.io) are normalized to docker.io."`
		CredentialType    string `json:"credentialType,omitempty" enum:"password,token" default:"token"`
		Username          string `json:"username" minLength:"1" maxLength:"255"`
		Secret            string `json:"secret" minLength:"1" maxLength:"8192" writeOnly:"true" doc:"Password or access token. Write-only: never returned, logged or audited."`
		RepositoryPattern string `json:"repositoryPattern,omitempty" maxLength:"255" example:"acme/*"`
		EnvironmentID     string `json:"environmentId,omitempty" maxLength:"64"`
		StackID           string `json:"stackId,omitempty" maxLength:"64"`
		Priority          int    `json:"priority,omitempty" minimum:"-1000" maximum:"1000"`
		PlainHTTP         bool   `json:"plainHttp,omitempty"`
	}
}

type updateRegistryInput struct {
	RegistryID string `path:"registryId" maxLength:"64" doc:"Registry connection ID."`
	IfMatchParam
	Body struct {
		Name              *string `json:"name,omitempty" example:"GitHub Container Registry" minLength:"1" maxLength:"100"`
		RepositoryPattern *string `json:"repositoryPattern,omitempty" example:"example/*" maxLength:"255" doc:"Empty matches every repository on the host."`
		EnvironmentID     *string `json:"environmentId,omitempty" maxLength:"64" doc:"Empty removes the binding."`
		StackID           *string `json:"stackId,omitempty" maxLength:"64" doc:"Empty removes the binding."`
		Priority          *int    `json:"priority,omitempty" minimum:"-1000" maximum:"1000"`
		PlainHTTP         *bool   `json:"plainHttp,omitempty"`
		Status            *string `json:"status,omitempty" enum:"revoked" doc:"revoked erases the stored credential; rotate a new one to re-activate the connection."`
	}
}

type deleteRegistryInput struct {
	RegistryID string `path:"registryId" maxLength:"64" doc:"Registry connection ID."`
	IfMatchParam
}

type rotateRegistryInput struct {
	RegistryID string `path:"registryId" maxLength:"64" doc:"Registry connection ID."`
	IfMatchParam
	Body struct {
		Secret         string  `json:"secret" minLength:"1" maxLength:"8192" writeOnly:"true" doc:"The new password or access token (write-only)."`
		Username       *string `json:"username,omitempty" example:"ci-bot" minLength:"1" maxLength:"255"`
		CredentialType *string `json:"credentialType,omitempty" example:"token" enum:"password,token"`
	}
}

type registryTestInput struct {
	RegistryID string `path:"registryId" maxLength:"64" doc:"Registry connection ID."`
	Body       struct {
		ImageReference string `json:"imageReference" minLength:"1" maxLength:"1024" example:"ghcr.io/acme/app:1.4.2" doc:"An image on the connection's host matching its repository matcher."`
		Platform       string `json:"platform,omitempty" maxLength:"64" example:"linux/amd64" doc:"Also select this platform's manifest from a multi-platform index."`
	}
}

// RegistryConnectionTest is the outcome of a connection test. A failed
// registry check is a successful test run (ok false, errorClass set).
type RegistryConnectionTest struct {
	Reference         string    `json:"reference" example:"ghcr.io/acme/app:1.4.2"`
	OK                bool      `json:"ok"`
	ErrorClass        string    `json:"errorClass,omitempty" enum:"unauthorized,forbidden,not_found,rate_limited,registry_unavailable,platform_not_found,invalid_response"`
	Message           string    `json:"message,omitempty" doc:"Explanation of the failure (never contains credentials)."`
	RetryAfterSeconds int       `json:"retryAfterSeconds,omitempty" doc:"The registry's retry guidance for rate limits."`
	Digest            string    `json:"digest,omitempty" doc:"Digest of the manifest (or index) the reference names."`
	PlatformDigest    string    `json:"platformDigest,omitempty" doc:"Digest of the requested platform's manifest."`
	MediaType         string    `json:"mediaType,omitempty"`
	CheckedAt         time.Time `json:"checkedAt"`
}

type registryTestOutput struct{ Body RegistryConnectionTest }

type registryMatchInput struct {
	Body struct {
		ImageReference string `json:"imageReference" minLength:"1" maxLength:"1024" example:"acme/app:1.4.2"`
		EnvironmentID  string `json:"environmentId,omitempty" maxLength:"64" doc:"Match in this environment (environment-bound connections)."`
		StackID        string `json:"stackId,omitempty" maxLength:"64" doc:"Match for this stack (stack-bound connections)."`
		RegistryID     string `json:"registryId,omitempty" maxLength:"64" doc:"Explicit selection; must be a candidate."`
	}
}

// RegistryCandidate is a connection matching the reference.
type RegistryCandidate struct {
	Connection RegistryConnection `json:"connection"`
	Binding    string             `json:"binding" enum:"stack,environment,none"`
	Specific   int                `json:"repositorySpecificity" doc:"0 for a connection without repository matcher; higher is more specific."`
}

// RegistryMatch previews which connection an image reference uses. It
// never contains a secret.
type RegistryMatch struct {
	Reference  string              `json:"reference" example:"docker.io/acme/app:1.4.2" doc:"The normalized reference."`
	Host       string              `json:"host" example:"docker.io"`
	Repository string              `json:"repository" example:"acme/app"`
	Selection  string              `json:"selection" enum:"connection,anonymous,ambiguous,revoked" doc:"connection: Selected is used; anonymous: no connection matches (public access); ambiguous: several candidates tie and a request must name one; revoked: the selected connection is revoked and jobs fail (no anonymous fallback)."`
	Explicit   bool                `json:"explicit"`
	Selected   *RegistryConnection `json:"selected,omitempty"`
	Tied       []string            `json:"tied,omitempty" doc:"IDs of the tied candidates (ambiguous)."`
	Candidates []RegistryCandidate `json:"candidates" doc:"Every matching connection, best first."`
}

type registryMatchOutput struct{ Body RegistryMatch }

func bindingName(b int) string {
	switch b {
	case 2:
		return "stack"
	case 1:
		return "environment"
	}
	return "none"
}

func registryETag(c RegistryConnection) ETagHeader {
	if c.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(c.Revision)}
}

// --- handlers ---

func (h *registriesAPI) list(ctx context.Context, in *struct{ PageParams }) (*registryListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("registries")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.RegistryConnection]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.RegistryConnection, error) {
			return svc.List(ctx, afterID, n)
		},
		Position: func(r domain.RegistryConnection) string { return r.ID },
		Visible:  func(r domain.RegistryConnection) bool { return authz.ViewOf(c, registryResource(r.ID)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]RegistryConnection, 0, len(items))
	for _, r := range items {
		out = append(out, newRegistryConnection(r, authz.ViewOf(c, registryResource(r.ID))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &registryListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *registriesAPI) get(ctx context.Context, in *registryIDInput) (*registryOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	r, err := svc.Get(ctx, in.RegistryID)
	if err != nil {
		return nil, registryError(err)
	}
	v := authz.ViewOf(c, registryResource(r.ID))
	if !v.Visible() {
		return nil, NotFound("registry connection not found")
	}
	body := newRegistryConnection(r, v)
	return &registryOutput{ETagHeader: registryETag(body), Body: body}, nil
}

// ownerView is the full view the owner gets back from management calls.
func ownerView() authz.View {
	return authz.View{Level: authz.Full, Actions: []string{string(CapRegistryRead)}}
}

func (h *registriesAPI) create(ctx context.Context, in *createRegistryInput) (*registryOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	r, err := svc.Create(ctx, domain.RegistryConnectionInput{Name: b.Name, Host: b.Host, CredentialType: domain.RegistryCredentialType(b.CredentialType),
		Username: b.Username, Secret: b.Secret, RepositoryPattern: b.RepositoryPattern, EnvironmentID: b.EnvironmentID, StackID: b.StackID,
		Priority: b.Priority, PlainHTTP: b.PlainHTTP})
	if err != nil {
		return nil, registryError(err)
	}
	body := newRegistryConnection(r, ownerView())
	return &registryOutput{ETagHeader: registryETag(body), Body: body}, nil
}

func (h *registriesAPI) staleOr(ctx context.Context, svc RegistryService, id string, err error) error {
	if !errors.Is(err, domain.ErrRevisionMismatch) {
		return registryError(err)
	}
	cur, gerr := svc.Get(ctx, id)
	if gerr != nil {
		return registryError(gerr)
	}
	return stale(cur.Revision)
}

// current loads a connection for an If-Match edit.
func (h *registriesAPI) current(ctx context.Context, svc RegistryService, id string, im IfMatchParam) (domain.RegistryConnection, error) {
	r, err := svc.Get(ctx, id)
	if err != nil {
		return r, registryError(err)
	}
	if err := im.CheckIfMatch(RevisionETag(r.Revision)); err != nil {
		return r, err
	}
	return r, nil
}

func (h *registriesAPI) update(ctx context.Context, in *updateRegistryInput) (*registryOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.RegistryID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	b := in.Body
	p := domain.RegistryConnectionPatch{Name: b.Name, RepositoryPattern: b.RepositoryPattern, EnvironmentID: b.EnvironmentID,
		StackID: b.StackID, Priority: b.Priority, PlainHTTP: b.PlainHTTP}
	if b.Status != nil {
		st := domain.RegistryConnectionStatus(*b.Status)
		p.Status = &st
	}
	r, err := svc.Update(ctx, cur.ID, cur.Revision, p)
	if err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	body := newRegistryConnection(r, ownerView())
	return &registryOutput{ETagHeader: registryETag(body), Body: body}, nil
}

func (h *registriesAPI) rotate(ctx context.Context, in *rotateRegistryInput) (*registryOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.RegistryID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	rot := domain.RegistryCredentialRotation{Secret: in.Body.Secret, Username: in.Body.Username}
	if in.Body.CredentialType != nil {
		t := domain.RegistryCredentialType(*in.Body.CredentialType)
		rot.CredentialType = &t
	}
	r, err := svc.Rotate(ctx, cur.ID, cur.Revision, rot)
	if err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	body := newRegistryConnection(r, ownerView())
	return &registryOutput{ETagHeader: registryETag(body), Body: body}, nil
}

func (h *registriesAPI) remove(ctx context.Context, in *deleteRegistryInput) (*struct{}, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.RegistryID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, cur.ID, cur.Revision); err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	return nil, nil
}

func (h *registriesAPI) test(ctx context.Context, in *registryTestInput) (*registryTestOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	t, err := svc.ConnectionTest(ctx, in.RegistryID, in.Body.ImageReference, in.Body.Platform)
	if err != nil {
		return nil, registryError(err)
	}
	return &registryTestOutput{Body: RegistryConnectionTest{Reference: t.Reference, OK: t.OK, ErrorClass: t.ErrorClass, Message: t.Message,
		RetryAfterSeconds: int(t.RetryAfter / time.Second), Digest: t.Digest, PlatformDigest: t.PlatformDigest, MediaType: t.MediaType,
		CheckedAt: t.CheckedAt}}, nil
}

func (h *registriesAPI) match(ctx context.Context, in *registryMatchInput) (*registryMatchOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapRegistryRead), authz.Instance()).Allowed {
		return nil, Forbidden("previewing registry matches needs registry.read on the instance")
	}
	b := in.Body
	sel, tied, err := svc.Preview(ctx, domain.RegistrySelectRequest{Reference: b.ImageReference, EnvironmentID: b.EnvironmentID,
		StackID: b.StackID, ConnectionID: b.RegistryID})
	if err != nil {
		return nil, registryError(err)
	}
	out := RegistryMatch{Reference: sel.Reference, Host: sel.Host, Repository: sel.Repository, Explicit: sel.Explicit, Tied: tied,
		Candidates: make([]RegistryCandidate, 0, len(sel.Candidates))}
	for _, cand := range sel.Candidates {
		out.Candidates = append(out.Candidates, RegistryCandidate{Binding: bindingName(cand.Binding), Specific: cand.Pattern,
			Connection: newRegistryConnection(cand.Connection, authz.ViewOf(c, registryResource(cand.Connection.ID)))})
	}
	switch {
	case len(tied) > 0:
		out.Selection = "ambiguous"
	case sel.Selected == nil:
		out.Selection = "anonymous"
	case !sel.Selected.Active():
		out.Selection = "revoked"
	default:
		out.Selection = "connection"
	}
	if sel.Selected != nil {
		s := newRegistryConnection(*sel.Selected, authz.ViewOf(c, registryResource(sel.Selected.ID)))
		out.Selected = &s
	}
	return &registryMatchOutput{Body: out}, nil
}

func registerRegistries(a huma.API, deps Deps) {
	h := &registriesAPI{svc: deps.Registries, authz: authz.OrDenyAll(deps.Authorizer)}
	stepUp := " Requires a recent step-up (403 step_up_required)."
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-registries", Method: http.MethodGet, Path: BasePath + "/registries",
			Summary: "List registry connections",
			Description: "Registry connections in creation order, filtered per item (#17): registry.read shows a connection in full, " +
				"any other capability on it only its id, name, host and status. Credentials are write-only and never returned.",
			Tags: []string{tagRegistries}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapRegistryRead, Scope: ScopeResource,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-registry", Method: http.MethodPost, Path: BasePath + "/registries",
			Summary: "Add a registry connection", DefaultStatus: http.StatusCreated,
			Description: "Stores a registry credential (username plus password or access token; least-privilege pull tokens recommended) " +
				"sealed with the manager's secret-protection key. The secret is write-only: the response carries only its fingerprint. " +
				"409 registry_connection_name_taken." + stepUp + " " + ownerOnly,
			Tags: []string{tagRegistries}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.create)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-registry", Method: http.MethodGet, Path: BasePath + "/registries/{registryId}",
			Summary: "Get a registry connection",
			Description: "Full with registry.read, minimal (id, name, host, status) with any other capability on it, 404 otherwise. " +
				"Never contains the credential.",
			Tags: []string{tagRegistries}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapRegistryRead, Scope: ScopeResource,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-registry", Method: http.MethodPatch, Path: BasePath + "/registries/{registryId}",
			Summary: "Update a registry connection",
			Description: "Edits the name, repository matcher, binding, priority or plain-HTTP flag, or revokes the credential " +
				"(status revoked erases it; the connection keeps matching so jobs fail instead of pulling anonymously). " +
				"Requires If-Match." + stepUp + " " + ownerOnly,
			Tags: []string{tagRegistries}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.update)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-registry", Method: http.MethodDelete, Path: BasePath + "/registries/{registryId}",
			Summary: "Delete a registry connection", DefaultStatus: http.StatusNoContent,
			Description: "Removes the connection and its credential. Queued jobs that name it fail at dispatch with credential_unavailable. " +
				"Requires If-Match." + stepUp + " " + ownerOnly,
			Tags: []string{tagRegistries}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.remove)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-registry-connection-test", Method: http.MethodPost, Path: BasePath + "/registries/{registryId}/connection-tests",
			Summary: "Test a registry connection", DefaultStatus: http.StatusOK,
			Description: "Resolves the digest of a supplied image reference with the connection's credential (a manifest HEAD request " +
				"from the manager, no pull, no cache) and records the result as the connection's last check. A registry failure is " +
				"reported in the body (ok false, errorClass unauthorized|forbidden|not_found|rate_limited|registry_unavailable|...). " +
				"The reference must be on the connection's host and match its repository matcher (422). 409 registry_connection_revoked. " +
				ownerOnly,
			Tags: []string{tagRegistries}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.test)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-registry-credential-rotation", Method: http.MethodPost,
			Path:    BasePath + "/registries/{registryId}/credential-rotations",
			Summary: "Rotate a registry credential", DefaultStatus: http.StatusOK,
			Description: "Replaces the credential (and optionally the username or credential type) and re-activates a revoked " +
				"connection. Jobs dispatched afterwards (including resumed attempts) use the new credential. Requires If-Match." +
				stepUp + " " + ownerOnly,
			Tags: []string{tagRegistries}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.rotate)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-registry-match", Method: http.MethodPost, Path: BasePath + "/registries/matches",
			Summary: "Preview the registry connection of an image", DefaultStatus: http.StatusOK,
			Description: "Shows which connection an image reference uses (deterministic: host incl. Docker Hub aliases, repository " +
				"matcher, environment/stack binding, then priority; a tie is ambiguous and needs explicit selection) and every " +
				"candidate. Metadata only, never a secret; candidates are shaped per registry.read. Needs registry.read on the instance.",
			Tags: []string{tagRegistries}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapRegistryRead, Scope: ScopeInstance, AuditAction: "registry.match",
	}, h.match)
}
