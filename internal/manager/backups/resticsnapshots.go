package backups

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// restic snapshots of a repository, read live (#10): every snapshot of
// every location Docker Manager knows, including the manifests and
// snapshots its index does not hold. restic reads only the snapshot
// files; nothing is changed (no initialization, no key migration).

// Snapshot classes.
const (
	SnapshotStack        = backup.MemberStack
	SnapshotVolume       = backup.MemberVolume
	SnapshotManagerState = backup.MemberManagerState
	SnapshotSetManifest  = "set_manifest"
	SnapshotHostManifest = "host_manifest"
	SnapshotForeign      = "foreign"
)

// MaxLocationSnapshots bounds the snapshots listed per location (the
// newest; the agent path lists at most backup.MaxListedSnapshots).
const MaxLocationSnapshots = 1000

// snapshotListTimeout covers restic's lock retry (2 min) per location.
const snapshotListTimeout = 3 * time.Minute

// ResticSnapshot is one restic snapshot with what Docker Manager knows
// about it.
type ResticSnapshot struct {
	Snapshot restic.Snapshot
	// Class is stack, volume, manager_state, set_manifest, host_manifest
	// or foreign (not written by Docker Manager).
	Class    string
	Item     string
	SetID    string
	PolicyID string
	// Backup is the index entry (nil when the index does not hold it).
	Backup *domain.BackupSnapshot
}

// ResticLocation is one location's snapshots, newest first.
type ResticLocation struct {
	Scope              string
	EnvironmentID      string
	ResticRepositoryID string
	// ErrorClass tells why the location could not be listed ("" = listed).
	ErrorClass string
	// Truncated: older snapshots exist beyond the listed ones.
	Truncated bool
	Snapshots []ResticSnapshot
}

// ResticSnapshots lists the restic snapshots of every location of a
// repository. Locations are read in parallel; one that cannot be read
// reports its error class instead of failing the listing.
func (s *Service) ResticSnapshots(ctx context.Context, repositoryID string) ([]ResticLocation, error) {
	r, err := store.GetBackupRepository(ctx, s.db, repositoryID)
	if err != nil {
		return nil, err
	}
	if r.State != domain.BackupRepositoryReady {
		return nil, domain.ErrRecoveryKeyNotConfirmed
	}
	indexed, err := store.ListBackupSnapshots(ctx, s.db, domain.BackupSnapshotFilter{RepositoryID: r.ID, IncludeForgotten: true})
	if err != nil {
		return nil, err
	}
	known := map[string]domain.BackupSnapshot{}
	for _, sn := range indexed {
		known[sn.Scope+"\x00"+sn.ResticSnapshotID] = sn
	}
	scopes := s.knownScopes(ctx, r)
	out := make([]ResticLocation, len(scopes))
	var wg sync.WaitGroup
	for i, scope := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, snapshotListTimeout)
			defer cancel()
			out[i] = s.listLocation(cctx, r, scope, known)
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *Service) listLocation(ctx context.Context, r domain.BackupRepository, scope string, known map[string]domain.BackupSnapshot) ResticLocation {
	loc := ResticLocation{Scope: scope}
	env, isEnv := backup.ScopeEnvironment(scope)
	loc.EnvironmentID = env
	var snaps []restic.Snapshot
	if isEnv && r.Kind == backup.KindLocal {
		snaps, loc.ResticRepositoryID, loc.ErrorClass = s.agentSnapshots(ctx, r, env, scope)
		loc.Truncated = len(snaps) >= backup.MaxListedSnapshots
	} else {
		snaps, loc.ResticRepositoryID, loc.ErrorClass = s.managerSnapshots(ctx, r, scope)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	if len(snaps) > MaxLocationSnapshots {
		snaps, loc.Truncated = snaps[:MaxLocationSnapshots], true
	}
	loc.Snapshots = make([]ResticSnapshot, 0, len(snaps))
	for _, sn := range snaps {
		rs := classify(sn, scope)
		if b, ok := known[scope+"\x00"+sn.ID]; ok {
			rs.Backup = &b
		}
		loc.Snapshots = append(loc.Snapshots, rs)
	}
	return loc
}

// managerSnapshots reads a location the manager opens itself (the manager
// scope, S3 locations), with the previous key while a rotation has not
// reached it.
func (s *Service) managerSnapshots(ctx context.Context, r domain.BackupRepository, scope string) ([]restic.Snapshot, string, string) {
	cur, prev, _, _, err := s.currentKeys(ctx, s.db)
	if err != nil {
		return nil, "", "recovery_key_not_confirmed"
	}
	creds, err := s.credentials(ctx, s.db, r.ID)
	if err != nil {
		return nil, "", "credential_unavailable"
	}
	loc := destination(r).Location(scope, creds)
	repo := s.opts.Restic.Open(loc, cur)
	snaps, err := repo.Snapshots(ctx, restic.SnapshotFilter{})
	if restic.IsCode(err, restic.CodeKeyRejected) && prev != "" {
		repo = s.opts.Restic.Open(loc, prev)
		snaps, err = repo.Snapshots(ctx, restic.SnapshotFilter{})
	}
	if err != nil {
		class := restic.CodeOf(err)
		if class == "" {
			class = restic.CodeFailed
		}
		return nil, "", class
	}
	id := ""
	if cfg, err := repo.Config(ctx); err == nil {
		id = cfg.ID
	}
	return snaps, id, ""
}

// agentSnapshots asks the agent owning a local repository (the newest
// backup.MaxListedSnapshots).
func (s *Service) agentSnapshots(ctx context.Context, r domain.BackupRepository, env, scope string) ([]restic.Snapshot, string, string) {
	if s.opts.Agents == nil {
		return nil, "", "agent_offline"
	}
	rec, _, _ := store.GetBackupKey(ctx, s.db)
	cred, err := s.credentialFor(ctx, s.db, r.ID)
	if err != nil {
		return nil, "", "recovery_key_not_confirmed"
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqBackupSnapshots,
		protocol.BackupSnapshotsInput{Repository: repositoryRef(r, scope, rec.State), Credential: cred}, snapshotListTimeout)
	if err != nil {
		return nil, "", agentErrorClass(err)
	}
	var out protocol.BackupSnapshotsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", "agent_error"
	}
	return out.Snapshots, out.ResticRepositoryID, ""
}

// classify tells what a snapshot holds from its tags.
func classify(sn restic.Snapshot, scope string) ResticSnapshot {
	rs := ResticSnapshot{Snapshot: sn, Class: SnapshotForeign, SetID: backup.SetOf(sn.Tags), PolicyID: backup.PolicyOf(sn.Tags),
		Item: backup.ItemOf(sn.Tags)}
	switch {
	case sn.HasTag(backup.TagManifest):
		rs.Class = SnapshotHostManifest
		if scope == backup.ScopeManager {
			rs.Class = SnapshotSetManifest
		}
	case sn.HasTag(backup.TagManagerState):
		rs.Class = SnapshotManagerState
	case sn.HasTag(backup.TagDockerManager) && strings.HasPrefix(rs.Item, "stack/"):
		rs.Class = SnapshotStack
	case sn.HasTag(backup.TagDockerManager) && strings.HasPrefix(rs.Item, "volume/"):
		rs.Class = SnapshotVolume
	}
	return rs
}
