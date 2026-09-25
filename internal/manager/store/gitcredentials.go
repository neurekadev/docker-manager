package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
)

// Git credential persistence (#33). As for registry connections the sealed
// secret never leaves this package inside a domain value.

type gitCredentialRow struct {
	bun.BaseModel `bun:"table:git_credentials"`

	ID                string     `bun:"id,pk"`
	Name              string     `bun:"name,notnull"`
	NameKey           string     `bun:"name_key,notnull"`
	Host              string     `bun:"host,notnull"`
	PathPrefix        string     `bun:"path_prefix,notnull"`
	Username          string     `bun:"username,notnull"`
	PlainHTTP         int        `bun:"plain_http,notnull"`
	Status            string     `bun:"status,notnull"`
	SecretSealed      string     `bun:"secret_sealed,notnull"`
	SecretFingerprint string     `bun:"secret_fingerprint,notnull"`
	SecretVersion     int        `bun:"secret_version,notnull"`
	SecretUpdatedAt   time.Time  `bun:"secret_updated_at,notnull"`
	LastUsedAt        *time.Time `bun:"last_used_at"`
	LastCheckAt       *time.Time `bun:"last_check_at"`
	LastCheckResult   string     `bun:"last_check_result,notnull"`
	RevokedAt         *time.Time `bun:"revoked_at"`
	Revision          int64      `bun:"revision,notnull"`
	CreatedAt         time.Time  `bun:"created_at,notnull"`
	UpdatedAt         time.Time  `bun:"updated_at,notnull"`
}

func fromGitCredential(c *domain.GitCredential, sealed string) gitCredentialRow {
	return gitCredentialRow{
		ID: c.ID, Name: c.Name, NameKey: NameKey(c.Name), Host: c.Host, PathPrefix: c.PathPrefix, Username: c.Username,
		PlainHTTP: b2i(c.PlainHTTP), Status: string(c.Status), SecretSealed: sealed, SecretFingerprint: c.SecretFingerprint,
		SecretVersion: c.SecretVersion, SecretUpdatedAt: c.SecretUpdatedAt.UTC(), LastUsedAt: utcPtr(c.LastUsedAt),
		LastCheckAt: utcPtr(c.LastCheckAt), LastCheckResult: c.LastCheckResult, RevokedAt: utcPtr(c.RevokedAt),
		Revision: c.Revision, CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
}

func (r gitCredentialRow) toDomain() domain.GitCredential {
	return domain.GitCredential{
		ID: r.ID, Name: r.Name, Host: r.Host, PathPrefix: r.PathPrefix, Username: r.Username, PlainHTTP: r.PlainHTTP == 1,
		Status: domain.RegistryConnectionStatus(r.Status), SecretFingerprint: r.SecretFingerprint, SecretVersion: r.SecretVersion,
		SecretUpdatedAt: r.SecretUpdatedAt.UTC(), LastUsedAt: utcPtr(r.LastUsedAt), LastCheckAt: utcPtr(r.LastCheckAt),
		LastCheckResult: r.LastCheckResult, RevokedAt: utcPtr(r.RevokedAt), Revision: r.Revision,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// InsertGitCredential stores a new credential with its sealed secret.
func InsertGitCredential(ctx context.Context, db bun.IDB, c *domain.GitCredential, sealed string) error {
	row := fromGitCredential(c, sealed)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "git_credentials.name_key") {
			return domain.ErrGitCredentialNameTaken
		}
		return fmt.Errorf("store: insert git credential: %w", err)
	}
	return nil
}

// UpdateGitCredential writes every column of c; sealed replaces the secret
// unless keepSecret is set. It only updates when the stored revision still
// equals expectRevision.
func UpdateGitCredential(ctx context.Context, db bun.IDB, c *domain.GitCredential, sealed string, keepSecret bool, expectRevision int64) error {
	row := fromGitCredential(c, sealed)
	cols := []string{"name", "name_key", "path_prefix", "username", "plain_http", "status", "secret_fingerprint", "secret_version",
		"secret_updated_at", "last_check_at", "last_check_result", "revoked_at", "revision", "updated_at"}
	if !keepSecret {
		cols = append(cols, "secret_sealed")
	}
	res, err := db.NewUpdate().Model(&row).Column(cols...).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "git_credentials.name_key") {
			return domain.ErrGitCredentialNameTaken
		}
		return fmt.Errorf("store: update git credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetGitCredential(ctx, db, c.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// RecordGitCheck stores the outcome of a test or use.
func RecordGitCheck(ctx context.Context, db bun.IDB, id string, at time.Time, result string, ok bool) error {
	q := db.NewUpdate().Model((*gitCredentialRow)(nil)).Where("id = ?", id).
		Set("last_check_at = ?", at.UTC()).Set("last_check_result = ?", result)
	if ok {
		q = q.Set("last_used_at = ?", at.UTC())
	}
	if _, err := q.Exec(ctx); err != nil {
		return fmt.Errorf("store: record git check: %w", err)
	}
	return nil
}

// DeleteGitCredential removes a credential when the revision matches.
func DeleteGitCredential(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*gitCredentialRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete git credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetGitCredential(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetGitCredential returns one credential (without its secret).
func GetGitCredential(ctx context.Context, db bun.IDB, id string) (domain.GitCredential, error) {
	var row gitCredentialRow
	err := db.NewSelect().Model(&row).ExcludeColumn("secret_sealed").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GitCredential{}, domain.ErrGitCredentialNotFound
	}
	if err != nil {
		return domain.GitCredential{}, fmt.Errorf("store: get git credential: %w", err)
	}
	return row.toDomain(), nil
}

// ListGitCredentials returns credentials in ID order after afterID (at most
// limit, 0 = all); host filters by host.
func ListGitCredentials(ctx context.Context, db bun.IDB, host, afterID string, limit int) ([]domain.GitCredential, error) {
	var rows []gitCredentialRow
	q := db.NewSelect().Model(&rows).ExcludeColumn("secret_sealed").Order("id ASC")
	if host != "" {
		q = q.Where("host = ?", host)
	}
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list git credentials: %w", err)
	}
	out := make([]domain.GitCredential, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GitSecret returns a credential's sealed secret and version.
func GitSecret(ctx context.Context, db bun.IDB, id string) (sealed string, version int, err error) {
	var row gitCredentialRow
	err = db.NewSelect().Model(&row).Column("secret_sealed", "secret_version").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, domain.ErrGitCredentialNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: read git secret: %w", err)
	}
	return row.SecretSealed, row.SecretVersion, nil
}
