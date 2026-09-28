package engine

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
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
