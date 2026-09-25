package compose

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// miniDocker is a stateful scripted Engine for one Compose project: just
// enough of the API for the SDK to recreate a diverged container (list,
// inspect, create, stop, remove, rename, start, the project network and
// the image). Created containers record their create request body.
type miniDocker struct {
	mu         sync.Mutex
	containers map[string]map[string]any // ID -> container summary
	created    []map[string]any          // create request bodies
	seq        int
}

func (m *miniDocker) list() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.containers))
	for _, c := range m.containers {
		out = append(out, c)
	}
	return out
}

func (m *miniDocker) find(ref string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.containers {
		if id == ref || strings.HasPrefix(id, ref) {
			return c
		}
		for _, n := range c["Names"].([]string) {
			if strings.TrimPrefix(n, "/") == ref {
				return c
			}
		}
	}
	return nil
}

// details renders a summary as a container inspect answer.
func details(c map[string]any) map[string]any {
	name := c["Names"].([]string)[0]
	state := c["State"].(string)
	var mounts []map[string]any
	for _, mt := range c["Mounts"].([]map[string]any) {
		mounts = append(mounts, map[string]any{"Type": mt["Type"], "Name": mt["Name"], "Source": "/var/lib/docker/volumes/" + mt["Name"].(string) + "/_data",
			"Destination": mt["Destination"], "RW": true})
	}
	return map[string]any{"Id": c["Id"], "Name": name, "Image": "sha256:" + strings.Repeat("a", 64),
		"State":      map[string]any{"Status": state, "Running": state == "running"},
		"Config":     map[string]any{"Image": c["Image"], "Labels": c["Labels"]},
		"HostConfig": map[string]any{"NetworkMode": "shop_default"},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"shop_default": map[string]any{
			"NetworkID": "net1", "Aliases": []string{"app"}}}},
		"Mounts": mounts}
}

func startMiniDocker(t *testing.T, anonVolume string) (*enginetest.Engine, *miniDocker) {
	t.Helper()
	fake := enginetest.Start(t, enginetest.Options{})
	m := &miniDocker{containers: map[string]map[string]any{
		"old1": {"Id": "old1", "Names": []string{"/shop-app-1"}, "Image": "busybox:1.37", "ImageID": "sha256:" + strings.Repeat("a", 64),
			"State": "running", "Labels": map[string]string{
				api.ProjectLabel: "shop", api.ServiceLabel: "app", api.ContainerNumberLabel: "1", api.OneoffLabel: "False",
				// A definition change: the SDK sees a diverged container.
				api.ConfigHashLabel: "stale", api.VersionLabel: api.ComposeVersion, api.WorkingDirLabel: "/stacks/shop"},
			"Mounts":          []map[string]any{{"Type": "volume", "Name": anonVolume, "Destination": "/data", "RW": true}},
			"NetworkSettings": map[string]any{"Networks": map[string]any{"shop_default": map[string]any{"NetworkID": "net1"}}}},
	}}
	network := map[string]any{"Id": "net1", "Name": "shop_default", "Driver": "bridge", "Scope": "local",
		"Labels": map[string]string{api.ProjectLabel: "shop", api.NetworkLabel: "default", api.VersionLabel: api.ComposeVersion}}
	fake.Handle(http.MethodGet, "/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, m.list())
	})
	fake.Handle(http.MethodGet, "/containers/[^/]+/json", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		c := m.find(ref)
		if c == nil {
			enginetest.Error(w, http.StatusNotFound, "No such container: "+ref)
			return
		}
		enginetest.JSON(w, http.StatusOK, details(c))
	})
	fake.Handle(http.MethodPost, "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		// The fake recorded (and consumed) the body before the handler.
		reqs := fake.Find(http.MethodPost, "/containers/create")
		var body map[string]any
		if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
			enginetest.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		m.mu.Lock()
		m.seq++
		id := "new" + string(rune('0'+m.seq))
		m.created = append(m.created, body)
		labels := map[string]string{}
		if l, ok := body["Labels"].(map[string]any); ok {
			for k, v := range l {
				labels[k], _ = v.(string)
			}
		}
		var mounts []map[string]any
		if hc, ok := body["HostConfig"].(map[string]any); ok {
			if ms, ok := hc["Mounts"].([]any); ok {
				for _, x := range ms {
					mt := x.(map[string]any)
					name, _ := mt["Source"].(string)
					if name == "" {
						name = "fresh" + id
					}
					mounts = append(mounts, map[string]any{"Type": mt["Type"], "Name": name, "Destination": mt["Target"], "RW": true})
				}
			}
		}
		m.containers[id] = map[string]any{"Id": id, "Names": []string{"/" + r.URL.Query().Get("name")}, "Image": body["Image"],
			"ImageID": "sha256:" + strings.Repeat("a", 64), "State": "created", "Labels": labels, "Mounts": mounts,
			"NetworkSettings": map[string]any{"Networks": map[string]any{"shop_default": map[string]any{"NetworkID": "net1"}}}}
		m.mu.Unlock()
		enginetest.JSON(w, http.StatusCreated, map[string]any{"Id": id, "Warnings": []string{}})
	})
	setState := func(state string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(r.URL.Path, "/")
			if c := m.find(parts[2]); c != nil {
				m.mu.Lock()
				c["State"] = state
				m.mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	fake.Handle(http.MethodPost, "/containers/[^/]+/stop", setState("exited"))
	fake.Handle(http.MethodPost, "/containers/[^/]+/start", setState("running"))
	fake.Handle(http.MethodPost, "/containers/[^/]+/rename", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if c := m.find(parts[2]); c != nil {
			m.mu.Lock()
			c["Names"] = []string{"/" + r.URL.Query().Get("name")}
			m.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	fake.Handle(http.MethodDelete, "/containers/[^/]+", func(w http.ResponseWriter, r *http.Request) {
		if c := m.find(strings.TrimPrefix(r.URL.Path, "/containers/")); c != nil {
			m.mu.Lock()
			delete(m.containers, c["Id"].(string))
			m.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	fake.Handle(http.MethodGet, "/networks", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, []any{network})
	})
	fake.Handle(http.MethodGet, "/networks/[^/]+", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, network)
	})
	fake.Handle(http.MethodPost, "/networks/[^/]+/(connect|disconnect)", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	fake.Handle(http.MethodGet, "/volumes", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Volumes": []any{}})
	})
	fake.Handle(http.MethodGet, "/images/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, []any{map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "RepoTags": []string{"busybox:1.37"}}})
	})
	fake.Handle(http.MethodGet, "/images/.*/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "RepoTags": []string{"busybox:1.37"},
			"Os": "linux", "Architecture": "amd64", "Config": map[string]any{}})
	})
	return fake, m
}

