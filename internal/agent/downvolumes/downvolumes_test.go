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
)

// TestRecordReplacesForgetsAndRenames: the next down replaces a project's
// record (an empty one forgets it), a removal forgets it, a rename moves
// it, and other projects are never touched.
func TestRecordReplacesForgetsAndRenames(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Record("app", []Volume{cache, data}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("shop", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("app", []Volume{cache}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{cache}) {
		t.Errorf("replaced record = %v", got)
	}
	if err := s.Rename("app", "media"); err != nil {
		t.Fatal(err)
	}
	if s.Volumes("app") != nil || !slices.Equal(s.Volumes("media"), []Volume{cache}) {
		t.Errorf("rename: app %v media %v", s.Volumes("app"), s.Volumes("media"))
	}
	if err := s.Forget("media"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("shop", nil); err != nil {
		t.Fatal(err)
	}
	if s.Volumes("media") != nil || s.Volumes("shop") != nil {
		t.Errorf("forgotten: media %v shop %v", s.Volumes("media"), s.Volumes("shop"))
	}
	// Renaming a project without a record changes nothing.
	if err := s.Rename("none", "other"); err != nil || s.Volumes("other") != nil {
		t.Errorf("rename without a record: %v %v", err, s.Volumes("other"))
	}
}

// TestAddKeepsWhatEarlierAttemptsRecorded: a retried down adds the
// volumes of the containers left to the record, sorted, without
// duplicates; adding nothing changes nothing.
func TestAddKeepsWhatEarlierAttemptsRecorded(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Add("app", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("app", []Volume{data, cache}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("app", nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes("app"); !slices.Equal(got, []Volume{cache, data}) {
		t.Errorf("record = %v", got)
	}
}

// TestRecordSurvivesARestart: the record is a file in the state directory;
// a nil store keeps nothing and an unreadable file reads as empty.
func TestRecordSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	if err := New(dir).Record("app", []Volume{data}); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); !slices.Equal(got, []Volume{data}) {
		t.Errorf("after a restart = %v", got)
	}
	var none *Store
	if err := none.Record("app", []Volume{data}); err != nil || none.Volumes("app") != nil || none.Rename("app", "b") != nil {
		t.Error("a nil store keeps something")
	}
	// A file holding null is an empty store, never a panic.
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).Record("app", []Volume{data}); err != nil {
		t.Errorf("record over null: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Volumes("app"); got != nil {
		t.Errorf("unreadable file = %v", got)
	}
	if err := New(dir).Record("app", nil); err == nil {
		t.Error("recording over an unreadable file succeeded")
	}
}
