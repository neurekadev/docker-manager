package app

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/templates"
)

// templateSource serves template versions to the stack service (stacks
// created from templates): this instance's own templates.
type templateSource struct {
	own        *templates.Service
	instanceID string
}

func (t templateSource) TemplateArchive(ctx context.Context, instanceID, templateID string, version int) (stacks.TemplateArchive, error) {
	if instanceID != t.instanceID {
		return stacks.TemplateArchive{}, domain.ErrTemplateNotFound
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
		Archive: archive, SHA256: v.ArchiveSHA256,
	}, nil
}
