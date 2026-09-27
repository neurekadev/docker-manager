package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/fsroot"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Drafts are served to the file manager like stack and volume roots, by
// the shared internal/fsroot operations on <DataDir>/templates/<id>/draft.

var templateKinds = fsroot.Kinds{
	Delete: jobspec.TemplateFilesDelete, Copy: jobspec.TemplateFilesCopy, Move: jobspec.TemplateFilesMove,
	Archive: jobspec.TemplateFilesArchive, Extract: jobspec.TemplateFilesExtract, Metadata: jobspec.TemplateFilesMetadata,
}

// Files returns the file service of template drafts (template scopes).
func (s *Service) Files() *fsroot.Service { return s.files }

// resolve maps a template scope to its draft directory.
func (s *Service) resolve(_ context.Context, scope protocol.FileScope) (string, error) {
	if scope.Kind != protocol.ScopeTemplate {
		return "", fsroot.Fail(protocol.CodeInvalidFrame, "not a template scope")
	}
	dir, err := filepath.EvalSymlinks(s.draftDir(scope.ID))
	if err != nil {
		return "", fsroot.Fail(protocol.CodeNotFound, "the template's draft does not exist")
	}
	return dir, nil
}

// invalidate publishes the changed draft paths for live file views and
// drops the cached draft size.
func (s *Service) invalidate(p protocol.FSInvalidationPayload) {
	s.forgetUsage(p.Scope.ID)
	if s.opts.Bus == nil {
		return
	}
	s.opts.Bus.Publish(events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope,
		ResourceID: p.Scope.Kind + ":" + p.Scope.ID, At: p.At.UTC(), Paths: p.Paths, Overflow: p.Overflow || len(p.Paths) == 0,
		Attributes: map[string]string{"scopeKind": p.Scope.Kind, "scopeId": p.Scope.ID}})
}

// FilesChanged drops the cached size of a draft after a change made
// outside the file operations (imports, restores).
func (s *Service) FilesChanged(id string) {
	s.forgetUsage(id)
	s.invalidate(protocol.FSInvalidationPayload{Scope: protocol.ScopeRef{Kind: protocol.ScopeTemplate, ID: id},
		At: s.opts.Clock.Now(), Overflow: true})
}

// draftUsage is what a draft holds.
type draftUsage struct {
	bytes   int64
	entries int
}

func (s *Service) forgetUsage(id string) {
	s.usageMu.Lock()
	delete(s.usage, id)
	s.usageMu.Unlock()
}

// errWalkLimit stops a draft walk that exceeds the entry limit.
var errWalkLimit = errors.New("too many entries")

// draftUsage measures a draft (regular file bytes and entries; symlinks
// are never followed), cached until the draft changes.
func (s *Service) draftUsage(id string) (draftUsage, error) {
	s.usageMu.Lock()
	u, ok := s.usage[id]
	s.usageMu.Unlock()
	if ok {
		return u, nil
	}
	dir := s.draftDir(id)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		u.entries++
		if u.entries > 2*s.opts.MaxEntries {
			return errWalkLimit
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			u.bytes += fi.Size()
		}
		return nil
	})
	if err != nil && !errors.Is(err, errWalkLimit) {
		return u, fmt.Errorf("templates: measure the draft: %w", err)
	}
	s.usageMu.Lock()
	s.usage[id] = u
	s.usageMu.Unlock()
	return u, nil
}

// CheckQuota refuses a change that adds adding bytes (and one entry) to a
// draft when the draft would exceed the template size or entry limit.
func (s *Service) CheckQuota(_ context.Context, id string, adding int64) error {
	u, err := s.draftUsage(id)
	if err != nil {
		return err
	}
	if u.bytes+adding > s.opts.MaxSize {
		return &domain.TemplateTooLargeError{Message: fmt.Sprintf("a template holds at most %d MiB (DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB); "+
			"remove files first", s.opts.MaxSize>>20)}
	}
	if u.entries+1 > s.opts.MaxEntries {
		return &domain.TemplateTooLargeError{Message: fmt.Sprintf("a template holds at most %d files and directories", s.opts.MaxEntries)}
	}
	return nil
}

// Writing takes the draft's shared lock for a file operation (publishing
// takes it exclusively, so a version never captures a half-written file).
func (s *Service) Writing(id string) func() {
	l := s.lock(id)
	l.RLock()
	return l.RUnlock
}

// guardExecutor runs a template file job under the draft's shared lock;
// jobs that add content (copy, extract) start only below the size limit.
func (s *Service) guardExecutor(x jobexec.Executor) jobexec.Executor {
	adds := x.Kind == jobspec.TemplateFilesCopy || x.Kind == jobspec.TemplateFilesExtract
	steps := make(map[string]jobexec.StepFunc, len(x.Steps))
	for name, fn := range x.Steps {
		steps[name] = func(ctx context.Context, sc *jobexec.StepContext) error {
			id, err := jobTemplate(sc)
			if err != nil {
				return err
			}
			defer s.Writing(id)()
			if adds {
				if err := s.CheckQuota(ctx, id, 0); err != nil {
					return err
				}
			}
			defer s.forgetUsage(id)
			return fn(ctx, sc)
		}
	}
	x.Steps = steps
	return x
}

// jobTemplate returns the template a template file job acts on (its
// input's scope; fsroot refuses other scope kinds for these jobs).
func jobTemplate(sc *jobexec.StepContext) (string, error) {
	var in protocol.FilesJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil || in.Scope.Kind != protocol.ScopeTemplate || in.Scope.ID == "" {
		return "", errors.New("malformed input: a template scope is required")
	}
	return in.Scope.ID, nil
}
