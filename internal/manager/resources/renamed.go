package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// StackRenamed follows a stack rename (#7; stacks.Service.OnRenamed, in the
// finishing transaction): the volumes the rename moved have new names, so
// the saved recreate specifications of the environment's Docker
// Manager-managed standalone containers (#6) that mount them are rewritten to
// the new names. The agent recreated those containers with the same name
// and labels (the spec label still points at their specification), so
// nothing else changes. Environment values stay sealed; only mount sources
// are rewritten.
func (s *Service) StackRenamed(ctx context.Context, db bun.IDB, r domain.StackRenamed) error {
	if len(r.Volumes) == 0 {
		return nil
	}
	ms, err := store.ListManagedContainers(ctx, db, r.EnvironmentID)
	if err != nil {
		return err
	}
	for _, m := range ms {
		_, sealed, err := store.GetManagedContainer(ctx, db, m.ID)
		if err != nil {
			return err
		}
		b, err := s.opts.Keyring.Open(sealed, "managed_containers/"+m.ID+"/spec")
		if err != nil {
			return fmt.Errorf("resources: open recreate specification: %w", err)
		}
		var spec protocol.ContainerSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			return fmt.Errorf("resources: decode recreate specification: %w", err)
		}
		if !renameMounts(&spec, r.Volumes) {
			continue
		}
		resealed, err := s.seal(m.ID, spec)
		if err != nil {
			return err
		}
		if err := store.UpdateManagedContainerSpec(ctx, db, m.ID, resealed, s.clk.Now()); err != nil {
			return err
		}
		s.log.Info("recreate specification follows a stack rename", "spec_id", m.ID, "container", m.Name,
			"stack_id", r.StackID, "from", r.From, "to", r.To)
	}
	return nil
}

// renameMounts rewrites the sources of volume mounts named in moved (former
// name -> new name) and reports whether any changed.
func renameMounts(spec *protocol.ContainerSpec, moved map[string]string) bool {
	changed := false
	for i, mt := range spec.Mounts {
		if mt.Type != "volume" {
			continue
		}
		if to, ok := moved[mt.Source]; ok && to != "" {
			spec.Mounts[i].Source = to
			changed = true
		}
	}
	return changed
}
