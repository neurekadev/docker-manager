package protocol

import (
	"strings"
	"testing"
)

func TestSourceHashIdentifiesADefinition(t *testing.T) {
	a := NewSourceSnapshot([]SourceFile{{Path: "compose.yaml", Content: []byte("services: {}\n")}, {Path: ".env", Content: []byte("A=1\n")}})
	b := NewSourceSnapshot([]SourceFile{{Path: ".env", Content: []byte("A=1\n")}, {Path: "compose.yaml", Content: []byte("services: {}\n")}})
	if a.Hash != b.Hash || len(a.Hash) != 64 || a.Files[0].Path != ".env" {
		t.Errorf("order-dependent hash: %s %s (%v)", a.Hash, b.Hash, a.Files)
	}
	// Content, paths and the file set all change the hash.
	for name, files := range map[string][]SourceFile{
		"content": {{Path: "compose.yaml", Content: []byte("services: {}\n")}, {Path: ".env", Content: []byte("A=2\n")}},
		"path":    {{Path: "compose.yaml", Content: []byte("services: {}\n")}, {Path: "app.env", Content: []byte("A=1\n")}},
		"set":     {{Path: "compose.yaml", Content: []byte("services: {}\n")}},
	} {
		if NewSourceSnapshot(files).Hash == a.Hash {
			t.Errorf("%s change kept the hash", name)
		}
	}
	// The hash depends on file hashes only: a hash-only snapshot compares
	// equal to the full one.
	hashOnly := make([]SourceFile, len(a.Files))
	for i, f := range a.Files {
		hashOnly[i] = SourceFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size}
	}
	if SourceHash(hashOnly) != a.Hash {
		t.Error("hash-only snapshot differs")
	}
	if a.Files[1].SHA256 != FileHash([]byte("services: {}\n")) || a.Files[1].Size != 13 {
		t.Errorf("file %+v", a.Files[1])
	}
}

func TestValidateSources(t *testing.T) {
	ok := []SourceFile{{Path: "compose.yaml", Content: []byte("x")}, {Path: "config/app.env", Content: []byte("y")}}
	if err := ValidateSources(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]SourceFile{
		"escape":    {{Path: "../x"}},
		"absolute":  {{Path: "/etc/passwd"}},
		"dot":       {{Path: "."}},
		"duplicate": {{Path: "a"}, {Path: "a"}},
		"backslash": {{Path: `a\b`}},
		"file size": {{Path: "a", Content: make([]byte, MaxSourceFile+1)}},
		"total": {{Path: "a", Content: make([]byte, MaxSourceFile)}, {Path: "b", Content: make([]byte, MaxSourceFile)},
			{Path: "c", Content: []byte("x")}},
	}
	for name, files := range cases {
		if err := ValidateSources(files); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	many := make([]SourceFile, MaxSourceFiles+1)
	for i := range many {
		many[i].Path = "f" + strings.Repeat("x", i)
	}
	if ValidateSources(many) == nil {
		t.Error("too many files accepted")
	}
}

func TestProjectRefValidate(t *testing.T) {
	good := []ProjectRef{
		{Root: RootStacks, Dir: "shop", ProjectName: "shop"},
		{Root: RootStacks, Dir: "team/shop", ProjectName: "shop_2", ConfigFiles: []string{"deploy/compose.yml"}, EnvFiles: []string{"prod.env"}},
		{Root: RootBind, RootPath: "/opt/stacks", Dir: "blog", ProjectName: "blog"},
	}
	for _, r := range good {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []ProjectRef{
		{Root: "home", Dir: "shop", ProjectName: "shop"},
		{Root: RootStacks, RootPath: "/x", Dir: "shop", ProjectName: "shop"},
		{Root: RootBind, RootPath: "relative", Dir: "shop", ProjectName: "shop"},
		{Root: RootBind, RootPath: "/opt/../etc", Dir: "shop", ProjectName: "shop"},
		{Root: RootStacks, Dir: ".", ProjectName: "shop"},
		{Root: RootStacks, Dir: "../shop", ProjectName: "shop"},
		{Root: RootStacks, Dir: "shop", ProjectName: "Shop"},
		{Root: RootStacks, Dir: "shop", ProjectName: "-shop"},
		{Root: RootStacks, Dir: "shop", ProjectName: "shop", ConfigFiles: []string{"../compose.yaml"}},
		{Root: RootStacks, Dir: "shop", ProjectName: "shop", EnvFiles: []string{"/etc/env"}},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	if ValidProjectName(strings.Repeat("a", 64)) || !ValidProjectName(strings.Repeat("a", 63)) {
		t.Error("project name length bound")
	}
}

func TestAnonymousVolumeName(t *testing.T) {
	id := strings.Repeat("0123456789abcdef", 4)
	if !AnonymousVolumeName(id) {
		t.Errorf("%s is the form of an anonymous volume", id)
	}
	for _, name := range []string{"shop_db-data", strings.ToUpper(id), id[:63], id + "0", strings.Repeat("g", 64), ""} {
		if AnonymousVolumeName(name) {
			t.Errorf("%q is not an anonymous volume name", name)
		}
	}
}
