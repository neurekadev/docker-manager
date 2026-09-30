package migrations

import (
	"context"
	"fmt"
	"slices"

	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// RetainedSource is the stopped source of a stack migration that Docker Manager
// keeps until the user confirms its removal (#35). Once the stack record
// moved, the source project is no longer a Docker Manager stack: without this
// hold a prune (#14) could remove its stopped containers, networks and
// (with the volume opt-in) its volumes, and the Docker resource routes
// (#6) would let a user delete them one by one, before "remove from
// source" was confirmed. The hold ends when the source removal succeeds.
// The project directory is never touched by either (only
// stack.remove_source deletes it).
type RetainedSource struct {
	MigrationID string
	StackID     string
	// Project is the source's Compose project name.
	Project string
	// Volumes are the source volumes the migration named (copied or not).
	Volumes []string
	// Reason is shown in prune previews and results and in refusals
	// (at most protocol.MaxPruneReasonLen bytes).
	Reason string
}

// RetainedSources lists the migrated stacks' sources Docker Manager keeps in an
// environment. Running migrations are included: the cut-over can happen
// at any moment while they run, and until then the stack itself protects
// the same project.
func (s *Service) RetainedSources(ctx context.Context, environmentID string) ([]RetainedSource, error) {
	ms, err := store.RetainedMigrationSources(ctx, s.db, environmentID)
	if err != nil {
		return nil, err
	}
	out := make([]RetainedSource, 0, len(ms))
	for _, m := range ms {
		if m.Source.Project == "" {
			continue
		}
		r := RetainedSource{MigrationID: m.ID, StackID: m.StackID, Project: m.Source.Project,
			Reason: fmt.Sprintf("part of the stopped source of migrated stack %q, kept until its removal is confirmed", m.Source.Project)}
		for _, v := range m.Volumes {
			if v.Source != "" && !slices.Contains(r.Volumes, v.Source) {
				r.Volumes = append(r.Volumes, v.Source)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// RetainedProjects maps the Compose projects of RetainedSources to their
// reason (the Docker resource service's hold, resources.RetainedProjects).
func (s *Service) RetainedProjects(ctx context.Context, environmentID string) (map[string]string, error) {
	rs, err := s.RetainedSources(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rs))
	for _, r := range rs {
		if _, dup := out[r.Project]; !dup {
			out[r.Project] = r.Reason
		}
	}
	return out, nil
}
