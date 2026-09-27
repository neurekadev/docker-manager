package fsroot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// The operations themselves are covered through the agent's service
// (internal/agent/files); these tests cover the seams other callers use.

var scope = protocol.FileScope{Kind: protocol.ScopeVolume, ID: "data"}

func dirResolver(dir string) Resolver {
	return func(context.Context, protocol.FileScope) (string, error) { return dir, nil }
}

func codeOfErr(t *testing.T, err error) string {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a *protocol.Error", err)
	}
	return pe.Code
}

func TestResolverDecidesTheRoot(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	s := New(Options{Resolve: dirResolver(dir), Clock: testutil.FakeClock()})

	e, err := s.Write(ctx, protocol.FilesWriteInput{Scope: scope, Path: "a/b.txt", Data: []byte("hi"), CreateOnly: true})
	if err == nil {
		t.Fatalf("write into a missing directory = %+v, want an error", e)
	}
	if _, err := s.Mkdir(ctx, protocol.FilesMkdirInput{Scope: scope, Path: "a", Type: protocol.FileTypeDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(ctx, protocol.FilesWriteInput{Scope: scope, Path: "a/b.txt", Data: []byte("hi"), CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "a", "b.txt"))
	if err != nil || string(b) != "hi" {
		t.Fatalf("file on disk = %q, %v", b, err)
	}
	if got, err := s.ScopeDir(ctx, scope); err != nil || got != dir {
		t.Fatalf("ScopeDir = %q, %v; want %q", got, err, dir)
	}

	var out bytes.Buffer
	if err := s.Download(ctx, protocol.FilesDownloadInput{Scope: scope, Paths: []string{"a/b.txt"}, Format: protocol.FormatRaw}, &out); err != nil || out.String() != "hi" {
		t.Fatalf("download = %q, %v", out.String(), err)
	}
	res, err := s.Upload(ctx, protocol.FilesUploadInput{Scope: scope, Dir: "a", Name: "c.txt", Size: 3, CreateOnly: true}, strings.NewReader("abc"))
	if err != nil || res.Entry.Path != "a/c.txt" || res.Entry.Size != 3 {
		t.Fatalf("upload = %+v, %v", res, err)
	}
}

func TestResolverRefusalIsReturned(t *testing.T) {
	ctx := testutil.Context(t)
	s := New(Options{Resolve: func(context.Context, protocol.FileScope) (string, error) {
		return "", Fail(protocol.CodeUnsupportedVolume, "not served")
	}})
	_, err := s.List(ctx, protocol.FilesListInput{Scope: scope, Path: "."})
	if got := codeOfErr(t, err); got != protocol.CodeUnsupportedVolume {
		t.Fatalf("code = %q, want %q", got, protocol.CodeUnsupportedVolume)
	}

	none := New(Options{})
	_, err = none.List(ctx, protocol.FilesListInput{Scope: scope, Path: "."})
	if got := codeOfErr(t, err); got != protocol.CodeInternal {
		t.Fatalf("without a resolver: code = %q, want %q", got, protocol.CodeInternal)
	}

	bad := New(Options{Resolve: dirResolver(t.TempDir())})
	_, err = bad.List(ctx, protocol.FilesListInput{Scope: protocol.FileScope{Kind: "nope", ID: "x"}, Path: "."})
	if got := codeOfErr(t, err); got != protocol.CodeInvalidFrame {
		t.Fatalf("invalid scope: code = %q, want %q", got, protocol.CodeInvalidFrame)
	}
}

func TestExecutorsUseTheConfiguredKinds(t *testing.T) {
	kinds := Kinds{Delete: "x.delete", Copy: "x.copy", Move: "x.move", Archive: "x.archive", Extract: "x.extract", Metadata: "x.metadata"}
	var got []domain.JobKind
	for _, e := range New(Options{Kinds: kinds}).Executors() {
		got = append(got, e.Kind)
	}
	want := []domain.JobKind{kinds.Delete, kinds.Copy, kinds.Move, kinds.Archive, kinds.Extract, kinds.Metadata}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}

	var def []domain.JobKind
	for _, e := range New(Options{}).Executors() {
		def = append(def, e.Kind)
	}
	if def[0] != AgentKinds.Delete || def[5] != AgentKinds.Metadata {
		t.Fatalf("default kinds = %v, want the agent's files.* kinds", def)
	}
}
