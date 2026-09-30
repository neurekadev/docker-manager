package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// API tokens (#31): a separately controlled credential type for scripts
// and integrations.
//
//   - A token is "dy_<id>_<256-bit secret>", shown once; only a SHA-256
//     verifier is stored and compared in constant time.
//   - It belongs to one user and carries an explicit list of allow grants
//     (catalog capabilities at instance, environment or resource scope)
//     chosen at creation, validated as a subset of the user's effective
//     permissions at that moment (ScopeValidator). Every request is then
//     evaluated as token scope ∩ the user's *current* permissions (the
//     permission service reads both per request), so a group change,
//     user deny, disable or deletion narrows or ends the token at once.
//   - Creating a token needs a full browser session with a recent step-up
//     (and api_tokens.create, checked by the route); tokens cannot create,
//     list or revoke tokens, and never reach owner-only administration or
//     interactive factor flows (api.Register refuses them centrally).
//   - A token authenticates only while: tokens are enabled instance-wide,
//     it is neither expired nor revoked, its account is active, and the
//     account satisfies the instance sign-in factor policy (a token never
//     stands in for a TOTP or passkey the policy requires). Every failure
//     is the same generic 401.
//   - Revocation (user, owner, account disable, credential resets that ask
//     for it, disaster restore) is final and closes the token's open
//     streams through the request hub.

// TokenUseGranularity bounds how often the last-used time and IP of a
// token are written.
const TokenUseGranularity = time.Minute

// ScopeValidator checks a requested token scope against the user's
// current effective permissions (permissions.Service.ValidateTokenScope).
// It returns a *domain.RuleError naming each grant the user does not hold.
type ScopeValidator interface {
	ValidateTokenScope(ctx context.Context, userID string, scopes []domain.PermissionRule) error
}

// SetScopeValidator installs the token scope validator (wiring: the
// permission service is created after the identity service).
func (s *Service) SetScopeValidator(v ScopeValidator) { s.scopes.Store(&v) }

func (s *Service) scopeValidator() ScopeValidator {
	if v := s.scopes.Load(); v != nil {
		return *v
	}
	return nil
}

func tokenIdemKey(tokenID string) string {
	return authz.Principal{Kind: authz.KindAPIToken, TokenID: tokenID}.Key()
}

func validTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", &domain.FieldError{Field: "name", Message: "1-64 characters without control characters"}
	}
	return name, nil
}

func scopeTexts(rules []domain.PermissionRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, policy.Rule{Capability: r.Capability, Effect: policy.Allow, Scope: policy.Scope{
			Kind: policy.ScopeKind(r.Scope.Kind), EnvironmentID: r.Scope.EnvironmentID,
			ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}}.Shorthand())
	}
	return out
}

