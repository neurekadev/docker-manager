package engine

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/volume"

	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The prune support of the adapter (#14) against the scripted Engine API:
// build cache listing on current and legacy (API < 1.52) disk usage
// answers, targeted build cache removal and volume usage.

func TestListBuildCache(t *testing.T) {
	last := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	recs := []build.CacheRecord{
		{ID: "leaf", Parents: []string{"root"}, Type: "regular", Description: "RUN make", Size: 200, CreatedAt: last.Add(-time.Hour), LastUsedAt: &last, UsageCount: 3},
		{ID: "root", Type: "regular", Shared: true, InUse: true, Size: 300, CreatedAt: last.Add(-2 * time.Hour)},
	}
	for _, tc := range []struct {
		api  string
		body any
	}{
		{"1.56", map[string]any{"BuildCacheUsage": map[string]any{"Items": recs}}},
		{"1.44", map[string]any{"BuildCache": recs}}, // Docker 25: legacy answer
	} {
		t.Run(tc.api, func(t *testing.T) {
			fake := enginetest.Start(t, enginetest.Options{APIVersion: tc.api})
			fake.Handle(http.MethodGet, "/system/df", func(w http.ResponseWriter, _ *http.Request) {
				enginetest.JSON(w, http.StatusOK, tc.body)
			})
			got, err := connect(t, fake).ListBuildCache(testutil.Context(t))
			if err != nil {
				t.Fatal(err)
			}
			want := []BuildCacheRecord{
				{ID: "leaf", Parents: []string{"root"}, Type: "regular", Description: "RUN make", Size: 200, CreatedAt: last.Add(-time.Hour),
					LastUsedAt: last, UsageCount: 3},
				{ID: "root", Type: "regular", Shared: true, InUse: true, Size: 300, CreatedAt: last.Add(-2 * time.Hour)},
			}
			if len(got) != 2 || !slices.Equal(got[0].Parents, want[0].Parents) || got[0].LastUsedAt != want[0].LastUsedAt ||
				got[1].ID != "root" || !got[1].Shared || !got[1].InUse || !got[1].LastUsedAt.IsZero() || got[0].Size != 200 {
				t.Fatalf("records %+v", got)
			}
			q := fake.Find(http.MethodGet, "/system/df")[0].Query
			if tc.api == "1.56" && (!slices.Equal(q["type"], []string{"build-cache"}) || q.Get("verbose") != "1") {
				t.Errorf("query %v", q)
			}
		})
	}
}

func TestRemoveBuildCacheIsTargeted(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodPost, "/build/prune", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, build.CachePruneReport{CachesDeleted: []string{"leaf"}, SpaceReclaimed: 200})
	})
	c := connect(t, fake)
	if _, err := c.RemoveBuildCache(testutil.Context(t), "", true); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("empty ID: %v", err)
	}
	if n := len(fake.Find(http.MethodPost, "/build/prune")); n != 0 {
		t.Fatalf("an empty ID reached the Engine (%d calls)", n)
	}
	res, err := c.RemoveBuildCache(testutil.Context(t), "leaf", false)
	if err != nil || !slices.Equal(res.Deleted, []string{"leaf"}) || res.SpaceReclaimed != 200 {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if _, err := c.RemoveBuildCache(testutil.Context(t), "root", true); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Find(http.MethodPost, "/build/prune")
	for i, want := range []struct {
		id  string
		all string
	}{{"leaf", ""}, {"root", "1"}} {
		q := reqs[i].Query
		var filters map[string]map[string]bool
		if err := json.Unmarshal([]byte(q.Get("filters")), &filters); err != nil {
			t.Fatalf("filters %q: %v", q.Get("filters"), err)
		}
		if len(filters) != 1 || len(filters["id"]) != 1 || !filters["id"][want.id] || q.Get("all") != want.all ||
			q.Get("keep-storage") != "" || q.Get("reserved-space") != "" {
			t.Errorf("request %d query %v", i, q)
		}
	}
}

func TestVolumeUsage(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/system/df", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"VolumeUsage": map[string]any{"Items": []volume.Volume{
			{Name: "data", Driver: "local", UsageData: &volume.UsageData{Size: 4096, RefCount: 1}},
			{Name: "nfs", Driver: "local"},
		}}})
	})
	got, err := connect(t, fake).VolumeUsage(testutil.Context(t))
	if err != nil {
		t.Fatal(err)
	}
	if got["data"] != (VolumeUsage{Size: 4096, RefCount: 1}) || got["nfs"] != (VolumeUsage{Size: -1, RefCount: -1}) {
		t.Fatalf("usage %+v", got)
	}
	if q := fake.Find(http.MethodGet, "/system/df")[0].Query; !slices.Equal(q["type"], []string{"volume"}) {
		t.Errorf("query %v", q)
	}
}

func TestListContainersSizesAndNetworks(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, []map[string]any{{"Id": "abc", "Names": []string{"/web"}, "State": "exited", "SizeRw": 1234,
			"NetworkSettings": map[string]any{"Networks": map[string]any{"front": map[string]any{}, "back": map[string]any{}}}}})
	})
	cs, err := connect(t, fake).ListContainers(testutil.Context(t), ContainerFilter{All: true, Size: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].SizeRw != 1234 || !slices.Equal(cs[0].Networks, []string{"back", "front"}) {
		t.Fatalf("containers %+v", cs)
	}
	if q := fake.Find(http.MethodGet, "/containers/json")[0].Query; q.Get("size") != "1" {
		t.Errorf("query %v", q)
	}
}