// mountsOf returns the create request's mounts by target.
func mountsOf(body map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	hc, _ := body["HostConfig"].(map[string]any)
	ms, _ := hc["Mounts"].([]any)
	for _, x := range ms {
		mt := x.(map[string]any)
		out[mt["Target"].(string)] = mt
	}
	return out
}

// TestUpKeepsAnonymousVolumesOnRecreate (#7 × #20): a deploy that recreates
// a container (its definition changed) through the real Compose SDK hands
// the old container's anonymous volume to the new one, like `docker
// compose up`; only an explicit RenewAnonymousVolumes starts it empty.
func TestUpKeepsAnonymousVolumesOnRecreate(t *testing.T) {
	const yaml = "services:\n  app:\n    image: busybox:1.37\n    volumes:\n      - /data\n"
	for _, tc := range []struct {
		name    string
		renew   bool
		inherit bool
	}{{"default inherits", false, true}, {"explicit renew", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			fake, m := startMiniDocker(t, "anon-data-1")
			a := newAdapter(t, fake)
			ctx := testutil.Context(t)
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"compose.yaml": yaml})
			p, err := a.Load(ctx, ProjectSpec{Dir: dir, Name: "shop"})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Up(ctx, p, UpOptions{RenewAnonymousVolumes: tc.renew}); err != nil {
				var unhandled []string
				for _, r := range fake.Requests() {
					unhandled = append(unhandled, r.Method+" "+r.Path)
				}
				t.Fatalf("up: %v\nrequests: %v", err, unhandled)
			}
			m.mu.Lock()
			created := append([]map[string]any(nil), m.created...)
			m.mu.Unlock()
			if len(created) != 1 {
				t.Fatalf("created %d containers, want 1 (a recreate)", len(created))
			}
			data := mountsOf(created[0])["/data"]
			src, _ := data["Source"].(string)
			switch {
			case tc.inherit && (data == nil || data["Type"] != "volume" || src != "anon-data-1"):
				b, _ := json.Marshal(created[0])
				t.Errorf("the recreated container did not inherit the anonymous volume: /data mount %v\n%s", data, b)
			case !tc.inherit && src == "anon-data-1":
				t.Errorf("renew kept the old anonymous volume: %v", data)
			}
			if len(fake.Find(http.MethodDelete, "/containers/old1")) != 1 {
				t.Error("the old container was not replaced")
			}
		})
	}
}
