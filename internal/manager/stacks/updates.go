package stacks

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// RecordUpdatedImages records the images a digest update (#20) applied to
// a stack's services — the new baseline of GET /stacks/{id}/image-status
// and of the next check — and the Engine state after the update, inside
// the update job's finishing transaction (db). The applied revision is
// unchanged: an update is a runtime deployment of the same definition
// bytes, never a source revision. A stack removed meanwhile is ignored.
func (s *Service) RecordUpdatedImages(ctx context.Context, db bun.IDB, stackID string, images []domain.StackImage, after []domain.StackServiceState) error {
	st, err := store.GetStack(ctx, db, stackID)
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, img := range images {
		replaced := false
		for i := range st.Images {
			if st.Images[i].Service == img.Service {
				st.Images[i], replaced = img, true
			}
		}
		if !replaced {
			st.Images = append(st.Images, img)
		}
	}
	if after != nil {
		s.setEngine(&st, after)
	}
	st.UpdatedAt = s.now()
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		return err
	}
	s.publish(EventUpdated, st, map[string]string{"change": "images_updated"})
	return nil
}
