package stackarchives

import (
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func codes(fs []migrations.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// TestEvaluateExport: what an export includes and what blocks it.
func TestEvaluateExport(t *testing.T) {
	f := &protocol.MigrationProjectFacts{Name: "web", DirBytes: 100, DirEntries: 3,
		Services: []protocol.MigrationServiceFacts{{Name: "app", Running: true}, {Name: "worker"}},
		Volumes: []protocol.MigrationVolumeFacts{
			{Key: "data", Name: "web_data", Exists: true, Supported: true, Bytes: 1000, Labels: map[string]string{"com.docker.compose.project": "web", "backup.exclude": "true"}},
			{Key: "fixed", Name: "shared-data", Exists: true, Supported: true, Bytes: 10},
			{Key: "cache", Name: "web_cache", Exists: true, Supported: true, Bytes: 5},
			{Key: "nfs", Name: "web_nfs", Exists: true, Supported: false, Reason: "driver options"},
			{Key: "ext", Name: "outside", External: true},
			{Key: "new", Name: "web_new"},
			{Key: "dm", Name: "web_dm", Exists: true, Supported: true, Protected: true},
			{Name: "3f2a", Anonymous: true, Exists: true},
		},
		Binds: []protocol.ComposeBind{{Service: "app", Source: "/srv/shared", External: true}, {Service: "app", Source: "/x/web/conf", RelPath: "conf"}},
	}
	plan := ExportPlan{MaxBytes: 1 << 20, ManagerFree: 10 << 30}
	evaluateExport(&plan, f, ExportRequest{ExcludeVolumes: []string{"cache"}})
	if !plan.Allowed() {
		t.Fatalf("blockers %+v", plan.Blockers)
	}
	type row struct {
		key       string
		included  bool
		excluded  bool
		hasReason bool
		follows   bool
	}
	var got []row
	for _, v := range plan.Volumes {
		got = append(got, row{v.Key, v.Included, v.Excluded, v.Reason != "", v.FollowsProject})
	}
	want := []row{{"data", true, false, false, true}, {"fixed", true, false, false, false}, {"cache", false, true, false, true},
		{"nfs", false, false, true, true}, {"ext", false, false, true, false}, {"new", false, false, true, true}, {"dm", false, false, true, true}}
	if !slices.Equal(got, want) {
		t.Fatalf("volumes\n got %+v\nwant %+v", got, want)
	}
	if l := plan.Volumes[0].Labels; len(l) != 1 || l["backup.exclude"] != "true" {
		t.Errorf("user labels %v", l)
	}
	if plan.VolumeBytes != 1010 || plan.TotalBytes != 1110 || !slices.Equal(plan.Running, []string{"app"}) || plan.DowntimeSeconds != 10 {
		t.Errorf("sizes %d/%d, running %v, downtime %d", plan.VolumeBytes, plan.TotalBytes, plan.Running, plan.DowntimeSeconds)
	}
	kinds := []string{}
	for _, e := range plan.NotIncluded {
		kinds = append(kinds, e.Kind+":"+e.Name)
	}
	if !slices.Equal(kinds, []string{"anonymous_volume:3f2a", "bind:/srv/shared"}) {
		t.Errorf("not included %v", kinds)
	}
	if c := codes(plan.Warnings); !slices.Equal(c, []string{migrations.FindingVolumeDefinitionOnly, migrations.FindingAnonymousVolume, migrations.FindingExternalBind}) {
		t.Errorf("warnings %v", c)
	}

	t.Run("blocked", func(t *testing.T) {
		plan := ExportPlan{MaxBytes: 500, ManagerFree: 600}
		evaluateExport(&plan, &protocol.MigrationProjectFacts{Name: "web", DirBytes: 1000, Protected: true}, ExportRequest{})
		if c := codes(plan.Blockers); !slices.Equal(c, []string{migrations.FindingDockerManagerResource, FindingArchiveTooLarge, FindingManagerSpace}) {
			t.Errorf("blockers %v", c)
		}
		if plan.DowntimeSeconds != 0 {
			t.Errorf("nothing runs, downtime %d", plan.DowntimeSeconds)
		}
	})
}

func shopManifest() Manifest {
	return Manifest{Format: FormatName, Version: FormatVersion, Stack: ManifestStack{Name: "shop"},
		Volumes: []ManifestVolume{{Key: "dbdata", Name: "shop_dbdata", FollowsProject: true}, {Key: "shared", Name: "global-shared"}},
		NotIncluded: []Exclusion{{Kind: ExcludedVolume, Key: "cache", Name: "shop_cache", Reason: "left out on export"},
			{Kind: ExcludedBind, Name: "/srv/shared", Reason: "outside the project folder"}},
		Services: []ManifestService{
			{Name: "db", Image: "postgres:17", Registry: true, ContainerNames: []string{"shop-db-1"}},
			{Name: "web", Image: "shop-web:local", ContainerNames: []string{"shop-web-1", "shop-web-2"}, Ports: []protocol.MigrationPort{{Published: 8080, Target: 80, Protocol: "tcp"}}},
			{Name: "proxy", Image: "nginx", Build: true, ContainerNames: []string{"edge-proxy"}},
		},
		Networks:        []ManifestNetwork{{Key: "default", Name: "shop_default"}, {Key: "front", Name: "frontend", External: true}, {Key: "own", Name: "named-net"}},
		ExternalVolumes: []string{"certs"}}
}

// TestDestinationQueryRenames: what Compose names after the project
// follows the new name; explicit names stay.
func TestDestinationQueryRenames(t *testing.T) {
	q := destinationQuery(shopManifest(), "boutique")
	if q.ProjectName != "boutique" || q.Dir != "boutique" {
		t.Fatalf("query %+v", q)
	}
	if !slices.Equal(q.ContainerNames, []string{"boutique-db-1", "boutique-web-1", "boutique-web-2", "edge-proxy"}) {
		t.Errorf("containers %v", q.ContainerNames)
	}
	if !slices.Equal(q.Volumes, []string{"boutique_dbdata", "global-shared", "boutique_cache"}) {
		t.Errorf("volumes %v", q.Volumes)
	}
	if !slices.Equal(q.Networks, []string{"boutique_default", "named-net"}) || !slices.Equal(q.ExternalNetworks, []string{"frontend"}) {
		t.Errorf("networks %v / %v", q.Networks, q.ExternalNetworks)
	}
	if !slices.Equal(q.ExternalVolumes, []string{"certs"}) || !slices.Equal(q.Images, []string{"nginx", "postgres:17", "shop-web:local"}) || len(q.Ports) != 1 {
		t.Errorf("external %v, images %v, ports %v", q.ExternalVolumes, q.Images, q.Ports)
	}
	same := destinationQuery(shopManifest(), "shop")
	if !slices.Equal(same.Volumes, []string{"shop_dbdata", "global-shared", "shop_cache"}) {
		t.Errorf("same name: volumes %v", same.Volumes)
	}
}

// TestEvaluateImport: the destination's facts become blockers and
// warnings; free space counts the archive's parts.
func TestEvaluateImport(t *testing.T) {
	u := Upload{ID: "a1", Manifest: shopManifest(), Project: PartStats{Bytes: 100},
		Volumes: map[string]PartStats{"dbdata": {Bytes: 1000}, "shared": {Bytes: 10}}}
	ok := evaluateImport(importFacts{upload: u, name: "boutique", online: true, supported: true,
		dest: &protocol.MigrationDestinationFacts{StacksOK: true, VolumesOK: true, StacksFree: 1 << 30, VolumesFree: 1 << 30,
			ImagesPresent: []string{"shop-web:local"}}})
	if !ok.Allowed() || ok.VolumeBytes != 1010 || len(ok.Volumes) != 2 || ok.Volumes[0].Name != "boutique_dbdata" || ok.Volumes[1].Name != "global-shared" {
		t.Fatalf("plan %+v", ok)
	}
	if c := codes(ok.Warnings); !slices.Equal(c, []string{FindingVolumeMissing, migrations.FindingExternalBind}) {
		t.Errorf("warnings %v (the image is present)", c)
	}

	bad := evaluateImport(importFacts{upload: u, name: "boutique", online: true, supported: true, nameErr: domain.ErrStackNameTaken,
		dest: &protocol.MigrationDestinationFacts{StacksOK: true, VolumesOK: false, Reason: "the volume directory is not verified",
			StacksFree: 50, VolumesFree: 500, DirExists: true, Containers: []string{"boutique-db-1"},
			Volumes: []protocol.MigrationExistingVolume{{Name: "boutique_dbdata"}}, Networks: []string{"boutique_default"},
			MissingNetworks: []string{"frontend"}, MissingVolumes: []string{"certs"},
			PortConflicts: []protocol.MigrationPortConflict{{Port: protocol.MigrationPort{Published: 8080, Protocol: "tcp"}, Container: "other"}}}})
	want := []string{migrations.FindingStackNameConflict, migrations.FindingStorageUnavailable, migrations.FindingDirectoryConflict,
		migrations.FindingContainerConflict, migrations.FindingVolumeConflict, migrations.FindingNetworkConflict,
		migrations.FindingExternalNetwork, migrations.FindingExternalVolume, migrations.FindingPortConflict,
		migrations.FindingInsufficientSpace, migrations.FindingInsufficientSpace}
	if c := codes(bad.Blockers); !slices.Equal(c, want) {
		t.Errorf("blockers\n got %v\nwant %v", c, want)
	}
	if !slices.Contains(codes(bad.Warnings), FindingImageMissing) {
		t.Errorf("warnings %v", codes(bad.Warnings))
	}

	offline := evaluateImport(importFacts{upload: u, name: "boutique"})
	if c := codes(offline.Blockers); !slices.Equal(c, []string{migrations.FindingEnvironmentOffline}) {
		t.Errorf("offline: %v", c)
	}
	running := evaluateImport(importFacts{upload: u, name: "boutique", online: true, supported: true,
		nameErr: &domain.StackError{Code: domain.StackErrProjectExists}, dest: &protocol.MigrationDestinationFacts{StacksOK: true, VolumesOK: true,
			StacksFree: -1, VolumesFree: -1, ProjectContainers: []string{"boutique-db-1"}}})
	if c := codes(running.Blockers); !slices.Equal(c, []string{migrations.FindingProjectNameConflict}) {
		t.Errorf("running project: %v", c)
	}
}
