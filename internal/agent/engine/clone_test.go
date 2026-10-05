package engine

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestCloneContainerKeepsTheConfiguration: the clone is created from the
// complete inspected configuration with the moved volumes renamed (binds
// and mounts), anonymous volumes mounted by name, the image it runs, a
// Docker-generated hostname dropped, its networks kept and label keys renamed.
func TestCloneContainerKeepsTheConfiguration(t *testing.T) {
	const id = "0123456789abcdef0123"
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/containers/"+id+"/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{
			"Id": id, "Name": "/backup", "Image": "sha256:running",
			"Config": map[string]any{"Image": "restic:latest", "Hostname": id[:12], "Env": []string{"A=1"},
				"Labels": map[string]string{"keep": "me", "old.a": "1", "old.b": "2", "new.b": "kept"}},
			"HostConfig": map[string]any{"Binds": []string{"app_data:/data:ro", "/srv:/srv"}, "CapAdd": []string{"SYS_ADMIN"},
				"Mounts": []map[string]any{{"Type": "volume", "Source": "app_cache", "Target": "/cache"}}, "NetworkMode": "backend"},
			"Mounts": []map[string]any{
				{"Type": "volume", "Name": "app_data", "Destination": "/data", "RW": false},
				{"Type": "volume", "Name": "app_cache", "Destination": "/cache", "RW": true},
				{"Type": "volume", "Name": "f00d", "Destination": "/var/lib/restic", "RW": true},
				{"Type": "bind", "Source": "/srv", "Destination": "/srv", "RW": true},
			},
			"NetworkSettings": map[string]any{"Networks": map[string]any{
				"backend": map[string]any{"Aliases": []string{id[:12], "backup"}}}},
		})
	})
	fake.Handle(http.MethodGet, "/images/restic:latest/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "sha256:newer"})
	})
	fake.Handle(http.MethodPost, "/containers/create", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusCreated, map[string]any{"Id": "clone"})
	})
	c := connect(t, fake)
	got, err := c.CloneContainer(testutil.Context(t), id, CloneOptions{Name: "backup",
		Volumes:      map[string]string{"app_data": "shop_data", "app_cache": "shop_cache"},
		RenameLabels: map[string]string{"old.a": "new.a", "old.b": "new.b"}})
	if err != nil || got != "clone" {
		t.Fatalf("clone = %q, %v", got, err)
	}
	reqs := fake.Find(http.MethodPost, "/containers/create")
	if len(reqs) != 1 || reqs[0].Query.Get("name") != "backup" {
		t.Fatalf("create requests %+v", reqs)
	}
	var body struct {
		Image      string
		Hostname   string
		Env        []string
		Labels     map[string]string
		HostConfig struct {
			Binds  []string
			CapAdd []string
			Mounts []struct {
				Type, Source, Target string
				ReadOnly             bool
			}
		}
		NetworkingConfig struct {
			EndpointsConfig map[string]struct{ Aliases []string }
		}
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	// The tag moved since: the clone runs the image the container ran.
	if body.Image != "sha256:running" || body.Hostname != "" || !slices.Equal(body.Env, []string{"A=1"}) || body.Labels["keep"] != "me" {
		t.Errorf("config %+v", body)
	}
	// Renamed label keys (Docker Manager's legacy keys); a key already
	// present under its new name keeps that value.
	if len(body.Labels) != 3 || body.Labels["new.a"] != "1" || body.Labels["new.b"] != "kept" {
		t.Errorf("labels %v", body.Labels)
	}
	if !slices.Equal(body.HostConfig.Binds, []string{"shop_data:/data:ro", "/srv:/srv"}) || !slices.Equal(body.HostConfig.CapAdd, []string{"CAP_SYS_ADMIN"}) {
		t.Errorf("host config %+v", body.HostConfig)
	}
	m := body.HostConfig.Mounts
	if len(m) != 2 || m[0].Source != "shop_cache" || m[1].Source != "f00d" || m[1].Target != "/var/lib/restic" {
		t.Errorf("mounts %+v", m)
	}
	if ep, ok := body.NetworkingConfig.EndpointsConfig["backend"]; !ok || !slices.Equal(ep.Aliases, []string{"backup"}) {
		t.Errorf("networks %+v", body.NetworkingConfig)
	}
}