// CreateAPIToken creates a token for the caller (a full browser session
// with a recent step-up; the route checks api_tokens.create). It returns
// the stored token and its secret, which is never shown again.
func (s *Service) CreateAPIToken(ctx context.Context, in domain.NewAPIToken) (domain.APIToken, string, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return domain.APIToken{}, "", err
	}
	if err := s.requireRecent(cur); err != nil {
		return domain.APIToken{}, "", err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.APIToken{}, "", err
	}
	if !set.APITokensEnabled {
		return domain.APIToken{}, "", domain.ErrAPITokensDisabled
	}
	name, err := validTokenName(in.Name)
	if err != nil {
		return domain.APIToken{}, "", err
	}
	now := s.now()
	var expires *time.Time
	switch {
	case in.NeverExpires && in.ExpiresAt != nil:
		return domain.APIToken{}, "", &domain.FieldError{Field: "neverExpires", Message: "set either expiresAt or neverExpires"}
	case in.NeverExpires:
		if !set.APITokensNonExpiring {
			return domain.APIToken{}, "", &domain.FieldError{Field: "neverExpires", Message: "the instance owner does not allow API tokens without expiry"}
		}
	case in.ExpiresAt == nil:
		return domain.APIToken{}, "", &domain.FieldError{Field: "expiresAt", Message: "an expiry is required"}
	default:
		exp := in.ExpiresAt.UTC().Truncate(time.Second)
		maxAt := now.Add(time.Duration(set.APITokenMaxDays) * 24 * time.Hour)
		if !exp.After(now) {
			return domain.APIToken{}, "", &domain.FieldError{Field: "expiresAt", Message: "must be in the future"}
		}
		if exp.After(maxAt) {
			return domain.APIToken{}, "", &domain.FieldError{Field: "expiresAt",
				Message: "at most " + strconv.Itoa(set.APITokenMaxDays) + " days ahead (instance maximum)"}
		}
		expires = &exp
	}
	if len(in.Scopes) == 0 {
		return domain.APIToken{}, "", &domain.FieldError{Field: "scopes", Message: "choose at least one capability"}
	}
	if len(in.Scopes) > domain.MaxAPITokenScopes {
		return domain.APIToken{}, "", &domain.FieldError{Field: "scopes", Message: "at most " + strconv.Itoa(domain.MaxAPITokenScopes) + " grants"}
	}
	scopes := make([]domain.PermissionRule, len(in.Scopes))
	for i, r := range in.Scopes {
		if r.Effect == "" {
			r.Effect = domain.PermissionAllow
		}
		if r.Effect != domain.PermissionAllow {
			return domain.APIToken{}, "", &domain.RuleError{Field: "scopes", Problems: []domain.RuleProblem{{Index: i, Message: "token scopes contain allow grants only"}}}
		}
		scopes[i] = r
	}
	v := s.scopeValidator()
	if v == nil {
		return domain.APIToken{}, "", domain.ErrIdentityUnavailable
	}
	if err := v.ValidateTokenScope(ctx, cur.user.ID, scopes); err != nil {
		return domain.APIToken{}, "", err
	}
	id := newID()
	minted, err := authsep.MintAPIToken(id)
	if err != nil {
		return domain.APIToken{}, "", err
	}
	t := domain.APIToken{ID: id, UserID: cur.user.ID, Username: cur.user.Username, Name: name, CreatedAt: now,
		ExpiresAt: expires, Scopes: scopes}
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.InsertAPIToken(ctx, tx, t, minted.Verifier)
	}); err != nil {
		return domain.APIToken{}, "", err
	}
	audit.SetDetail(ctx, "tokenName", name)
	audit.SetDetail(ctx, "scopes", scopeTexts(scopes))
	if expires != nil {
		audit.SetDetail(ctx, "expiresAt", expires.Format(time.RFC3339))
	} else {
		audit.SetDetail(ctx, "neverExpires", true)
	}
	s.record(ctx, "api_token.create", OutcomeSuccess, cur.user.ID, "api_token", id, "")
	return t, minted.Token, nil
}

// ListMyAPITokens lists the caller's tokens (every status), newest first.
func (s *Service) ListMyAPITokens(ctx context.Context, beforeID string, limit int) ([]domain.APIToken, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return nil, err
	}
	return store.ListAPITokens(ctx, s.db, cur.user.ID, beforeID, limit)
}

// GetMyAPIToken returns one of the caller's tokens (another user's token
// is not found).
func (s *Service) GetMyAPIToken(ctx context.Context, id string) (domain.APIToken, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return domain.APIToken{}, err
	}
	return store.GetAPIToken(ctx, s.db, id, cur.user.ID)
}

// RenameMyAPIToken changes the name of one of the caller's tokens (the
// only editable field: scope and expiry are fixed at creation).
func (s *Service) RenameMyAPIToken(ctx context.Context, id, name string) (domain.APIToken, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return domain.APIToken{}, err
	}
	name, err = validTokenName(name)
	if err != nil {
		return domain.APIToken{}, err
	}
	before, err := store.GetAPIToken(ctx, s.db, id, cur.user.ID)
	if err != nil {
		return domain.APIToken{}, err
	}
	if err := store.RenameAPIToken(ctx, s.db, id, cur.user.ID, name); err != nil {
		return domain.APIToken{}, err
	}
	audit.SetDiff(ctx, map[string]string{"tokenName": before.Name}, map[string]string{"tokenName": name})
	s.record(ctx, "api_token.rename", OutcomeSuccess, cur.user.ID, "api_token", id, "")
	return store.GetAPIToken(ctx, s.db, id, cur.user.ID)
}

// RevokeMyAPIToken revokes one of the caller's tokens and closes its open
// streams. Revoking a revoked token again is harmless.
func (s *Service) RevokeMyAPIToken(ctx context.Context, id string) error {
	cur, err := s.session(ctx, false)
	if err != nil {
		return err
	}
	return s.revokeToken(ctx, id, cur.user.ID, cur.user.ID, domain.RevokedByUser)
}

