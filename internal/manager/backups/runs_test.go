package backups

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestSetFinishesOnlyWhenEveryEnvironmentReported: an environment that
// failed does not finish a set while another one still backs up, so the
// retention after the backup is not flagged early (it would only see the
// snapshots indexed so far, and the later environment would get none).
func TestSetFinishesOnlyWhenEveryEnvironmentReported(t *testing.T) {
	ctx := testutil.Context(t)
	s := &Service{opts: Options{Clock: testutil.FakeClock()}}
	set := domain.BackupSet{PolicyID: "pol-1", State: backup.StatePending, Members: []domain.BackupSetMember{
		{Item: "stack/a", Scope: backup.EnvironmentScope("env-a"), State: backup.StateFailed, ErrorClass: "engine_error"},
		{Item: "stack/b", Scope: backup.EnvironmentScope("env-b"), State: backup.StatePending},
	}}
	wasPending := set.State == backup.StatePending
	s.settle(&set)
	// Still running: no database access, no follow-up.
	s.flagRetention(ctx, nil, &set, wasPending)
	if set.State != backup.StatePending || set.FinishedAt != nil || set.FollowUp != "" {
		t.Fatalf("one environment failed, one running: state %s finished %v follow-up %q", set.State, set.FinishedAt, set.FollowUp)
	}

	set.Members[1].State, set.Members[1].SnapshotID = backup.StateComplete, "snap-b"
	s.settle(&set)
	if set.State != backup.StatePartial || set.FinishedAt == nil {
		t.Errorf("every environment reported: state %s finished %v", set.State, set.FinishedAt)
	}

	// A set that had already finished is never flagged again.
	s.flagRetention(ctx, nil, &set, false)
	if set.FollowUp != "" {
		t.Errorf("a finished set was flagged again: %q", set.FollowUp)
	}
}

type fakeStacks map[string]bool

func (f fakeStacks) Get(_ context.Context, id string) (domain.Stack, error) {
	if !f[id] {
		return domain.Stack{}, domain.ErrStackNotFound
	}
	return domain.Stack{ID: id}, nil
}

type fakeEnvironments map[string]domain.EnvironmentStatus

func (f fakeEnvironments) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	st, ok := f[id]
	if !ok {
		return domain.Environment{}, domain.ErrEnvironmentNotFound
	}
	return domain.Environment{ID: id, Status: st}, nil
}

type offlineVolumes struct{ fakeVolumes }

func (offlineVolumes) ListVolumes(context.Context, string) ([]protocol.VolumeInfo, error) {
	return nil, errors.New("the agent is offline")
}

// TestExpiredItemsOnlyDeletedAndOldEnough: the expiry names a stack Docker
// Manager no longer has and a volume its environment no longer has, once
// their newest backup is older than the setup's days; never an item that
// still exists, a recent one, the manager's state, or anything of an
// offline or archived environment.
func TestExpiredItemsOnlyDeletedAndOldEnough(t *testing.T) {
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	now := clk.Now().UTC()
	old, recent := now.Add(-40*24*time.Hour), now.Add(-5*24*time.Hour)
	scope := backup.EnvironmentScope("env-1")
	snaps := []domain.BackupSnapshot{
		{Scope: scope, Kind: backup.MemberStack, Item: backup.StackItem("gone"), StackID: "gone", SnapshotTime: old},
		{Scope: scope, Kind: backup.MemberStack, Item: backup.StackItem("here"), StackID: "here", SnapshotTime: old},
		{Scope: scope, Kind: backup.MemberVolume, Item: backup.VolumeItem("media"), Volume: "media", SnapshotTime: old},
		{Scope: scope, Kind: backup.MemberVolume, Item: backup.VolumeItem("scratch"), Volume: "scratch", SnapshotTime: old},
		// Deleted, but backed up recently: kept until it is old enough.
		{Scope: scope, Kind: backup.MemberVolume, Item: backup.VolumeItem("fresh"), Volume: "fresh", SnapshotTime: old},
		{Scope: scope, Kind: backup.MemberVolume, Item: backup.VolumeItem("fresh"), Volume: "fresh", SnapshotTime: recent},
	}
	p := domain.BackupSetup{Retention: domain.BackupRetention{ExpireDeletedDays: 30}}
	s := &Service{opts: Options{Clock: clk, Stacks: fakeStacks{"here": true}, Environments: fakeEnvironments{"env-1": domain.EnvironmentActive}},
		volumes: scopeVolumes()} // has media and scratch, not fresh
	if got := s.expiredItems(ctx, p, scope, snaps); !slices.Equal(got, []string{"stack/gone"}) {
		t.Errorf("expired %v, want only the deleted stack", got)
	}
	s.volumes = fakeVolumes{volumes: []protocol.VolumeInfo{{Name: "media"}}}
	if got := s.expiredItems(ctx, p, scope, snaps); !slices.Equal(got, []string{"stack/gone", "volume/scratch"}) {
		t.Errorf("expired %v, want the deleted stack and volume", got)
	}
	// Off, the manager's state, an offline agent or an archived environment.
	if got := s.expiredItems(ctx, domain.BackupSetup{}, scope, snaps); got != nil {
		t.Errorf("expiry off: %v", got)
	}
	if got := s.expiredItems(ctx, p, backup.ScopeManager, snaps); got != nil {
		t.Errorf("manager state: %v", got)
	}
	s.volumes = offlineVolumes{}
	if got := s.expiredItems(ctx, p, scope, snaps); !slices.Equal(got, []string{"stack/gone"}) {
		t.Errorf("offline agent: %v, want no volume", got)
	}
	s.volumes = fakeVolumes{}
	s.opts.Environments = fakeEnvironments{"env-1": domain.EnvironmentArchived}
	if got := s.expiredItems(ctx, p, scope, snaps); !slices.Equal(got, []string{"stack/gone"}) {
		t.Errorf("archived environment: %v, want no volume", got)
	}
}
