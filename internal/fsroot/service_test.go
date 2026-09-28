package fsroot

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
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

func TestLimitsForUsesTheManagersLimitsWithinTheCaps(t *testing.T) {
	s := New(Options{Limits: Limits{MaxUpload: 1 << 20}})
	if got := s.limitsFor(nil); got != s.Limits() {
		t.Fatalf("without limits = %+v, want the service's %+v", got, s.Limits())
	}
	got := s.limitsFor(&protocol.FileLimits{MaxUpload: 5 << 20, MaxDownload: 2 * protocol.MaxFileLimitBytes, MaxExtractBytes: 3,
		MaxExtractRatio: 10 * protocol.MaxFileLimitRatio, MaxArchiveEntries: 10 * protocol.MaxFileLimitEntries})
	want := s.Limits()
	want.MaxUpload, want.MaxDownload, want.MaxExtractBytes = 5<<20, protocol.MaxFileLimitBytes, 3
	want.MaxExtractRatio, want.MaxArchiveEntries = protocol.MaxFileLimitRatio, protocol.MaxFileLimitEntries
	if got != want {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	// Zero fields keep the service's values.
	if got := s.limitsFor(&protocol.FileLimits{MaxDownload: 7}); got.MaxUpload != 1<<20 || got.MaxDownload != 7 || got.MaxArchiveEntries != 100_000 {
		t.Fatalf("partial limits = %+v", got)
	}
}

func TestDefaultLimitsAreTheBuiltInOnes(t *testing.T) {
	d := DefaultLimits()
	if d.MaxInline != protocol.MaxInlineContent || d.MaxUpload != 2<<30 || d.MaxDownload != 10<<30 || d.MaxArchiveEntries != 100_000 ||
		d.MaxExtractBytes != 10<<30 || d.MaxExtractRatio != 100 || d.ExtractRatioFloor != 1<<20 || d.MaxWalk != 1_000_000 {
		t.Fatalf("defaults = %+v", d)
	}
}

// TestMaxInlineBoundsReadsAndWrites: the manager's template drafts use its
// edit limit instead of the agents' frame-bound default.
func TestMaxInlineBoundsReadsAndWrites(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	s := New(Options{Resolve: dirResolver(dir), Clock: testutil.FakeClock(), Limits: Limits{MaxInline: 1024}})
	if _, err := s.Write(ctx, protocol.FilesWriteInput{Scope: scope, Path: "big", Data: make([]byte, 1025), CreateOnly: true}); codeOfErr(t, err) != protocol.CodeTooLarge {
		t.Fatalf("write over the limit: %v", err)
	}
	if _, err := s.Write(ctx, protocol.FilesWriteInput{Scope: scope, Path: "ok", Data: bytes.Repeat([]byte("a"), 1024), CreateOnly: true}); err != nil {
		t.Fatalf("write at the limit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), bytes.Repeat([]byte("b"), 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.Read(ctx, protocol.FilesReadInput{Scope: scope, Path: "long.txt"})
	if err != nil || len(out.Data) != 1024 || !out.Truncated || out.Binary {
		t.Fatalf("read = %d bytes truncated=%v binary=%v, %v", len(out.Data), out.Truncated, out.Binary, err)
	}
	// A larger edit limit reads more than one agent frame would carry.
	big := New(Options{Resolve: dirResolver(dir), Clock: testutil.FakeClock(), Limits: Limits{MaxInline: 1 << 20}})
	data := bytes.Repeat([]byte("c"), protocol.MaxInlineContent+10)
	if _, err := big.Write(ctx, protocol.FilesWriteInput{Scope: scope, Path: "edited", Data: data, CreateOnly: true}); err != nil {
		t.Fatalf("write above the agents' inline limit: %v", err)
	}
	if out, err := big.Read(ctx, protocol.FilesReadInput{Scope: scope, Path: "edited"}); err != nil || len(out.Data) != len(data) || out.Truncated {
		t.Fatalf("read = %d bytes truncated=%v, %v", len(out.Data), out.Truncated, err)
	}
}

// TestOperationLimitsApply: limits in an operation's input (the manager's
// configuration) replace the service's for that operation only.
func TestOperationLimitsApply(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	s := New(Options{Resolve: dirResolver(dir), Clock: testutil.FakeClock(), Limits: Limits{MaxUpload: 4}})

	up := protocol.FilesUploadInput{Scope: scope, Dir: ".", Name: "six", Size: 6, CreateOnly: true}
	if _, err := s.Upload(ctx, up, strings.NewReader("123456")); codeOfErr(t, err) != protocol.CodeTooLarge {
		t.Fatalf("upload over the service's limit: %v", err)
	}
	up.Limits = &protocol.FileLimits{MaxUpload: 8}
	if _, err := s.Upload(ctx, up, strings.NewReader("123456")); err != nil {
		t.Fatalf("upload within the operation's limit: %v", err)
	}
	up.Name, up.Limits = "seven", &protocol.FileLimits{MaxUpload: 5}
	if _, err := s.Upload(ctx, up, strings.NewReader("123456")); codeOfErr(t, err) != protocol.CodeTooLarge {
		t.Fatalf("upload over a lower operation limit: %v", err)
	}

	dl := protocol.FilesDownloadInput{Scope: scope, Paths: []string{"six"}, Format: protocol.FormatRaw, Limits: &protocol.FileLimits{MaxDownload: 5}}
	if err := s.Download(ctx, dl, io.Discard); codeOfErr(t, err) != protocol.CodeTooLarge {
		t.Fatalf("raw download over the operation's limit: %v", err)
	}
	dl.Limits = nil
	if err := s.Download(ctx, dl, io.Discard); err != nil {
		t.Fatalf("raw download within the default: %v", err)
	}

	// An archive of three entries: an extract preview with a limit of two
	// refuses it.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, n := range []string{"a", "b", "c"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(n))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.zip"), zb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pv := protocol.FilesPreviewInput{Scope: scope, Operation: protocol.FileOpExtract, Paths: []string{"x.zip"}, Destination: "out",
		Limits: &protocol.FileLimits{MaxArchiveEntries: 2}}
	if _, err := s.Preview(ctx, pv); codeOfErr(t, err) != protocol.CodeTooLarge {
		t.Fatalf("preview over the entry limit: %v", err)
	}
	pv.Limits = nil
	if out, err := s.Preview(ctx, pv); err != nil || out.Impact.Files != 3 {
		t.Fatalf("preview = %+v, %v", out, err)
	}
}
