package managermove

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/templates"
)

// Package is a built handoff package: the parts in its directory, their
// manifest, and the key the stream is encrypted with.
type Package struct {
	dir      string
	Manifest Manifest
	modTime  time.Time
	key      []byte
}

// Size is the total size of the parts (the manifest excluded; the
// encrypted stream is a little longer).
func (p *Package) Size() int64 { return p.Manifest.Size() }

// Write streams the package encrypted (crypt.go): the parts, then
// manifest.json.
func (p *Package) Write(w io.Writer) error {
	sw, err := newSealWriter(w, p.key)
	if err != nil {
		return err
	}
	if err := writePackage(sw, p.dir, p.Manifest, p.modTime); err != nil {
		return err
	}
	return sw.Close()
}

// authenticate resolves a move request to its move and code: the
// Authorization header must be signed with the move's code (sealed in the
// database), within AuthWindow, with a new nonce. Unknown moves, ended
// moves (their code is forgotten), wrong signatures and replays answer
// domain.ErrMoveCodeInvalid alike; a correct signature with a time too
// far off answers domain.ErrMoveClockSkew.
func (s *Service) authenticate(ctx context.Context, a MoveAuth) (string, string, error) {
	p, err := parseAuth(a.Header)
	if err != nil {
		return "", "", err
	}
	sealed, err := store.ManagerMoveSealedCode(ctx, s.db, p.moveID)
	if errors.Is(err, domain.ErrManagerMoveNotFound) || (err == nil && sealed == "") {
		return "", "", domain.ErrMoveCodeInvalid
	}
	if err != nil {
		return "", "", err
	}
	code, err := s.opts.Keyring.Open(sealed, SealContext(p.moveID))
	if err != nil {
		return "", "", domain.ErrMoveCodeInvalid
	}
	if err := verifyAuthMAC(p, string(code), a, s.opts.Clock.Now(), s.replay); err != nil {
		return "", "", err
	}
	return p.moveID, string(code), nil
}

// sameCode reports whether code is still move id's code (under mu). New
// setup files replace the code: a request signed with the old one that
// raced the change is refused like any other wrong code.
func (s *Service) sameCode(ctx context.Context, id, code string) bool {
	sealed, err := store.ManagerMoveSealedCode(ctx, s.db, id)
	if err != nil || sealed == "" {
		return false
	}
	cur, err := s.opts.Keyring.Open(sealed, SealContext(id))
	return err == nil && subtle.ConstantTimeCompare(cur, []byte(code)) == 1
}

// recordCheckIn records the waiting manager's request on m (under mu) and
// announces it when the check-in starts counting again or comes from
// another address (a check-in every 10 s changes nothing else the owner
// sees).
func (s *Service) recordCheckIn(ctx context.Context, m *domain.ManagerMove, addr string) error {
	now, from := s.now(), m.State
	news := !s.checkInFresh(m.CheckedInAt) || m.HandoffAddress != addr
	m.CheckedInAt, m.HandoffAddress, m.UpdatedAt = &now, addr, now
	if err := store.UpdateManagerMove(ctx, s.db, m, from); err != nil {
		return err
	}
	if news {
		s.publish(m.ID)
		s.notify() // the Run loop announces when it stops counting
	}
	return nil
}

// clientAddress is the request's client IP ("" unknown).
func clientAddress(ctx context.Context) string {
	info, _ := requestinfo.From(ctx)
	if info.ClientIP.IsValid() {
		return info.ClientIP.String()
	}
	return ""
}

// CheckIn is the old manager's answer to the waiting manager's check-in.
type CheckIn struct {
	State domain.ManagerMoveState
	// StacksMoved of StacksTotal moved (moving), CurrentStack moving now.
	StacksMoved  int
	StacksTotal  int
	CurrentStack string
	// JobsRunning are the jobs a draining move waits for.
	JobsRunning int
}

