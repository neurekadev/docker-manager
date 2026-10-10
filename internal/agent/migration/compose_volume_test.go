package migration_test

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/migration/migrationtest"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// putProject writes a project directory with a Compose file into e's
// stacks volume.
func putProject(e *migrationtest.Env, dir, compose string) {
	p := e.ProjectDir(dir)
	e.Host.MkdirAll(p)
	e.Host.Put(p, migrationtest.Entry{Type: "dir", Mode: 0o755})
	e.Host.Put(p+"/compose.yaml", migrationtest.Entry{Mode: 0o644, Data: compose})
}

func composeSpec(name, dir, key string, labels map[string]string) *protocol.MigrationVolumeSpec {
	return &protocol.MigrationVolumeSpec{Name: name, Labels: labels, Compose: &protocol.MigrationComposeVolume{
		Stack: protocol.ProjectRef{Root: protocol.RootStacks, Dir: dir, ProjectName: dir}, Key: key}}
}

// TestVolumeReceiveComposeSpec (#313): a volume named by its Compose key
// is created as Compose would create it for the project in the stacks
// volume, under that project's name (a stack created from an archive under
// a new name), keeping only the labels Compose does not set.
func TestVolumeReceiveComposeSpec(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	putProject(dst, "boutique", migrationtest.ShopCompose)
	data, sum := framed(t, src.Host, src.VolumesDir+"/shop_dbdata/_data")
	labels := map[string]string{"com.docker.compose.project": "shop", "backup.exclude": "true"}
	res, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartVolume,
		Volume: composeSpec("boutique_dbdata", "boutique", "dbdata", labels)}, data)
	if err != nil || res.SHA256 != sum.SHA256 {
		t.Fatalf("%+v %v", res, err)
	}
	v, err := dst.Engine.InspectVolume(testutil.Context(t), "boutique_dbdata")
	if err != nil {
		t.Fatal(err)
	}
	l := v.Labels
	// The version label is compose's own version (empty unless compose's
	// internal version is set at build time), so only its presence is
	// checked.
	if _, version := l["com.docker.compose.version"]; l["com.docker.compose.project"] != "boutique" || l["com.docker.compose.volume"] != "dbdata" ||
		!version || l["com.docker.compose.config-hash"] == "" || l["backup.exclude"] != "true" || protocol.LabelValue(l, protocol.LabelMigration) != migID {
		t.Errorf("labels %v", l)
	}
	if got := dst.Host.Tree(v.Mountpoint); got["PG_VERSION"].UID != 999 {
		t.Errorf("data %+v", got)
	}
}

// TestVolumeReceiveComposeSpecRefusals: a name Compose would not give the
// volume, a volume with driver options and a project outside the stacks
// volume are refused before any volume exists.
func TestVolumeReceiveComposeSpecRefusals(t *testing.T) {
	src, dst := env(t, "src"), env(t, "dst")
	migrationtest.SeedShop(src)
	putProject(dst, "boutique", migrationtest.ShopCompose)
	putProject(dst, "nfs", "services:\n  db:\n    image: postgres:17\n    volumes: [data:/data]\nvolumes:\n  data:\n    driver_opts:\n      type: nfs\n      o: addr=10.0.0.1\n      device: :/export\n")
	data, _ := framed(t, src.Host, src.VolumesDir+"/shop_dbdata/_data")
	for name, c := range map[string]struct {
		spec *protocol.MigrationVolumeSpec
		code string
	}{
		"other name":   {composeSpec("shop_dbdata", "boutique", "dbdata", nil), protocol.CodeConflict},
		"unknown key":  {composeSpec("boutique_web", "boutique", "web", nil), protocol.CodeNotFound},
		"driver opts":  {composeSpec("nfs_data", "nfs", "data", nil), protocol.CodeUnsupportedRequest},
		"missing dir":  {composeSpec("gone_dbdata", "gone", "dbdata", nil), protocol.CodeNotFound},
		"outside root": {&protocol.MigrationVolumeSpec{Name: "x_data", Compose: &protocol.MigrationComposeVolume{Stack: protocol.ProjectRef{Root: protocol.RootBind, RootPath: "/srv", Dir: "x", ProjectName: "x"}, Key: "data"}}, protocol.CodeInvalidFrame},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := receive(t, pipe(t, dst), protocol.MigrationReceiveInput{MigrationID: migID, Part: protocol.PartVolume, Volume: c.spec}, data)
			if code(err) != c.code {
				t.Fatalf("error %v (%s), want %s", err, code(err), c.code)
			}
			if _, err := dst.Engine.InspectVolume(testutil.Context(t), c.spec.Name); err == nil {
				t.Error("a volume was created")
			}
		})
	}
}
