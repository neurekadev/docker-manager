package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// API token routes (#31). The flows live in internal/manager/auth
// (apitokens.go); this file is the transport contract. Every route here is
// session-only: tokens never create, list or revoke tokens.

const tagAPITokens = "API tokens"

// CapAPITokensCreate is the capability to create API tokens (#17 catalog;
// the owner always has it).
const CapAPITokensCreate = "api_tokens.create" //nolint:gosec // G101: a capability key, not a credential

// APITokenService is the token part of the identity layer as seen by the
// API (implemented by *auth.Service). Methods read the caller's session
// from ctx and enforce ownership of the token, owner-only access and the
// step-up of creation.
type APITokenService interface {
	CreateAPIToken(ctx context.Context, in domain.NewAPIToken) (domain.APIToken, string, error)
	ListMyAPITokens(ctx context.Context, beforeID string, limit int) ([]domain.APIToken, error)
	GetMyAPIToken(ctx context.Context, id string) (domain.APIToken, error)
	RenameMyAPIToken(ctx context.Context, id, name string) (domain.APIToken, error)
	RevokeMyAPIToken(ctx context.Context, id string) error
	ListAPITokens(ctx context.Context, beforeID string, limit int) ([]domain.APIToken, error)
	RevokeAPIToken(ctx context.Context, id string) error
	Now() time.Time
}

// TokenGrant is one allow grant of an API token's scope: a catalog
// capability at instance, environment or resource scope.
type TokenGrant struct {
	Capability string          `json:"capability" maxLength:"128" example:"container.restart"`
	Scope      PermissionScope `json:"scope"`
}

func (g TokenGrant) domain() domain.PermissionRule {
	return PermissionRule{Capability: g.Capability, Effect: string(domain.PermissionAllow), Scope: g.Scope}.domain()
}

// APIToken is an API token (never its secret).
type APIToken struct {
	ID       string       `json:"id" doc:"Token ID; also embedded in the token value (dy_<id>_...), so a leaked value can be matched to its record."`
	Name     string       `json:"name"`
	UserID   string       `json:"userId" doc:"The token's user: it acts with at most this user's current permissions."`
	Username string       `json:"username,omitempty" doc:"Set in the owner's list of all tokens."`
	Status   string       `json:"status" enum:"active,expired,revoked"`
	Scopes   []TokenGrant `json:"scopes" doc:"The grants chosen at creation. Effective access is these grants intersected with the user's current permissions, evaluated on every request."`
	// ExpiresAt is absent for tokens that never expire.
	ExpiresAt     *time.Time `json:"expiresAt,omitempty" doc:"Absent: the token never expires (only when the owner allows it)."`
	CreatedAt     time.Time  `json:"createdAt"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty" doc:"Recorded at most once a minute."`
	LastUsedIP    string     `json:"lastUsedIp,omitempty" doc:"Client IP of the last recorded use (behind trusted proxies: the forwarded client)."`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	RevokedReason string     `json:"revokedReason,omitempty" enum:"user,owner,user_disabled,credential_reset,restore"`
}

func newAPIToken(t domain.APIToken, now time.Time) APIToken {
	out := APIToken{ID: t.ID, Name: t.Name, UserID: t.UserID, Username: t.Username, Status: string(t.Status(now)),
		Scopes: []TokenGrant{}, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, LastUsedIP: t.LastUsedIP,
		RevokedAt: t.RevokedAt, RevokedReason: string(t.RevokedReason)}
	for _, r := range t.Scopes {
		out.Scopes = append(out.Scopes, TokenGrant{Capability: r.Capability, Scope: newRule(r).Scope})
	}
	return out
}

type apiTokenIDInput struct {
	TokenID string `path:"tokenId" maxLength:"64" doc:"API token ID."`
}

type apiTokenOutput struct{ Body APIToken }

type apiTokenListOutput struct{ Body Page[APIToken] }

