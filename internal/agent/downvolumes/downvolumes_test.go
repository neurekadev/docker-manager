package downvolumes

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var (
	cache = Volume{Name: "aaaa", Service: "web", Destination: "/cache"}
	data  = Volume{Name: "bbbb", Service: "db", Destination: "/data"}
	logs  = Volume{Name: "cccc", Service: "db", Destination: "/logs"}
)

// TestRecordPerJob: a re-run of the down job that wrote a project's record
// adds to it (sorted, no duplicates); another down replaces it; other
// projects are never touched.
func TestRecordPerJob(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Record("app", "job-1", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("shop", "job-2", []Volume{logs}); err != nil {
		t.Fatal(err)
	}
	// The same job again (an earlier run removed the db container).
	if err := s.Record("app", "job-1", []Volume{data, cache}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{cache, data}) {
		t.Errorf("re-run = %v", got)
	}
	// A later down replaces the record.
	if err := s.Record("app", "job-3", []Volume{logs}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{logs}) {
		t.Errorf("later down = %v", got)
	}
	if got := s.Volumes("shop"); !slices.Equal(got, []Volume{logs}) {
		t.Errorf("other project = %v", got)
	}
}

// TestForgetAndRename: a deploy or a removal forgets a project's record, a
// rename moves it; a project without a record is left alone.
func TestForgetAndRename(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Record("app", "job-1", []Volume{cache}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("app", "media"); err != nil {
		t.Fatal(err)
	}
	if s.Volumes("app") != nil || !slices.Equal(s.Volumes("media"), []Volume{cache}) {
		t.Errorf("rename: app %v media %v", s.Volumes("app"), s.Volumes("media"))
	}
	// The renamed record still belongs to its job.
	if err := s.Record("media", "job-1", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("media"); !slices.Equal(got, []Volume{cache, data}) {
		t.Errorf("re-run after rename = %v", got)
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
	if err := New(dir).Record("app", "job-1", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); !slices.Equal(got, []Volume{data}) {
		t.Errorf("after a restart = %v", got)
	}
	// The job survives the restart too: its re-run adds.
	restarted := New(dir)
	if err := restarted.Record("app", "job-1", []Volume{cache}); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Volumes("app"); !slices.Equal(got, []Volume{cache, data}) {
		t.Errorf("re-run after a restart = %v", got)
	}
	var none *Store
	if err := none.Record("app", "job-1", []Volume{data}); err != nil || none.Volumes("app") != nil ||
		none.Rename("app", "b") != nil || none.Forget("app") != nil {
		t.Error("a nil store keeps something")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).Record("app", "job-1", []Volume{data}); err != nil {
		t.Errorf("record over null: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); got != nil {
		t.Errorf("unreadable file = %v", got)
	}
	if err := New(dir).Record("app", "job-1", nil); err == nil {
		t.Error("recording over an unreadable file succeeded")
	}
}
