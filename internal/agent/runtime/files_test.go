package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestFilesServiceWiring: with Files the agent serves the files.*
// requests and streams and executes the files.* job kinds, and advertises
// them; explicitly configured handlers keep precedence.
func TestFilesServiceWiring(t *testing.T) {
	own := func(context.Context, json.RawMessage) (any, error) { return "own", nil }
	a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
		Geteuid: func() int { return 0 }, Files: true, Requests: map[string]session.RequestHandler{protocol.ReqFilesStat: own}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{protocol.ReqFilesList, protocol.ReqFilesRead, protocol.ReqFilesWrite, protocol.ReqFilesMkdir, protocol.ReqFilesConflictPreview} {
		if a.opts.Requests[name] == nil {
			t.Errorf("request %s not served", name)
		}
	}
	if out, _ := a.opts.Requests[protocol.ReqFilesStat](context.Background(), nil); out != "own" {
		t.Error("an explicitly configured handler was replaced")
	}
	for _, kind := range []string{protocol.StreamFilesDownload, protocol.StreamFilesUpload} {
		if a.opts.Streams[kind] == nil {
			t.Errorf("stream %s not served", kind)
		}
	}
	p, _ := a.CapabilitiesPayload()
	for _, k := range []string{"files.archive", "files.copy", "files.delete", "files.extract", "files.metadata", "files.move"} {
		if !slices.Contains(p.Commands, k) {
			t.Errorf("command %s not advertised: %v", k, p.Commands)
		}
	}
	if !slices.Contains(p.Features, protocol.FeatureFileLimits) {
		t.Errorf("features %v lack %s", p.Features, protocol.FeatureFileLimits)
	}
	// Before the storage check ran the service refuses everything.
	_, err = a.opts.Requests[protocol.ReqFilesList](context.Background(),
		json.RawMessage(`{"scope":{"kind":"volume","id":"data"},"path":"."}`))
	var he *session.HandlerError
	if !errors.As(err, &he) || he.Code != protocol.CodeUnsupportedVolume {
		t.Fatalf("list before the storage check: %v", err)
	}
}
