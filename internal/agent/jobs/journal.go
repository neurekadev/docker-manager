package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/neurekadev/dockyard/internal/jobexec"
)

// Journal file layout inside the agent state directory.
const (
	JournalDir  = "jobs"
	JournalFile = "journal.json"
	journalVer  = 1
)

// journalData is the on-disk format.
type journalData struct {
	Version int `json:"version"`
	// HighWater is the highest fencing token this agent ever accepted.
	HighWater uint64                   `json:"highWater"`
	Entries   map[string]jobexec.State `json:"entries"`
}

// Journal is the agent's durable record of in-flight and unacknowledged
// job attempts plus its fencing high-water mark. Every mutation rewrites the
// file atomically (temp file, fsync, rename, directory fsync) before
// returning, so an acknowledged command or a completed step survives a crash.
type Journal struct {
	path string
	mu   sync.Mutex
	data journalData
}

// OpenJournal opens (creating if needed) the journal under stateDir.
func OpenJournal(stateDir string) (*Journal, error) {
	dir := filepath.Join(stateDir, JournalDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agent jobs: create journal dir: %w", err)
	}
	j := &Journal{path: filepath.Join(dir, JournalFile), data: journalData{Version: journalVer, Entries: map[string]jobexec.State{}}}
	b, err := os.ReadFile(j.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return j, nil
	case err != nil:
		return nil, fmt.Errorf("agent jobs: read journal: %w", err)
	}
	if err := json.Unmarshal(b, &j.data); err != nil {
		return nil, fmt.Errorf("agent jobs: journal %s is corrupt: %w", j.path, err)
	}
	if j.data.Version != journalVer {
		return nil, fmt.Errorf("agent jobs: unsupported journal version %d", j.data.Version)
	}
	if j.data.Entries == nil {
		j.data.Entries = map[string]jobexec.State{}
	}
	return j, nil
}

// HighWater returns the highest accepted fencing token.
func (j *Journal) HighWater() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.data.HighWater
}

// Get returns a copy of the entry for jobID.
func (j *Journal) Get(jobID string) (jobexec.State, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	st, ok := j.data.Entries[jobID]
	if !ok {
		return jobexec.State{}, false
	}
	return st.Clone(), true
}

// Entries returns copies of all entries sorted by job ID.
func (j *Journal) Entries() []jobexec.State {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]jobexec.State, 0, len(j.data.Entries))
	for _, st := range j.data.Entries {
		out = append(out, st.Clone())
	}
	sort.Slice(out, func(a, b int) bool { return out[a].JobID < out[b].JobID })
	return out
}

// Accept durably records a newly accepted command and raises the
// high-water mark to its fencing token.
func (j *Journal) Accept(st *jobexec.State) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prevHW := j.data.HighWater
	prev, had := j.data.Entries[st.JobID]
	j.data.Entries[st.JobID] = st.Clone()
	if st.FencingToken > j.data.HighWater {
		j.data.HighWater = st.FencingToken
	}
	if err := j.writeLocked(); err != nil {
		j.data.HighWater = prevHW
		if had {
			j.data.Entries[st.JobID] = prev
		} else {
			delete(j.data.Entries, st.JobID)
		}
		return err
	}
	return nil
}

// Save implements jobexec.Journal.
func (j *Journal) Save(_ context.Context, st *jobexec.State) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	cur, ok := j.data.Entries[st.JobID]
	if ok && cur.Attempt > st.Attempt {
		return fmt.Errorf("agent jobs: attempt %d of %s superseded by %d", st.Attempt, st.JobID, cur.Attempt)
	}
	j.data.Entries[st.JobID] = st.Clone()
	return j.writeLocked()
}

// Forget removes finished entries; running ones are kept.
func (j *Journal) Forget(jobIDs ...string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	changed := false
	for _, id := range jobIDs {
		if st, ok := j.data.Entries[id]; ok && st.Outcome != nil {
			delete(j.data.Entries, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return j.writeLocked()
}

func (j *Journal) writeLocked() error {
	b, err := json.Marshal(j.data)
	if err != nil {
		return fmt.Errorf("agent jobs: encode journal: %w", err)
	}
	dir := filepath.Dir(j.path)
	tmp, err := os.CreateTemp(dir, ".journal-*.tmp")
	if err != nil {
		return fmt.Errorf("agent jobs: write journal: %w", err)
	}
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("agent jobs: write journal: %w", err)
	}
	if err := os.Rename(tmp.Name(), j.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("agent jobs: write journal: %w", err)
	}
	return syncDir(dir)
}

// syncDir makes the rename durable. Windows cannot fsync directories (the
// agent only runs on Linux; Windows is a development platform).
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("agent jobs: sync journal dir: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("agent jobs: sync journal dir: %w", err)
	}
	return nil
}
