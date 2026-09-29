package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Manager moves (docs/internal/architecture/manager-move.md). The move
// code is never stored: code_verifier is the SHA-256 of its secret part;
// sealed_code holds the code sealed with the secret key on the new
// manager until the old one confirmed.

type managerMoveRow struct {
	bun.BaseModel `bun:"table:manager_moves"`

	ID              string     `bun:"id,pk"`
	State           string     `bun:"state,notnull"`
	CodeVerifier    string     `bun:"code_verifier,notnull"`
	CreatedBy       string     `bun:"created_by,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	ExpiresAt       time.Time  `bun:"expires_at,notnull"`
	DrainingAt      *time.Time `bun:"draining_at"`
	HandedOffAt     *time.Time `bun:"handed_off_at"`
	ConfirmedAt     *time.Time `bun:"confirmed_at"`
	EndedAt         *time.Time `bun:"ended_at"`
	HandoffAddress  string     `bun:"handoff_address,notnull"`
	SourceURL       string     `bun:"source_url,notnull"`
	ArrivedAt       *time.Time `bun:"arrived_at"`
	SealedCode      string     `bun:"sealed_code,notnull"`
	ConfirmAttempts int        `bun:"confirm_attempts,notnull"`
	ConfirmError    string     `bun:"confirm_error,notnull"`
	LastConfirmAt   *time.Time `bun:"last_confirm_at"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
}

func (r managerMoveRow) toDomain() domain.ManagerMove {
	return domain.ManagerMove{ID: r.ID, State: domain.ManagerMoveState(r.State), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(), DrainingAt: utcPtr(r.DrainingAt), HandedOffAt: utcPtr(r.HandedOffAt), ConfirmedAt: utcPtr(r.ConfirmedAt),
		EndedAt: utcPtr(r.EndedAt), HandoffAddress: r.HandoffAddress, SourceURL: r.SourceURL, ArrivedAt: utcPtr(r.ArrivedAt),
		ConfirmAttempts: r.ConfirmAttempts, ConfirmError: r.ConfirmError, LastConfirmAt: utcPtr(r.LastConfirmAt), UpdatedAt: r.UpdatedAt.UTC()}
}

// managerMoveColumns are the columns UpdateManagerMove writes (never the
// verifier or the sealed code).
var managerMoveColumns = []string{"state", "draining_at", "handed_off_at", "confirmed_at", "ended_at", "handoff_address", "source_url",
	"arrived_at", "confirm_attempts", "confirm_error", "last_confirm_at", "updated_at"}

func fromManagerMove(m *domain.ManagerMove) managerMoveRow {
	return managerMoveRow{ID: m.ID, State: string(m.State), CreatedBy: m.CreatedBy, CreatedAt: m.CreatedAt.UTC(), ExpiresAt: m.ExpiresAt.UTC(),
		DrainingAt: utcPtr(m.DrainingAt), HandedOffAt: utcPtr(m.HandedOffAt), ConfirmedAt: utcPtr(m.ConfirmedAt), EndedAt: utcPtr(m.EndedAt),
		HandoffAddress: m.HandoffAddress, SourceURL: m.SourceURL, ArrivedAt: utcPtr(m.ArrivedAt), ConfirmAttempts: m.ConfirmAttempts,
		ConfirmError: m.ConfirmError, LastConfirmAt: utcPtr(m.LastConfirmAt), UpdatedAt: m.UpdatedAt.UTC()}
}

// InsertManagerMove stores a new move with its code verifier.
func InsertManagerMove(ctx context.Context, db bun.IDB, m *domain.ManagerMove, verifier string) error {
	row := fromManagerMove(m)
	row.CodeVerifier = verifier
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert manager move: %w", err)
	}
	return nil
}

// GetManagerMove returns a move (domain.ErrManagerMoveNotFound).
func GetManagerMove(ctx context.Context, db bun.IDB, id string) (domain.ManagerMove, error) {
	row, err := getManagerMoveRow(ctx, db, id)
	if err != nil {
		return domain.ManagerMove{}, err
	}
	return row.toDomain(), nil
}

func getManagerMoveRow(ctx context.Context, db bun.IDB, id string) (managerMoveRow, error) {
	var row managerMoveRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return row, domain.ErrManagerMoveNotFound
	}
	if err != nil {
		return row, fmt.Errorf("store: read manager move: %w", err)
	}
	return row, nil
}

// ManagerMoveVerifier returns the code verifier of a move
// (domain.ErrManagerMoveNotFound).
func ManagerMoveVerifier(ctx context.Context, db bun.IDB, id string) (string, error) {
	row, err := getManagerMoveRow(ctx, db, id)
	return row.CodeVerifier, err
}

// ActiveManagerMove returns the move that is open or in progress (open,
// draining, handed_off, confirmed); found is false when there is none.
func ActiveManagerMove(ctx context.Context, db bun.IDB) (domain.ManagerMove, bool, error) {
	var row managerMoveRow
	err := db.NewSelect().Model(&row).Where("state IN (?)", bun.List(activeMoveStates())).
		OrderExpr("created_at DESC, id DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagerMove{}, false, nil
	}
	if err != nil {
		return domain.ManagerMove{}, false, fmt.Errorf("store: read the active manager move: %w", err)
	}
	return row.toDomain(), true, nil
}

// LatestArrivedManagerMove returns the newest move this manager arrived
// by (the copy it runs); found is false when there is none.
func LatestArrivedManagerMove(ctx context.Context, db bun.IDB) (domain.ManagerMove, bool, error) {
	var row managerMoveRow
	err := db.NewSelect().Model(&row).Where("state = ?", string(domain.MoveArrived)).
		OrderExpr("created_at DESC, id DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagerMove{}, false, nil
	}
	if err != nil {
		return domain.ManagerMove{}, false, fmt.Errorf("store: read the arrived manager move: %w", err)
	}
	return row.toDomain(), true, nil
}

// UpdateManagerMove writes m's state, times and confirmation fields when
// the stored move is still in state from (domain.ErrManagerMoveState
// otherwise, domain.ErrManagerMoveNotFound when it does not exist).
func UpdateManagerMove(ctx context.Context, db bun.IDB, m *domain.ManagerMove, from domain.ManagerMoveState) error {
	row := fromManagerMove(m)
	res, err := db.NewUpdate().Model(&row).Column(managerMoveColumns...).Where("id = ? AND state = ?", m.ID, string(from)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update manager move: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := getManagerMoveRow(ctx, db, m.ID); err != nil {
		return err
	}
	return domain.ErrManagerMoveState
}

// SetManagerMoveSealedCode stores (or with "" clears) the sealed move code
// of an arrived move.
func SetManagerMoveSealedCode(ctx context.Context, db bun.IDB, id, sealed string) error {
	res, err := db.NewUpdate().Model((*managerMoveRow)(nil)).Set("sealed_code = ?", sealed).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: seal the manager move code: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrManagerMoveNotFound
	}
	return nil
}

// ManagerMoveSealedCode returns the sealed move code of a move ("" none).
func ManagerMoveSealedCode(ctx context.Context, db bun.IDB, id string) (string, error) {
	row, err := getManagerMoveRow(ctx, db, id)
	return row.SealedCode, err
}

func activeMoveStates() []string {
	var out []string
	for _, s := range domain.ActiveMoveStates() {
		out = append(out, string(s))
	}
	return out
}