type createAPITokenInput struct {
	IdempotencyKeyParam
	Body struct {
		Name         string       `json:"name" minLength:"1" maxLength:"64" example:"backup script"`
		ExpiresAt    *time.Time   `json:"expiresAt,omitempty" doc:"When the token stops working; required unless neverExpires. At most the instance maximum ahead (security settings, default 90 days)."`
		NeverExpires bool         `json:"neverExpires,omitempty" doc:"A token without expiry; only when the owner allows them (security settings)."`
		Scopes       []TokenGrant `json:"scopes" minItems:"1" maxItems:"200" doc:"The capabilities the token may use and where. Each must be held by you now; the token never exceeds your current permissions."`
	}
}

type createAPITokenOutput struct {
	Body struct {
		APIToken APIToken `json:"apiToken"`
		Token    string   `json:"token" doc:"The token value (dy_...). Shown only in this response; DockYard stores a verifier. Send it as Authorization: Bearer <token>."`
	}
}

type renameAPITokenInput struct {
	TokenID string `path:"tokenId" maxLength:"64" doc:"API token ID."`
	Body    struct {
		Name string `json:"name" example:"Backup script" minLength:"1" maxLength:"64"`
	}
}

type tokensAPI struct {
	svc  APITokenService
	deps Deps
}

func (h *tokensAPI) service() (APITokenService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "API tokens are not available on this manager")
	}
	return h.svc, nil
}

// tokenError maps API token errors.
func tokenError(err error) error {
	var re *domain.RuleError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re):
		details := make([]ErrorDetail, 0, len(re.Problems))
		for _, p := range re.Problems {
			details = append(details, Field(fmt.Sprintf("body.%s[%d]", re.Field, p.Index), p.Message))
		}
		return Invalid("invalid token scope", details...)
	case errors.Is(err, domain.ErrAPITokenNotFound):
		return NotFound("API token not found")
	case errors.Is(err, domain.ErrAPITokensDisabled):
		return NewError(http.StatusForbidden, CodeAPITokensDisabled, "the instance owner disabled API tokens")
	}
	return identityError(err)
}

func (h *tokensAPI) list(ctx context.Context, fpName string, in PageParams,
	fetch func(ctx context.Context, before string, limit int) ([]domain.APIToken, error)) (*apiTokenListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint(fpName)
	var pos idCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &pos); err != nil {
			return nil, err
		}
	}
	limit := in.PageLimit()
	toks, err := fetch(ctx, pos.After, limit+1)
	if err != nil {
		return nil, tokenError(err)
	}
	next := ""
	if len(toks) > limit {
		toks = toks[:limit]
		if next, err = CursorFor(fp, idCursor{After: toks[limit-1].ID}); err != nil {
			return nil, Internal(err)
		}
	}
	now := svc.Now()
	items := make([]APIToken, 0, len(toks))
	for _, t := range toks {
		items = append(items, newAPIToken(t, now))
	}
	return &apiTokenListOutput{Body: NewPage(items, next, nil)}, nil
}

const sessionOnlyNote = " Browser session only: API tokens cannot manage tokens (403 api_token_not_allowed)."

