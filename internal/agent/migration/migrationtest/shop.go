package migrationtest

import (
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// ShopCompose is the definition of the test stack "shop": db with a named
// volume, web depending on db with a published port, a relative bind
// directory (migrated with the project) and an external bind path (not).
const ShopCompose = `services:
  db:
    image: postgres:17
    volumes:
      - dbdata:/var/lib/postgresql/data
  web:
    image: shop-web:local
    depends_on:
      db:
        condition: service_started
    ports:
      - "8080:80"
    volumes:
      - ./data:/srv/data
      - /srv/shared:/shared
volumes:
  dbdata: {}
`

// ShopTime is the modification time of the seeded files.
var ShopTime = time.Date(2026, 5, 6, 7, 8, 9, 101010101, time.UTC)

// SeedShop creates the "shop" stack on e: its project directory (compose
// file, .env, a relative bind directory with owned files), the named volume
// shop_dbdata with owned data, the images (postgres:17 from a registry,
// shop-web:local built locally, no repository digest) and the two running
// containers with their Compose and dependency labels. It returns the
// project reference.
func SeedShop(e *Env) protocol.ProjectRef {
	dir := e.ProjectDir("shop")
	h := e.Host
	h.MkdirAll(dir)
	h.Put(dir, Entry{Type: "dir", Mode: 0o755, MTime: ShopTime})
	h.Put(dir+"/compose.yaml", Entry{Mode: 0o644, MTime: ShopTime, Data: ShopCompose})
	h.Put(dir+"/.env", Entry{Mode: 0o600, MTime: ShopTime, Data: "TAG=17\n"})
	h.Put(dir+"/data", Entry{Type: "dir", Mode: 0o2775, UID: 33, GID: 33, MTime: ShopTime})
	h.Put(dir+"/data/index.html", Entry{Mode: 0o644, UID: 33, GID: 33, MTime: ShopTime, Data: "<h1>shop</h1>\n"})
	h.Put(dir+"/data/uploads", Entry{Type: "dir", Mode: 0o750, UID: 33, GID: 33, MTime: ShopTime})
	h.Put(dir+"/data/current", Entry{Type: "symlink", Target: "index.html", UID: 33, GID: 33})
	mp := e.AddVolume("shop_dbdata", map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.volume": "dbdata"})
	h.Put(mp, Entry{Type: "dir", Mode: 0o700, UID: 999, GID: 999, MTime: ShopTime})
	h.Put(mp+"/PG_VERSION", Entry{Mode: 0o600, UID: 999, GID: 999, MTime: ShopTime, Data: "17\n"})
	h.MkdirAll(mp + "/base")
	h.Put(mp+"/base", Entry{Type: "dir", Mode: 0o700, UID: 999, GID: 999, MTime: ShopTime})
	h.Put(mp+"/base/16384", Entry{Mode: 0o600, UID: 999, GID: 999, MTime: ShopTime, Data: string(make([]byte, 300_000))})
	e.Engine.AddImage("postgres:17")
	e.Engine.AddImageDetails(engine.ImageDetails{RepoTags: []string{"shop-web:local"}, Size: 5 << 20, Architecture: "amd64"})
	labels := func(service, deps string) map[string]string {
		l := map[string]string{protocol.ComposeProjectLabel: "shop", protocol.ComposeServiceLabel: service,
			protocol.ComposeWorkingDirLabel: dir, "dev.neureka.docker-manager.depends_on": deps}
		return l
	}
	e.Engine.AddContainer(engine.ContainerSpec{Name: "shop-db-1", Image: "postgres:17", Labels: labels("db", ""),
		Mounts: []engine.MountSpec{{Type: "volume", Source: "shop_dbdata", Target: "/var/lib/postgresql/data"}}}, true)
	e.Engine.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "shop-web:local", Labels: labels("web", "db:service_started:false:true"),
		Ports:  []engine.PortBinding{{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"}},
		Mounts: []engine.MountSpec{{Type: "bind", Source: dir + "/data", Target: "/srv/data"}, {Type: "bind", Source: "/srv/shared", Target: "/shared"}}}, true)
	return protocol.ProjectRef{Root: protocol.RootStacks, Dir: "shop", ProjectName: "shop"}
}