// ListAPITokens lists every user's tokens, newest first (owner).
func (s *Service) ListAPITokens(ctx context.Context, beforeID string, limit int) ([]domain.APIToken, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return nil, err
	}
	return store.ListAPITokens(ctx, s.db, "", beforeID, limit)
}

// RevokeAPIToken revokes any user's token (owner) and closes its streams.
func (s *Service) RevokeAPIToken(ctx context.Context, id string) error {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return err
	}
	return s.revokeToken(ctx, id, "", cur.user.ID, domain.RevokedByOwner)
}

func (s *Service) revokeToken(ctx context.Context, id, ofUser, by string, reason domain.APITokenRevocation) error {
	owner, revoked, err := store.RevokeAPIToken(ctx, s.db, id, ofUser, s.now(), by, reason)
	if err != nil {
		return err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "user", ID: owner})
	if revoked {
		audit.SetDetail(ctx, "revokedReason", string(reason))
		s.record(ctx, "api_token.revoke", OutcomeSuccess, by, "api_token", id, string(reason))
		s.endTokens(ctx, []string{id})
	}
	return nil
}

// RevokeAllAPITokens revokes every API token of every user and closes
// their streams. It is the disaster-restore hook (#24): after a restore of
// the manager, all restored tokens are revoked (reason restore) so a token
// that leaked with a backup cannot be used. In-process only (no HTTP
// route); recorded in the audit trail with the service actor.
func (s *Service) RevokeAllAPITokens(ctx context.Context, reason domain.APITokenRevocation) (int, error) {
	got, err := store.RevokeAllAPITokens(ctx, s.db, s.now(), "", reason)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(got))
	for id := range got {
		ids = append(ids, id)
	}
	s.endTokens(ctx, ids)
	s.audit.Record(ctx, AuditEvent{Action: "api_token.revoke_all", Outcome: OutcomeSuccess, Reason: string(reason)})
	return len(ids), nil
}

// revokeUserTokens revokes every live token of userID (account disable,
// credential resets asking for it) and closes their streams.
func (s *Service) revokeUserTokens(ctx context.Context, userID, by string, reason domain.APITokenRevocation) error {
	ids, err := store.RevokeUserAPITokens(ctx, s.db, userID, s.now(), by, reason)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		// A count under a metadata-suffixed key: the audit redaction treats
		// every key containing "token" as secret unless it ends in Count,
		// ID, Name, ... ("apiTokensRevoked" was stored as [REDACTED]).
		audit.SetDetail(ctx, "apiTokenCount", len(ids))
	}
	s.endTokens(ctx, ids)
	return nil
}

// endTokens closes the open requests of tokens and forgets their stored
// idempotent responses.
func (s *Service) endTokens(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		s.forgetToken(ctx, id)
	}
	s.hub.revokeTokens(ids, authz.ErrSessionEnded)
}

func (s *Service) forgetToken(ctx context.Context, id string) {
	if s.idem == nil {
		return
	}
	if _, err := s.idem.Forget(context.WithoutCancel(ctx), tokenIdemKey(id)); err != nil {
		s.log.Warn("forget idempotent responses", "token_id", id, "error", err)
	}
}

// forgetUserTokens forgets the stored idempotent responses of every token
// of userID (their access changed).
func (s *Service) forgetUserTokens(ctx context.Context, userID string) {
	if s.idem == nil {
		return
	}
	ids, err := store.UserAPITokenIDs(context.WithoutCancel(ctx), s.db, userID)
	if err != nil {
		s.log.Warn("list api tokens to forget idempotent responses", "user_id", userID, "error", err)
		return
	}
	for _, id := range ids {
		s.forgetToken(ctx, id)
	}
}

