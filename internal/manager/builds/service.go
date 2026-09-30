// Package builds owns manual image builds from Git (#33): validating a
// build request, selecting its Git credential and base-image registry
// connections (never putting a secret into the job), enqueueing the
// image.build job, build records (whose ID is the job ID) with the outcome
// copied from the job, and saved build definitions that can be re-run.
package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/gitremote"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/imageref"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// JobEngine is the part of the job engine builds use.
type JobEngine interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
}

// GitCredentials selects a repository's Git credential (gitcreds.Service).
type GitCredentials interface {
	Select(ctx context.Context, repo gitremote.Repo, explicitID string) (*domain.GitCredential, error)
}

// RegistryCredentials offers registry connections to builds
// (registries.Service).
type RegistryCredentials interface {
	BuildCredentials(ctx context.Context, environmentID string) (ids, ambiguous []string, err error)
	Usable(ctx context.Context, ids []string) error
}

// Options configures the service.
type Options struct {
	DB         *bun.DB
	Clock      clock.Clock
	Logger     *slog.Logger
	Jobs       JobEngine
	Git        GitCredentials
	Registries RegistryCredentials
	// ForgetResource removes permission rules naming a deleted definition.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
}

// Service manages builds and build definitions.
type Service struct {
	opts Options
	db   *bun.DB
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Jobs == nil || opts.Git == nil || opts.Registries == nil {
		return nil, errors.New("builds: DB, Jobs, Git and Registries are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{opts: opts, db: opts.DB}, nil
}

func fieldErr(field, message string) error { return &domain.FieldError{Field: field, Message: message} }

var (
	refRE        = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,255}$`)
	argKeyRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	targetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	platformRE   = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)
)

// Limits.
const (
	MaxArgValueLen   = 4096
	MaxDockerfileLen = 255
	MaxNameLen       = 100
	MaxDescription   = 1000
)

// normalize validates a build source and returns its normalized form.
func normalize(src domain.BuildSource) (domain.BuildSource, gitremote.Repo, error) {
	repo, err := gitremote.ParseURL(src.GitURL)
	if err != nil {
		var ge *gitremote.Error
		msg := "invalid Git URL"
		if errors.As(err, &ge) {
			msg = ge.Message
		}
		return src, repo, fieldErr("gitUrl", msg)
	}
	src.GitURL = repo.String()
	src.Ref = strings.TrimSpace(src.Ref)
	if src.Ref != "" && (!refRE.MatchString(src.Ref) || strings.HasPrefix(src.Ref, "-") || strings.Contains(src.Ref, "..")) {
		return src, repo, fieldErr("ref", "a branch, tag, full ref or commit ID")
	}
	if src.ContextPath, err = jobspec.CleanContextPath(src.ContextPath); err != nil {
		return src, repo, fieldErr("contextPath", err.Error())
	}
	src.Dockerfile = strings.TrimSpace(src.Dockerfile)
	if len(src.Dockerfile) > MaxDockerfileLen || strings.ContainsAny(src.Dockerfile, "\x00\\") {
		return src, repo, fieldErr("dockerfile", "invalid Dockerfile path")
	}
	if src.Target != "" && !targetNameRE.MatchString(src.Target) {
		return src, repo, fieldErr("target", "invalid build stage name")
	}
	if src.Platform != "" && !platformRE.MatchString(src.Platform) {
		return src, repo, fieldErr("platform", "os/arch[/variant], e.g. linux/amd64")
	}
	if len(src.Tags) == 0 || len(src.Tags) > jobspec.MaxBuildTags {
		return src, repo, fieldErr("tags", fmt.Sprintf("1 to %d image names (name:tag)", jobspec.MaxBuildTags))
	}
	seen := map[string]bool{}
	for i, t := range src.Tags {
		t = strings.TrimSpace(t)
		r, err := imageref.Parse(t)
		if err != nil || r.Pinned() {
			return src, repo, fieldErr(fmt.Sprintf("tags[%d]", i), "an image name like registry.example.com/team/app:1.2 (no digest)")
		}
		if seen[t] {
			return src, repo, fieldErr(fmt.Sprintf("tags[%d]", i), "duplicate tag")
		}
		seen[t] = true
		src.Tags[i] = t
	}
	if len(src.BuildArgs) > jobspec.MaxBuildArgs {
		return src, repo, fieldErr("buildArgs", fmt.Sprintf("at most %d build arguments", jobspec.MaxBuildArgs))
	}
	for k, v := range src.BuildArgs {
		if !argKeyRE.MatchString(k) || len(v) > MaxArgValueLen {
			return src, repo, fieldErr("buildArgs", "argument names are letters, digits and _; values at most 4096 bytes")
		}
	}
	if src.TimeoutSeconds < 0 || src.TimeoutSeconds > int(jobspec.MaxBuildTimeout.Seconds()) {
		return src, repo, fieldErr("timeoutSeconds", fmt.Sprintf("0 (default %s) to %d", jobspec.DefaultBuildTimeout, int(jobspec.MaxBuildTimeout.Seconds())))
	}
	src.RegistryConnectionIDs = dedupe(src.RegistryConnectionIDs)
	if len(src.RegistryConnectionIDs) > 16 {
		return src, repo, fieldErr("registryIds", "at most 16 registry connections")
	}
	return src, repo, nil
}

func dedupe(v []string) []string {
	var out []string
	for _, s := range v {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) activeEnvironment(ctx context.Context, envID string) error {
	env, err := store.GetEnvironment(ctx, s.db, envID)
	if err != nil {
		return err
	}
	if env.Status != domain.EnvironmentActive {
		return domain.ErrEnvironmentArchived
	}
	return nil
}

// credentials resolves the Git credential and registry connections of a
// build (IDs only; secrets are resolved at dispatch).
func (s *Service) credentials(ctx context.Context, envID string, src domain.BuildSource, repo gitremote.Repo) (string, []string, error) {
	cred, err := s.opts.Git.Select(ctx, repo, src.GitCredentialID)
	if err != nil {
		return "", nil, err
	}
	gitID := ""
	if cred != nil {
		if !repo.Secure() && !cred.PlainHTTP {
			return "", nil, fieldErr("gitUrl", "the repository uses plain HTTP but its Git credential does not allow plain HTTP; use https")
		}
		gitID = cred.ID
	}
	regs := src.RegistryConnectionIDs
	if len(regs) > 0 {
		if err := s.opts.Registries.Usable(ctx, regs); err != nil {
			return "", nil, err
		}
	} else {
		auto, ambiguous, err := s.opts.Registries.BuildCredentials(ctx, envID)
		if err != nil {
			return "", nil, err
		}
		if len(ambiguous) > 0 {
			s.opts.Logger.Info("registry hosts with tied connections are not offered to the build; name them explicitly",
				"environment_id", envID, "hosts", ambiguous)
		}
		regs = auto
	}
	return gitID, regs, nil
}

// Start validates a build, enqueues its image.build job (authorized as
// p) and records it. The build ID is the job ID. definitionID marks a
// definition run (authorized on the definition instead of the images).
// With an idempotency key a repeated request returns the existing build.
func (s *Service) Start(ctx context.Context, p authz.Principal, envID string, src domain.BuildSource, definitionID, idempotencyKey string) (domain.ImageBuild, domain.Job, error) {
	if err := s.activeEnvironment(ctx, envID); err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	src, repo, err := normalize(src)
	if err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	gitID, regs, err := s.credentials(ctx, envID, src, repo)
	if err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	in := jobspec.ImageBuildInput{GitURL: src.GitURL, Ref: src.Ref, ContextPath: src.ContextPath, Dockerfile: src.Dockerfile,
		Target: src.Target, BuildArgs: src.BuildArgs, Tags: src.Tags, NoCache: src.NoCache, Pull: src.Pull, Platform: src.Platform,
		TimeoutSeconds: src.TimeoutSeconds}
	in.RegistryConnections = regs
	if gitID != "" {
		in.GitCredentials = []string{gitID}
	}
	if err := in.Validate(); err != nil {
		return domain.ImageBuild{}, domain.Job{}, fieldErr("body", err.Error())
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	var targets []domain.JobTarget
	if definitionID != "" {
		targets = append(targets, domain.JobTarget{Type: domain.TargetBuildDefinition, ID: definitionID})
	}
	for _, t := range src.Tags {
		targets = append(targets, domain.JobTarget{Type: domain.TargetImage, ID: t})
	}
	job, created, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.ImageBuild, Principal: p, EnvironmentID: envID,
		Targets: targets, Input: json.RawMessage(raw), IdempotencyKey: idempotencyKey})
	if err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	if !created {
		if b, err := store.GetImageBuild(ctx, s.db, job.ID); err == nil {
			return b, job, nil
		}
	}
	keys := make([]string, 0, len(src.BuildArgs))
	for k := range src.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := domain.ImageBuild{ID: job.ID, EnvironmentID: envID, JobID: job.ID, DefinitionID: definitionID, GitURL: src.GitURL, Ref: src.Ref,
		ContextPath: src.ContextPath, Dockerfile: src.Dockerfile, Target: src.Target, Tags: src.Tags, Platform: src.Platform,
		NoCache: src.NoCache, Pull: src.Pull, BuildArgKeys: keys, GitCredentialID: gitID, RegistryConnectionIDs: regs,
		Status: domain.BuildQueued, InitiatorUserID: p.UserID, CreatedAt: job.CreatedAt}
	if err := store.InsertImageBuild(ctx, s.db, &b); err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	if definitionID != "" {
		if err := store.SetDefinitionLastBuild(ctx, s.db, definitionID, b.ID); err != nil {
			return domain.ImageBuild{}, domain.Job{}, err
		}
	}
	// Audit (#30): the Git URL (never with credentials), ref and
	// credential IDs; build arguments are never recorded (only their count).
	audit.AddTarget(ctx, domain.AuditTarget{Type: "image_build", ID: b.ID, EnvironmentID: envID})
	audit.SetDetail(ctx, "gitUrl", src.GitURL)
	audit.SetDetail(ctx, "ref", src.Ref)
	audit.SetDetail(ctx, "tags", src.Tags)
	audit.SetDetail(ctx, "buildArgCount", len(src.BuildArgs))
	if gitID != "" {
		audit.SetDetail(ctx, "gitCredentialId", gitID)
	}
	return b, job, nil
}

// statusOf maps a job state to a build status.
func statusOf(st domain.JobState) domain.ImageBuildStatus {
	switch st {
	case domain.JobRunning, domain.JobCancelling:
		return domain.BuildRunning
	case domain.JobSucceeded:
		return domain.BuildSucceeded
	case domain.JobFailed, domain.JobPartial:
		return domain.BuildFailed
	case domain.JobCancelled:
		return domain.BuildCancelled
	case domain.JobInterrupted:
		return domain.BuildInterrupted
	}
	return domain.BuildQueued
}

// sync copies the job's state and results into an unfinished record.
func (s *Service) sync(ctx context.Context, b domain.ImageBuild) (domain.ImageBuild, error) {
	if b.Status.Terminal() {
		return b, nil
	}
	j, err := s.opts.Jobs.Get(ctx, b.JobID)
	next := b
	switch {
	case errors.Is(err, domain.ErrJobNotFound):
		next.Status, next.ErrorClass = domain.BuildInterrupted, "job_expired"
		next.ErrorMessage = "the build's job history expired before its outcome was recorded"
		now := s.opts.Clock.Now().UTC()
		next.FinishedAt = &now
	case err != nil:
		return b, err
	default:
		next.Status = statusOf(j.State)
		next.StartedAt, next.FinishedAt = j.StartedAt, j.FinishedAt
		next.ErrorClass, next.ErrorMessage = j.ErrorClass, j.ErrorMessage
		for _, it := range j.Items {
			switch it.Name {
			case jobspec.BuildItemCommit:
				next.ResolvedCommit = it.Message
			case jobspec.BuildItemRef:
				next.ResolvedRef = it.Message
			case jobspec.BuildItemImage:
				next.ImageID = it.Message
			}
		}
	}
	if next.Status == b.Status && next.ResolvedCommit == b.ResolvedCommit && next.ImageID == b.ImageID &&
		ptrEq(next.StartedAt, b.StartedAt) && ptrEq(next.FinishedAt, b.FinishedAt) {
		return b, nil
	}
	if err := store.UpdateImageBuildOutcome(ctx, s.db, &next); err != nil {
		return b, err
	}
	return next, nil
}

func ptrEq[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Get returns a build record with its current outcome.
func (s *Service) Get(ctx context.Context, id string) (domain.ImageBuild, error) {
	b, err := store.GetImageBuild(ctx, s.db, id)
	if err != nil {
		return b, err
	}
	return s.sync(ctx, b)
}

// List returns an environment's builds, newest first.
func (s *Service) List(ctx context.Context, envID, definitionID, beforeID string, limit int) ([]domain.ImageBuild, error) {
	bs, err := store.ListImageBuilds(ctx, s.db, envID, definitionID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	for i := range bs {
		if bs[i], err = s.sync(ctx, bs[i]); err != nil {
			return nil, err
		}
	}
	return bs, nil
}

// --- definitions ---

func validDefinitionText(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return "", "", fieldErr("name", fmt.Sprintf("must be 1 to %d characters", MaxNameLen))
	}
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > MaxDescription {
		return "", "", fieldErr("description", fmt.Sprintf("at most %d characters", MaxDescription))
	}
	return name, description, nil
}

// checkSource validates a definition's source and its explicitly named
// credentials (they must exist; matching happens at every run).
func (s *Service) checkSource(ctx context.Context, src domain.BuildSource) (domain.BuildSource, error) {
	src, repo, err := normalize(src)
	if err != nil {
		return src, err
	}
	if src.GitCredentialID != "" {
		if _, err := s.opts.Git.Select(ctx, repo, src.GitCredentialID); err != nil {
			return src, err
		}
	}
	if err := s.opts.Registries.Usable(ctx, src.RegistryConnectionIDs); err != nil {
		return src, err
	}
	return src, nil
}

// CreateDefinition stores a definition in an environment.
func (s *Service) CreateDefinition(ctx context.Context, envID, name, description string, src domain.BuildSource) (domain.BuildDefinition, error) {
	if err := s.activeEnvironment(ctx, envID); err != nil {
		return domain.BuildDefinition{}, err
	}
	name, description, err := validDefinitionText(name, description)
	if err != nil {
		return domain.BuildDefinition{}, err
	}
	if src, err = s.checkSource(ctx, src); err != nil {
		return domain.BuildDefinition{}, err
	}
	now := s.opts.Clock.Now().UTC()
	d := domain.BuildDefinition{ID: ids.New(), EnvironmentID: envID, Name: name, Description: description, Source: src, Revision: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := store.InsertBuildDefinition(ctx, s.db, &d); err != nil {
		return domain.BuildDefinition{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "build_definition", ID: d.ID, EnvironmentID: envID})
	audit.SetDetail(ctx, "gitUrl", src.GitURL)
	return d, nil
}

// GetDefinition returns a definition.
func (s *Service) GetDefinition(ctx context.Context, id string) (domain.BuildDefinition, error) {
	return store.GetBuildDefinition(ctx, s.db, id)
}

// ListDefinitions returns an environment's definitions in creation order.
func (s *Service) ListDefinitions(ctx context.Context, envID, afterID string, limit int) ([]domain.BuildDefinition, error) {
	return store.ListBuildDefinitions(ctx, s.db, envID, afterID, limit)
}

// defAudit is the audited view of a definition: build argument names only.
func defAudit(d domain.BuildDefinition) map[string]any {
	keys := make([]string, 0, len(d.Source.BuildArgs))
	for k := range d.Source.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{"name": d.Name, "gitUrl": d.Source.GitURL, "ref": d.Source.Ref, "contextPath": d.Source.ContextPath,
		"dockerfile": d.Source.Dockerfile, "target": d.Source.Target, "tags": d.Source.Tags, "buildArgNames": keys,
		"gitCredentialId": d.Source.GitCredentialID, "registryConnectionIds": d.Source.RegistryConnectionIDs}
}

// UpdateDefinition edits a definition (compare-and-set on revision).
func (s *Service) UpdateDefinition(ctx context.Context, id string, revision int64, p domain.BuildDefinitionPatch) (domain.BuildDefinition, error) {
	cur, err := store.GetBuildDefinition(ctx, s.db, id)
	if err != nil {
		return cur, err
	}
	if cur.Revision != revision {
		return cur, domain.ErrRevisionMismatch
	}
	next := cur
	name, desc := cur.Name, cur.Description
	if p.Name != nil {
		name = *p.Name
	}
	if p.Description != nil {
		desc = *p.Description
	}
	if next.Name, next.Description, err = validDefinitionText(name, desc); err != nil {
		return cur, err
	}
	if p.Source != nil {
		if next.Source, err = s.checkSource(ctx, *p.Source); err != nil {
			return cur, err
		}
	}
	next.Revision, next.UpdatedAt = cur.Revision+1, s.opts.Clock.Now().UTC()
	if err := store.UpdateBuildDefinition(ctx, s.db, &next, revision); err != nil {
		return cur, err
	}
	audit.SetDiff(ctx, defAudit(cur), defAudit(next))
	return next, nil
}

// DeleteDefinition removes a definition (its build records stay).
func (s *Service) DeleteDefinition(ctx context.Context, id string, revision int64) error {
	if err := store.DeleteBuildDefinition(ctx, s.db, id, revision); err != nil {
		return err
	}
	if s.opts.ForgetResource != nil {
		if _, err := s.opts.ForgetResource(ctx, authz.ResourceRef{Type: "build_definition", ID: id}); err != nil {
			s.opts.Logger.Warn("could not remove the permission rules of a deleted build definition", "build_definition_id", id, "error", err)
		}
	}
	return nil
}

// RunDefinition starts a build of a definition.
func (s *Service) RunDefinition(ctx context.Context, p authz.Principal, id, idempotencyKey string) (domain.ImageBuild, domain.Job, error) {
	d, err := store.GetBuildDefinition(ctx, s.db, id)
	if err != nil {
		return domain.ImageBuild{}, domain.Job{}, err
	}
	audit.SetDetail(ctx, "buildDefinitionId", d.ID)
	return s.Start(ctx, p, d.EnvironmentID, d.Source, d.ID, idempotencyKey)
}
