package backups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
)

// ListSnapshots returns indexed snapshots ("backups").
func (s *Service) ListSnapshots(ctx context.Context, f domain.BackupSnapshotFilter) ([]domain.BackupSnapshot, error) {
	return store.ListBackupSnapshots(ctx, s.db, f)
}

// GetSnapshot returns one indexed snapshot.
func (s *Service) GetSnapshot(ctx context.Context, id string) (domain.BackupSnapshot, error) {
	return store.GetBackupSnapshot(ctx, s.db, id)
}

// GetSet returns one backup set.
func (s *Service) GetSet(ctx context.Context, id string) (domain.BackupSet, error) {
	return store.GetBackupSet(ctx, s.db, id)
}

// ListSets returns a policy's newest sets.
func (s *Service) ListSets(ctx context.Context, policyID string, limit int) ([]domain.BackupSet, error) {
	return store.ListBackupSets(ctx, s.db, policyID, limit)
}

// ErrContentUnavailable is returned when the snapshot contents cannot be read now.
var ErrContentUnavailable = errors.New("the snapshot contents cannot be read right now")

// ErrNotAFile is returned when a download names a directory or special file.
var ErrNotAFile = errors.New("only regular files can be downloaded")

// ErrFileTooLarge is returned when the file exceeds the download limit.
var ErrFileTooLarge = errors.New("the file is larger than the download limit")

// MaxDownloadBytes bounds single-file downloads from snapshots.
const MaxDownloadBytes int64 = 2 << 30

// Contents lists a directory of a snapshot (dir "" or "/" lists the
// top level recursively bounded by limit). Manager-state snapshots are
// read by the manager, environment snapshots by their agent.
func (s *Service) Contents(ctx context.Context, sn domain.BackupSnapshot, dir string, recursive bool, limit int) (restic.Listing, error) {
	if dir != "" && !protocol.ValidSnapshotPath(dir) {
		return restic.Listing{}, fieldErr("path", "must be a clean absolute path inside the snapshot")
	}
	if limit <= 0 || limit > restic.DefaultMaxNodes {
		limit = 1000
	}
	if sn.Scope == backup.ScopeManager {
		o, _, _, err := s.managerLocation(ctx, sn.RepositoryID, false)
		if err != nil {
			return restic.Listing{}, err
		}
		return o.Repo.Ls(ctx, sn.ResticSnapshotID, dir, recursive, limit)
	}
	_, cred, ref, err := s.agentAccess(ctx, sn)
	if err != nil {
		return restic.Listing{}, err
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, sn.EnvironmentID, protocol.ReqBackupContents, protocol.BackupContentsInput{
		Repository: ref, Credential: cred, SnapshotID: sn.ResticSnapshotID, Path: dir, Recursive: recursive, Limit: limit}, 2*time.Minute)
	if err != nil {
		return restic.Listing{}, agentFailure(err)
	}
	var out protocol.BackupContentsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return restic.Listing{}, ErrContentUnavailable
	}
	return restic.Listing{Nodes: out.Nodes, Truncated: out.Truncated}, nil
}

// FileInfo returns a regular file of a snapshot for a download (refusing
// directories, special files and files above MaxDownloadBytes).
func (s *Service) FileInfo(ctx context.Context, sn domain.BackupSnapshot, file string) (restic.Node, error) {
	if !protocol.ValidSnapshotPath(file) || file == "/" {
		return restic.Node{}, fieldErr("path", "must be the absolute path of a file inside the snapshot")
	}
	l, err := s.Contents(ctx, sn, file, false, 2)
	if err != nil {
		return restic.Node{}, err
	}
	for _, n := range l.Nodes {
		if n.Path != file {
			continue
		}
		if n.Type != "file" {
			return n, ErrNotAFile
		}
		if n.Size > MaxDownloadBytes {
			return n, ErrFileTooLarge
		}
		return n, nil
	}
	return restic.Node{}, &restic.Error{Op: "dump", Code: restic.CodeSnapshotNotFound, Message: "no such file in the snapshot"}
}

