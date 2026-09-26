// Package sessions configures Docker Manager's browser sessions (#16, #18):
// alexedwards/scs/v2 owns token generation, the cookie, renewal and
// idle/absolute expiry; Store persists SCS sessions in Docker Manager's
// Bun/SQLite database (migration create_sessions).
//
// Why a Docker Manager store instead of SCS's upstream bunstore (ADR 0003): the
// upstream module is untagged (pseudo-versions only), reads the wall clock
// directly, runs an unmanaged cleanup goroutine that logs with the standard
// logger, masks database errors on lookup, and its module requires the
// MySQL/PostgreSQL drivers. This store implements the same scs.CtxStore and
// scs.IterableCtxStore contracts on the shared single-writer connection,
// takes the injectable clock and leaves sweeping to the manager's
// housekeeping (DeleteExpired).
package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
)

// Compile-time checks: Store satisfies every SCS store contract.
var (
	_ scs.Store            = (*Store)(nil)
	_ scs.CtxStore         = (*Store)(nil)
	_ scs.IterableStore    = (*Store)(nil)
	_ scs.IterableCtxStore = (*Store)(nil)
)

// sessionRow is the database model of one session.
type sessionRow struct {
	bun.BaseModel `bun:"table:sessions"`

	Token  string    `bun:"token,pk"`
	Data   []byte    `bun:"data,notnull"`
	Expiry time.Time `bun:"expiry,notnull"`
}

// Store is an scs.CtxStore and scs.IterableCtxStore over Bun.
type Store struct {
	db  bun.IDB
	clk clock.Clock
}

// NewStore returns a store on db (the manager's single-writer connection).
func NewStore(db bun.IDB, clk clock.Clock) *Store {
	if clk == nil {
		clk = clock.Real()
	}
	return &Store{db: db, clk: clk}
}

func (s *Store) now() time.Time { return s.clk.Now().UTC() }

// FindCtx returns the data of an unexpired session. Unknown and expired
// tokens are not found (found=false, err=nil); err is only for database
// failures, which SCS turns into a 500 instead of an anonymous session.
func (s *Store) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var row sessionRow
	err := s.db.NewSelect().Model(&row).Column("data").
		Where("token = ? AND expiry > ?", token, s.now()).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("sessions: find: %w", err)
	}
	return row.Data, true, nil
}

// CommitCtx inserts or replaces a session.
func (s *Store) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	row := sessionRow{Token: token, Data: b, Expiry: expiry.UTC()}
	_, err := s.db.NewInsert().Model(&row).
		On("CONFLICT (token) DO UPDATE").
		Set("data = EXCLUDED.data").
		Set("expiry = EXCLUDED.expiry").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("sessions: commit: %w", err)
	}
	return nil
}

// DeleteCtx removes a session; unknown tokens are a no-op.
func (s *Store) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.NewDelete().Model((*sessionRow)(nil)).Where("token = ?", token).Exec(ctx); err != nil {
		return fmt.Errorf("sessions: delete: %w", err)
	}
	return nil
}

// AllCtx returns every unexpired session (token → data). SCS's Iterate uses
// it to revoke sessions by content (for example all sessions of a user).
func (s *Store) AllCtx(ctx context.Context) (map[string][]byte, error) {
	var rows []sessionRow
	if err := s.db.NewSelect().Model(&rows).Where("expiry > ?", s.now()).Scan(ctx); err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	out := make(map[string][]byte, len(rows))
	for _, r := range rows {
		out[r.Token] = r.Data
	}
	return out, nil
}

// DeleteExpired removes dead sessions and returns how many it removed. The
// manager's housekeeping calls it periodically.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	res, err := s.db.NewDelete().Model((*sessionRow)(nil)).Where("expiry <= ?", s.now()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("sessions: delete expired: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RevokeWhere deletes every live session for which match reports true and
// returns how many it deleted. match reads the session through sm (e.g.
// sm.GetString(ctx, key)).
//
// It deletes by the stored token directly: with HashTokenInStore, SCS's
// Iterate hands out the stored (already hashed) tokens, so calling
// sm.Destroy inside Iterate would hash them a second time and delete
// nothing (scs v2.9.0).
func RevokeWhere(ctx context.Context, sm *scs.SessionManager, s *Store, match func(ctx context.Context) bool) (int, error) {
	var stored []string
	err := sm.Iterate(ctx, func(sctx context.Context) error {
		if match(sctx) {
			stored = append(stored, sm.Token(sctx))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, token := range stored {
		if err := s.DeleteCtx(ctx, token); err != nil {
			return 0, err
		}
	}
	return len(stored), nil
}

// Find implements scs.Store (SCS prefers FindCtx).
func (s *Store) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

// Commit implements scs.Store (SCS prefers CommitCtx).
func (s *Store) Commit(token string, b []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, b, expiry)
}

// Delete implements scs.Store (SCS prefers DeleteCtx).
func (s *Store) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}

// All implements scs.IterableStore (SCS prefers AllCtx).
func (s *Store) All() (map[string][]byte, error) {
	return s.AllCtx(context.Background())
}
