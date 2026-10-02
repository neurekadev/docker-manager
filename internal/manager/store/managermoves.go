package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Manager moves (docs/internal/architecture/manager-move.md). The move
// code is never stored in clear: sealed_code holds it sealed with the
// secret key (old manager: authentication and package encryption; new
// manager: until the old one confirmed).

type managerMoveRow struct {
	bun.BaseModel `bun:"table:manager_moves"`

	ID                    string     `bun:"id,pk"`
	State                 string     `bun:"state,notnull"`
	CreatedBy             string     `bun:"created_by,notnull"`
	CreatedAt             time.Time  `bun:"created_at,notnull"`
	ExpiresAt             time.Time  `bun:"expires_at,notnull"`
	ThisServerAddress     string     `bun:"this_server_address,notnull"`
	NewServerAddress      string     `bun:"new_server_address,notnull"`
	EnrollmentID          string     `bun:"enrollment_id,notnull"`
	SourceEnvironmentID   string     `bun:"source_environment_id,notnull"`
	TargetEnvironmentID   string     `bun:"target_environment_id,notnull"`
	CheckedInAt           *time.Time `bun:"checked_in_at"`
	MoveJobID             string     `bun:"move_job_id,notnull"`
	MigrationID           string     `bun:"migration_id,notnull"`
	ReadyAt               *time.Time `bun:"ready_at"`
	DrainingAt            *time.Time `bun:"draining_at"`
	HandedOffAt           *time.Time `bun:"handed_off_at"`
	ConfirmedAt           *time.Time `bun:"confirmed_at"`
	EndedAt               *time.Time `bun:"ended_at"`
	HandoffAddress        string     `bun:"handoff_address,notnull"`
	Redirects             string     `bun:"redirects,notnull"`
	SourceURL             string     `bun:"source_url,notnull"`
	ArrivedAt             *time.Time `bun:"arrived_at"`
	SealedCode            string     `bun:"sealed_code,notnull"`
	ConfirmAttempts       int        `bun:"confirm_attempts,notnull"`
	ConfirmError          string     `bun:"confirm_error,notnull"`
	LastConfirmAt         *time.Time `bun:"last_confirm_at"`
	ConfirmAcknowledgedAt *time.Time `bun:"confirm_acknowledged_at"`
	UpdatedAt             time.Time  `bun:"updated_at,notnull"`
}

// redirectJSON is one entry of the redirects column.
type redirectJSON struct {
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	Role            string `json:"role"`
	URL             string `json:"url"`
	Sent            bool   `json:"sent"`
	ErrorClass      string `json:"errorClass,omitempty"`
	// Set by the new manager after the move (secure.go in managermove).
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
	RotatedAt  *time.Time `json:"rotatedAt,omitempty"`
}

