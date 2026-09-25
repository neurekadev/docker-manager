package restictest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/neurekadev/dockyard/internal/restic"
)

// The JSON form of the store (Persist).
type persistedStore struct {
	Seq   int                      `json:"seq"`
	Repos map[string]persistedRepo `json:"repos"`
	Calls []Call                   `json:"calls"`
}

type persistedRepo struct {
	ID      string          `json:"id"`
	Keys    []persistedKey  `json:"keys"`
	Snaps   []persistedSnap `json:"snaps"`
	Damaged bool            `json:"damaged"`
}

type persistedKey struct {
	ID       string `json:"id"`
	Password string `json:"password"`
}

type persistedSnap struct {
	Snapshot restic.Snapshot          `json:"snapshot"`
	Files    map[string]persistedFile `json:"files"`
}

type persistedFile struct {
	Data []byte      `json:"data"`
	Mode fs.FileMode `json:"mode"`
	Link string      `json:"link"`
	UID  int         `json:"uid"`
	GID  int         `json:"gid"`
}

// Persist backs the store with a file, like a repository on disk outliving
// the process that runs restic: when path exists its repositories are
// loaded (replacing the current ones), and from now on every operation
// writes the state back before it returns. Crash tests (-tags faultinject)
// kill and restart the agent over the same file. Injected failures and
// OnBackup are not persisted.
func (s *Store) Persist(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(path) //nolint:gosec // test fake: the test's own state file
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		var p persistedStore
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("restictest: state %s: %w", path, err)
		}
		s.seq, s.calls, s.repos = p.Seq, p.Calls, map[string]*repoState{}
		for loc, r := range p.Repos {
			rs := &repoState{id: r.ID, damaged: r.Damaged}
			for _, k := range r.Keys {
				rs.keys = append(rs.keys, key{id: k.ID, password: k.Password})
			}
			for _, sn := range r.Snaps {
				files := make(map[string]*file, len(sn.Files))
				for name, f := range sn.Files {
					files[name] = &file{data: f.Data, mode: f.Mode, link: f.Link, uid: f.UID, gid: f.GID}
				}
				rs.snaps = append(rs.snaps, &snap{Snapshot: sn.Snapshot, files: files})
			}
			s.repos[loc] = rs
		}
	}
	s.persistPath = path
	return s.saveLocked()
}

// saveLocked writes the state (callers hold s.mu).
func (s *Store) saveLocked() error {
	p := persistedStore{Seq: s.seq, Calls: s.calls, Repos: make(map[string]persistedRepo, len(s.repos))}
	for loc, r := range s.repos {
		pr := persistedRepo{ID: r.id, Damaged: r.damaged}
		for _, k := range r.keys {
			pr.Keys = append(pr.Keys, persistedKey{ID: k.id, Password: k.password})
		}
		for _, sn := range r.snaps {
			ps := persistedSnap{Snapshot: sn.Snapshot, Files: make(map[string]persistedFile, len(sn.files))}
			for name, f := range sn.files {
				ps.Files[name] = persistedFile{Data: f.data, Mode: f.mode, Link: f.link, UID: f.uid, GID: f.gid}
			}
			pr.Snaps = append(pr.Snaps, ps)
		}
		p.Repos[loc] = pr
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := s.persistPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.persistPath)
}

// unlock releases s.mu after persisting the state (Persist).
func (s *Store) unlock() {
	if s.persistPath != "" {
		if err := s.saveLocked(); err != nil {
			s.mu.Unlock()
			panic("restictest: persist: " + err.Error()) // a lost write would invalidate the test
		}
	}
	s.mu.Unlock()
}