// CheckIn records the waiting manager's check-in (open to draining) and
// answers the move's state and progress; authenticated like Handoff. The
// waiting manager asks every 10 s and calls the handoff once the move is
// ready (a read: unaudited, unlike the handoff).
func (s *Service) CheckIn(ctx context.Context, a MoveAuth) (CheckIn, error) {
	if a.Method != http.MethodGet {
		return CheckIn{}, domain.ErrMoveCodeInvalid
	}
	id, code, err := s.authenticate(ctx, a)
	if err != nil {
		return CheckIn{}, err
	}
	s.mu.Lock()
	if err := s.expireDue(ctx); err != nil {
		s.mu.Unlock()
		return CheckIn{}, err
	}
	if !s.sameCode(ctx, id, code) {
		s.mu.Unlock()
		return CheckIn{}, domain.ErrMoveCodeInvalid
	}
	m, err := store.GetManagerMove(ctx, s.db, id)
	if err != nil {
		s.mu.Unlock()
		return CheckIn{}, err
	}
	switch m.State {
	case domain.MoveOpen, domain.MoveMoving, domain.MoveReady, domain.MoveDraining:
		if err := s.recordCheckIn(ctx, &m, clientAddress(ctx)); err != nil {
			s.mu.Unlock()
			return CheckIn{}, err
		}
	case domain.MoveHandedOff, domain.MoveConfirmed:
	default:
		s.mu.Unlock()
		return CheckIn{}, domain.ErrMoveCodeInvalid
	}
	s.mu.Unlock()
	c := CheckIn{State: m.State}
	c.StacksMoved, c.StacksTotal, c.CurrentStack = s.migrationProgress(ctx, m.MigrationID)
	if m.State == domain.MoveDraining {
		if c.JobsRunning, err = s.activeJobs(ctx); err != nil {
			return CheckIn{}, err
		}
	}
	return c, nil
}

// Handoff hands the state to the waiting manager that signed a. Refused
// for unknown, wrong, replayed, expired or cancelled moves
// (domain.ErrMoveCodeInvalid) and clocks too far apart
// (domain.ErrMoveClockSkew). Every call records the waiting manager's
// check-in. Until the apps moved (open, moving) it answers
// *domain.MoveNotReadyError with their progress. A ready move starts
// draining (read-only); while jobs run it answers
// *domain.JobsRunningError. With no job left the agents it can place are
// told the new address (manager.redirect), agents are refused, the state
// is copied (in the copy the instance's generation goes up by one and the
// move's row becomes arrived) and the move is handed off. A handed-off
// move returns the same package again (a broken transfer is retried).
func (s *Service) Handoff(ctx context.Context, a MoveAuth) (*Package, error) {
	id, code, err := s.authenticate(ctx, a)
	if err != nil {
		return nil, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: id})
	addr := clientAddress(ctx)
	audit.SetDetail(ctx, "handoffAddress", addr)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	s.mu.Lock()
	if err := s.expireDue(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !s.sameCode(ctx, id, code) {
		s.mu.Unlock()
		return nil, domain.ErrMoveCodeInvalid
	}
	m, err := store.GetManagerMove(ctx, s.db, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	switch m.State {
	case domain.MoveOpen, domain.MoveMoving, domain.MoveReady, domain.MoveDraining:
		// The waiting manager checked in.
		if err := s.recordCheckIn(ctx, &m, addr); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	now := s.now()
	switch m.State {
	case domain.MoveOpen, domain.MoveMoving:
		s.mu.Unlock()
		moved, total, current := s.migrationProgress(ctx, m.MigrationID)
		audit.SetDetail(ctx, "state", string(m.State))
		return nil, &domain.MoveNotReadyError{State: m.State, StacksMoved: moved, StacksTotal: total, CurrentStack: current,
			RetryAfter: HandoffRetryAfter}
	case domain.MoveReady:
		m.State, m.DrainingAt, m.UpdatedAt = domain.MoveDraining, &now, now
		if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveReady); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.opts.Lock.Set(movelock.ReadOnly)
		s.log.Warn("the new manager asked for the handoff: this manager is read-only while its jobs finish", "move_id", m.ID,
			"handoff_address", addr)
		s.published(m, domain.MoveReady)
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

	// No job is left. The agents this manager can place hear the new
	// manager's address first (once), then agents are refused before the
	// copy is taken, so no agent changes anything the copy misses.
	if m.Redirects == nil {
		m.Redirects = s.sendRedirects(ctx, m)
		s.mu.Lock()
		m.UpdatedAt = s.now()
		err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveDraining)
		s.mu.Unlock()
		if err != nil {
			if errors.Is(err, domain.ErrManagerMoveState) {
				return nil, domain.ErrMoveCodeInvalid
			}
			return nil, err
		}
		audit.SetDetail(ctx, "redirectCount", countSent(m.Redirects))
		s.publish(m.ID)
	}
	s.opts.Lock.Set(movelock.AgentsRefused)
	pkg, err := s.buildPackage(ctx, m, code)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.syncLock(ctx)
		return nil, fmt.Errorf("copy the manager state: %w", err)
	}
	now = s.now()
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
	s.published(m, domain.MoveDraining)
	audit.SetDetail(ctx, "state", string(domain.MoveHandedOff))
	audit.SetDetail(ctx, "generation", s.opts.Instance.Generation+1)
	s.log.Warn("the manager state was handed to a new manager: agents are refused from now on", "move_id", m.ID,
		"handoff_address", addr, "bytes", pkg.Size())
	return pkg, nil
}

