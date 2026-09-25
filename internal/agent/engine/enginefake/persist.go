package enginefake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/neurekadev/dockyard/internal/agent/engine"
)

// persisted is the Engine's durable state: what a real Engine keeps across
// a restart of the process that talks to it.
type persisted struct {
	Seq         int                                 `json:"seq"`
	Containers  map[string]*Container               `json:"containers"`
	Images      map[string]*Image                   `json:"images"`
	Volumes     map[string]*engine.Volume           `json:"volumes"`
	Networks    map[string]*engine.Network          `json:"networks"`
	Calls       []string                            `json:"calls"`
	VolumeRoot  string                              `json:"volumeRoot"`
	Events      []engine.Event                      `json:"events"`
	BuildCache  map[string]*engine.BuildCacheRecord `json:"buildCache"`
	VolumeSizes map[string]int64                    `json:"volumeSizes"`
	Sizes       map[string]int64                    `json:"sizes"`
	Remote      map[string]string                   `json:"remote"`
	StartHealth map[string]string                   `json:"startHealth"`
}

// Persist backs the Engine with a file, like a real Engine outliving the
// agent: when path exists its state is loaded (replacing the current
// one), and from now on every operation writes the state back (atomically,
// before the operation returns). Crash tests (-tags faultinject) kill and
// restart the agent process over the same file. Injected failures and exec
// instances are not persisted.
func (e *Engine) Persist(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, err := os.ReadFile(path) //nolint:gosec // test fake: the test's own state file
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		var p persisted
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("enginefake: state %s: %w", path, err)
		}
		e.seq, e.containers, e.images, e.volumes, e.networks = p.Seq, p.Containers, p.Images, p.Volumes, p.Networks
		e.calls, e.volumeRoot, e.events = p.Calls, p.VolumeRoot, p.Events
		e.buildCache, e.volumeSizes, e.sizes, e.remote, e.startHealth = p.BuildCache, p.VolumeSizes, p.Sizes, p.Remote, p.StartHealth
		e.ensureMaps()
	}
	e.persistPath = path
	return e.saveLocked()
}

func (e *Engine) ensureMaps() {
	if e.containers == nil {
		e.containers = map[string]*Container{}
	}
	if e.images == nil {
		e.images = map[string]*Image{}
	}
	if e.volumes == nil {
		e.volumes = map[string]*engine.Volume{}
	}
	if e.networks == nil {
		e.networks = map[string]*engine.Network{}
	}
	if e.buildCache == nil {
		e.buildCache = map[string]*engine.BuildCacheRecord{}
	}
	if e.volumeSizes == nil {
		e.volumeSizes = map[string]int64{}
	}
	if e.sizes == nil {
		e.sizes = map[string]int64{}
	}
	if e.remote == nil {
		e.remote = map[string]string{}
	}
	if e.startHealth == nil {
		e.startHealth = map[string]string{}
	}
}

// saveLocked writes the state (callers hold e.mu).
func (e *Engine) saveLocked() error {
	b, err := json.Marshal(persisted{Seq: e.seq, Containers: e.containers, Images: e.images, Volumes: e.volumes, Networks: e.networks,
		Calls: e.calls, VolumeRoot: e.volumeRoot, Events: e.events, BuildCache: e.buildCache, VolumeSizes: e.volumeSizes,
		Sizes: e.sizes, Remote: e.remote, StartHealth: e.startHealth})
	if err != nil {
		return err
	}
	tmp := e.persistPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(e.persistPath))
}

// unlock releases e.mu after persisting the state (Persist).
func (e *Engine) unlock() {
	if e.persistPath != "" {
		if err := e.saveLocked(); err != nil {
			e.mu.Unlock()
			panic("enginefake: persist: " + err.Error()) // test fake: a lost write would invalidate the test
		}
	}
	e.mu.Unlock()
}