// Download writes a file returned by FileInfo to dst (never more than its
// size).
func (s *Service) Download(ctx context.Context, sn domain.BackupSnapshot, node restic.Node, dst io.Writer) error {
	file := node.Path
	if sn.Scope == backup.ScopeManager {
		o, _, _, err := s.managerLocation(ctx, sn.RepositoryID, false)
		if err != nil {
			return err
		}
		return o.Repo.Dump(ctx, sn.ResticSnapshotID, file, &limitedWriter{w: dst, left: node.Size})
	}
	_, cred, ref, err := s.agentAccess(ctx, sn)
	if err != nil {
		return err
	}
	st, err := s.opts.Agents.OpenStream(ctx, sn.EnvironmentID, protocol.StreamBackupFile, protocol.BackupFileStreamInput{
		Repository: ref, Credential: cred, SnapshotID: sn.ResticSnapshotID, Path: file, MaxBytes: node.Size},
		streammux.OpenOptions{MaxBytes: node.Size})
	if err != nil {
		return agentFailure(err)
	}
	if _, err := io.Copy(&limitedWriter{w: dst, left: node.Size}, st); err != nil {
		st.Abort("error", protocol.CodeInternal, "download aborted")
		return err
	}
	return nil
}

type limitedWriter struct {
	w    io.Writer
	left int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, ErrFileTooLarge
	}
	n, err := l.w.Write(p)
	l.left -= int64(n)
	return n, err
}

func (s *Service) agentAccess(ctx context.Context, sn domain.BackupSnapshot) (domain.BackupRepository, *protocol.RepositoryCredential, protocol.BackupRepositoryRef, error) {
	if s.opts.Agents == nil {
		return domain.BackupRepository{}, nil, protocol.BackupRepositoryRef{}, ErrContentUnavailable
	}
	repo, err := store.GetBackupRepository(ctx, s.db, sn.RepositoryID)
	if err != nil {
		return repo, nil, protocol.BackupRepositoryRef{}, err
	}
	key, err := s.KeyState(ctx)
	if err != nil {
		return repo, nil, protocol.BackupRepositoryRef{}, err
	}
	cred, err := s.credentialFor(ctx, s.db, repo.ID)
	if err != nil {
		return repo, nil, protocol.BackupRepositoryRef{}, err
	}
	return repo, cred, repositoryRef(repo, sn.Scope, key), nil
}

// AgentError is an agent's refusal of a backup request.
type AgentError struct {
	Class string
}

func (e *AgentError) Error() string { return "the agent could not read the repository: " + e.Class }

func agentFailure(err error) error {
	return &AgentError{Class: agentErrorClass(err)}
}

// ContentsCapabilities returns what reading a snapshot's contents needs
// besides backup.contents.read (#10, #17): stack snapshots contain
// compose.yaml and .env, so they need stack.definition.read on the stack;
// volume snapshots need volume.files.read on the volume; manager-state
// snapshots are owner-only.
func ContentsCapabilities(sn domain.BackupSnapshot) (capability string, res authz.Resource, ownerOnly bool) {
	switch sn.Kind {
	case backup.MemberStack:
		return "stack.definition.read", authz.Resource{Type: "stack", ID: sn.StackID, EnvironmentID: sn.EnvironmentID}, false
	case backup.MemberVolume:
		return "volume.files.read", authz.Resource{Type: "volume", ID: sn.Volume, EnvironmentID: sn.EnvironmentID}, false
	}
	return "", authz.Resource{}, true
}

// VerifySnapshot queues a verification of the snapshot's location with a
// read-data subset (manual; the caller authorized backup.verify).
func (s *Service) VerifySnapshot(ctx context.Context, sn domain.BackupSnapshot, principal authz.Principal, subset, idempotencyKey string) (domain.Job, error) {
	if !protocol.ValidReadDataSubset(subset) {
		return domain.Job{}, fieldErr("readDataSubset", "must be a percentage (5%%), a fraction (1/10) or a size (500M)")
	}
	req, err := s.verifyRequest(ctx, sn.RepositoryID, sn.Scope, subset)
	if err != nil {
		return domain.Job{}, err
	}
	req.Principal, req.IdempotencyKey = principal, idempotencyKey
	j, _, err := s.opts.Jobs.Enqueue(ctx, req)
	return j, err
}
