// Package settings owns the editable instance settings (#4,
// GET/PATCH /api/v1/settings): the display name of this DockYard. The
// sign-in policy (#16), schedule defaults (#13) and maintenance defaults
// (#14) are separate resources owned by their packages; deployment
// configuration comes from environment variables and is read-only.
package settings

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// MaxNameLength bounds the display name (characters).
const MaxNameLength = 64

// ErrInvalidName is returned for an empty, too long or control-character
// display name.
var ErrInvalidName = errors.New("the name must be 1-64 characters without control characters")

// Service reads and changes the instance settings.
type Service struct {
	db    *bun.DB
	clock clock.Clock
}

// New returns the settings service; clk nil uses the wall clock.
func New(db *bun.DB, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{db: db, clock: clk}
}

// Get returns the current settings.
func (s *Service) Get(ctx context.Context) (domain.InstanceSettings, error) {
	return store.GetInstanceSettings(ctx, s.db)
}

// NormalizeName trims surrounding white space and validates a display name.
func NormalizeName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", ErrInvalidName
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLength {
		return "", ErrInvalidName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrInvalidName
		}
	}
	return name, nil
}

// Update applies p if the settings are still at revision
// (domain.ErrRevisionConflict otherwise; ErrInvalidName for a bad name).
func (s *Service) Update(ctx context.Context, revision int64, p domain.InstanceSettingsPatch) (before, after domain.InstanceSettings, err error) {
	if p.Name != nil {
		name, err := NormalizeName(*p.Name)
		if err != nil {
			return before, after, err
		}
		p.Name = &name
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if before, err = store.GetInstanceSettings(ctx, tx); err != nil {
			return err
		}
		if before.Revision != revision {
			return domain.ErrRevisionConflict
		}
		if err := store.UpdateInstanceSettings(ctx, tx, revision, p, s.clock.Now()); err != nil {
			return err
		}
		after, err = store.GetInstanceSettings(ctx, tx)
		return err
	})
	return before, after, err
}
