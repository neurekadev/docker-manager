package stacks_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux/muxtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// streamingAgents adds the agent's migration streams to fakeAgents.
type streamingAgents struct {
	*fakeAgents
	pipe *muxtest.Pipe
}

func (s streamingAgents) OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	return s.pipe.Open(ctx, kind, input, o)
}

func (s streamingAgents) EnvironmentServes(environmentID, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.handlers[name]
	return s.online && environmentID == env && ok
}

// fakeTemplates serves one template version.
type fakeTemplates struct {
	archive []byte
	sha     string
}

func (f fakeTemplates) TemplateArchive(_ context.Context, instanceID, templateID string, version int) (stacks.TemplateArchive, error) {
	if templateID != "tpl-1" || version != 2 {
		return stacks.TemplateArchive{}, domain.ErrTemplateNotFound
	}
	return stacks.TemplateArchive{Ref: domain.StackTemplateRef{InstanceID: instanceID, TemplateID: templateID, Name: "Cloud",
		Version: 2, VersionLabel: "1.1.0"}, Archive: f.archive, SHA256: f.sha}, nil
}

// templateArchive builds a canonical template archive.
func templateArchive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	mod := time.Unix(0, 0)
	write := func(h *tar.Header) {
		h.ModTime, h.Format = mod, tar.FormatPAX
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	write(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	write(&tar.Header{Name: "config/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, name := range []string{".env", "compose.yaml", "config/app.conf"} {
		c, ok := files[name]
		if !ok {
			continue
		}
		write(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(c))})
		if _, err := tw.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// templateService is a stack service whose agent also serves the
// migration transfer, with a template source.
func (h *harness) templateService(src stacks.TemplateSource, withMigration bool) *stacks.Service {
	h.t.Helper()
	res := &storage.Result{StacksDir: filepath.ToSlash(h.root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(h.root), OK: true}}}
	mig := migration.New(migration.Options{Deps: agentDeps{c: h.comp, eng: h.engine, st: res}, Clock: h.clk, Logger: testutil.Logger(h.t),
		FreeBytes: func(string) int64 { return 1 << 30 }})
	if withMigration {
		h.agents.mu.Lock()
		for k, v := range mig.Requests() {
			h.agents.handlers[k] = v
		}
		h.agents.mu.Unlock()
	}
	hs := map[string]muxtest.Handler{}
	for k, v := range mig.Streams() {
		hs[k] = muxtest.Handler(v)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	svc, err := stacks.New(stacks.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(h.t), Keyring: secrets.NewKeyring(key),
		Agents: streamingAgents{fakeAgents: h.agents, pipe: muxtest.New(h.t, hs)}, Environments: fakeEnvironments{h.agents}, Jobs: h.eng, Bus: h.bus})
	if err != nil {
		h.t.Fatal(err)
	}
	svc.SetTemplates(src)
	return svc
}

func stackCode(err error) string {
	var se *domain.StackError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestCreateFromTemplateCopiesEveryFile(t *testing.T) {
	h := newHarness(t)
	archive, sum := templateArchive(t, map[string]string{
		"compose.yaml":    "services:\n  web:\n    image: nginx:alpine\n    volumes:\n      - ./config/app.conf:/etc/app.conf:ro\n",
		".env":            "PASSWORD=template-default\n",
		"config/app.conf": "listen 80;\n",
	})
	svc := h.templateService(fakeTemplates{archive: archive, sha: sum}, true)
	req := domain.StackFromTemplate{EnvironmentID: env, Name: "cloud", DisplayName: "My cloud", InstanceID: "inst-1", TemplateID: "tpl-1", Version: 2}
	st, v, err := svc.CreateFromTemplate(h.ctx, alice, req)
	if err != nil {
		t.Fatalf("create: %v (validation %+v)", err, v)
	}
	if st.Template == nil || st.Template.TemplateID != "tpl-1" || st.Template.VersionLabel != "1.1.0" || st.Template.InstanceID != "inst-1" {
		t.Fatalf("template ref %+v", st.Template)
	}
	if st.Status != domain.StackUndeployed || st.Observed == nil || st.Origin != domain.StackOriginCreated || st.DisplayName != "My cloud" {
		t.Fatalf("stack %+v", st)
	}
	if got := h.read("cloud", "config", "app.conf"); got != "listen 80;\n" {
		t.Fatalf("bind-mounted file = %q", got)
	}
	if got := h.read("cloud", ".env"); got != "PASSWORD=template-default\n" {
		t.Fatalf(".env = %q", got)
	}
	// The staging area is gone; the stack reads back from the store.
	if entries, _ := os.ReadDir(h.path(protocol.MigrationStagingDir)); len(entries) != 0 {
		t.Fatalf("staging left behind: %v", entries)
	}
	if got := h.get(st.ID); got.Template == nil || got.Template.Name != "Cloud" {
		t.Fatalf("stored stack %+v", got)
	}

	// The same name again: taken.
	if _, _, err := svc.CreateFromTemplate(h.ctx, alice, req); !errors.Is(err, domain.ErrStackNameTaken) {
		t.Fatalf("second create: %v", err)
	}
}

func TestCreateFromTemplateRefusals(t *testing.T) {
	h := newHarness(t)
	archive, sum := templateArchive(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx:alpine\n"})
	svc := h.templateService(fakeTemplates{archive: archive, sha: sum}, true)
	req := func(name string) domain.StackFromTemplate {
		return domain.StackFromTemplate{EnvironmentID: env, Name: name, InstanceID: "inst-1", TemplateID: "tpl-1", Version: 2}
	}

	// An existing directory is never overwritten.
	if err := os.MkdirAll(h.path("taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path("taken", "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateFromTemplate(h.ctx, alice, req("taken")); stackCode(err) != domain.StackErrDirectoryExists {
		t.Fatalf("existing directory: %v", err)
	}
	if got := h.read("taken", "keep.txt"); got != "mine" {
		t.Fatalf("existing directory changed: %q", got)
	}
	if entries, _ := os.ReadDir(h.path(protocol.MigrationStagingDir)); len(entries) != 0 {
		t.Fatalf("staging left behind: %v", entries)
	}

	bad := h.templateService(fakeTemplates{archive: archive, sha: "0000"}, true)
	if _, _, err := bad.CreateFromTemplate(h.ctx, alice, req("digest")); stackCode(err) != domain.StackErrContentUnavailable {
		t.Fatalf("digest mismatch: %v", err)
	}
	missing := req("missing")
	missing.Version = 9
	if _, _, err := svc.CreateFromTemplate(h.ctx, alice, missing); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
	var in *domain.InputError
	if _, _, err := svc.CreateFromTemplate(h.ctx, alice, req("Not A Name")); !errors.As(err, &in) {
		t.Fatalf("invalid name: %v", err)
	}
}

func TestCreateFromTemplateNeedsTheTransfer(t *testing.T) {
	h := newHarness(t)
	archive, sum := templateArchive(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx:alpine\n"})
	svc := h.templateService(fakeTemplates{archive: archive, sha: sum}, false)
	_, _, err := svc.CreateFromTemplate(h.ctx, alice, domain.StackFromTemplate{EnvironmentID: env, Name: "old", InstanceID: "i",
		TemplateID: "tpl-1", Version: 2})
	if stackCode(err) != domain.StackErrEnvironmentUnsupported {
		t.Fatalf("agent without the transfer: %v", err)
	}
}
