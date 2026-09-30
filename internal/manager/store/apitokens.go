package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// API token persistence (#31). internal/manager/auth owns every state
// change; only verifiers of the secrets are stored.

type apiTokenRow struct {
	bun.BaseModel `bun:"table:api_tokens"`

	ID            string     `bun:"id,pk"`
	UserID        string     `bun:"user_id,notnull"`
	Name          string     `bun:"name,notnull"`
	Verifier      string     `bun:"verifier,notnull"`
	CreatedAt     time.Time  `bun:"created_at,notnull"`
	ExpiresAt     *time.Time `bun:"expires_at"`
	LastUsedAt    *time.Time `bun:"last_used_at"`
	LastUsedIP    string     `bun:"last_used_ip,notnull"`
	RevokedAt     *time.Time `bun:"revoked_at"`
	RevokedBy     string     `bun:"revoked_by,notnull"`
	RevokedReason *string    `bun:"revoked_reason"`
	// Username is joined by the owner's list (not a column).
	Username string `bun:"username,scanonly"`
}

func (r apiTokenRow) toDomain() domain.APIToken {
	return domain.APIToken{
		ID: r.ID, UserID: r.UserID, Username: r.Username, Name: r.Name, CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: utcPtr(r.ExpiresAt), LastUsedAt: utcPtr(r.LastUsedAt), LastUsedIP: r.LastUsedIP,
		RevokedAt: utcPtr(r.RevokedAt), RevokedBy: r.RevokedBy, RevokedReason: domain.APITokenRevocation(deref(r.RevokedReason)),
	}
}

type apiTokenScopeRow struct {
	TokenID       string `bun:"token_id"`
	Capability    string `bun:"capability"`
	ScopeKind     string `bun:"scope_kind"`
	EnvironmentID string `bun:"environment_id"`
	ResourceType  string `bun:"resource_type"`
	ResourceID    string `bun:"resource_id"`
}

// InsertAPIToken stores a new token with its verifier and scope. Run it in
// a transaction.
func InsertAPIToken(ctx context.Context, db bun.IDB, t domain.APIToken, verifier string) error {
	row := apiTokenRow{ID: t.ID, UserID: t.UserID, Name: t.Name, Verifier: verifier, CreatedAt: t.CreatedAt.UTC(),
		ExpiresAt: utcPtr(t.ExpiresAt)}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert api token: %w", err)
	}
	for i, r := range t.Scopes {
		if _, err := db.NewRaw(`INSERT INTO api_token_scopes (token_id, capability, scope_kind, environment_id, resource_type, resource_id, position)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, t.ID, r.Capability, r.Scope.Kind, r.Scope.EnvironmentID, r.Scope.ResourceType,
			r.Scope.ResourceID, i).Exec(ctx); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return &domain.RuleError{Field: "scopes", Problems: []domain.RuleProblem{{Index: i, Message: "duplicates another grant (same capability and scope)"}}}
			}
			return fmt.Errorf("store: insert api token scope: %w", err)
		}
	}
	return nil
}

// APITokenScopes returns a token's scope in creation order.
func APITokenScopes(ctx context.Context, db bun.IDB, tokenID string) ([]domain.PermissionRule, error) {
	m, err := apiTokenScopes(ctx, db, []string{tokenID})
	if err != nil {
		return nil, err
	}
	return m[tokenID], nil
}

func apiTokenScopes(ctx context.Context, db bun.IDB, ids []string) (map[string][]domain.PermissionRule, error) {
	out := map[string][]domain.PermissionRule{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []apiTokenScopeRow
	err := db.NewRaw(`SELECT token_id, capability, scope_kind, environment_id, resource_type, resource_id
		FROM api_token_scopes WHERE token_id IN (?) ORDER BY token_id, position`, bun.List(ids)).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read api token scopes: %w", err)
	}
	for _, r := range rows {
		out[r.TokenID] = append(out[r.TokenID], domain.PermissionRule{Capability: r.Capability, Effect: domain.PermissionAllow,
			Scope: domain.PermissionScope{Kind: r.ScopeKind, EnvironmentID: r.EnvironmentID, ResourceType: r.ResourceType, ResourceID: r.ResourceID}})
	}
	return out, nil
}

func withScopes(ctx context.Context, db bun.IDB, rows []apiTokenRow) ([]domain.APIToken, error) {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	scopes, err := apiTokenScopes(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.APIToken, 0, len(rows))
	for _, r := range rows {
		t := r.toDomain()
		t.Scopes = scopes[r.ID]
		if t.Scopes == nil {
			t.Scopes = []domain.PermissionRule{}
		}
		out = append(out, t)
	}
	return out, nil
}

//nolint:gosec // G101: a query, not a credential
const apiTokenSelect = `SELECT t.id, t.user_id, t.name, t.verifier, t.created_at, t.expires_at, t.last_used_at, t.last_used_ip,
	t.revoked_at, t.revoked_by, t.revoked_reason, u.username
	FROM api_tokens t JOIN users u ON u.id = t.user_id`

// GetAPIToken returns a token with its scope. userID, when not empty,
// restricts the lookup to that user's tokens (domain.ErrAPITokenNotFound
// for another user's token).
func GetAPIToken(ctx context.Context, db bun.IDB, id, userID string) (domain.APIToken, error) {
	var rows []apiTokenRow
	q := apiTokenSelect + ` WHERE t.id = ?`
	args := []any{id}
	if userID != "" {
		q += ` AND t.user_id = ?`
		args = append(args, userID)
	}
	if err := db.NewRaw(q, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.APIToken{}, fmt.Errorf("store: read api token: %w", err)
	}
	if len(rows) == 0 {
		return domain.APIToken{}, domain.ErrAPITokenNotFound
	}
	out, err := withScopes(ctx, db, rows)
	if err != nil {
		return domain.APIToken{}, err
	}
	return out[0], nil
}

// ListAPITokens lists tokens newest first (IDs are UUIDv7), before
// beforeID when set; userID, when not empty, restricts the list to that
// user's tokens.
func ListAPITokens(ctx context.Context, db bun.IDB, userID, beforeID string, limit int) ([]domain.APIToken, error) {
	var (
		conds []string
		args  []any
		rows  []apiTokenRow
	)
	if userID != "" {
		conds, args = append(conds, "t.user_id = ?"), append(args, userID)
	}
	if beforeID != "" {
		conds, args = append(conds, "t.id < ?"), append(args, beforeID)
	}
	q := apiTokenSelect
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY t.id DESC LIMIT ?"
	args = append(args, limit)
	if err := db.NewRaw(q, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	return withScopes(ctx, db, rows)
}

// RenameAPIToken changes the name of userID's token.
func RenameAPIToken(ctx context.Context, db bun.IDB, id, userID, name string) error {
	res, err := db.NewRaw(`UPDATE api_tokens SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: rename api token: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrAPITokenNotFound
	}
	return nil
}

