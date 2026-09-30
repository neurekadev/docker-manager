package backup

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/restic/restictest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestOpenLocationKeepsCompressionOnVersion2(t *testing.T) {
	ctx := testutil.Context(t)
	st := restictest.New(nil)
	loc := restic.Location{Repository: "/backups/docker-manager", Compression: restic.CompressionMax}
	o, err := OpenLocation(ctx, st, loc, "current-key", "", true)
	if err != nil || !o.Initialized || o.CompressionIgnored {
		t.Fatalf("init: %+v %v", o, err)
	}
	o, err = OpenLocation(ctx, st, loc, "current-key", "", false)
	if err != nil || o.CompressionIgnored {
		t.Fatalf("open: %+v %v", o, err)
	}
	if err := o.Repo.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	calls := st.Calls()
	if c := calls[len(calls)-1]; c.Op != "prune" || c.Compression != restic.CompressionMax {
		t.Errorf("prune call = %+v", c)
	}
}

func TestOpenLocationDropsCompressionOnVersion1(t *testing.T) {
	ctx := testutil.Context(t)
	st := restictest.New(nil)
	repo := "/backups/docker-manager"
	if _, err := st.Open(restic.Location{Repository: repo}, "current-key").Init(ctx); err != nil {
		t.Fatal(err)
	}
	st.SetVersion(repo, 1)
	for _, mode := range []string{restic.CompressionMax, restic.CompressionOff} {
		o, err := OpenLocation(ctx, st, restic.Location{Repository: repo, Compression: mode}, "current-key", "", false)
		if err != nil || !o.CompressionIgnored {
			t.Fatalf("%s: %+v %v", mode, o, err)
		}
		if err := o.Repo.Prune(ctx); err != nil {
			t.Fatal(err)
		}
		calls := st.Calls()
		if c := calls[len(calls)-1]; c.Op != "prune" || c.Compression != "" {
			t.Errorf("%s: prune call = %+v, want no compression", mode, c)
		}
	}
	// Auto needs nothing dropped.
	o, err := OpenLocation(ctx, st, restic.Location{Repository: repo}, "current-key", "", false)
	if err != nil || o.CompressionIgnored {
		t.Errorf("auto: %+v %v", o, err)
	}
}
