package stackarchives

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 10, 8, 9, 10, 123456789, time.UTC)

// member is one entry of a test tar.
type member struct {
	name, link string
	typ        byte
	mode       int64
	uid, gid   int
	data       string
}

// transferTar builds a part as migration.send writes it: "./" first, names
// relative to it.
func transferTar(t *testing.T, ms ...member) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, m := range ms {
		h := &tar.Header{Format: tar.FormatPAX, Name: m.name, Linkname: m.link, Typeflag: m.typ, Mode: m.mode, Uid: m.uid, Gid: m.gid,
			ModTime: testTime, AccessTime: testTime}
		if m.typ == tar.TypeReg {
			h.Size = int64(len(m.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testManifest() Manifest {
	return Manifest{Format: FormatName, Version: FormatVersion, ExportedAt: testTime, ManagerVersion: "edge",
		Stack:   ManifestStack{Name: "shop", DisplayName: "Shop"},
		Volumes: []ManifestVolume{{Key: "dbdata", Name: "shop_dbdata", FollowsProject: true, Labels: map[string]string{"backup.exclude": "true"}}}}
}

var projectPart = []member{
	{name: "./", typ: tar.TypeDir, mode: 0o755},
	{name: "compose.yaml", typ: tar.TypeReg, mode: 0o644, data: "name: shop\nservices: {}\n"},
	{name: "data/", typ: tar.TypeDir, mode: 0o2775, uid: 33, gid: 33},
	{name: "data/index.html", typ: tar.TypeReg, mode: 0o644, uid: 33, gid: 33, data: "<h1>shop</h1>\n"},
	{name: "data/copy.html", typ: tar.TypeLink, link: "data/index.html"},
	{name: "data/current", typ: tar.TypeSymlink, link: "index.html", uid: 33, gid: 33},
	{name: "data/pipe", typ: tar.TypeFifo, mode: 0o600},
}

var volumePart = []member{
	{name: "./", typ: tar.TypeDir, mode: 0o700, uid: 999, gid: 999},
	{name: "PG_VERSION", typ: tar.TypeReg, mode: 0o600, uid: 999, gid: 999, data: "17\n"},
}

// writeArchive writes an archive of the manifest and parts.
func writeArchive(t *testing.T, m Manifest, parts map[Part][]byte, order ...Part) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := NewWriter(&b, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range order {
		if _, err := w.AddPart(p, bytes.NewReader(parts[p])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func headers(t *testing.T, b []byte) []tar.Header {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	var out []tar.Header
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *h)
	}
}

// TestRoundTrip: a part comes back exactly as the agents' transfer wrote
// it (names, types, owners, modes, times, links, contents), and the
// archive itself is an ordinary tar.gz with the parts below their prefixes.
func TestRoundTrip(t *testing.T) {
	proj, vol := transferTar(t, projectPart...), transferTar(t, volumePart...)
	b := writeArchive(t, testManifest(), map[Part][]byte{{}: proj, {Volume: "dbdata"}: vol}, Part{}, Part{Volume: "dbdata"})

	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var names []string
	for _, h := range headers(t, raw) {
		names = append(names, h.Name)
		if h.Name == "project/data/copy.html" && h.Linkname != "project/data/index.html" {
			t.Errorf("hard link target %q", h.Linkname)
		}
	}
	want := "docker-manager-stack.json project/ project/compose.yaml project/data/ project/data/index.html project/data/copy.html " +
		"project/data/current project/data/pipe volumes/dbdata/ volumes/dbdata/PG_VERSION"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("members\n got %s\nwant %s", got, want)
	}

	rd, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if m := rd.Manifest(); m.Stack.Name != "shop" || len(m.Volumes) != 1 || m.Volumes[0].Labels["backup.exclude"] != "true" {
		t.Fatalf("manifest %+v", m)
	}
	for _, wantPart := range []struct {
		p    Part
		src  []byte
		want PartStats
	}{{Part{}, proj, PartStats{Entries: 7, Bytes: 38}}, {Part{Volume: "dbdata"}, vol, PartStats{Entries: 2, Bytes: 3}}} {
		p, err := rd.Next()
		if err != nil || p != wantPart.p {
			t.Fatalf("Next = %v, %v; want %v", p, err, wantPart.p)
		}
		var out bytes.Buffer
		st, err := rd.WriteTo(p, &out)
		if err != nil {
			t.Fatal(err)
		}
		if st != wantPart.want {
			t.Errorf("%s stats %+v, want %+v", p.Name(), st, wantPart.want)
		}
		got, src := headers(t, out.Bytes()), headers(t, wantPart.src)
		if len(got) != len(src) {
			t.Fatalf("%s: %d members, want %d", p.Name(), len(got), len(src))
		}
		for i := range src {
			g, s := got[i], src[i]
			if g.Name != s.Name || g.Linkname != s.Linkname || g.Typeflag != s.Typeflag || g.Mode != s.Mode || g.Uid != s.Uid ||
				g.Gid != s.Gid || g.Size != s.Size || !g.ModTime.Equal(s.ModTime) || !g.AccessTime.Equal(s.AccessTime) {
				t.Errorf("%s member %d:\n got %+v\nwant %+v", p.Name(), i, g, s)
			}
		}
	}
	if _, err := rd.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last part: %v", err)
	}
	if got := PinnedName(rd.ComposeFiles(), nil); got != "shop" {
		t.Errorf("PinnedName = %q", got)
	}
}

// rawArchive builds an archive member by member (to break the rules).
func rawArchive(t *testing.T, gz bool, m *Manifest, ms ...member) []byte {
	t.Helper()
	var all []member
	if m != nil {
		b, _ := json.Marshal(m)
		all = append(all, member{name: ManifestName, typ: tar.TypeReg, mode: 0o644, data: string(b)})
	}
	b := transferTar(t, append(all, ms...)...)
	if !gz {
		return b
	}
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return out.Bytes()
}

// readAll reads every part of an archive to nowhere.
func readAll(b []byte) error {
	_, err := inspect(bytes.NewReader(b), 1<<30)
	return err
}

// TestReaderAcceptsToolArchives: a plain tar and the directory entries tar
// tools add above the parts ("./", "volumes/") read like our own output.
func TestReaderAcceptsToolArchives(t *testing.T) {
	m := testManifest()
	b := rawArchive(t, false, &m,
		member{name: "./", typ: tar.TypeDir, mode: 0o755},
		member{name: "./project/", typ: tar.TypeDir, mode: 0o755},
		member{name: "./project/compose.yaml", typ: tar.TypeReg, mode: 0o644, data: "services: {}\n"},
		member{name: "./volumes/", typ: tar.TypeDir, mode: 0o755},
		member{name: "./volumes/dbdata/", typ: tar.TypeDir, mode: 0o700},
		member{name: "./volumes/dbdata/PG_VERSION", typ: tar.TypeReg, mode: 0o600, data: "17\n"})
	u, err := inspect(bytes.NewReader(b), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if u.Project.Entries != 2 || u.Volumes["dbdata"].Bytes != 3 || u.PinnedName != "" {
		t.Fatalf("upload %+v", u)
	}
}

// TestReaderRefuses: everything the agents' extraction would refuse, and
// everything that breaks the layout, is refused before any part is used.
func TestReaderRefuses(t *testing.T) {
	m := testManifest()
	dir := func(n string) member { return member{name: n, typ: tar.TypeDir, mode: 0o755} }
	file := func(n string) member { return member{name: n, typ: tar.TypeReg, mode: 0o644, data: "x"} }
	vol := []member{dir("volumes/dbdata/"), file("volumes/dbdata/x")}
	bad := func(edit func(m *Manifest)) *Manifest { c := testManifest(); edit(&c); return &c }
	cases := map[string]struct {
		m  *Manifest
		ms []member
	}{
		"no manifest":         {nil, []member{dir("project/"), file("project/compose.yaml")}},
		"manifest not first":  {nil, []member{dir("project/"), file(ManifestName)}},
		"no project":          {&m, vol},
		"volume first":        {&m, append(append([]member{}, vol...), dir("project/"))},
		"missing volume":      {&m, []member{dir("project/"), file("project/a")}},
		"unknown prefix":      {&m, []member{dir("project/"), dir("other/"), file("other/x")}},
		"unknown volume":      {&m, []member{dir("project/"), dir("volumes/web/"), file("volumes/web/x")}},
		"part not contiguous": {&m, append(append([]member{dir("project/")}, vol...), file("project/late"))},
		"part without root":   {&m, []member{file("project/a")}},
		"escaping name":       {&m, []member{dir("project/"), file("project/../../etc/passwd")}},
		"device node":         {&m, []member{dir("project/"), {name: "project/dev", typ: tar.TypeChar}}},
		"hard link across":    {&m, append([]member{dir("project/"), file("project/a")}, dir("volumes/dbdata/"), member{name: "volumes/dbdata/b", typ: tar.TypeLink, link: "project/a"})},
		"hard link to root":   {&m, append([]member{dir("project/"), {name: "project/b", typ: tar.TypeLink, link: "project"}}, vol...)},
		"wrong format":        {bad(func(m *Manifest) { m.Format = "zip" }), nil},
		"newer version":       {bad(func(m *Manifest) { m.Version = 2 }), nil},
		"bad stack name":      {bad(func(m *Manifest) { m.Stack.Name = "Shop!" }), nil},
		"bad volume key":      {bad(func(m *Manifest) { m.Volumes[0].Key = "../x" }), nil},
		"compose label":       {bad(func(m *Manifest) { m.Volumes[0].Labels = map[string]string{"com.docker.compose.project": "x"} }), nil},
		"own label":           {bad(func(m *Manifest) { m.Volumes[0].Labels = map[string]string{"docker-manager.migration": "x"} }), nil},
		"not following":       {bad(func(m *Manifest) { m.Volumes[0].Name = "other" }), nil},
		"definition file":     {bad(func(m *Manifest) { m.Stack.ConfigFiles = []string{"../compose.yaml"} }), nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := readAll(rawArchive(t, true, c.m, c.ms...))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	t.Run("not a tar", func(t *testing.T) {
		if err := readAll([]byte("PK\x03\x04 a zip file")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("oversized manifest", func(t *testing.T) {
		big := member{name: ManifestName, typ: tar.TypeReg, mode: 0o644, data: strings.Repeat(" ", MaxManifest+1)}
		if err := readAll(rawArchive(t, true, nil, big)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("checksum", func(t *testing.T) {
		b := writeArchive(t, testManifest(), map[Part][]byte{{}: transferTar(t, projectPart...), {Volume: "dbdata"}: transferTar(t, volumePart...)},
			Part{}, Part{Volume: "dbdata"})
		b[len(b)-8] ^= 0xff // the gzip trailer's CRC-32
		if err := readAll(b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("decompression bomb", func(t *testing.T) {
		b := writeArchive(t, testManifest(), map[Part][]byte{{}: transferTar(t, projectPart...), {Volume: "dbdata"}: transferTar(t, volumePart...)},
			Part{}, Part{Volume: "dbdata"})
		if _, err := inspect(bytes.NewReader(b), 40); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
		if _, err := inspect(bytes.NewReader(b), 41); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		b := writeArchive(t, testManifest(), map[Part][]byte{{}: transferTar(t, projectPart...), {Volume: "dbdata"}: transferTar(t, volumePart...)},
			Part{}, Part{Volume: "dbdata"})
		if err := readAll(b[:len(b)/2]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestWriterRefusesParts: AddPart only takes a transfer tar.
func TestWriterRefusesParts(t *testing.T) {
	for name, ms := range map[string][]member{
		"no root":     {{name: "a", typ: tar.TypeReg, data: "x"}},
		"late root":   {{name: "a", typ: tar.TypeReg, data: "x"}, {name: "./", typ: tar.TypeDir}},
		"escape":      {{name: "./", typ: tar.TypeDir}, {name: "../a", typ: tar.TypeReg}},
		"link to dot": {{name: "./", typ: tar.TypeDir}, {name: "a", typ: tar.TypeLink, link: "./"}},
	} {
		t.Run(name, func(t *testing.T) {
			w, err := NewWriter(io.Discard, testManifest())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.AddPart(Part{}, bytes.NewReader(transferTar(t, ms...))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestExplicitVolumeNames(t *testing.T) {
	files := map[string][]byte{
		"compose.yaml":          []byte("volumes:\n  data:\n    name: shop_data\n  cache: {}\n  env:\n    name: ${PREFIX}_env\n  bad:\n    name: \"no/slash\"\n  plain:\n"),
		"compose.override.yaml": []byte("volumes:\n  logs:\n    name: logs-forever\n  data:\n    name: override_data\n"),
		"compose.bak.yaml":      []byte("volumes:\n  logs:\n    name: never-loaded\n"),
		"stack.yml":             []byte("volumes:\n  data:\n    name: configured\n"),
	}
	// The defaults: the base file, then its override (which wins); other
	// files are not loaded; invalid and interpolated names are ignored.
	got := ExplicitVolumeNames(files, nil)
	if len(got) != 2 || got["data"] != "override_data" || got["logs"] != "logs-forever" {
		t.Fatalf("named %v", got)
	}
	if got := ExplicitVolumeNames(files, []string{"stack.yml"}); len(got) != 1 || got["data"] != "configured" {
		t.Fatalf("configured files: %v", got)
	}
}

// TestReaderBounds: the whole decompressed stream is bounded from its
// first byte (headers count, not only file data), and little may follow
// the archive's end.
func TestReaderBounds(t *testing.T) {
	m := testManifest()
	m.Volumes = nil
	ms := []member{{name: "project/", typ: tar.TypeDir}}
	for i := range 2000 {
		ms = append(ms, member{name: "project/d" + strings.Repeat("x", 50) + itoa(i) + "/", typ: tar.TypeDir})
	}
	b := rawArchive(t, true, &m, ms...)
	if _, err := inspect(bytes.NewReader(b), 1<<20); err != nil {
		t.Fatalf("within the bound: %v", err)
	}
	// Past the stream bound (set tight here; SetLimit adds a margin of
	// 64 MiB for headers), header-only members fail too.
	rd, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	rd.src.max = 64 << 10
	p, err := rd.Next()
	if err == nil {
		_, err = rd.WriteTo(p, io.Discard)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("2000 headers past a 64 KiB bound: %v", err)
	}
	if rd, err := NewLimitedReader(bytes.NewReader(b), 1<<20); err != nil || rd.src.max != 2<<20+64<<20 {
		t.Fatalf("limited reader: %v", err)
	}

	raw := transferTar(t, member{name: ManifestName, typ: tar.TypeReg, data: mustJSON(t, m)}, member{name: "project/", typ: tar.TypeDir})
	tail := append(append([]byte{}, raw...), make([]byte, maxTrailing+1024)...)
	if err := readAll(tail); !errors.Is(err, ErrInvalid) {
		t.Fatalf("data after the end: %v", err)
	}
	if err := readAll(append(append([]byte{}, raw...), make([]byte, 10240)...)); err != nil {
		t.Fatalf("record padding: %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPinnedName(t *testing.T) {
	for _, c := range []struct {
		files map[string][]byte
		want  string
	}{
		{map[string][]byte{"compose.yaml": []byte("services: {}\n")}, ""},
		{map[string][]byte{"compose.yaml": []byte("name: shop\nservices: {}\n")}, "shop"},
		{map[string][]byte{"compose.yaml": []byte("name: ${COMPOSE_PROJECT_NAME}\n")}, ""},
		{map[string][]byte{"compose.yaml": []byte("services:\n  web:\n    name: nested\n")}, ""},
		{map[string][]byte{"compose.yaml": []byte(": not yaml [")}, ""},
		{map[string][]byte{"compose.yaml": []byte("name: base\n"), "compose.override.yaml": []byte("name: pinned\n")}, "pinned"},
		{map[string][]byte{"compose.yaml": []byte("services: {}\n"), "compose.bak.yaml": []byte("name: never\n")}, ""},
	} {
		if got := PinnedName(c.files, nil); got != c.want {
			t.Errorf("PinnedName(%v) = %q, want %q", c.files, got, c.want)
		}
	}
}

// TestComposeFilesCaptured: only the project's root Compose files (and its
// configured ones) are read on the way.
func TestComposeFilesCaptured(t *testing.T) {
	m := testManifest()
	m.Volumes = nil
	m.Stack.ConfigFiles = []string{"stack.yml"}
	b := rawArchive(t, true, &m,
		member{name: "project/", typ: tar.TypeDir},
		member{name: "project/compose.yaml", typ: tar.TypeReg, data: "a"},
		member{name: "project/docker-compose.override.yml", typ: tar.TypeReg, data: "b"},
		member{name: "project/stack.yml", typ: tar.TypeReg, data: "c"},
		member{name: "project/notes.yaml", typ: tar.TypeReg, data: "d"},
		member{name: "project/sub/", typ: tar.TypeDir},
		member{name: "project/sub/compose.yaml", typ: tar.TypeReg, data: "e"})
	rd, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	p, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rd.WriteTo(p, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := rd.ComposeFiles()
	if len(got) != 3 || string(got["compose.yaml"]) != "a" || string(got["docker-compose.override.yml"]) != "b" || string(got["stack.yml"]) != "c" {
		t.Fatalf("compose files %v", got)
	}
}
