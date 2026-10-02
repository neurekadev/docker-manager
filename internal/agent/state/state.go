// Package state manages the agent's persistent identity files in its state
// directory (DOCKER_AGENT_STATE_DIR, a named volume):
//
//	install-id              generated once; with the Engine ID it identifies
//	                        this installation (Engine IDs collide on clones)
//	credential.json         agent ID, environment ID and the bearer
//	                        credential (0600)
//	enrollment-token        a token handed over by `docker-agent enroll`
//	                        (0600, deleted once used)
//	enrollment-status.json  the outcome of the last enrollment attempt, read
//	                        by `docker-agent enroll` to report the result
//	enrollment-used.json    SHA-256 of tokens already used, so a token left
//	                        in DOCKER_AGENT_ENROLLMENT_TOKEN is not retried
//	manager.json            the highest manager generation seen (welcome,
//	                        manager.identity, manager.redirect; a manager
//	                        with a lower one is refused) and the address a
//	                        moving manager sent in manager.redirect, dialed
//	                        instead of DOCKER_AGENT_MANAGER_URL
//	                        (docs/internal/architecture/manager-move.md)
//
// Every write is atomic: temporary file (0600), fsync, rename, directory
// fsync. The credential is written before the agent opens a session with it.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// File names inside the state directory.
const (
	InstallIDFile        = "install-id"
	CredentialFile       = "credential.json" //nolint:gosec // G101: a file name, not a credential
	TokenFile            = "enrollment-token"
	EnrollmentStatusFile = "enrollment-status.json"
	UsedTokensFile       = "enrollment-used.json"
	ManagerFile          = "manager.json"
)

// maxUsedTokens bounds enrollment-used.json.
const maxUsedTokens = 32

// Credential is the agent's enrolled identity.
type Credential struct {
	AgentID         string    `json:"agentId"`
	EnvironmentID   string    `json:"environmentId"`
	EnvironmentName string    `json:"environmentName,omitempty"`
	Credential      string    `json:"credential"`
	ManagerURL      string    `json:"managerUrl"`
	EnrolledAt      time.Time `json:"enrolledAt"`
}

// Enrollment status values.
const (
	EnrollPending  = "pending"
	EnrollEnrolled = "enrolled"
	EnrollFailed   = "failed"
)

// EnrollStatus is the outcome of the last enrollment attempt.
type EnrollStatus struct {
	// TokenID identifies the token (TokenID of its value, never the value).
	TokenID       string    `json:"tokenId"`
	Status        string    `json:"status"`
	Message       string    `json:"message,omitempty"`
	Code          string    `json:"code,omitempty"`
	AgentID       string    `json:"agentId,omitempty"`
	EnvironmentID string    `json:"environmentId,omitempty"`
	At            time.Time `json:"at"`
}

// Store reads and writes the state files. It is safe for concurrent use
// within one process; `docker-agent enroll` only writes TokenFile.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open ensures dir exists (0700) and returns a Store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// InstallID returns the install ID, generating and persisting it on first
// use.
func (s *Store) InstallID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(InstallIDFile))
	switch {
	case err == nil:
		id := strings.TrimSpace(string(b))
		if _, perr := uuid.Parse(id); perr != nil {
			return "", fmt.Errorf("state: %s is corrupt; delete it only if this agent is enrolled again", InstallIDFile)
		}
		return id, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("state: read install ID: %w", err)
	}
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	id := u.String()
	if err := writeAtomic(s.dir, InstallIDFile, []byte(id+"\n")); err != nil {
		return "", err
	}
	return id, nil
}

// Credential returns the stored credential, or nil when not enrolled.
func (s *Store) Credential() (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Credential
	ok, err := readJSON(s.path(CredentialFile), &c)
	if err != nil || !ok {
		return nil, err
	}
	if c.AgentID == "" || !strings.HasPrefix(c.Credential, protocol.CredentialPrefix) {
		return nil, fmt.Errorf("state: %s is corrupt", CredentialFile)
	}
	return &c, nil
}

// SaveCredential persists c atomically (0600).
func (s *Store) SaveCredential(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.dir, CredentialFile, c)
}

// ReplaceCredential swaps the bearer credential (rotation) atomically.
func (s *Store) ReplaceCredential(credential string) error {
	if !strings.HasPrefix(credential, protocol.CredentialPrefix) {
		return errors.New("state: not an agent credential")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Credential
	ok, err := readJSON(s.path(CredentialFile), &c)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("state: not enrolled")
	}
	c.Credential = credential
	return writeJSON(s.dir, CredentialFile, c)
}

