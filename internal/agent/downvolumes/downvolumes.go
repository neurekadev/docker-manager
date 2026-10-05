// Package downvolumes keeps the anonymous volumes a stack's containers had
// when Docker Manager brought the stack down (Compose down, #276). The
// Engine creates anonymous volumes without a Compose project label, so once
// the containers are gone nothing ties the volumes to their stack any more:
// backups of the stopped stack would silently leave their data out. The
// down step records them per project; a backup includes the recorded
// volumes while the project has no containers (the next deploy creates new
// ones, as with docker compose down). Each record names the down job that
// wrote it: a re-run of that job adds to it (an earlier run may have
// removed some containers already), another down replaces it. A successful
// deploy and deleting the stack forget it and a rename moves it.
//
// The store is a JSON file in the agent's state directory, replaced
// atomically; a nil *Store keeps nothing.
package downvolumes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// FileName is the store's file in the state directory.
const FileName = "down-volumes.json"

// Volume is an anonymous volume a container of the project mounted.
type Volume struct {
	Name        string `json:"name"`
	Service     string `json:"service"`
	Destination string `json:"destination"`
}

// record is a project's entry: the down job that wrote it and the volumes.
type record struct {
	Job     string   `json:"job,omitempty"`
	Volumes []Volume `json:"volumes"`
}

// Store is the recorded anonymous volumes per Compose project.
type Store struct {
	dir string

	mu       sync.Mutex
	loaded   bool
	projects map[string]record
}

// New returns the store kept in dir ("" keeps it in memory only).
func New(dir string) *Store { return &Store{dir: dir} }

// Record records the volumes the down job is about to leave behind: a
// re-run of the job that wrote the project's record adds to it (a name
// already recorded keeps its entry), any other down replaces it. The
// volumes stay sorted by name.
func (s *Store) Record(project, job string, vols []Volume) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	var merged []Volume
	if cur, ok := s.projects[project]; ok && cur.Job == job {
		merged = slices.Clone(cur.Volumes)
	}
	for _, v := range vols {
		if !slices.ContainsFunc(merged, func(x Volume) bool { return x.Name == v.Name }) {
			merged = append(merged, v)
		}
	}
	slices.SortFunc(merged, func(a, b Volume) int { return strings.Compare(a.Name, b.Name) })
	next := maps.Clone(s.projects)
	next[project] = record{Job: job, Volumes: merged}
	return s.save(next)
}

// Forget drops a project's record (its stack was removed or deployed).
func (s *Store) Forget(project string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	if _, ok := s.projects[project]; !ok {
		return nil
	}
	next := maps.Clone(s.projects)
	delete(next, project)
	return s.save(next)
}

// Rename moves a project's record to its new name (stack rename).
func (s *Store) Rename(from, to string) error {
	if s == nil || from == to {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	rec, ok := s.projects[from]
	if !ok {
		return nil
	}
	next := maps.Clone(s.projects)
	delete(next, from)
	next[to] = rec
	return s.save(next)
}

// Volumes returns the volumes recorded for a project (nil when none or
// when the store cannot be read).
func (s *Store) Volumes(project string) []Volume {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.load() != nil {
		return nil
	}
	return slices.Clone(s.projects[project].Volumes)
}

// load reads the file once (s.mu held); a missing file is an empty store.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.projects = map[string]record{}
	if s.dir != "" {
		b, err := os.ReadFile(filepath.Join(s.dir, FileName))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("down volumes: %w", err)
		default:
			if err := json.Unmarshal(b, &s.projects); err != nil {
				return fmt.Errorf("down volumes: %s is unreadable: %w", FileName, err)
			}
			if s.projects == nil { // the file holds null
				s.projects = map[string]record{}
			}
		}
	}
	s.loaded = true
	return nil
}

// save writes next atomically and makes it current (s.mu held).
func (s *Store) save(next map[string]record) error {
	if s.dir != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, FileName+".*")
		if err != nil {
			return fmt.Errorf("down volumes: %w", err)
		}
		tmp := f.Name()
		_, werr := f.Write(append(b, '\n'))
		serr := f.Sync()
		cerr := f.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("down volumes: %w", err)
		}
		if err := os.Rename(tmp, filepath.Join(s.dir, FileName)); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("down volumes: %w", err)
		}
	}
	s.projects = next
	return nil
}
