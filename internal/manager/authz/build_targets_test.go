package authz

import (
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
)

// TestBuildDefinitionCoversItsImages: a build definition run (#33) is
// authorized on the definition, whose tags it produces; a manual build on
// its image targets.
func TestBuildDefinitionCoversItsImages(t *testing.T) {
	run := TargetResources("e1", []domain.JobTarget{{Type: domain.TargetBuildDefinition, ID: "def-1"},
		{Type: domain.TargetImage, ID: "acme/app:1"}})
	if len(run) != 1 || run[0].Type != "build_definition" || run[0].ID != "def-1" || run[0].EnvironmentID != "e1" {
		t.Fatalf("run resources %+v", run)
	}
	manual := TargetResources("e1", []domain.JobTarget{{Type: domain.TargetImage, ID: "acme/app:1"}, {Type: domain.TargetImage, ID: "acme/app:latest"}})
	if len(manual) != 2 || manual[0].Type != "image" {
		t.Fatalf("manual resources %+v", manual)
	}
}