// ClearCredential removes the stored credential (it was revoked).
func (s *Store) ClearCredential() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(CredentialFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(s.dir)
}

// SubmitToken hands an enrollment token to the running agent (used by
// `docker-agent enroll`).
func (s *Store) SubmitToken(token string) error {
	token = strings.TrimSpace(token)
	if err := ValidateToken(token); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.dir, TokenFile, []byte(token+"\n"))
}

// ValidateToken checks the shape of an enrollment token (not its validity).
func ValidateToken(token string) error {
	if !strings.HasPrefix(token, protocol.EnrollmentTokenPrefix) || len(token) > 256 || strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("not an enrollment token (they start with %s)", protocol.EnrollmentTokenPrefix)
	}
	return nil
}

// PendingToken returns a handed-over token ("" when none).
func (s *Store) PendingToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(TokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ClearPendingToken deletes the handed-over token if it is still token.
func (s *Store) ClearPendingToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(TokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != token {
		return nil // a newer token was handed over meanwhile
	}
	if err := os.Remove(s.path(TokenFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(s.dir)
}

// TokenID returns a stable, non-secret identifier of a token value (the
// first 16 hex digits of its SHA-256).
func TokenID(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:8])
}

type usedTokens struct {
	Used []string `json:"used"`
}

// TokenUsed reports whether token was already used for an enrollment
// attempt that must not be repeated.
func (s *Store) TokenUsed(token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u usedTokens
	if _, err := readJSON(s.path(UsedTokensFile), &u); err != nil {
		return false, err
	}
	return slices.Contains(u.Used, fullHash(token)), nil
}

// MarkTokenUsed records token as used.
func (s *Store) MarkTokenUsed(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u usedTokens
	if _, err := readJSON(s.path(UsedTokensFile), &u); err != nil {
		return err
	}
	h := fullHash(token)
	if slices.Contains(u.Used, h) {
		return nil
	}
	u.Used = append(u.Used, h)
	if len(u.Used) > maxUsedTokens {
		u.Used = u.Used[len(u.Used)-maxUsedTokens:]
	}
	return writeJSON(s.dir, UsedTokensFile, u)
}

func fullHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// WriteEnrollStatus records an enrollment outcome.
func (s *Store) WriteEnrollStatus(st EnrollStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.dir, EnrollmentStatusFile, st)
}

// EnrollStatus returns the last enrollment outcome (nil when none).
func (s *Store) EnrollStatus() (*EnrollStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st EnrollStatus
	ok, err := readJSON(s.path(EnrollmentStatusFile), &st)
	if err != nil || !ok {
		return nil, err
	}
	return &st, nil
}

// ManagerState is manager.json: the highest manager generation this agent
// has seen and the manager address a move gave it.
type ManagerState struct {
	Generation int64 `json:"generation"`
	// Redirect is the address received in manager.redirect (nil: none;
	// the agent dials DOCKER_AGENT_MANAGER_URL).
	Redirect *ManagerRedirect `json:"redirect,omitempty"`
}

// ManagerRedirect is the manager address received in manager.redirect.
// It replaces DOCKER_AGENT_MANAGER_URL until that variable is changed.
type ManagerRedirect struct {
	// URL is the origin to dial (http or https, validated by the runtime).
	URL string `json:"url"`
	// Replaces is the DOCKER_AGENT_MANAGER_URL origin configured when the
	// redirect arrived. A different configured origin at startup means the
	// operator changed the variable since: the redirect is then dropped.
	Replaces string `json:"replaces"`
	// At is when the redirect arrived.
	At time.Time `json:"at"`
}

// ErrGenerationNotNewer refuses a manager.redirect whose generation is not
// higher than the highest recorded one.
var ErrGenerationNotNewer = errors.New("state: the manager generation is not newer than the one this agent follows")

// ManagerState returns manager.json: the zero state when none was recorded
// yet, and the zero state with an error when it is unreadable or corrupt
// (the caller warns and treats it as empty).
func (s *Store) ManagerState() (ManagerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readManager()
}

func (s *Store) readManager() (ManagerState, error) {
	var m ManagerState
	ok, err := readJSON(s.path(ManagerFile), &m)
	if err != nil || !ok {
		return ManagerState{}, err
	}
	if m.Generation < 0 {
		return ManagerState{}, fmt.Errorf("state: %s is corrupt: negative generation", ManagerFile)
	}
	if m.Redirect != nil && (m.Redirect.URL == "" || m.Redirect.Replaces == "") {
		return ManagerState{}, fmt.Errorf("state: %s is corrupt: incomplete redirect", ManagerFile)
	}
	return m, nil
}

