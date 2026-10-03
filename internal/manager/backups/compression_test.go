package backups

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestDestinationCarriesCompressionExceptAuto(t *testing.T) {
	r := domain.BackupRepository{ID: "r1", Endpoint: "https://s3.example.com", Bucket: "backups"}
	for mode, want := range map[string]string{
		domain.BackupCompressionAuto: "", domain.BackupCompressionMax: "max", domain.BackupCompressionOff: "off",
	} {
		r.Compression = mode
		if got := destination(r).Compression; got != want {
			t.Errorf("%s: destination compression = %q, want %q", mode, got, want)
		}
		// Refs never carry it: CommandInput adds it at dispatch.
		if got := repositoryRef(r, backup.EnvironmentScope("e1"), domain.BackupKeyState{}).Destination.Compression; got != "" {
			t.Errorf("%s: ref compression = %q", mode, got)
		}
	}
	for _, ok := range []string{"auto", "max", "off"} {
		if !validCompression(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "fastest", "Max"} {
		if validCompression(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestWithCompressionSetsTheDestinationMode(t *testing.T) {
	in := protocol.BackupRunInput{SetID: "set-1", PolicyID: "pol-1", Repository: protocol.BackupRepositoryRef{RepositoryID: "r1",
		Destination: backup.Destination{Kind: backup.KindS3, Endpoint: "https://s3.example.com", Bucket: "bkt", PathStyle: true},
		Scope:       backup.EnvironmentScope("e1"), KeyGeneration: 2}, Items: []protocol.BackupItem{}}
	raw, _ := json.Marshal(in)
	out, err := withCompression(raw, "max")
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.BackupRunInput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := in
	want.Repository.Destination.Compression = "max"
	if got.Repository != want.Repository || got.SetID != "set-1" || got.PolicyID != "pol-1" {
		t.Errorf("adapted input = %+v", got)
	}
	for _, bad := range []string{`[]`, `{}`, `{"repository":{}}`, `{"repository":null}`} {
		if _, err := withCompression(json.RawMessage(bad), "max"); err == nil {
			t.Errorf("adapted %s", bad)
		}
	}
}

type featureHub struct{ features map[string][]string }

func (featureHub) RequestEnvironment(context.Context, string, string, any, time.Duration) (json.RawMessage, error) {
	return nil, nil
}

func (featureHub) OpenStream(context.Context, string, string, any, streammux.OpenOptions) (*streammux.Stream, error) {
	return nil, nil
}

func (h featureHub) EnvironmentHasFeature(env, feature string) bool {
	for _, f := range h.features[env] {
		if f == feature {
			return true
		}
	}
	return false
}

// TestCommandInputOnlyForWritingCommandsOfNewAgents: verification,
// restores and agents without backup.compression keep the stored input
// (nil), without reading the repository.
func TestCommandInputOnlyForWritingCommandsOfNewAgents(t *testing.T) {
	ctx := testutil.Context(t)
	s := &Service{opts: Options{Agents: featureHub{features: map[string][]string{"new": {protocol.FeatureBackupCompression}}}}}
	repo := []domain.JobTarget{repoTarget("r1")}
	for _, j := range []domain.Job{
		{Kind: jobspec.BackupVerify, EnvironmentID: "new", Targets: repo},
		{Kind: jobspec.RestoreRun, EnvironmentID: "new", Targets: repo},
		{Kind: jobspec.BackupRun, EnvironmentID: "old", Targets: repo},
		{Kind: jobspec.BackupRetention, EnvironmentID: "old", Targets: repo},
		{Kind: jobspec.BackupRun, EnvironmentID: "new"}, // no repository
	} {
		if got := s.CommandInput(ctx, &j); got != nil {
			t.Errorf("%s on %s: adapted input %s", j.Kind, j.EnvironmentID, got)
		}
	}
}
