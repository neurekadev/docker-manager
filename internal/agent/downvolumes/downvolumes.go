// Package downvolumes keeps the anonymous volumes a stack's containers had
// when Docker Manager brought the stack down (Compose down, #276). The
// Engine creates anonymous volumes without a Compose project label, so once
// the containers are gone nothing ties the volumes to their stack any more:
// backups of the stopped stack would silently leave their data out. The
// down step records them per project; a backup includes the recorded
// volumes while the project has no containers (the next deploy creates new
// ones, as with docker compose down). The next down replaces a project's
// record, deleting the stack forgets it and a rename moves it.
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

// Store is the recorded anonymous volumes per Compose project.
type Store struct {
	dir string

	mu       sync.Mutex
	loaded   bool
	projects map[string][]Volume
}

// New returns the store kept in dir ("" keeps it in memory only).
func New(dir string) *Store { return &Store{dir: dir} }

// Record replaces the volumes recorded for a project (none forgets it).
func (s *Store) Record(project string, vols []Volume) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	next := maps.Clone(s.projects)
	if len(vols) == 0 {
		delete(next, project)
	} else {
		next[project] = slices.Clone(vols)
	}
	return s.save(next)
}

// Forget drops a project's record (its stack was removed).
func (s *Store) Forget(project string) error { return s.Record(project, nil) }

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
	vols, ok := s.projects[from]
	if !ok {
		return nil
	}
	next := maps.Clone(s.projects)
	delete(next, from)
	next[to] = vols
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
	return slices.Clone(s.projects[project])
}

// load reads the file once (s.mu held); a missing file is an empty store.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.projects = map[string][]Volume{}
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
		}
	}
	s.loaded = true
	return nil
}

// save writes next atomically and makes it current (s.mu held).
func (s *Store) save(next map[string][]Volume) error {
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
