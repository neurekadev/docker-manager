package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// Placeholder secrets of the seeded connections (they authenticate nowhere).
const (
	ghcrPlaceholder   = "devstack-placeholder-ghcr-token"   //nolint:gosec // G101: a published placeholder of the local devstack
	hubPlaceholder    = "devstack-placeholder-hub-password" //nolint:gosec // G101: a published placeholder of the local devstack
	mirrorPlaceholder = "devstack-placeholder-mirror"       //nolint:gosec // G101: a published placeholder of the local devstack
	gitPlaceholder    = "devstack-placeholder-github-token" //nolint:gosec // G101: a published placeholder of the local devstack
)

// seedCredentialsAndBuilds gives the registries and builds screens (#19,
// #33) something to show: registry connections (one bound to homelab, one
// revoked), a Git credential, a saved build definition and two past builds
// on homelab. The secrets are placeholders: connection tests against the
// real registries answer "unauthorized" or "unavailable", which is the
// point of the demo. Build records are inserted directly because the
// devstack cannot run BuildKit (their job logs are therefore absent).
func (s *seeder) seedCredentialsAndBuilds(ctx context.Context, owner *apiClient) error {
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": ownerPassword}, nil); err != nil {
		return err
	}
	hl := s.envs["homelab"]
	type created struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	var ghcr, hub, old created
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/registries", map[string]any{"name": "GHCR (silo pull token)", "host": "ghcr.io",
		"username": "silo-bot", "secret": ghcrPlaceholder, "repositoryPattern": "silo/*", "priority": 10}, &ghcr); err != nil {
		return err
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/registries", map[string]any{"name": "Docker Hub (homelab)", "host": "docker.io",
		"credentialType": "password", "username": "homelab", "secret": hubPlaceholder, "environmentId": hl}, &hub); err != nil {
		return err
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/registries", map[string]any{"name": "Old registry mirror", "host": "registry.homelab.lan:5000",
		"username": "mirror", "secret": mirrorPlaceholder, "plainHttp": true}, &old); err != nil {
		return err
	}
	if _, err := owner.do(ctx, http.MethodPatch, "/api/v1/registries/"+old.ID, map[string]any{"status": "revoked"}, nil,
		"If-Match", fmt.Sprintf(`"%d"`, old.Revision)); err != nil {
		return err
	}
	var git created
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/git-credentials", map[string]any{"name": "GitHub (silo builds)", "host": "github.com",
		"pathPrefix": "silo", "username": "x-access-token", "secret": gitPlaceholder}, &git); err != nil {
		return err
	}
	var def created
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/environments/"+hl+"/build-definitions", map[string]any{"name": "silo-web",
		"description": "The Silo web frontend from its Git repository", "source": map[string]any{"gitUrl": "https://github.com/silo/web.git",
			"ref": "main", "dockerfile": "Dockerfile", "target": "runtime", "tags": []string{"ghcr.io/silo/web:latest"},
			"buildArgs": map[string]string{"NODE_VERSION": "22"}, "gitCredentialId": git.ID}}, &def); err != nil {
		return err
	}

	homelab := s.hosts[0]
	img, err := homelab.engine.InspectImage(ctx, "ghcr.io/silo/web:latest")
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	builds := []domain.ImageBuild{
		{GitURL: "https://github.com/silo/web.git", Ref: "main", Dockerfile: "Dockerfile", Target: "runtime",
			Tags: []string{"ghcr.io/silo/web:latest"}, BuildArgKeys: []string{"NODE_VERSION"}, GitCredentialID: git.ID, DefinitionID: def.ID,
			Status: domain.BuildSucceeded, ResolvedRef: "refs/heads/main", ResolvedCommit: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
			ImageID: img.ID, CreatedAt: *at(50 * time.Hour), StartedAt: at(50 * time.Hour), FinishedAt: at(50*time.Hour - 3*time.Minute - 12*time.Second)},
		{GitURL: "https://github.com/silo/web.git", Ref: "feature/uploads", Dockerfile: "Dockerfile", Tags: []string{"ghcr.io/silo/web:uploads"},
			GitCredentialID: git.ID, Status: domain.BuildFailed, ResolvedRef: "refs/heads/feature/uploads",
			ResolvedCommit: "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432", ErrorClass: "build_failed",
			ErrorMessage: "failed to solve: process \"/bin/sh -c npm ci\" did not complete successfully: exit code 1",
			CreatedAt:    *at(26 * time.Hour), StartedAt: at(26 * time.Hour), FinishedAt: at(26*time.Hour - 48*time.Second)},
	}
	for i := range builds {
		b := &builds[i]
		b.ID = ids.New()
		b.JobID, b.EnvironmentID, b.RegistryConnectionIDs = b.ID, hl, []string{}
		if err := store.InsertImageBuild(ctx, s.m.DB(), b); err != nil {
			return err
		}
	}
	s.log.Info("seeded registry connections, a Git credential, a build definition and builds", "registry_id", ghcr.ID, "hub_id", hub.ID)
	return nil
}
