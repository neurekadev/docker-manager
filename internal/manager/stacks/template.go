package stacks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/transfer"
)

// Stacks from templates (template registry). A stack is created from a
// published template version: its archive is streamed to the agent with
// the stack-migration transfer (migration.receive into the stacks volume's
// staging area, migration.commit into the new project directory, which is
// never an existing one), then compose.read records revision 1. Nothing is
// deployed. The user's own .env values are saved afterwards through the
// file manager, so they never pass through this path.

// TemplateArchive is a template version to create a stack from.
type TemplateArchive struct {
	Ref domain.StackTemplateRef
	// Archive is the version's tar.gz; SHA256 its expected digest.
	Archive []byte
	SHA256  string
	// Links are the template's links: the new stack starts with them.
	Links []domain.Link
}

// TemplateSource resolves template versions (the template service and,
// for added registries, their cached indexes).
type TemplateSource interface {
	TemplateArchive(ctx context.Context, instanceID, templateID string, version int) (TemplateArchive, error)
}

// SetTemplates installs the template source (the template service is
// created after the stack service).
func (s *Service) SetTemplates(t TemplateSource) { s.templates = t }

// streams opens agent streams (*agents.Hub).
type streams interface {
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
	EnvironmentServes(environmentID, name string) bool
}

// transferTimeout bounds streaming a template to the agent.
const transferTimeout = 5 * time.Minute

// maxTemplateTar bounds the unzipped tar of a template (decompression
// bombs): the largest template size setting plus room for headers.
const maxTemplateTar = 1<<30 + 64<<20

// CreateFromTemplate creates a stack named r.Name in r.EnvironmentID from
// a template version. Like Create, it refuses taken names and existing
// Compose projects or directories and validates the definition on the
// agent first; the files are the template's, owned by root. The stack
// starts with a copy of the template's links (its own from then on).
func (s *Service) CreateFromTemplate(ctx context.Context, p authz.Principal, r domain.StackFromTemplate) (domain.Stack, domain.StackValidation, error) {
	var none domain.StackValidation
	if !protocol.ValidProjectName(r.Name) {
		return domain.Stack{}, none, &domain.InputError{Field: "name", Message: "must be a Compose project name: 1-63 lower-case letters, digits, '-' and '_', starting with a letter or digit"}
	}
	if err := checkMeta(r.DisplayName, r.Meta); err != nil {
		return domain.Stack{}, none, err
	}
	if s.templates == nil {
		return domain.Stack{}, none, domain.ErrTemplateNotFound
	}
	if _, err := s.activeEnvironment(ctx, r.EnvironmentID); err != nil {
		return domain.Stack{}, none, err
	}
	hub, ok := s.opts.Agents.(streams)
	if !ok || !hub.EnvironmentServes(r.EnvironmentID, protocol.ReqMigrationCommit) {
		if !s.Online(ctx, r.EnvironmentID) {
			return domain.Stack{}, none, &domain.StackError{Code: domain.StackErrOffline, Message: "the environment's agent is offline"}
		}
		return domain.Stack{}, none, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
			Message: "the environment's agent cannot receive templates; upgrade it"}
	}
	src, err := s.templates.TemplateArchive(ctx, r.InstanceID, r.TemplateID, r.Version)
	if err != nil {
		return domain.Stack{}, none, err
	}
	if sum := sha256.Sum256(src.Archive); src.SHA256 != "" && hex.EncodeToString(sum[:]) != src.SHA256 {
		return domain.Stack{}, none, &domain.StackError{Code: domain.StackErrContentUnavailable,
			Message: "the template version does not match its digest; refresh the registry and try again"}
	}
	files, err := archiveDefinition(src.Archive)
	if err != nil {
		return domain.Stack{}, none, err
	}
	if _, err := store.FindStackByName(ctx, s.db, r.EnvironmentID, r.Name); err == nil {
		return domain.Stack{}, none, domain.ErrStackNameTaken
	} else if !errors.Is(err, domain.ErrStackNotFound) {
		return domain.Stack{}, none, err
	}
	projects, err := s.discovered(ctx, r.EnvironmentID)
	if err != nil {
		return domain.Stack{}, none, err
	}
	if dp, ok := projects[r.Name]; ok && !dp.Containerless {
		return domain.Stack{}, none, &domain.StackError{Code: domain.StackErrProjectExists,
			Message: "a Compose project named " + r.Name + " already runs on this environment; import it or choose another name"}
	}
	ref := newRef(r.Name)
	var v protocol.ComposeValidateOutput
	if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: ref, Files: sourceFiles(files)}, &v); err != nil {
		return domain.Stack{}, none, err
	}
	if !v.Valid {
		return domain.Stack{}, validationOf(v), invalidDefinition(v)
	}
	if v.ProjectName != "" && v.ProjectName != r.Name {
		return domain.Stack{}, validationOf(v), &domain.StackError{Code: domain.StackErrInvalidDefinition,
			Message: "the template's Compose file sets the project name " + v.ProjectName + "; a stack from it must use that name"}
	}

	id := ids.New()
	if err := s.receiveTemplate(ctx, hub, r.EnvironmentID, id, src.Archive); err != nil {
		s.cleanupTemplate(r.EnvironmentID, id, "")
		return domain.Stack{}, none, err
	}
	var co protocol.MigrationCommitOutput
	raw, err := s.opts.Agents.RequestEnvironment(ctx, r.EnvironmentID, protocol.ReqMigrationCommit,
		protocol.MigrationCommitInput{MigrationID: id, Dir: r.Name}, s.opts.RequestTimeout)
	if err != nil {
		s.cleanupTemplate(r.EnvironmentID, id, "")
		var ce protocol.CodedError
		if errors.As(err, &ce) && ce.ProtocolCode() == protocol.CodeAlreadyExists {
			return domain.Stack{}, none, &domain.StackError{Code: domain.StackErrDirectoryExists,
				Message: "the directory " + r.Name + " already exists in the stacks volume; import it or choose another name"}
		}
		return domain.Stack{}, none, agentError(err)
	}
	_ = json.Unmarshal(raw, &co)

	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.Name, DisplayName: r.DisplayName, Meta: r.Meta,
		Root: domain.StackRootStacks, Dir: r.Name, Origin: domain.StackOriginCreated, Status: domain.StackUndeployed,
		Services: servicesFrom(v.Services), Binds: bindsFrom(v.Binds), EngineState: domain.EngineStateMissing,
		Links: domain.SanitizeLinks(src.Links), Template: &src.Ref, Revision: 1, CreatedAt: now, UpdatedAt: now}
	importLabelMeta(&st, v.Services)
	var read protocol.ComposeReadOutput
	if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &read); err != nil || read.Missing {
		// The copy is committed but unreadable: remove it again.
		s.cleanupTemplate(r.EnvironmentID, id, r.Name)
		if err == nil {
			err = &domain.StackError{Code: domain.StackErrAgent, Message: "the template's files could not be read back on the host"}
		}
		return domain.Stack{}, none, err
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := store.InsertStack(ctx, tx, &st); err != nil {
			return err
		}
		if _, err := s.observe(ctx, tx, &st, read.Snapshot, domain.RevisionEditor, p); err != nil {
			return err
		}
		return store.UpdateStack(ctx, tx, &st)
	})
	if err != nil {
		s.cleanupTemplate(r.EnvironmentID, id, r.Name)
		return domain.Stack{}, none, err
	}
	s.cleanupTemplate(r.EnvironmentID, id, "finished")
	s.publish(EventCreated, st, map[string]string{"origin": "template"})
	return st, validationOf(v), nil
}

