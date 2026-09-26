package api

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
)

// emptyBuilds is a BuildService without builds or definitions, for route
// authorization tests (#33).
type emptyBuilds struct{}

func (emptyBuilds) Start(context.Context, authz.Principal, string, domain.BuildSource, string, string) (domain.ImageBuild, domain.Job, error) {
	return domain.ImageBuild{}, domain.Job{}, domain.ErrEnvironmentNotFound
}

func (emptyBuilds) Get(context.Context, string) (domain.ImageBuild, error) {
	return domain.ImageBuild{}, domain.ErrImageBuildNotFound
}

func (emptyBuilds) List(context.Context, string, string, string, int) ([]domain.ImageBuild, error) {
	return nil, nil
}

func (emptyBuilds) CreateDefinition(context.Context, string, string, string, domain.BuildSource) (domain.BuildDefinition, error) {
	return domain.BuildDefinition{}, domain.ErrEnvironmentNotFound
}

func (emptyBuilds) GetDefinition(context.Context, string) (domain.BuildDefinition, error) {
	return domain.BuildDefinition{}, domain.ErrBuildDefinitionNotFound
}

func (emptyBuilds) ListDefinitions(context.Context, string, string, int) ([]domain.BuildDefinition, error) {
	return nil, nil
}

func (emptyBuilds) UpdateDefinition(context.Context, string, int64, domain.BuildDefinitionPatch) (domain.BuildDefinition, error) {
	return domain.BuildDefinition{}, domain.ErrBuildDefinitionNotFound
}

func (emptyBuilds) DeleteDefinition(context.Context, string, int64) error {
	return domain.ErrBuildDefinitionNotFound
}

func (emptyBuilds) RunDefinition(context.Context, authz.Principal, string, string) (domain.ImageBuild, domain.Job, error) {
	return domain.ImageBuild{}, domain.Job{}, domain.ErrBuildDefinitionNotFound
}

// sampleBodies are valid request bodies of routes with required fields,
// so authorization (not validation) decides in route sweeps.
var sampleBodies = map[string]any{
	"create-prune-preview": map[string]any{"rules": []map[string]any{{"category": "dangling_images", "enabled": true, "minAgeHours": 24}}},
	"create-prune": map[string]any{"rules": []map[string]any{{"category": "dangling_images", "enabled": true, "minAgeHours": 24}},
		"confirm": true},
	"create-image-build": map[string]any{"gitUrl": "https://git.example.com/acme/app.git", "tags": []string{"acme/app:1"}},
	"create-build-definition": map[string]any{"name": "app", "source": map[string]any{
		"gitUrl": "https://git.example.com/acme/app.git", "tags": []string{"acme/app:1"}}},
}
