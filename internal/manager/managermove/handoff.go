package managermove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/requestinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/templates"
)

// Package is a built handoff package: the parts in its directory and
// their manifest.
type Package struct {
	dir      string
	Manifest Manifest
	modTime  time.Time
}

// Size is the total size of the parts (the manifest excluded).
func (p *Package) Size() int64 { return p.Manifest.Size() }

// Write streams the package: the parts, then manifest.json.
func (p *Package) Write(w io.Writer) error { return writePackage(w, p.dir, p.Manifest, p.modTime) }

// Handoff hands the state to the new manager holding code. It is refused
// outside a secure origin (domain.InsecureOriginError) and for unknown,
// wrong, expired or cancelled codes (domain.ErrMoveCodeInvalid). An open
// move starts draining (read-only); while jobs run it answers
// *domain.JobsRunningError. With no job left, agents are refused, the
// state is copied (in the copy the instance's generation goes up by one
// and the move's row becomes arrived) and the move is handed off. A
// handed-off move returns the same package again (a broken transfer is
// retried).
func (s *Service) Handoff(ctx context.Context, code string) (*Package, error) {
	if err := s.checkOrigin(ctx); err != nil {
		return nil, err
	}
	id, err := s.verifyCode(ctx, code)
	if err != nil {
		return nil, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: id})
	info, _ := requestinfo.From(ctx)
	addr := ""
	if info.ClientIP.IsValid() {
		addr = info.ClientIP.String()
	}
	audit.SetDetail(ctx, "handoffAddress", addr)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	s.mu.Lock()
	if err := s.expireDue(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	m, err := store.GetManagerMove(ctx, s.db, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	switch m.State {
	case domain.MoveOpen:
		now := s.now()
		m.State, m.DrainingAt, m.UpdatedAt = domain.MoveDraining, &now, now
		if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveOpen); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.opts.Lock.Set(movelock.ReadOnly)
		s.log.Warn("a new manager asked for the handoff: this manager is read-only while its jobs finish", "move_id", m.ID,
			"handoff_address", addr)
		s.notify()
	case domain.MoveDraining:
	case domain.MoveHandedOff:
		s.mu.Unlock()
		audit.SetDetail(ctx, "state", string(m.State))
		audit.SetDetail(ctx, "repeated", true)
		return s.existingPackage(ctx, m, code)
	case domain.MoveConfirmed:
		s.mu.Unlock()
		return nil, domain.ErrManagerMoveState
	default:
		s.mu.Unlock()
		return nil, domain.ErrMoveCodeInvalid
	}
	n, err := s.activeJobs(ctx)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if n > 0 {
		s.mu.Unlock()
		audit.SetDetail(ctx, "state", string(m.State))
		audit.SetDetail(ctx, "jobCount", n)
		return nil, &domain.JobsRunningError{Count: n, RetryAfter: HandoffRetryAfter}
	}
	s.mu.Unlock()

	// No job is left: agents are refused before the copy is taken, so no
	// agent can change anything the copy should have seen.
	s.opts.Lock.Set(movelock.AgentsRefused)
	pkg, err := s.buildPackage(ctx, m, code)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.syncLock(ctx)
		return nil, fmt.Errorf("copy the manager state: %w", err)
	}
	now := s.now()
	m.State, m.HandedOffAt, m.HandoffAddress, m.UpdatedAt = domain.MoveHandedOff, &now, addr, now
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveDraining); err != nil {
		// The move ended meanwhile (expired or cancelled).
		_ = os.RemoveAll(pkg.dir)
		s.syncLock(ctx)
		if errors.Is(err, domain.ErrManagerMoveState) {
			return nil, domain.ErrMoveCodeInvalid
		}
		return nil, err
	}
	audit.SetDetail(ctx, "state", string(domain.MoveHandedOff))
	audit.SetDetail(ctx, "generation", s.opts.Instance.Generation+1)
	s.log.Warn("the manager state was handed to a new manager: agents are refused from now on", "move_id", m.ID,
		"handoff_address", addr, "bytes", pkg.Size())
	return pkg, nil
}

// existingPackage returns the package of a handed-off move (built again
// from the unchanged state when its directory is gone).
func (s *Service) existingPackage(ctx context.Context, m domain.ManagerMove, code string) (*Package, error) {
	dir := s.outgoingDir(m.ID)
	raw, err := os.ReadFile(filepath.Join(dir, PartManifest)) //nolint:gosec // below the data directory
	if err == nil {
		var man Manifest
		if json.Unmarshal(raw, &man) == nil && man.Format == PackageFormat && partsPresent(dir, man) {
			mod := s.now()
			if st, err := os.Stat(filepath.Join(dir, PartManifest)); err == nil {
				mod = st.ModTime().UTC()
			}
			return &Package{dir: dir, Manifest: man, modTime: mod}, nil
		}
	}
	s.log.Warn("the handoff package is missing; copying the (locked) state again", "move_id", m.ID)
	return s.buildPackage(ctx, m, code)
}

