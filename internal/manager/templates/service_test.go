package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return db
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	db    *bun.DB
	data  string
	svc   *Service
	kinds []domain.JobKind
}

func newFixture(t *testing.T, mods ...func(*Options)) *fixture {
	t.Helper()
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: testutil.Context(t), db: openDB(t), data: t.TempDir()}
	o := Options{DB: f.db, Keyring: secrets.NewKeyring(key), Clock: testutil.FakeClock(), Logger: testutil.Logger(t), DataDir: f.data,
		Executors: func(x jobexec.Executor) error { f.kinds = append(f.kinds, x.Kind); return nil }}
	for _, m := range mods {
		m(&o)
	}
	if f.svc, err = New(f.ctx, o); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) create(name string) domain.Template {
	f.t.Helper()
	tm, err := f.svc.Create(f.ctx, domain.TemplateInput{Name: name, Tags: []string{"Web", "web", " proxy "}}, "user-1")
	if err != nil {
		f.t.Fatal(err)
	}
	return tm
}

func (f *fixture) write(id, rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.svc.draftDir(id), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// entries lists an archive's entries as "name type size".
func entries(t *testing.T, b []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name+" "+string(h.Typeflag))
	}
}

func TestCreateWritesAPrivateStarterDraft(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Nextcloud")
	if tm.Visibility != domain.TemplatePrivate || tm.Revision != 1 || tm.Latest != nil || tm.Versions != 0 {
		t.Fatalf("created %+v", tm)
	}
	if strings.Join(tm.Tags, ",") != "proxy,web" {
		t.Fatalf("tags = %v, want normalized [proxy web]", tm.Tags)
	}
	if _, err := os.Stat(filepath.Join(f.svc.draftDir(tm.ID), "compose.yaml")); err != nil {
		t.Fatalf("starter compose.yaml: %v", err)
	}
	if _, err := f.svc.Create(f.ctx, domain.TemplateInput{Name: " nextcloud "}, ""); !errors.Is(err, domain.ErrTemplateNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	var fe *domain.FieldError
	if _, err := f.svc.Create(f.ctx, domain.TemplateInput{Name: "x", Tags: []string{"no spaces"}}, ""); !errors.As(err, &fe) {
		t.Fatalf("bad tag: %v", err)
	}
	if len(f.kinds) != 6 || f.kinds[0] != jobspec.TemplateFilesDelete {
		t.Fatalf("registered executors %v", f.kinds)
	}
}

func TestUpdateAndVisibilityUseRevisions(t *testing.T) {
	f := newFixture(t)
	tm := f.create("App")
	name := "App 2"
	up, err := f.svc.Update(f.ctx, tm.ID, tm.Revision, domain.TemplatePatch{Name: &name})
	if err != nil || up.Name != name || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := f.svc.Update(f.ctx, tm.ID, tm.Revision, domain.TemplatePatch{Name: &name}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := f.svc.SetVisibility(f.ctx, tm.ID, up.Revision, domain.TemplatePublic, false); !errors.Is(err, domain.ErrTemplatePublicAckRequired) {
		t.Fatalf("public without acknowledgement: %v", err)
	}
	pub, err := f.svc.SetVisibility(f.ctx, tm.ID, up.Revision, domain.TemplatePublic, true)
	if err != nil || pub.Visibility != domain.TemplatePublic {
		t.Fatalf("public = %+v, %v", pub, err)
	}
	priv, err := f.svc.SetVisibility(f.ctx, tm.ID, pub.Revision, domain.TemplatePrivate, false)
	if err != nil || priv.Visibility != domain.TemplatePrivate {
		t.Fatalf("private = %+v, %v", priv, err)
	}
}

func TestPublishFreezesACanonicalSealedArchive(t *testing.T) {
	f := newFixture(t)
	tm := f.create("App")
	f.write(tm.ID, ".env", "PASSWORD=draft-secret\n")
	f.write(tm.ID, "config/nginx.conf", "server {}\n")
	v, err := f.svc.Publish(f.ctx, tm.ID, "1.0.0", "first", false, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Number != 1 || v.Entries != 4 || v.ContentSize != int64(len(starter)+len("PASSWORD=draft-secret\n")+len("server {}\n")) {
		t.Fatalf("version %+v", v)
	}
	if len(v.Definition) != 2 || v.Definition[0].Path != ".env" || v.Definition[1].Path != "compose.yaml" {
		t.Fatalf("definition %+v", v.Definition)
	}
	got, b, err := f.svc.Archive(f.ctx, tm.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != got.ArchiveSHA256 || int64(len(b)) != got.ArchiveSize {
		t.Fatalf("archive does not match its recorded digest")
	}
	want := []string{"./ 5", ".env 0", "compose.yaml 0", "config/ 5", "config/nginx.conf 0"}
	if e := entries(t, b); strings.Join(e, "|") != strings.Join(want, "|") {
		t.Fatalf("entries %v, want %v", e, want)
	}
	// The draft's .env is sealed at rest, never stored in the clear.
	sealed, err := store.TemplateVersionArchive(f.ctx, f.db, tm.ID, 1)
	if err != nil || strings.Contains(sealed, "draft-secret") {
		t.Fatalf("sealed archive: %v", err)
	}
	if _, err := f.svc.Publish(f.ctx, tm.ID, "1.0.0", "", false, ""); !errors.Is(err, domain.ErrTemplateVersionLabelTaken) {
		t.Fatalf("duplicate label: %v", err)
	}
	// Numbers are never reused, even after deleting the newest version.
	if err := f.svc.DeleteVersion(f.ctx, tm.ID, 1); err != nil {
		t.Fatal(err)
	}
	v2, err := f.svc.Publish(f.ctx, tm.ID, "1.0.1", "", false, "")
	if err != nil || v2.Number != 2 {
		t.Fatalf("second version = %+v, %v", v2, err)
	}
	cur, _ := f.svc.Get(f.ctx, tm.ID)
	if cur.Versions != 1 || cur.Latest == nil || cur.Latest.Label != "1.0.1" {
		t.Fatalf("template after publish %+v", cur)
	}
}

func TestPublishIsDeterministic(t *testing.T) {
	f := newFixture(t)
	tm := f.create("App")
	var sums []string
	for i, label := range []string{"a", "b"} {
		_, err := f.svc.Publish(f.ctx, tm.ID, label, "", false, "")
		if err != nil {
			t.Fatal(err)
		}
		v, b, err := f.svc.Archive(f.ctx, tm.ID, i+1)
		if err != nil {
			t.Fatal(err)
		}
		_ = v
		sum := sha256.Sum256(b)
		sums = append(sums, hex.EncodeToString(sum[:]))
	}
	// The fake clock does not move: the same draft yields the same bytes.
	if sums[0] != sums[1] {
		t.Fatalf("two publications of the same draft differ")
	}
}

func TestPublishRefusesUnusableDrafts(t *testing.T) {
	f := newFixture(t)
	var def *domain.TemplateDefinitionError

	pinned := f.create("Pinned")
	f.write(pinned.ID, "compose.yaml", "name: fixed\nservices:\n  web:\n    image: nginx\n")
	if _, err := f.svc.Publish(f.ctx, pinned.ID, "1", "", false, ""); !errors.As(err, &def) || !strings.Contains(def.Message, "name:") {
		t.Fatalf("pinned project name: %v", err)
	}

	override := f.create("Override")
	f.write(override.ID, "compose.override.yaml", "name: fixed\n")
	if _, err := f.svc.Publish(f.ctx, override.ID, "1", "", false, ""); !errors.As(err, &def) {
		t.Fatalf("pinned name in an override file: %v", err)
	}

	empty := f.create("Empty")
	if err := os.Remove(filepath.Join(f.svc.draftDir(empty.ID), "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Publish(f.ctx, empty.ID, "1", "", false, ""); !errors.As(err, &def) || !strings.Contains(def.Message, "compose.yaml") {
		t.Fatalf("no compose file: %v", err)
	}

	if _, err := f.svc.Publish(f.ctx, empty.ID, "bad label", "", false, ""); err == nil {
		t.Fatal("a label with a space was accepted")
	}

	pub := f.create("Public")
	pub, err := f.svc.SetVisibility(f.ctx, pub.ID, pub.Revision, domain.TemplatePublic, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Publish(f.ctx, pub.ID, "1", "", false, ""); !errors.Is(err, domain.ErrTemplatePublicAckRequired) {
		t.Fatalf("public publish without acknowledgement: %v", err)
	}
}

func TestPublishRefusesEscapingSymlinks(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Links")
	dir := f.svc.draftDir(tm.ID)
	if err := os.Symlink("../../outside", filepath.Join(dir, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	var def *domain.TemplateDefinitionError
	if _, err := f.svc.Publish(f.ctx, tm.ID, "1", "", false, ""); !errors.As(err, &def) || !strings.Contains(def.Message, "escape") {
		t.Fatalf("escaping symlink: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("compose.yaml", filepath.Join(dir, "alias.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Publish(f.ctx, tm.ID, "1", "", false, ""); err != nil {
		t.Fatalf("a symlink inside the template was refused: %v", err)
	}
}

func TestSizeLimitAppliesToDraftAndPublish(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxSize = 1 << 10; o.MaxEntries = 3 })
	tm := f.create("Small")
	var tooLarge *domain.TemplateTooLargeError
	if err := f.svc.CheckQuota(f.ctx, tm.ID, 2<<10); !errors.As(err, &tooLarge) {
		t.Fatalf("quota for 2 KiB: %v", err)
	}
	if err := f.svc.CheckQuota(f.ctx, tm.ID, 10); err != nil {
		t.Fatalf("quota for 10 bytes: %v", err)
	}
	f.write(tm.ID, "big.bin", strings.Repeat("x", 2<<10))
	f.svc.FilesChanged(tm.ID)
	if _, err := f.svc.Publish(f.ctx, tm.ID, "1", "", false, ""); !errors.As(err, &tooLarge) {
		t.Fatalf("publish over the size limit: %v", err)
	}
}

func TestDraftFilesUseTheTemplateRoot(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Files")
	scope := protocol.FileScope{Kind: protocol.ScopeTemplate, ID: tm.ID}
	out, err := f.svc.Files().List(f.ctx, protocol.FilesListInput{Scope: scope, Path: "."})
	if err != nil || len(out.Entries) != 1 || out.Entries[0].Name != "compose.yaml" {
		t.Fatalf("listing = %+v, %v", out, err)
	}
	var pe *protocol.Error
	_, err = f.svc.Files().List(f.ctx, protocol.FilesListInput{Scope: protocol.FileScope{Kind: protocol.ScopeTemplate, ID: "missing"}, Path: "."})
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("unknown template: %v", err)
	}
	if _, err := f.svc.resolve(f.ctx, protocol.FileScope{Kind: protocol.ScopeVolume, ID: "data"}); err == nil {
		t.Fatal("a volume scope was resolved by the template service")
	}
}

func TestDeleteRemovesTheDraftAndSweepRemovesOrphans(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Gone")
	orphan := filepath.Join(f.data, "templates", "orphan", "draft")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(f.ctx, tm.ID, tm.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.data, "templates", tm.ID)); !os.IsNotExist(err) {
		t.Fatalf("draft directory after delete: %v", err)
	}
	if _, err := f.svc.Get(f.ctx, tm.ID); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := New(f.ctx, f.svc.opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned draft after restart: %v", err)
	}
}

func TestIconsAreStoredAndReplaced(t *testing.T) {
	f := newFixture(t)
	tm := f.create("Icon")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
	got, err := f.svc.SetIcon(f.ctx, tm.ID, svg)
	if err != nil || got.Icon == nil || got.Icon.MediaType != IconSVG {
		t.Fatalf("set icon = %+v, %v", got.Icon, err)
	}
	icon, data, err := f.svc.Icon(f.ctx, tm.ID)
	if err != nil || !bytes.Equal(data, svg) || icon.SHA256 != got.Icon.SHA256 {
		t.Fatalf("icon = %+v, %v", icon, err)
	}
	if _, err := f.svc.RemoveIcon(f.ctx, tm.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Icon(f.ctx, tm.ID); !errors.Is(err, domain.ErrTemplateIconNotFound) {
		t.Fatalf("icon after removal: %v", err)
	}
}