// ManagerGeneration returns the highest manager generation this agent has
// seen: 0 when none was recorded yet, and 0 with an error when manager.json
// is unreadable or corrupt (the caller warns and treats it as 0).
func (s *Store) ManagerGeneration() (int64, error) {
	m, err := s.ManagerState()
	return m.Generation, err
}

// SaveManagerGeneration records the highest manager generation seen
// (atomically, 0600). It never lowers the record (a concurrent
// manager.redirect may have raised it meanwhile) and keeps the redirect;
// an unreadable or corrupt record is replaced.
func (s *Store) SaveManagerGeneration(generation int64) error {
	if generation < 0 {
		return errors.New("state: negative manager generation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManager()
	if err == nil && generation <= m.Generation {
		return nil
	}
	m.Generation = generation
	return writeJSON(s.dir, ManagerFile, m)
}

// SaveManagerRedirect records a manager.redirect: the new address and the
// new manager's generation, in one atomic write (0600). generation must be
// higher than the recorded one (else ErrGenerationNotNewer, nothing
// written); the same redirect again (equal generation and URL) is a no-op.
// An unreadable or corrupt record counts as empty and is replaced.
func (s *Store) SaveManagerRedirect(generation int64, r ManagerRedirect) error {
	if generation < 1 || r.URL == "" || r.Replaces == "" {
		return errors.New("state: invalid manager redirect")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _ := s.readManager()
	if generation <= m.Generation {
		if generation == m.Generation && m.Redirect != nil && m.Redirect.URL == r.URL {
			return nil
		}
		return fmt.Errorf("%w (generation %d; this agent follows %d)", ErrGenerationNotNewer, generation, m.Generation)
	}
	m.Generation, m.Redirect = generation, &r
	return writeJSON(s.dir, ManagerFile, m)
}

// ReplaceManagerRedirect records a manager.redirect at the generation the
// agent already follows: the manager it follows gives it another address
// of its own (after a move, its HTTPS public address instead of the
// plain-HTTP one the move gave). r nil forgets the redirect (the agent
// dials DOCKER_AGENT_MANAGER_URL again). generation must equal the
// recorded one, else ErrGenerationNotNewer and nothing is written; an
// unreadable or corrupt record is an error (the caller decides nothing
// from it). Which addresses qualify is the runtime's rule.
func (s *Store) ReplaceManagerRedirect(generation int64, r *ManagerRedirect) error {
	if r != nil && (r.URL == "" || r.Replaces == "") {
		return errors.New("state: invalid manager redirect")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManager()
	if err != nil {
		return err
	}
	if generation < 1 || generation != m.Generation {
		return fmt.Errorf("%w (generation %d; this agent follows %d)", ErrGenerationNotNewer, generation, m.Generation)
	}
	m.Redirect = r
	return writeJSON(s.dir, ManagerFile, m)
}

// ClearManagerRedirect forgets the redirect (the agent dials
// DOCKER_AGENT_MANAGER_URL again) and keeps the generation. A corrupt
// record is left for the next generation write to replace.
func (s *Store) ClearManagerRedirect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManager()
	if err != nil {
		return err
	}
	if m.Redirect == nil {
		return nil
	}
	m.Redirect = nil
	return writeJSON(s.dir, ManagerFile, m)
}

func readJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("state: read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("state: %s is corrupt: %w", filepath.Base(path), err)
	}
	return true, nil
}

func writeJSON(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(dir, name, b)
}

// writeAtomic replaces dir/name with b: temp file (0600), fsync, rename,
// directory fsync.
func writeAtomic(dir, name string, b []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("state: write %s: %w", name, err)
	}
	_, werr := tmp.Write(b)
	cherr := tmp.Chmod(0o600)
	if runtime.GOOS == "windows" {
		cherr = nil // development platform; CreateTemp already restricts access
	}
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, cherr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("state: write %s: %w", name, err)
	}
	// A concurrent reader (`docker-agent enroll` polling the status) can
	// make the rename fail on some platforms for a moment: retry briefly.
	var rerr error
	for range 5 {
		if rerr = os.Rename(tmp.Name(), filepath.Join(dir, name)); rerr == nil {
			return syncDir(dir)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = os.Remove(tmp.Name())
	return fmt.Errorf("state: write %s: %w", name, rerr)
}

// syncDir makes a rename durable. Windows cannot fsync directories (the
// agent runs on Linux; Windows is a development platform).
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("state: sync %s: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("state: sync %s: %w", dir, err)
	}
	return nil
}
