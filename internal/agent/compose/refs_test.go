package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestDefinitionFilesFollowIncludesAndExtends (#283): every file the
// definition loads through include (nested includes, their env files) and
// extends: file: (the whole chain) is a definition file, with paths
// resolved like compose-go resolves them and interpolated from the
// project's env files.
func TestDefinitionFilesFollowIncludesAndExtends(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"compose.yaml": `include:
  - inc/${PART}.yaml
  - path: [db/db.yaml, db/db.override.yaml]
    env_file: db/db.env
services:
  web:
    extends:
      file: base/common.yaml
      service: web
`,
		".env": "PART=app\n",
		// An included file resolves its own includes and extends from its
		// directory, and interpolates with its directory's .env.
		"inc/app.yaml":         "include: [nested/${NESTED}.yaml]\nservices:\n  app:\n    extends: {file: shared.yaml, service: app}\n",
		"inc/.env":             "NESTED=more\n",
		"inc/nested/more.yaml": "services:\n  more:\n    image: more:1\n",
		"inc/shared.yaml":      "services:\n  app:\n    image: app:1\n",
		"db/db.yaml":           "services:\n  db:\n    image: db:${DB_TAG}\n",
		"db/db.override.yaml":  "services:\n  db:\n    labels: [x=1]\n",
		"db/db.env":            "DB_TAG=16\n",
		// An extended file's own extends resolve from its directory; only
		// the chain of the extended service is followed.
		"base/common.yaml": "services:\n  web:\n    extends: {file: root.yaml, service: root}\n  other:\n    extends: {file: unused.yaml, service: x}\n",
		"base/root.yaml":   "services:\n  root:\n    extends: middle\n  middle:\n    extends: {file: ../leaf.yaml, service: leaf}\n",
		"leaf.yaml":        "services:\n  leaf:\n    image: web:1\n",
		"base/unused.yaml": "services:\n  x:\n    image: x:1\n",
	})
	p, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range p.DefinitionFiles {
		r, _ := filepath.Rel(dir, f)
		got = append(got, filepath.ToSlash(r))
	}
	want := []string{".env", "base/common.yaml", "base/root.yaml", "compose.yaml", "db/db.env", "db/db.override.yaml", "db/db.yaml",
		"inc/.env", "inc/app.yaml", "inc/nested/more.yaml", "inc/shared.yaml", "leaf.yaml"}
	if !slices.Equal(got, want) {
		t.Errorf("definition files\n got %v\nwant %v", got, want)
	}
	if img := p.model.Services["web"].Image; img != "web:1" {
		t.Errorf("web image %q", img)
	}

	// A definition loaded from memory walks the included files on disk.
	c, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p", Content: map[string][]byte{
		"compose.yaml": []byte("include: [db/db.yaml]\n"), ".env": []byte("DB_TAG=1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.DefinitionFiles, filepath.Join(dir, "db", "db.yaml")) {
		t.Errorf("in-memory definition files %v", c.DefinitionFiles)
	}
}

// TestRefusesReferencesOutsideTheProject (#283): an included or extended
// file outside the project directory (also through a symlink), a remote
// include and an include path compose-go would resolve against the agent's
// own working directory are refused before compose-go reads them.
func TestRefusesReferencesOutsideTheProject(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		link  string // inc.yaml -> ../outside.yaml
		want  string
	}{
		"include":          {files: map[string]string{"compose.yaml": "include: [../outside.yaml]\n"}, want: "outside the project directory"},
		"absolute include": {files: map[string]string{"compose.yaml": "include: [/etc/outside.yaml]\n"}, want: "outside the project directory"},
		"extends": {files: map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: ../outside.yaml, service: web}\n"},
			want: `service "web" extends`},
		"nested extends": {files: map[string]string{
			"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: web}\n",
			"base.yaml":    "services:\n  web:\n    extends: {file: ../outside.yaml, service: web}\n"}, want: "outside the project directory"},
		"include env_file": {files: map[string]string{"compose.yaml": "include:\n  - path: inc.yaml\n    env_file: ../outside.env\n",
			"inc.yaml": "services:\n  a:\n    image: a\n"}, want: "env file"},
		"nested include": {files: map[string]string{"compose.yaml": "include: [inc/inc.yaml]\n", "inc/inc.yaml": "include: [../../outside.yaml]\n"},
			want: "inc/inc.yaml includes"},
		"interpolated": {files: map[string]string{"compose.yaml": "include: [${WHERE}/outside.yaml]\n", ".env": "WHERE=..\n"},
			want: "outside the project directory"},
		"symlink": {files: map[string]string{"compose.yaml": "include: [inc.yaml]\n"}, link: "inc.yaml", want: "outside the project directory"},
		"remote":  {files: map[string]string{"compose.yaml": "include: [oci://registry.example/app:1]\n"}, want: "remote include"},
		"git":     {files: map[string]string{"compose.yaml": "include: [git@github.com:x/y.git]\n"}, want: "remote include"},
		"relative env_file in a nested include": {files: map[string]string{"compose.yaml": "include: [inc/inc.yaml]\n",
			"inc/inc.yaml":  "include:\n  - path: more.yaml\n    env_file: more.env\n",
			"inc/more.yaml": "services:\n  a:\n    image: a\n", "inc/more.env": "A=1\n"}, want: "relative project_directory or env_file"},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "app")
			writeFiles(t, dir, tc.files)
			writeFiles(t, parent, map[string]string{"outside.yaml": "services:\n  web:\n    image: x\n", "outside.env": "A=1\n"})
			if tc.link != "" {
				if err := os.Symlink(filepath.Join(parent, "outside.yaml"), filepath.Join(dir, tc.link)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p"})
			if engine.CodeOf(err) != engine.CodeUnsupportedFeature || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadProject = %v (%s), want unsupported_compose_feature with %q", err, engine.CodeOf(err), tc.want)
			}
		})
	}
}

// TestWalksInMemoryComposeFilesNamedWithDots: a declared Compose file
// whose name starts with ".." is inside the project, so its in-memory
// content is walked (and its outside include refused).
func TestWalksInMemoryComposeFilesNamedWithDots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	_, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p", ConfigFiles: []string{"..compose.yml"},
		Content: map[string][]byte{"..compose.yml": []byte("include: [../outside.yaml]\n")}})
	if engine.CodeOf(err) != engine.CodeUnsupportedFeature || !strings.Contains(err.Error(), "outside the project directory") {
		t.Fatalf("LoadProject = %v (%s), want the outside include refused", err, engine.CodeOf(err))
	}
}

// TestReferencedFilesOfABrokenDefinition: a definition that does not load
// still names the included and extended files inside the project, so they
// are read and recorded with it.
func TestReferencedFilesOfABrokenDefinition(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"compose.yaml": "include: [inc.yaml, ../outside.yaml]\nservices:\n  web:\n    image: web\n    extends: {file: base.yaml, service: web}\n    bogus: true\n",
		"inc.yaml":     "services:\n  a:\n    image: a\n",
		"base.yaml":    "services:\n  web:\n    image: web\n",
	})
	if _, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p"}); err == nil {
		t.Fatal("the broken definition loaded")
	}
	want := []string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "inc.yaml")}
	if got := ReferencedFiles(ProjectSpec{Dir: dir}); !slices.Equal(got, want) {
		t.Errorf("ReferencedFiles = %v, want %v", got, want)
	}
}