// receiveTemplate streams the version's tar (unzipped) into the agent's
// staging area and checks what the agent received.
func (s *Service) receiveTemplate(ctx context.Context, hub streams, env, id string, archive []byte) error {
	ctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("stacks: read the template archive: %w", err)
	}
	st, err := hub.OpenStream(ctx, env, protocol.StreamMigrationReceive,
		protocol.MigrationReceiveInput{MigrationID: id, Part: protocol.PartProject}, streammux.OpenOptions{})
	if err != nil {
		return agentError(err)
	}
	fw := transfer.NewWriter(st, 0)
	n, err := io.CopyN(fw, zr, maxTemplateTar+1)
	if err != nil && !errors.Is(err, io.EOF) {
		st.Abort(protocol.CloseReasonError, protocol.CodeInternal, "")
		return transferErr(st, err)
	}
	if n > maxTemplateTar {
		st.Abort(protocol.CloseReasonError, protocol.CodeTooLarge, "")
		return &domain.StackError{Code: domain.StackErrDefinitionTooLarge, Message: "the template unpacks to more than 1 GiB"}
	}
	if err := fw.Close(); err != nil {
		st.Abort(protocol.CloseReasonError, protocol.CodeInternal, "")
		return transferErr(st, err)
	}
	sum := fw.Summary()
	if err := st.CloseWrite(); err != nil {
		return transferErr(st, err)
	}
	res, err := st.Result(ctx)
	if err != nil {
		return transferErr(st, err)
	}
	var got protocol.MigrationPartResult
	if err := json.Unmarshal(res, &got); err != nil || got.Bytes != sum.Bytes || got.SHA256 != sum.SHA256 {
		return &domain.StackError{Code: domain.StackErrAgent, Message: "the host received different bytes than were sent; try again"}
	}
	return nil
}

// transferErr maps a failed stream: the agent's refusal (an unsafe
// archive, a full disk) or a lost session.
func transferErr(st *streammux.Stream, err error) error {
	var ce *streammux.CloseError
	if e := st.Err(); e != nil && errors.As(e, &ce) && !ce.Local {
		return &domain.StackError{Code: domain.StackErrAgent, Message: "the host refused the template's files (" + ce.Code + ")"}
	}
	return agentError(err)
}

// cleanupTemplate removes the agent's staging area of a template transfer
// (best effort): "finished" keeps the committed project directory, a
// project name removes it again, "" only removes staging.
func (s *Service) cleanupTemplate(env, id, project string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.RequestTimeout)
	defer cancel()
	in := protocol.MigrationCleanupInput{MigrationID: id}
	switch project {
	case "finished":
		in.Finished = true
	case "":
	default:
		in.Project = project
	}
	if _, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqMigrationCleanup, in, s.opts.RequestTimeout); err != nil {
		s.log.Warn("could not clean up a template transfer on the host", "environment_id", env, "error", err)
	}
}

// archiveDefinition reads the default Compose files and .env at the root
// of a template archive (what Compose loads without extra options), for
// validation.
func archiveDefinition(archive []byte) ([]domain.StackFile, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("stacks: read the template archive: %w", err)
	}
	tr := tar.NewReader(zr)
	var out []domain.StackFile
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("stacks: read the template archive: %w", err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag != tar.TypeReg || !slices.Contains(definitionNames, name) {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, protocol.MaxSourceFile+1))
		if err != nil {
			return nil, fmt.Errorf("stacks: read the template archive: %w", err)
		}
		out = append(out, domain.StackFile{Path: name, Content: b})
	}
	if err := checkDefinition(domain.StackDefinition{Name: "template", Files: out}); err != nil {
		return nil, err
	}
	return out, nil
}
