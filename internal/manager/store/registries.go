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

// Registry connection persistence (#19). The sealed secret never leaves
// this package inside a domain value: it is written with the connection and
// read only through RegistrySecret.

type registryConnectionRow struct {
	bun.BaseModel `bun:"table:registry_connections"`

	ID                string     `bun:"id,pk"`
	Name              string     `bun:"name,notnull"`
	NameKey           string     `bun:"name_key,notnull"`
	Host              string     `bun:"host,notnull"`
	CredentialType    string     `bun:"credential_type,notnull"`
	Username          string     `bun:"username,notnull"`
	RepositoryPattern string     `bun:"repository_pattern,notnull"`
	EnvironmentID     *string    `bun:"environment_id"`
	StackID           string     `bun:"stack_id,notnull"`
	Priority          int        `bun:"priority,notnull"`
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

// NameKey is the case-insensitive uniqueness key of a display name.
func NameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func fromRegistryConnection(c *domain.RegistryConnection, sealed string) registryConnectionRow {
	return registryConnectionRow{
		ID: c.ID, Name: c.Name, NameKey: NameKey(c.Name), Host: c.Host, CredentialType: string(c.CredentialType),
		Username: c.Username, RepositoryPattern: c.RepositoryPattern, EnvironmentID: nullable(c.EnvironmentID), StackID: c.StackID,
		Priority: c.Priority, PlainHTTP: b2i(c.PlainHTTP), Status: string(c.Status), SecretSealed: sealed,
		SecretFingerprint: c.SecretFingerprint, SecretVersion: c.SecretVersion, SecretUpdatedAt: c.SecretUpdatedAt.UTC(),
		LastUsedAt: utcPtr(c.LastUsedAt), LastCheckAt: utcPtr(c.LastCheckAt), LastCheckResult: c.LastCheckResult,
		RevokedAt: utcPtr(c.RevokedAt), Revision: c.Revision, CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
}

func (r registryConnectionRow) toDomain() domain.RegistryConnection {
	return domain.RegistryConnection{
		ID: r.ID, Name: r.Name, Host: r.Host, CredentialType: domain.RegistryCredentialType(r.CredentialType), Username: r.Username,
		RepositoryPattern: r.RepositoryPattern, EnvironmentID: deref(r.EnvironmentID), StackID: r.StackID, Priority: r.Priority,
		PlainHTTP: r.PlainHTTP == 1, Status: domain.RegistryConnectionStatus(r.Status), SecretFingerprint: r.SecretFingerprint,
		SecretVersion: r.SecretVersion, SecretUpdatedAt: r.SecretUpdatedAt.UTC(), LastUsedAt: utcPtr(r.LastUsedAt),
		LastCheckAt: utcPtr(r.LastCheckAt), LastCheckResult: r.LastCheckResult, RevokedAt: utcPtr(r.RevokedAt),
		Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// InsertRegistryConnection stores a new connection with its sealed secret.
func InsertRegistryConnection(ctx context.Context, db bun.IDB, c *domain.RegistryConnection, sealed string) error {
	row := fromRegistryConnection(c, sealed)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "registry_connections.name_key") {
			return domain.ErrRegistryConnectionNameTaken
		}
		return fmt.Errorf("store: insert registry connection: %w", err)
	}
	return nil
}

// UpdateRegistryConnection writes the metadata columns of c (not the
// secret) when the stored revision still equals expectRevision.
func UpdateRegistryConnection(ctx context.Context, db bun.IDB, c *domain.RegistryConnection, expectRevision int64) error {
	row := fromRegistryConnection(c, "")
	return updateRegistryRow(ctx, db, &row, expectRevision, "name", "name_key", "repository_pattern", "environment_id",
		"stack_id", "priority", "plain_http", "revision", "updated_at")
}

// ReplaceRegistrySecret writes a connection's status, credential columns
// and sealed secret ("" revokes) when the revision still matches.
func ReplaceRegistrySecret(ctx context.Context, db bun.IDB, c *domain.RegistryConnection, sealed string, expectRevision int64) error {
	row := fromRegistryConnection(c, sealed)
	return updateRegistryRow(ctx, db, &row, expectRevision, "credential_type", "username", "status", "secret_sealed",
		"secret_fingerprint", "secret_version", "secret_updated_at", "revoked_at", "last_check_at", "last_check_result",
		"revision", "updated_at")
}

func updateRegistryRow(ctx context.Context, db bun.IDB, row *registryConnectionRow, expectRevision int64, cols ...string) error {
	res, err := db.NewUpdate().Model(row).Column(cols...).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "registry_connections.name_key") {
			return domain.ErrRegistryConnectionNameTaken
		}
		return fmt.Errorf("store: update registry connection: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetRegistryConnection(ctx, db, row.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// RecordRegistryCheck stores the outcome of a test or use (no revision
// change: it is status, not configuration). ok also stamps LastUsedAt.
func RecordRegistryCheck(ctx context.Context, db bun.IDB, id string, at time.Time, result string, ok bool) error {
	q := db.NewUpdate().Model((*registryConnectionRow)(nil)).Where("id = ?", id).
		Set("last_check_at = ?", at.UTC()).Set("last_check_result = ?", result)
	if ok {
		q = q.Set("last_used_at = ?", at.UTC())
	}
	if _, err := q.Exec(ctx); err != nil {
		return fmt.Errorf("store: record registry check: %w", err)
	}
	return nil
}

// DeleteRegistryConnection removes a connection when the revision matches.
func DeleteRegistryConnection(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*registryConnectionRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete registry connection: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetRegistryConnection(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetRegistryConnection returns one connection (without its secret).
func GetRegistryConnection(ctx context.Context, db bun.IDB, id string) (domain.RegistryConnection, error) {
	var row registryConnectionRow
	err := db.NewSelect().Model(&row).ExcludeColumn("secret_sealed").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RegistryConnection{}, domain.ErrRegistryConnectionNotFound
	}
	if err != nil {
		return domain.RegistryConnection{}, fmt.Errorf("store: get registry connection: %w", err)
	}
	return row.toDomain(), nil
}

// ListRegistryConnections returns connections in ID (creation) order after
// afterID, at most limit (0 = all). host filters by normalized host.
func ListRegistryConnections(ctx context.Context, db bun.IDB, host, afterID string, limit int) ([]domain.RegistryConnection, error) {
	var rows []registryConnectionRow
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
		return nil, fmt.Errorf("store: list registry connections: %w", err)
	}
	out := make([]domain.RegistryConnection, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// RegistrySecret returns a connection's sealed secret and its version
// ("" when revoked).
func RegistrySecret(ctx context.Context, db bun.IDB, id string) (sealed string, version int, err error) {
	var row registryConnectionRow
	err = db.NewSelect().Model(&row).Column("secret_sealed", "secret_version").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, domain.ErrRegistryConnectionNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: read registry secret: %w", err)
	}
	return row.SecretSealed, row.SecretVersion, nil
}