// TestCloneContainerCurrentImage (#273): with CurrentImage the clone runs
// the image the tag names now and leaves the old image's settings (merged
// into the configuration at creation) to it, keeping its own; a tag that no
// longer names a local image keeps the image the container runs.
func TestCloneContainerCurrentImage(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	for id, ref := range map[string]string{"0123456789abcdef0001": "nginx:1.27", "0123456789abcdef0002": "gone:1"} {
		fake.Handle(http.MethodGet, "/containers/"+id+"/json", func(w http.ResponseWriter, _ *http.Request) {
			enginetest.JSON(w, http.StatusOK, map[string]any{
				"Id": id, "Name": "/web", "Image": "sha256:old",
				"Config": map[string]any{"Image": ref, "Env": []string{"PATH=/old/bin", "NGINX_VERSION=1.27.0", "TOKEN=s3cret"},
					"Cmd": []string{"nginx", "-g", "daemon off;"}, "Entrypoint": []string{"/docker-entrypoint.sh"}, "StopSignal": "SIGQUIT",
					"Labels": map[string]string{"maintainer": "NGINX", "team": "ops"}, "ExposedPorts": map[string]any{"80/tcp": map[string]any{}, "8443/tcp": map[string]any{}}},
				"HostConfig": map[string]any{"NetworkMode": "bridge"},
			})
		})
	}
	fake.Handle(http.MethodGet, "/images/nginx:1.27/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "sha256:new"})
	})
	fake.Handle(http.MethodGet, "/images/sha256:old/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": "sha256:old", "Config": map[string]any{
			"Env": []string{"PATH=/old/bin", "NGINX_VERSION=1.27.0"}, "Cmd": []string{"nginx", "-g", "daemon off;"},
			"Entrypoint": []string{"/docker-entrypoint.sh"}, "StopSignal": "SIGQUIT", "Labels": map[string]string{"maintainer": "NGINX"},
			"ExposedPorts": map[string]any{"80/tcp": map[string]any{}}}})
	})
	fake.Handle(http.MethodPost, "/containers/create", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusCreated, map[string]any{"Id": "clone"})
	})
	c := connect(t, fake)
	type created struct {
		Image        string
		Env          []string
		Cmd          []string
		Entrypoint   []string
		StopSignal   string
		Labels       map[string]string
		ExposedPorts map[string]struct{}
	}
	clone := func(id string) created {
		t.Helper()
		before := len(fake.Find(http.MethodPost, "/containers/create"))
		if _, err := c.CloneContainer(testutil.Context(t), id, CloneOptions{Name: "web", CurrentImage: true}); err != nil {
			t.Fatal(err)
		}
		reqs := fake.Find(http.MethodPost, "/containers/create")
		if len(reqs) != before+1 {
			t.Fatalf("create requests %+v", reqs)
		}
		var body created
		if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := clone("0123456789abcdef0001")
	if body.Image != "nginx:1.27" || !slices.Equal(body.Env, []string{"TOKEN=s3cret"}) || body.Cmd != nil || body.Entrypoint != nil ||
		body.StopSignal != "" || len(body.Labels) != 1 || body.Labels["team"] != "ops" || len(body.ExposedPorts) != 1 {
		t.Errorf("current image %+v", body)
	}
	if _, ok := body.ExposedPorts["8443/tcp"]; !ok {
		t.Errorf("exposed ports %v", body.ExposedPorts)
	}
	body = clone("0123456789abcdef0002")
	if body.Image != "sha256:old" || len(body.Env) != 3 || len(body.Cmd) != 3 || body.Labels["maintainer"] != "NGINX" {
		t.Errorf("unresolved tag %+v", body)
	}
}
