package compose

import (
	"path/filepath"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestResources summarizes what a project would create on an Engine
// (migration previews, #35): Engine names of volumes and networks,
// container names per replica, expanded published ports and devices.
func TestResources(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	const src = `
services:
  db:
    image: postgres:17
    volumes: [dbdata:/var/lib/postgresql/data, /tmp/anon-target]
    devices: ["/dev/fuse:/dev/fuse"]
  web:
    image: nginx:1.27
    container_name: shop-frontend
    ports: ["8080:80", "127.0.0.1:9000-9002:9000/udp", "443"]
    networks: [front, legacy]
  worker:
    image: busybox
    scale: 2
    volumes:
      - type: volume
        target: /scratch
volumes:
  dbdata: {}
  unused: {}
networks:
  front: {}
  legacy:
    external: true
    name: corp-net
`
	p, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "shop", Content: map[string][]byte{"compose.yaml": []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	r := p.Resources()
	if len(r.Volumes) != 1 || r.Volumes[0].Key != "dbdata" || r.Volumes[0].Name != "shop_dbdata" || r.Volumes[0].Driver != "local" {
		t.Errorf("volumes %+v (unused volumes are dropped)", r.Volumes)
	}
	var nets []string
	for _, n := range r.Networks {
		nets = append(nets, n.Key+"="+n.Name)
		if n.Key == "legacy" && !n.External {
			t.Error("legacy must be external")
		}
	}
	slices.Sort(nets)
	if !slices.Equal(nets, []string{"default=shop_default", "front=shop_front", "legacy=corp-net"}) {
		t.Errorf("networks %v", nets)
	}
	by := map[string]ServiceResources{}
	for _, s := range r.Services {
		by[s.Name] = s
	}
	if db := by["db"]; !slices.Equal(db.ContainerNames, []string{"shop-db-1"}) || !slices.Equal(db.Devices, []string{"/dev/fuse"}) ||
		!slices.Equal(db.Volumes, []string{"dbdata"}) || db.Anonymous != 1 {
		t.Errorf("db %+v", db)
	}
	if w := by["worker"]; !slices.Equal(w.ContainerNames, []string{"shop-worker-1", "shop-worker-2"}) || w.Anonymous != 1 {
		t.Errorf("worker %+v", w)
	}
	web := by["web"]
	if !slices.Equal(web.ContainerNames, []string{"shop-frontend"}) {
		t.Errorf("web names %v", web.ContainerNames)
	}
	want := []PortDef{{Published: 8080, Target: 80, Protocol: "tcp"},
		{HostIP: "127.0.0.1", Published: 9000, Target: 9000, Protocol: "udp"},
		{HostIP: "127.0.0.1", Published: 9001, Target: 9000, Protocol: "udp"},
		{HostIP: "127.0.0.1", Published: 9002, Target: 9000, Protocol: "udp"},
		{Target: 443, Protocol: "tcp"}}
	if !slices.Equal(web.Ports, want) {
		t.Errorf("web ports %+v", web.Ports)
	}
}