func encodeRedirects(rs []domain.ManagerMoveRedirect) string {
	out := make([]redirectJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, redirectJSON{EnvironmentID: r.EnvironmentID, EnvironmentName: r.EnvironmentName, Role: r.Role, URL: r.URL,
			Sent: r.Sent, ErrorClass: r.ErrorClass, ReturnedAt: utcPtr(r.ReturnedAt), RotatedAt: utcPtr(r.RotatedAt)})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func decodeRedirects(raw string) []domain.ManagerMoveRedirect {
	var in []redirectJSON
	if raw == "" || json.Unmarshal([]byte(raw), &in) != nil || len(in) == 0 {
		return nil
	}
	out := make([]domain.ManagerMoveRedirect, 0, len(in))
	for _, r := range in {
		out = append(out, domain.ManagerMoveRedirect{EnvironmentID: r.EnvironmentID, EnvironmentName: r.EnvironmentName, Role: r.Role,
			URL: r.URL, Sent: r.Sent, ErrorClass: r.ErrorClass, ReturnedAt: utcPtr(r.ReturnedAt), RotatedAt: utcPtr(r.RotatedAt)})
	}
	return out
}

func (r managerMoveRow) toDomain() domain.ManagerMove {
	return domain.ManagerMove{ID: r.ID, State: domain.ManagerMoveState(r.State), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(), ThisServerAddress: r.ThisServerAddress, NewServerAddress: r.NewServerAddress, EnrollmentID: r.EnrollmentID,
		SourceEnvironmentID: r.SourceEnvironmentID, TargetEnvironmentID: r.TargetEnvironmentID, CheckedInAt: utcPtr(r.CheckedInAt),
		MoveJobID: r.MoveJobID, MigrationID: r.MigrationID, ReadyAt: utcPtr(r.ReadyAt), DrainingAt: utcPtr(r.DrainingAt),
		HandedOffAt: utcPtr(r.HandedOffAt), ConfirmedAt: utcPtr(r.ConfirmedAt), EndedAt: utcPtr(r.EndedAt), HandoffAddress: r.HandoffAddress,
		Redirects: decodeRedirects(r.Redirects), SourceURL: r.SourceURL, ArrivedAt: utcPtr(r.ArrivedAt), ConfirmAttempts: r.ConfirmAttempts,
		ConfirmError: r.ConfirmError, LastConfirmAt: utcPtr(r.LastConfirmAt), ConfirmAcknowledgedAt: utcPtr(r.ConfirmAcknowledgedAt),
		UpdatedAt: r.UpdatedAt.UTC()}
}

// managerMoveColumns are the columns UpdateManagerMove writes (never the
// sealed code).
var managerMoveColumns = []string{"state", "this_server_address", "new_server_address", "enrollment_id", "source_environment_id",
	"target_environment_id", "checked_in_at", "move_job_id", "migration_id", "ready_at", "draining_at", "handed_off_at", "confirmed_at",
	"ended_at", "handoff_address", "redirects", "source_url", "arrived_at", "confirm_attempts", "confirm_error", "last_confirm_at",
	"confirm_acknowledged_at", "updated_at"}

func fromManagerMove(m *domain.ManagerMove) managerMoveRow {
	return managerMoveRow{ID: m.ID, State: string(m.State), CreatedBy: m.CreatedBy, CreatedAt: m.CreatedAt.UTC(), ExpiresAt: m.ExpiresAt.UTC(),
		ThisServerAddress: m.ThisServerAddress, NewServerAddress: m.NewServerAddress, EnrollmentID: m.EnrollmentID,
		SourceEnvironmentID: m.SourceEnvironmentID, TargetEnvironmentID: m.TargetEnvironmentID, CheckedInAt: utcPtr(m.CheckedInAt),
		MoveJobID: m.MoveJobID, MigrationID: m.MigrationID, ReadyAt: utcPtr(m.ReadyAt), DrainingAt: utcPtr(m.DrainingAt),
		HandedOffAt: utcPtr(m.HandedOffAt), ConfirmedAt: utcPtr(m.ConfirmedAt), EndedAt: utcPtr(m.EndedAt), HandoffAddress: m.HandoffAddress,
		Redirects: encodeRedirects(m.Redirects), SourceURL: m.SourceURL, ArrivedAt: utcPtr(m.ArrivedAt), ConfirmAttempts: m.ConfirmAttempts,
		ConfirmError: m.ConfirmError, LastConfirmAt: utcPtr(m.LastConfirmAt), ConfirmAcknowledgedAt: utcPtr(m.ConfirmAcknowledgedAt),
		UpdatedAt: m.UpdatedAt.UTC()}
}

// InsertManagerMove stores a new move with its sealed code.
func InsertManagerMove(ctx context.Context, db bun.IDB, m *domain.ManagerMove, sealedCode string) error {
	row := fromManagerMove(m)
	row.SealedCode = sealedCode
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

// ActiveManagerMove returns the move that is open or in progress (open,
// moving, ready, draining, handed_off, confirmed); found is false when
// there is none.
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

// UpdateManagerMove writes m's state, times and fields when the stored
// move is still in state from (domain.ErrManagerMoveState otherwise,
// domain.ErrManagerMoveNotFound when it does not exist).
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
// of a move.
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

// ManagerMoveSealedCode returns the sealed move code of a move ("" none;
// domain.ErrManagerMoveNotFound).
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
