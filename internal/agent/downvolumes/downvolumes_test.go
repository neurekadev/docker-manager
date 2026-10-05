package downvolumes

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var (
	webVol    = Volume{Name: "aaaa", Service: "web", Destination: "/cache"}
	dbVol     = Volume{Name: "bbbb", Service: "db", Destination: "/data"}
	newDBVol  = Volume{Name: "cccc", Service: "db", Destination: "/data"}
	workerVol = Volume{Name: "dddd", Service: "worker", Destination: "/tmp"}

	db1, web1, worker1 = Container{"db1", "db"}, Container{"web1", "web"}, Container{"worker1", "worker"}
)

// TestRecordFollowsTheServices: a later down keeps the recorded volumes of
// every service without a new container (an earlier down removed some
// containers already, or only temporary containers are left), drops those
// of a service that has a new container (its data is in new volumes) and
// adds what it finds; other projects are never touched.
func TestRecordFollowsTheServices(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Record("app", []Container{db1, web1, worker1}, []Volume{dbVol, webVol, workerVol}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("shop", []Container{{"shop1", "db"}}, []Volume{newDBVol}); err != nil {
		t.Fatal(err)
	}
	// An earlier down removed db1 and worker1; web1 is left.
	if err := s.Record("app", []Container{web1}, []Volume{webVol}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{webVol, dbVol, workerVol}) {
		t.Errorf("down of what was left = %v", got)
	}
	// Only temporary containers were left: nothing found, nothing changes.
	if err := s.Record("app", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{webVol, dbVol, workerVol}) {
		t.Errorf("down without containers = %v", got)
	}
	// A deploy that failed recreated db (db2) and kept web1; worker has no
	// container: db's old volume goes, web's and worker's stay.
	if err := s.Record("app", []Container{{"db2", "db"}, web1}, []Volume{newDBVol, webVol}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{webVol, newDBVol, workerVol}) {
		t.Errorf("down after db came back = %v", got)
	}
	if got := s.Volumes("shop"); !slices.Equal(got, []Volume{newDBVol}) {
		t.Errorf("other project = %v", got)
	}
}

// TestForgetAndRename: a deploy or a removal forgets a project's record, a
// rename moves it with its containers; a project without a record is left
// alone.
func TestForgetAndRename(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Record("app", []Container{db1, web1}, []Volume{dbVol, webVol}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("app", "media"); err != nil {
		t.Fatal(err)
	}
	if s.Volumes("app") != nil || !slices.Equal(s.Volumes("media"), []Volume{webVol, dbVol}) {
		t.Errorf("rename: app %v media %v", s.Volumes("app"), s.Volumes("media"))
	}
	// The renamed record keeps its containers: web1 is not new.
	if err := s.Record("media", []Container{web1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("media"); !slices.Equal(got, []Volume{webVol, dbVol}) {
		t.Errorf("down after rename = %v", got)
	}
	if err := s.Forget("media"); err != nil {
		t.Fatal(err)
	}
	if s.Volumes("media") != nil {
		t.Errorf("forgotten: %v", s.Volumes("media"))
	}
	if err := s.Rename("none", "other"); err != nil || s.Volumes("other") != nil {
		t.Errorf("rename without a record: %v %v", err, s.Volumes("other"))
	}
	if err := s.Forget("none"); err != nil {
		t.Errorf("forget without a record: %v", err)
	}
}

// TestRecordSurvivesARestart: the record is a file in the state directory;
// a nil store keeps nothing, a file holding null is an empty store and an
// unreadable file reads as empty but refuses writes.
func TestRecordSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	if err := New(dir).Record("app", []Container{db1, web1}, []Volume{dbVol}); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); !slices.Equal(got, []Volume{dbVol}) {
		t.Errorf("after a restart = %v", got)
	}
	// The containers survive the restart too: web1 is not new.
	restarted := New(dir)
	if err := restarted.Record("app", []Container{web1}, []Volume{webVol}); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Volumes("app"); !slices.Equal(got, []Volume{webVol, dbVol}) {
		t.Errorf("down after a restart = %v", got)
	}
	var none *Store
	if err := none.Record("app", []Container{db1}, []Volume{dbVol}); err != nil || none.Volumes("app") != nil ||
		none.Rename("app", "b") != nil || none.Forget("app") != nil {
		t.Error("a nil store keeps something")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).Record("app", []Container{db1}, []Volume{dbVol}); err != nil {
		t.Errorf("record over null: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); got != nil {
		t.Errorf("unreadable file = %v", got)
	}
	if err := New(dir).Record("app", []Container{db1}, nil); err == nil {
		t.Error("recording over an unreadable file succeeded")
	}
}
