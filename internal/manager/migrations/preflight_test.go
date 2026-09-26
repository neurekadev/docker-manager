package migrations

import (
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

func basePreflight() PreflightInput {
	src := &protocol.MigrationSourceFacts{Platform: "linux/amd64", Project: &protocol.MigrationProjectFacts{
		Name: "shop", Dir: "/stacks/shop",
		Services: []protocol.MigrationServiceFacts{
			{Name: "db", Image: "postgres:17", ImageID: "sha256:a", ImagePlatform: "linux/amd64", RepoDigests: []string{"postgres@sha256:1"},
				ContainerNames: []string{"shop-db-1"}, Running: true},
			{Name: "web", Image: "registry.example/shop/web:1", ImageID: "sha256:b", ImagePlatform: "linux/amd64", ImageSize: 5000,
				RepoDigests: []string{"registry.example/shop/web@sha256:2"}, ContainerNames: []string{"shop-web-1"}, Running: true,
				Ports: []protocol.MigrationPort{{Published: 8080, Target: 80, Protocol: "tcp"}}},
		},
		Volumes:  []protocol.MigrationVolumeFacts{{Key: "dbdata", Name: "shop_dbdata", Exists: true, Supported: true, Driver: "local", Bytes: 1000, Entries: 3}},
		Networks: []protocol.MigrationNetworkFacts{{Key: "default", Name: "shop_default"}},
		Binds:    []protocol.ComposeBind{{Service: "web", Source: "/stacks/shop/data", Target: "/srv/data", RelPath: "data"}},
		DirBytes: 100, DirEntries: 5}}
	dst := &protocol.MigrationDestinationFacts{Platform: "linux/amd64", StacksOK: true, VolumesOK: true, StacksFree: 1 << 30, VolumesFree: 1 << 30}
	return PreflightInput{Kind: domain.MigrationKindStack, StackID: "s1", SourceEnvironmentID: "e1", TargetEnvironmentID: "e2",
		SourceOnline: true, TargetOnline: true, SourceSupports: true, TargetSupports: true, Source: src, Target: dst, TargetDir: "shop",
		Registry: map[string]RegistryCheck{"postgres:17": {}, "registry.example/shop/web:1": {ConnectionID: "reg-1"}}}
}

func baseVolumePreflight() PreflightInput {
	in := basePreflight()
	in.Kind = domain.MigrationKindVolume
	in.Source = &protocol.MigrationSourceFacts{Platform: "linux/amd64", Volume: &protocol.MigrationVolumeFacts{Name: "media", Exists: true,
		Supported: true, Driver: "local", Bytes: 4096, Entries: 2,
		UsedBy: []protocol.MigrationContainerUse{{Name: "photos", Running: false}}}}
	return in
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// TestPreflightCorpus (#35 Done-when 2): the preview blocks or warns
// before any downtime on platform mismatches, image availability, port
// and name conflicts, missing external networks and volumes, external
// bind paths, devices, non-local drivers, free space, transport and
// Docker Manager's own resources.
func TestPreflightCorpus(t *testing.T) {
	web := func(in *PreflightInput) *protocol.MigrationServiceFacts { return &in.Source.Project.Services[1] }
	cases := []struct {
		name     string
		mutate   func(in *PreflightInput)
		blockers []string
		warnings []string
		check    func(t *testing.T, p Plan)
	}{
		{name: "clean", mutate: func(*PreflightInput) {}, warnings: []string{FindingHostPortsUnknown},
			check: func(t *testing.T, p Plan) {
				if p.Services[0].Action != ImagePull || p.Services[1].RegistryConnectionID != "reg-1" || p.Volumes[0].Action != VolumeCopy {
					t.Errorf("plan %+v %+v", p.Services, p.Volumes)
				}
				if p.Data.TotalBytes != 100+1000+8*perEntryOverhead || p.Downtime.EstimatedSeconds < 50 {
					t.Errorf("data %+v downtime %+v", p.Data, p.Downtime)
				}
			}},
		{name: "local image to another platform", mutate: func(in *PreflightInput) {
			web(in).RepoDigests = nil
			in.Target.Platform = "linux/arm64"
		}, blockers: []string{FindingPlatformMismatch}},
		{name: "local image copied", mutate: func(in *PreflightInput) { web(in).RepoDigests = nil },
			warnings: []string{FindingImageTransfer}, check: func(t *testing.T, p Plan) {
				if p.Services[1].Action != ImageTransfer || p.Data.ImageBytes != 5000 {
					t.Errorf("plan %+v data %+v", p.Services[1], p.Data)
				}
			}},
		{name: "registry lacks the platform", mutate: func(in *PreflightInput) {
			in.Target.Platform = "linux/arm64"
			in.Registry["registry.example/shop/web:1"] = RegistryCheck{Class: "platform_not_found"}
		}, blockers: []string{FindingPlatformMismatch}},
		{name: "aarch64 is arm64", mutate: func(in *PreflightInput) {
			in.Source.Platform, in.Target.Platform = "linux/aarch64", "linux/arm64"
			web(in).ImagePlatform = "linux/arm64/v8"
			web(in).RepoDigests = nil
		}, warnings: []string{FindingImageTransfer}},
		{name: "single-platform image", mutate: func(in *PreflightInput) {
			in.Target.Platform = "linux/arm64"
			in.Registry["registry.example/shop/web:1"] = RegistryCheck{SinglePlatform: true}
		}, warnings: []string{FindingPlatformMismatch}},
		{name: "rebuilt from build section", mutate: func(in *PreflightInput) {
			web(in).Build = true
			in.Target.Platform = "linux/arm64"
		}, warnings: []string{FindingImageRebuild}, check: func(t *testing.T, p Plan) {
			if !strings.Contains(p.Warnings[slices.IndexFunc(p.Warnings, func(f Finding) bool { return f.Code == FindingImageRebuild })].Message, "linux/arm64") {
				t.Errorf("warnings %+v", p.Warnings)
			}
		}},
		{name: "image not in its registry", mutate: func(in *PreflightInput) {
			in.Registry["registry.example/shop/web:1"] = RegistryCheck{Class: "not_found"}
		}, blockers: []string{FindingImageNotPullable}},
		{name: "registry rate limited", mutate: func(in *PreflightInput) {
			in.Registry["registry.example/shop/web:1"] = RegistryCheck{Class: "rate_limited"}
		}, warnings: []string{FindingImageUnverified}},
		{name: "ambiguous registry connection", mutate: func(in *PreflightInput) {
			in.Registry["registry.example/shop/web:1"] = RegistryCheck{Class: "ambiguous_registry_connection", Message: "two connections"}
		}, blockers: []string{FindingRegistrySelection}},
		{name: "image transfer selected", mutate: func(in *PreflightInput) {
			in.Selection.TransferImages = []string{"registry.example/shop/web:1"}
		}, warnings: []string{FindingImageTransfer}},
		{name: "local image already on the destination", mutate: func(in *PreflightInput) {
			web(in).RepoDigests = nil
			in.Target.ImagesPresent = []string{"registry.example/shop/web:1"}
			in.Target.Platform = "linux/arm64"
		}, check: func(t *testing.T, p Plan) {
			if p.Services[1].Action != ImagePresent {
				t.Errorf("plan %+v", p.Services[1])
			}
		}},
		{name: "image already on the destination", mutate: func(in *PreflightInput) {
			in.Target.ImagesPresent = []string{"postgres:17"}
			delete(in.Registry, "postgres:17")
		}, check: func(t *testing.T, p Plan) {
			if p.Services[0].Action != ImagePresent {
				t.Errorf("plan %+v", p.Services[0])
			}
		}},
		{name: "port conflict", mutate: func(in *PreflightInput) {
			in.Target.PortConflicts = []protocol.MigrationPortConflict{{Port: protocol.MigrationPort{Published: 8080, Protocol: "tcp"}, Container: "proxy"}}
		}, blockers: []string{FindingPortConflict}},
		{name: "stack name taken", mutate: func(in *PreflightInput) { in.StackNameTaken = true }, blockers: []string{FindingStackNameConflict}},
		{name: "compose project exists", mutate: func(in *PreflightInput) {
			in.Target.ProjectContainers = []string{"shop-web-1"}
			in.Target.Containers = []string{"shop-web-1"}
		}, blockers: []string{FindingContainerConflict, FindingProjectNameConflict}},
		{name: "container name taken", mutate: func(in *PreflightInput) { in.Target.Containers = []string{"shop-db-1"} },
			blockers: []string{FindingContainerConflict}},
		{name: "volume name taken", mutate: func(in *PreflightInput) {
			in.Target.Volumes = []protocol.MigrationExistingVolume{{Name: "shop_dbdata"}}
		}, blockers: []string{FindingVolumeConflict}},
		{name: "volume left by an earlier migration", mutate: func(in *PreflightInput) {
			in.Target.Volumes = []protocol.MigrationExistingVolume{{Name: "shop_dbdata", Migration: "m-old"}}
			in.Target.DirExists = true
			in.Leftovers = []domain.Migration{{ID: "m-old", TargetDir: "shop", Volumes: []domain.MigrationVolume{{Source: "shop_dbdata", Target: "shop_dbdata"}}}}
		}, warnings: []string{FindingLeftovers}},
		{name: "volume of another migration", mutate: func(in *PreflightInput) {
			in.Target.Volumes = []protocol.MigrationExistingVolume{{Name: "shop_dbdata", Migration: "m-other"}}
		}, blockers: []string{FindingVolumeConflict}},
		{name: "network name taken", mutate: func(in *PreflightInput) { in.Target.Networks = []string{"shop_default"} },
			blockers: []string{FindingNetworkConflict}},
		{name: "directory exists", mutate: func(in *PreflightInput) { in.Target.DirExists = true }, blockers: []string{FindingDirectoryConflict}},
		{name: "missing external network", mutate: func(in *PreflightInput) {
			in.Source.Project.Networks = append(in.Source.Project.Networks, protocol.MigrationNetworkFacts{Key: "corp", Name: "corp", External: true})
			in.Target.MissingNetworks = []string{"corp"}
		}, blockers: []string{FindingExternalNetwork}},
		{name: "missing external volume", mutate: func(in *PreflightInput) {
			in.Source.Project.Volumes = append(in.Source.Project.Volumes, protocol.MigrationVolumeFacts{Key: "media", Name: "media", External: true, Exists: true})
			in.Target.MissingVolumes = []string{"media"}
		}, blockers: []string{FindingExternalVolume}, check: func(t *testing.T, p Plan) {
			if p.Volumes[1].Action != VolumeExternal {
				t.Errorf("volumes %+v", p.Volumes)
			}
		}},
		{name: "external bind path", mutate: func(in *PreflightInput) {
			in.Source.Project.Binds = append(in.Source.Project.Binds, protocol.ComposeBind{Service: "web", Source: "/srv/shared", External: true})
		}, warnings: []string{FindingExternalBind}},
		{name: "device mapping", mutate: func(in *PreflightInput) { web(in).Devices = []string{"/dev/ttyUSB0"} },
			warnings: []string{FindingDevice}},
		{name: "non-local volume driver", mutate: func(in *PreflightInput) {
			v := &in.Source.Project.Volumes[0]
			v.Supported, v.Driver, v.Reason = false, "rexray", `volume driver "rexray": only local volumes`
		}, warnings: []string{FindingVolumeDefinitionOnly}, check: func(t *testing.T, p Plan) {
			if p.Volumes[0].Action != VolumeDefinitionOnly || p.Data.VolumeBytes != 0 {
				t.Errorf("volumes %+v", p.Volumes)
			}
		}},
		{name: "anonymous volume skipped", mutate: func(in *PreflightInput) {
			in.Source.Project.Volumes = append(in.Source.Project.Volumes, protocol.MigrationVolumeFacts{Name: "4f1e", Anonymous: true, Exists: true, Supported: true})
		}, warnings: []string{FindingAnonymousVolume}, check: func(t *testing.T, p Plan) {
			if p.Volumes[1].Action != VolumeSkip {
				t.Errorf("volumes %+v", p.Volumes)
			}
		}},
		{name: "anonymous volume selected", mutate: func(in *PreflightInput) {
			in.Source.Project.Volumes = append(in.Source.Project.Volumes, protocol.MigrationVolumeFacts{Name: "4f1e", Anonymous: true, Exists: true, Supported: true, Bytes: 7})
			in.Selection.AnonymousVolumes = []string{"4f1e"}
		}, warnings: []string{FindingAnonymousVolume}, check: func(t *testing.T, p Plan) {
			if p.Volumes[1].Action != VolumeCopy || p.Data.VolumeBytes != 1007 {
				t.Errorf("volumes %+v", p.Volumes)
			}
		}},
		{name: "volume excluded", mutate: func(in *PreflightInput) { in.Selection.ExcludeVolumes = []string{"shop_dbdata"} },
			check: func(t *testing.T, p Plan) {
				if p.Volumes[0].Action != VolumeSkip || p.Data.VolumeBytes != 0 {
					t.Errorf("volumes %+v", p.Volumes)
				}
			}},
		{name: "not enough space for the project", mutate: func(in *PreflightInput) { in.Target.StacksFree = 10 },
			blockers: []string{FindingInsufficientSpace}},
		{name: "not enough space for the volumes", mutate: func(in *PreflightInput) { in.Target.VolumesFree = 999 },
			blockers: []string{FindingInsufficientSpace}},
		{name: "unknown free space", mutate: func(in *PreflightInput) { in.Target.StacksFree, in.Target.VolumesFree = -1, -1 }},
		{name: "size estimated", mutate: func(in *PreflightInput) { in.Source.Project.Volumes[0].Truncated = true },
			warnings: []string{FindingSizeEstimated}},
		{name: "plain HTTP", mutate: func(in *PreflightInput) { in.TargetPlainHTTP = true }, warnings: []string{FindingPlainHTTP}},
		{name: "Docker Manager's own project", mutate: func(in *PreflightInput) {
			in.Source.Project.Protected, in.Source.Project.ProtectionReason = true, "Docker Manager's own Compose project"
		}, blockers: []string{FindingDockerManagerResource}},
		{name: "Docker Manager's own image", mutate: func(in *PreflightInput) { web(in).ImageProtected = true },
			blockers: []string{FindingDockerManagerResource}},
		{name: "Docker Manager's own volume", mutate: func(in *PreflightInput) {
			v := &in.Source.Project.Volumes[0]
			v.Protected, v.Supported, v.Reason = true, false, "the Docker Manager stacks volume"
		}, check: func(t *testing.T, p Plan) {
			if p.Volumes[0].Action != VolumeSkip || len(p.Excluded) != 1 {
				t.Errorf("volumes %+v excluded %+v", p.Volumes, p.Excluded)
			}
		}},
		{name: "source offline", mutate: func(in *PreflightInput) { in.SourceOnline = false }, blockers: []string{FindingEnvironmentOffline}},
		{name: "destination agent too old", mutate: func(in *PreflightInput) { in.TargetSupports = false }, blockers: []string{FindingAgentUnsupported}},
		{name: "destination storage unverified", mutate: func(in *PreflightInput) { in.Target.StacksOK = false }, blockers: []string{FindingStorageUnavailable}},
		{name: "same environment", mutate: func(in *PreflightInput) { in.TargetEnvironmentID = "e1" }, blockers: []string{FindingSameEnvironment}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := basePreflight()
			c.mutate(&in)
			p := Evaluate(in)
			if got := codes(p.Blockers); !slices.Equal(got, sorted(c.blockers)) {
				t.Errorf("blockers %v, want %v: %+v", got, c.blockers, p.Blockers)
			}
			for _, w := range c.warnings {
				if !slices.Contains(codes(p.Warnings), w) {
					t.Errorf("warning %s missing: %+v", w, p.Warnings)
				}
			}
			if p.Allowed() != (len(c.blockers) == 0) {
				t.Errorf("allowed %v", p.Allowed())
			}
			if c.check != nil {
				c.check(t, p)
			}
		})
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// TestVolumePreflight: a standalone volume's copy is blocked by running
// users unless a crash-consistent copy is acknowledged, by name conflicts
// on the destination and by unsupported or Docker Manager volumes.
func TestVolumePreflight(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(in *PreflightInput)
		blockers []string
		warnings []string
	}{
		{name: "stopped users", mutate: func(*PreflightInput) {}},
		{name: "running users", mutate: func(in *PreflightInput) { in.Source.Volume.UsedBy[0].Running = true },
			blockers: []string{FindingContainersRunning}},
		{name: "running users acknowledged", mutate: func(in *PreflightInput) {
			in.Source.Volume.UsedBy[0].Running = true
			in.Selection.AcknowledgeCrashConsistency = true
		}, warnings: []string{FindingCrashConsistency}},
		{name: "name taken", mutate: func(in *PreflightInput) {
			in.Target.Volumes = []protocol.MigrationExistingVolume{{Name: "media"}}
		}, blockers: []string{FindingVolumeConflict}},
		{name: "renamed around a conflict", mutate: func(in *PreflightInput) {
			in.Target.Volumes = []protocol.MigrationExistingVolume{{Name: "media"}}
			in.Selection.TargetName = "media-copy"
		}},
		{name: "unsupported", mutate: func(in *PreflightInput) {
			in.Source.Volume.Supported, in.Source.Volume.Reason = false, "NFS-backed"
		}, blockers: []string{FindingVolumeDefinitionOnly}},
		{name: "Docker Manager's own", mutate: func(in *PreflightInput) {
			in.Source.Volume.Protected, in.Source.Volume.Reason = true, "the manager's data"
		}, blockers: []string{FindingDockerManagerResource}},
		{name: "no space", mutate: func(in *PreflightInput) { in.Target.VolumesFree = 100 }, blockers: []string{FindingInsufficientSpace}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseVolumePreflight()
			c.mutate(&in)
			p := Evaluate(in)
			if got := codes(p.Blockers); !slices.Equal(got, sorted(c.blockers)) {
				t.Errorf("blockers %v, want %v: %+v", got, c.blockers, p.Blockers)
			}
			for _, w := range c.warnings {
				if !slices.Contains(codes(p.Warnings), w) {
					t.Errorf("warning %s missing: %+v", w, p.Warnings)
				}
			}
			if p.Downtime.EstimatedSeconds != 0 {
				t.Errorf("a volume copy has no downtime of its own: %+v", p.Downtime)
			}
		})
	}
}