func partsPresent(dir string, m Manifest) bool {
	for _, p := range m.Parts {
		st, err := os.Stat(filepath.Join(dir, p.Name))
		if err != nil || st.Size() != p.Size {
			return false
		}
	}
	return true
}

// buildPackage copies the state into the move's outgoing directory: the
// database (VACUUM INTO, then the generation and the move row changed in
// the copy only), the template drafts, the secret key sealed under the
// code, state.json and the manifest. The directory appears complete or not
// at all.
func (s *Service) buildPackage(ctx context.Context, m domain.ManagerMove, code string) (*Package, error) {
	final := s.outgoingDir(m.ID)
	tmp := final + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	dbPath := filepath.Join(tmp, PartDatabase)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dbPath); err != nil {
		return nil, fmt.Errorf("database snapshot: %w", err)
	}
	now := s.now()
	gen, err := prepareCopy(ctx, dbPath, m.ID, now)
	if err != nil {
		return nil, err
	}
	templatesIncluded := false
	if s.opts.DataDir != "" {
		if err := writeDrafts(filepath.Join(tmp, PartTemplates), templates.DraftsDir(s.opts.DataDir)); err != nil {
			return nil, fmt.Errorf("template drafts: %w", err)
		}
		templatesIncluded = true
	}
	key := s.opts.Keyring.Primary()
	sealed, err := sealSecretKey(key, code, s.opts.Instance.ID)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, PartSealedKey), sealed, 0o600); err != nil {
		return nil, err
	}
	var migrations []string
	if s.opts.Migrations != nil {
		if migrations, err = s.opts.Migrations(ctx); err != nil {
			return nil, err
		}
	}
	info := StateInfo{Format: PackageFormat, Version: PackageVersion, MoveID: m.ID, InstanceID: s.opts.Instance.ID, Generation: gen,
		CreatedAt: now, App: backup.AppInfo{Version: s.opts.Build.Version, Commit: s.opts.Build.Commit},
		Schema: backup.SchemaInfo{Migrations: migrations}, SecretKeyID: key.ID(), TemplatesIncluded: templatesIncluded}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, PartState), b, 0o600); err != nil {
		return nil, err
	}
	man, err := buildManifest(tmp)
	if err != nil {
		return nil, err
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, PartManifest), mb, 0o600); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(final); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	ok = true
	return &Package{dir: final, Manifest: man, modTime: now}, nil
}

// prepareCopy changes the database copy at path (never the live one): the
// instance's generation goes up by one and the move's row becomes
// arrived. It returns the copy's generation. The copy is left in rollback
// journal mode (no -wal file next to it).
func prepareCopy(ctx context.Context, path, moveID string, now time.Time) (int64, error) {
	db, err := store.Open(ctx, path)
	if err != nil {
		return 0, err
	}
	var gen int64
	err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if gen, err = store.BumpInstanceGeneration(ctx, tx); err != nil {
			return err
		}
		m, err := store.GetManagerMove(ctx, tx, moveID)
		if err != nil {
			return err
		}
		from := m.State
		m.State, m.UpdatedAt = domain.MoveArrived, now
		if m.HandedOffAt == nil {
			m.HandedOffAt = &now
		}
		return store.UpdateManagerMove(ctx, tx, &m, from)
	})
	if err == nil {
		_, err = db.ExecContext(ctx, "PRAGMA journal_mode=DELETE")
	}
	return gen, errors.Join(err, db.Close())
}

// writeDrafts writes the template drafts archive.
func writeDrafts(file, templatesDir string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // below the data directory
	if err != nil {
		return err
	}
	werr := templates.WriteDrafts(f, templatesDir)
	return errors.Join(werr, f.Close())
}

// Confirm records that the new manager runs the instance: handed_off →
// confirmed (repeating it is harmless). Refused outside a secure origin
// and for unknown or wrong codes (domain.ErrMoveCodeInvalid); any other
// state answers domain.ErrManagerMoveState (the old manager was resumed,
// the move ended, or the address reaches the new manager itself).
func (s *Service) Confirm(ctx context.Context, code string) (domain.ManagerMove, error) {
	if err := s.checkOrigin(ctx); err != nil {
		return domain.ManagerMove{}, err
	}
	id, err := s.verifyCode(ctx, code)
	if err != nil {
		return domain.ManagerMove{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: id})
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := store.GetManagerMove(ctx, s.db, id)
	if err != nil {
		return domain.ManagerMove{}, err
	}
	switch m.State {
	case domain.MoveConfirmed:
		audit.SetDetail(ctx, "repeated", true)
		return m, nil
	case domain.MoveHandedOff:
	default:
		audit.SetDetail(ctx, "state", string(m.State))
		return domain.ManagerMove{}, domain.ErrManagerMoveState
	}
	now := s.now()
	m.State, m.ConfirmedAt, m.UpdatedAt = domain.MoveConfirmed, &now, now
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveHandedOff); err != nil {
		return domain.ManagerMove{}, err
	}
	_ = os.RemoveAll(s.outgoingDir(m.ID))
	s.log.Warn("the new manager confirmed the move: it runs the instance now; this manager stays read-only", "move_id", m.ID)
	return m, nil
}
