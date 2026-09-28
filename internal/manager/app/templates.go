package app

import (
	"context"
	"errors"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/templates"
)

// templateSource serves template versions to the stack service (stacks
// created from templates): this instance's own templates and those of
// added registries.
type templateSource struct {
	own        *templates.Service
	instanceID string
}

func (t templateSource) TemplateArchive(ctx context.Context, instanceID, templateID string, version int) (stacks.TemplateArchive, error) {
	if instanceID != t.instanceID {
		// A template of an added registry: downloaded now, checked against
		// the cached index's digest.
		rt, v, archive, err := t.own.RegistryArchive(ctx, instanceID, templateID, version)
		if errors.Is(err, domain.ErrTemplateRegistryNotFound) {
			return stacks.TemplateArchive{}, domain.ErrTemplateNotFound
		}
		if err != nil {
			return stacks.TemplateArchive{}, err
		}
		return stacks.TemplateArchive{
			Ref:     domain.StackTemplateRef{InstanceID: instanceID, TemplateID: templateID, Name: rt.Name, Version: v.Number, VersionLabel: v.Label},
			Archive: archive, SHA256: v.ArchiveSHA256, Links: rt.Links,
		}, nil
	}
	tm, err := t.own.Get(ctx, templateID)
	if err != nil {
		return stacks.TemplateArchive{}, err
	}
	v, archive, err := t.own.Archive(ctx, templateID, version)
	if err != nil {
		return stacks.TemplateArchive{}, err
	}
	return stacks.TemplateArchive{
		Ref:     domain.StackTemplateRef{InstanceID: instanceID, TemplateID: tm.ID, Name: tm.Name, Version: v.Number, VersionLabel: v.Label},
		Archive: archive, SHA256: v.ArchiveSHA256, Links: tm.Links,
	}, nil
}