// RevokeAPITokens revokes the tokens matching where (a SQL condition on
// api_tokens) that are not revoked yet and returns their IDs and users.
func revokeAPITokens(ctx context.Context, db bun.IDB, now time.Time, by string, reason domain.APITokenRevocation,
	where string, args ...any) (map[string]string, error) {
	var rows []struct {
		ID     string `bun:"id"`
		UserID string `bun:"user_id"`
	}
	q := `UPDATE api_tokens SET revoked_at = ?, revoked_by = ?, revoked_reason = ? WHERE revoked_at IS NULL`
	if where != "" {
		q += " AND " + where
	}
	q += " RETURNING id, user_id"
	all := append([]any{now.UTC(), by, string(reason)}, args...)
	if err := db.NewRaw(q, all...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: revoke api tokens: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.UserID
	}
	return out, nil
}

// RevokeAPIToken revokes one token (of userID when not empty). It returns
// the token's user, whether this call revoked it (false when it was
// revoked before) and domain.ErrAPITokenNotFound for unknown tokens.
func RevokeAPIToken(ctx context.Context, db bun.IDB, id, userID string, now time.Time, by string,
	reason domain.APITokenRevocation) (owner string, revoked bool, err error) {
	t, err := GetAPIToken(ctx, db, id, userID)
	if err != nil {
		return "", false, err
	}
	got, err := revokeAPITokens(ctx, db, now, by, reason, "id = ?", id)
	if err != nil {
		return "", false, err
	}
	_, revoked = got[id]
	return t.UserID, revoked, nil
}

// RevokeUserAPITokens revokes every live token of a user and returns their IDs.
func RevokeUserAPITokens(ctx context.Context, db bun.IDB, userID string, now time.Time, by string, reason domain.APITokenRevocation) ([]string, error) {
	got, err := revokeAPITokens(ctx, db, now, by, reason, "user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	return keys(got), nil
}

// RevokeAllAPITokens revokes every token of every user and returns the
// revoked tokens (ID -> user).
func RevokeAllAPITokens(ctx context.Context, db bun.IDB, now time.Time, by string, reason domain.APITokenRevocation) (map[string]string, error) {
	return revokeAPITokens(ctx, db, now, by, reason, "")
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// APITokenCredentials returns what authentication needs about tokens:
// verifier, expiry, revocation and the account's status. Unknown IDs are
// absent from the map.
func APITokenCredentials(ctx context.Context, db bun.IDB, ids []string) (map[string]domain.APITokenCredential, error) {
	out := map[string]domain.APITokenCredential{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID         string     `bun:"id"`
		UserID     string     `bun:"user_id"`
		Verifier   string     `bun:"verifier"`
		ExpiresAt  *time.Time `bun:"expires_at"`
		RevokedAt  *time.Time `bun:"revoked_at"`
		Status     string     `bun:"status"`
		LastUsedAt *time.Time `bun:"last_used_at"`
		LastUsedIP string     `bun:"last_used_ip"`
	}
	err := db.NewRaw(`SELECT t.id, t.user_id, t.verifier, t.expires_at, t.revoked_at, u.status, t.last_used_at, t.last_used_ip
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id IN (?)`, bun.List(ids)).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read api token credentials: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = domain.APITokenCredential{TokenID: r.ID, UserID: r.UserID, Verifier: r.Verifier, ExpiresAt: utcPtr(r.ExpiresAt),
			Revoked: r.RevokedAt != nil, UserActive: r.Status == string(domain.UserActive), LastUsedAt: utcPtr(r.LastUsedAt),
			LastUsedIP: r.LastUsedIP}
	}
	return out, nil
}

// TouchAPIToken records a use of a live token.
func TouchAPIToken(ctx context.Context, db bun.IDB, id string, at time.Time, ip string) error {
	if _, err := db.NewRaw(`UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ? AND revoked_at IS NULL`,
		at.UTC(), ip, id).Exec(ctx); err != nil {
		return fmt.Errorf("store: record api token use: %w", err)
	}
	return nil
}

// UserAPITokenIDs returns the IDs of every token of a user.
func UserAPITokenIDs(ctx context.Context, db bun.IDB, userID string) ([]string, error) {
	var ids []string
	if err := db.NewRaw(`SELECT id FROM api_tokens WHERE user_id = ?`, userID).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list api token ids: %w", err)
	}
	return ids, nil
}