// AuthenticateAPIToken checks a presented bearer token (the middleware).
// Every refusal is domain.ErrAPITokenInvalid, whatever the reason. The
// last-used time and client IP are recorded at most every
// TokenUseGranularity.
func (s *Service) AuthenticateAPIToken(ctx context.Context, raw string) (authz.Principal, error) {
	id, secret, ok := authsep.ParseAPIToken(raw)
	if !ok {
		return authz.Principal{}, domain.ErrAPITokenInvalid
	}
	creds, err := store.APITokenCredentials(ctx, s.db, []string{id})
	if err != nil {
		return authz.Principal{}, err
	}
	c, found := creds[id]
	if !found || !authsep.VerifierMatches(c.Verifier, secret) {
		return authz.Principal{}, domain.ErrAPITokenInvalid
	}
	set, err := s.settings(ctx)
	if err != nil {
		return authz.Principal{}, err
	}
	now := s.now()
	if !s.tokenLive(c, set, now) {
		return authz.Principal{}, domain.ErrAPITokenInvalid
	}
	if ok, err := s.meetsFactorPolicy(ctx, c.UserID, set); err != nil {
		return authz.Principal{}, err
	} else if !ok {
		return authz.Principal{}, domain.ErrAPITokenInvalid
	}
	ip := ""
	if a := requestinfo.ClientIP(ctx); a.IsValid() {
		ip = a.String()
	}
	if c.LastUsedAt == nil || now.Sub(*c.LastUsedAt) >= TokenUseGranularity {
		if err := store.TouchAPIToken(ctx, s.db, id, now, ip); err != nil {
			s.log.Warn("record api token use", "token_id", id, "error", err)
		}
	}
	return authz.Principal{Kind: authz.KindAPIToken, UserID: c.UserID, TokenID: id}, nil
}

// tokenLive reports whether a token may authenticate at now (not the
// factor policy, which needs the account).
func (s *Service) tokenLive(c domain.APITokenCredential, set domain.SecuritySettings, now time.Time) bool {
	return set.APITokensEnabled && !c.Revoked && c.UserActive && (c.ExpiresAt == nil || now.Before(*c.ExpiresAt))
}

// meetsFactorPolicy reports whether the account has enrolled the factors
// the instance sign-in policy requires: a token never stands in for them.
func (s *Service) meetsFactorPolicy(ctx context.Context, userID string, set domain.SecuritySettings) (bool, error) {
	if set.RequiredFactors == domain.FactorsNone {
		return true, nil
	}
	u, err := store.GetUser(ctx, s.db, userID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	enrolled, err := s.enrolled(ctx, u)
	if err != nil {
		return false, err
	}
	return enrollmentComplete(set.RequiredFactors, enrolled), nil
}

// APITokenScopes returns the scope of a live token for the permission
// service (permissions.TokenScopes): ok is false for unknown, expired or
// revoked tokens, tokens of disabled accounts and when tokens are disabled
// instance-wide, which then deny everything (also at job dispatch).
func (s *Service) APITokenScopes(ctx context.Context, tokenID string) ([]domain.PermissionRule, bool, error) {
	creds, err := store.APITokenCredentials(ctx, s.db, []string{tokenID})
	if err != nil {
		return nil, false, err
	}
	c, found := creds[tokenID]
	if !found {
		return nil, false, nil
	}
	set, err := s.settings(ctx)
	if err != nil {
		return nil, false, err
	}
	if !s.tokenLive(c, set, s.now()) {
		return nil, false, nil
	}
	rules, err := store.APITokenScopes(ctx, s.db, tokenID)
	if err != nil {
		return nil, false, err
	}
	return rules, true, nil
}

// sweepTokens closes open requests of tokens that stopped being valid.
func (s *Service) sweepTokens(ctx context.Context, tokens map[string]string) error {
	if len(tokens) == 0 {
		return nil
	}
	ids := make([]string, 0, len(tokens))
	for id := range tokens {
		ids = append(ids, id)
	}
	creds, err := store.APITokenCredentials(ctx, s.db, ids)
	if err != nil {
		return err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	var dead []string
	for _, id := range ids {
		if c, ok := creds[id]; !ok || !s.tokenLive(c, set, now) {
			dead = append(dead, id)
		}
	}
	s.hub.revokeTokens(dead, authz.ErrSessionEnded)
	return nil
}

// tokenAccount returns the account of an API-token request (GET /me).
func (s *Service) tokenAccount(ctx context.Context) (domain.Account, bool, error) {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok || p.Kind != authz.KindAPIToken {
		return domain.Account{}, false, nil
	}
	u, err := store.GetUser(ctx, s.db, p.UserID)
	if err != nil {
		return domain.Account{}, true, err
	}
	a, err := s.account(ctx, u)
	return a, true, err
}