func countSent(rs []domain.ManagerMoveRedirect) int {
	n := 0
	for _, r := range rs {
		if r.Sent {
			n++
		}
	}
	return n
}

// existingPackage returns the package of a handed-off move (built again
// from the unchanged state when its directory is gone).
func (s *Service) existingPackage(ctx context.Context, m domain.ManagerMove, code string) (*Package, error) {
	key, err := packageKey(code, m.ID)
	if err != nil {
		return nil, err
	}
	dir := s.outgoingDir(m.ID)
	raw, err := os.ReadFile(filepath.Join(dir, PartManifest)) //nolint:gosec // below the data directory
	if err == nil {
		var man Manifest
		if json.Unmarshal(raw, &man) == nil && man.Format == PackageFormat && partsPresent(dir, man) {
			mod := s.now()
			if st, err := os.Stat(filepath.Join(dir, PartManifest)); err == nil {
				mod = st.ModTime().UTC()
			}
			return &Package{dir: dir, Manifest: man, modTime: mod, key: key}, nil
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
	key, err := packageKey(code, m.ID)
	if err != nil {
		return nil, err
	}
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
	sk := s.opts.Keyring.Primary()
	sealed, err := sealSecretKey(sk, code, s.opts.Instance.ID)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, PartSealedKey), sealed, 0o600); err != nil {
		return nil, err
	}
	var applied []string
	if s.opts.SchemaMigrations != nil {
		if applied, err = s.opts.SchemaMigrations(ctx); err != nil {
			return nil, err
		}
	}
	info := StateInfo{Format: PackageFormat, Version: PackageVersion, MoveID: m.ID, InstanceID: s.opts.Instance.ID, Generation: gen,
		CreatedAt: now, App: backup.AppInfo{Version: s.opts.Build.Version, Commit: s.opts.Build.Commit},
		Schema: backup.SchemaInfo{Migrations: applied}, SecretKeyID: sk.ID(), TemplatesIncluded: templatesIncluded}
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
	return &Package{dir: final, Manifest: man, modTime: now, key: key}, nil
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
// confirmed (repeating it is harmless). Authenticated like Handoff; any
// other state answers domain.ErrManagerMoveState (the old manager was
// resumed, the move ended, or the address reaches the new manager itself).
func (s *Service) Confirm(ctx context.Context, a MoveAuth) (domain.ManagerMove, error) {
	id, _, err := s.authenticate(ctx, a)
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
	s.published(m, domain.MoveHandedOff)
	s.log.Warn("the new manager confirmed the move: it runs the instance now; this manager stays read-only", "move_id", m.ID)
	return m, nil
}