func registerAPITokens(a huma.API, deps Deps) {
	h := &tokensAPI{svc: deps.APITokens, deps: deps}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-my-api-tokens", Method: http.MethodGet, Path: BasePath + "/me/api-tokens",
			Summary: "List my API tokens", Description: "The caller's tokens (active, expired and revoked), newest first. Values are never listed." + sessionOnlyNote,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone, SessionOnly: true,
	}, func(ctx context.Context, in *struct{ PageParams }) (*apiTokenListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		return h.list(ctx, "my-api-tokens", in.PageParams, svc.ListMyAPITokens)
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-my-api-token", Method: http.MethodPost, Path: BasePath + "/me/api-tokens",
			Summary: "Create an API token", DefaultStatus: http.StatusCreated,
			Description: "Creates a token for scripts and integrations and returns its value once (DockYard keeps a verifier). " +
				"The token carries exactly the listed grants, each of which you must hold now; every request with it is evaluated " +
				"as these grants intersected with your current permissions, so later permission changes narrow it at once. " +
				"Owner administration, sign-in and factor flows and token management are never reachable with a token, and a " +
				"terminal (exec) needs container.exec in the token's own grants. Requires api_tokens.create (the owner always " +
				"has it), a recent step-up (403 step_up_required) and tokens being enabled (403 api_tokens_disabled). " +
				"The expiry is required unless the owner allows non-expiring tokens." + sessionOnlyNote,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapAPITokensCreate, Scope: ScopeInstance, Idempotency: IdempotencyStored, SessionOnly: true,
	}, func(ctx context.Context, in *createAPITokenInput) (*createAPITokenOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		c, _, err := CheckerFor(ctx, h.deps.Authorizer)
		if err != nil {
			return nil, err
		}
		if !c.Can(CapAPITokensCreate, authz.Instance()).Allowed {
			return nil, Forbidden("creating API tokens needs the api_tokens.create capability")
		}
		req := domain.NewAPIToken{Name: in.Body.Name, ExpiresAt: in.Body.ExpiresAt, NeverExpires: in.Body.NeverExpires}
		for _, g := range in.Body.Scopes {
			req.Scopes = append(req.Scopes, g.domain())
		}
		t, secret, err := svc.CreateAPIToken(ctx, req)
		if err != nil {
			return nil, tokenError(err)
		}
		out := &createAPITokenOutput{}
		out.Body.APIToken, out.Body.Token = newAPIToken(t, svc.Now()), secret
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-my-api-token", Method: http.MethodGet, Path: BasePath + "/me/api-tokens/{tokenId}",
			Summary: "Get one of my API tokens", Description: "404 for other users' tokens." + sessionOnlyNote,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone, SessionOnly: true,
	}, func(ctx context.Context, in *apiTokenIDInput) (*apiTokenOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		t, err := svc.GetMyAPIToken(ctx, in.TokenID)
		if err != nil {
			return nil, tokenError(err)
		}
		return &apiTokenOutput{Body: newAPIToken(t, svc.Now())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-my-api-token", Method: http.MethodPatch, Path: BasePath + "/me/api-tokens/{tokenId}",
			Summary: "Rename one of my API tokens",
			Description: "Only the name can change; grants and expiry are fixed at creation (create a new token instead)." +
				sessionOnlyNote,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone, SessionOnly: true, AuditAction: "api_token.rename",
	}, func(ctx context.Context, in *renameAPITokenInput) (*apiTokenOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		t, err := svc.RenameMyAPIToken(ctx, in.TokenID, in.Body.Name)
		if err != nil {
			return nil, tokenError(err)
		}
		return &apiTokenOutput{Body: newAPIToken(t, svc.Now())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-my-api-token", Method: http.MethodDelete, Path: BasePath + "/me/api-tokens/{tokenId}",
			Summary: "Revoke one of my API tokens", DefaultStatus: http.StatusNoContent,
			Description: "The token stops working at once and its open streams close. Revocation is final; revoking twice is harmless." +
				sessionOnlyNote,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone, SessionOnly: true, AuditAction: "api_token.revoke",
	}, func(ctx context.Context, in *apiTokenIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeMyAPIToken(ctx, in.TokenID); err != nil {
			return nil, tokenError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-api-tokens", Method: http.MethodGet, Path: BasePath + "/api-tokens",
			Summary: "List every user's API tokens", Description: "All tokens of all users, newest first, with their user. Values are never listed. " + ownerOnly,
			Tags: []string{tagAPITokens}, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *struct{ PageParams }) (*apiTokenListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		return h.list(ctx, "api-tokens", in.PageParams, svc.ListAPITokens)
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-api-token", Method: http.MethodDelete, Path: BasePath + "/api-tokens/{tokenId}",
			Summary: "Revoke any user's API token", DefaultStatus: http.StatusNoContent,
			Description: "The token stops working at once and its open streams close. Revocation is final; revoking twice is harmless. " + ownerOnly,
			Tags:        []string{tagAPITokens}, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "api_token.revoke",
	}, func(ctx context.Context, in *apiTokenIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeAPIToken(ctx, in.TokenID); err != nil {
			return nil, tokenError(err)
		}
		return &emptyOutput{}, nil
	})
}
