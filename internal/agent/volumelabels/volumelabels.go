// Package volumelabels keeps the labels a stack's Compose file declares on
// its volumes that the volumes do not carry. Docker never changes the
// labels of an existing volume, and a deploy never recreates a volume (it
// holds data), so a label added to compose.yaml after the volume was
// created never reaches it. Every deploy records, per volume, Docker
// Manager's own labels (protocol.UserLabels) whose declared value the
// volume lacks; backups and maintenance honor them as if the volume carried
// them (Effective), and the volume's details show them as Compose labels.
//
// Entries are tied to the volume's creation time: a volume created again
// (removed, or recreated by Compose with the declared labels) no longer
// matches its old entry. The store is a JSON file in the agent's state
// directory, replaced atomically; a nil *Store keeps nothing.
package volumelabels

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// FileName is the store's file in the state directory.
const FileName = "volume-labels.json"

// Store is the recorded Compose labels of the agent's volumes.
type Store struct {
	dir string

	mu      sync.Mutex
	loaded  bool
	entries map[string]entry
}

type entry struct {
	Project string            `json:"project"`
	Created time.Time         `json:"created"`
	Labels  map[string]string `json:"labels"`
}

// New returns the store kept in dir ("" keeps it in memory only).
func New(dir string) *Store { return &Store{dir: dir} }

// Volume is one volume a project declares, as it is after a deploy.
type Volume struct {
	// Name is the Engine volume name; Created its creation time.
	Name    string
	Created time.Time
	// Actual are the volume's labels; Declared the Compose file's.
	Actual   map[string]string
	Declared map[string]string
}

// Record replaces the entries of a project with the Docker Manager labels
// each of its volumes declares with a value the volume does not carry.
func (s *Store) Record(project string, vols []Volume) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	next := maps.Clone(s.entries)
	maps.DeleteFunc(next, func(_ string, e entry) bool { return e.Project == project })
	for _, v := range vols {
		if d := Missing(v.Actual, v.Declared); len(d) > 0 {
			next[v.Name] = entry{Project: project, Created: v.Created.UTC(), Labels: d}
		}
	}
	return s.save(next)
}

// Forget drops the entries of a project (its stack was removed: no Compose
// file declares anything any more).
func (s *Store) Forget(project string) error {
	return s.Record(project, nil)
}

// Compose returns the Compose labels of a volume created at created: the
// Docker Manager labels its stack's Compose file declared at the last
// deploy with a value the volume does not carry (nil when none).
func (s *Store) Compose(name string, created time.Time) map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.load() != nil {
		return nil
	}
	e, ok := s.entries[name]
	if !ok || !e.Created.Equal(created.UTC()) {
		return nil
	}
	return maps.Clone(e.Labels)
}

// Missing returns the Docker Manager labels declared with a value the
// actual labels lack (nil when none). Other labels are Docker's and
// Compose's business and are never taken from the declaration.
func Missing(actual, declared map[string]string) map[string]string {
	var out map[string]string
	for _, k := range protocol.UserLabels {
		v, ok := declared[k]
		if !ok {
			continue
		}
		if cur, has := actual[k]; has && cur == v {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// Effective is the labels a volume is treated as carrying: its own, with
// the Compose labels taking precedence.
func Effective(labels, compose map[string]string) map[string]string {
	if len(compose) == 0 {
		return labels
	}
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, compose)
	return out
}

// load reads the file once (s.mu held); a missing file is an empty store.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.entries = map[string]entry{}
	if s.dir != "" {
		b, err := os.ReadFile(filepath.Join(s.dir, FileName))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("volume labels: %w", err)
		default:
			if err := json.Unmarshal(b, &s.entries); err != nil {
				return fmt.Errorf("volume labels: %s is unreadable: %w", FileName, err)
			}
		}
	}
	s.loaded = true
	return nil
}

// save writes next atomically and makes it current (s.mu held).
func (s *Store) save(next map[string]entry) error {
	if s.dir != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, FileName+".*")
		if err != nil {
			return fmt.Errorf("volume labels: %w", err)
		}
		tmp := f.Name()
		_, werr := f.Write(append(b, '\n'))
		serr := f.Sync()
		cerr := f.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("volume labels: %w", err)
		}
		if err := os.Rename(tmp, filepath.Join(s.dir, FileName)); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("volume labels: %w", err)
		}
	}
	s.entries = next
	return nil
}
