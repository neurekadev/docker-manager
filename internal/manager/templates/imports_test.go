package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

type member struct {
	name, body, link string
	typ              byte
}

func tarGz(t *testing.T, ms ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, m := range ms {
		h := &tar.Header{Name: m.name, Typeflag: m.typ, Mode: 0o644, Size: int64(len(m.body)), Linkname: m.link}
		if m.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportDraftReplacesTheDraftSafely(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Imported")
	archive := tarGz(t,
		member{name: "compose.yaml", body: "services: {}\n", typ: tar.TypeReg},
		member{name: "config/", typ: tar.TypeDir},
		member{name: "config/app.conf", body: "x", typ: tar.TypeReg},
		member{name: "../escape.txt", body: "no", typ: tar.TypeReg},
		member{name: "/abs.txt", body: "no", typ: tar.TypeReg},
		member{name: "dev", typ: tar.TypeChar},
		member{name: protocol.SkippedListName, body: "note", typ: tar.TypeReg},
	)
	if err := f.svc.ImportDraft(f.ctx, tm.ID, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	dir := f.svc.draftDir(tm.ID)
	if b, err := os.ReadFile(filepath.Join(dir, "config", "app.conf")); err != nil || string(b) != "x" {
		t.Fatalf("config/app.conf = %q, %v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "compose.yaml,config" {
		t.Fatalf("draft holds %v", names)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("an escaping member was written outside the draft")
	}
}

func TestImportDraftKeepsTheDraftOnFailure(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxSize = 16 })
	tm := f.create("Small")
	before, _ := os.ReadFile(filepath.Join(f.svc.draftDir(tm.ID), "compose.yaml"))
	big := tarGz(t, member{name: "big.bin", body: strings.Repeat("x", 64), typ: tar.TypeReg})
	var tooLarge *domain.TemplateTooLargeError
	if err := f.svc.ImportDraft(f.ctx, tm.ID, bytes.NewReader(big)); !errors.As(err, &tooLarge) {
		t.Fatalf("import over the limit: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(f.svc.draftDir(tm.ID), "compose.yaml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the draft changed after a failed import: %q, %v", after, err)
	}
	var def *domain.TemplateDefinitionError
	if err := f.svc.ImportDraft(f.ctx, tm.ID, strings.NewReader("not gzip")); !errors.As(err, &def) {
		t.Fatalf("not an archive: %v", err)
	}
}

func TestImportDraftSkipsEscapingSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newFixture(t)
	tm := f.create("Links")
	archive := tarGz(t,
		member{name: "compose.yaml", body: "services: {}\n", typ: tar.TypeReg},
		member{name: "inside", link: "compose.yaml", typ: tar.TypeSymlink},
		member{name: "outside", link: "../../etc/passwd", typ: tar.TypeSymlink},
	)
	if err := f.svc.ImportDraft(f.ctx, tm.ID, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	dir := f.svc.draftDir(tm.ID)
	if _, err := os.Readlink(filepath.Join(dir, "inside")); err != nil {
		t.Fatalf("inside link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "outside")); !os.IsNotExist(err) {
		t.Fatal("an escaping symlink was created")
	}
}

func TestRestoreAndDuplicateUseVersions(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Origin")
	f.write(tm.ID, "config/app.conf", "v1\n")
	if _, err := f.svc.Publish(f.ctx, tm.ID, "1.0.0", "", false, ""); err != nil {
		t.Fatal(err)
	}
	f.write(tm.ID, "config/app.conf", "edited\n")
	if _, err := f.svc.RestoreDraft(f.ctx, tm.ID, 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.svc.draftDir(tm.ID), "config", "app.conf")); string(b) != "v1\n" {
		t.Fatalf("restored file = %q", b)
	}

	dup, err := f.svc.Duplicate(f.ctx, domain.TemplateInput{Name: "Copy"}, "", tm.ID, 1, "u")
	if err != nil || dup.Visibility != domain.TemplatePrivate || dup.ID == tm.ID {
		t.Fatalf("duplicate = %+v, %v", dup, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.svc.draftDir(dup.ID), "config", "app.conf")); string(b) != "v1\n" {
		t.Fatalf("duplicated file = %q", b)
	}
	// A failed fill removes the new template again.
	if _, err := f.svc.CreateFrom(f.ctx, domain.TemplateInput{Name: "Broken"}, strings.NewReader("junk"), ""); err == nil {
		t.Fatal("a broken archive was accepted")
	}
	all, _ := f.svc.List(f.ctx, "", 0)
	for _, x := range all {
		if x.Name == "Broken" {
			t.Fatal("the template of a failed fill was kept")
		}
	}
}

func TestCleanMember(t *testing.T) {
	for in, want := range map[string]string{"./": ".", "a/b/": "a/b", "./a": "a", "a/./b": "", "../a": "", "a/../../b": "", `a\b`: ""} {
		got, ok := cleanMember(in)
		if (want == "") == ok || (ok && got != want) {
			t.Errorf("cleanMember(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
