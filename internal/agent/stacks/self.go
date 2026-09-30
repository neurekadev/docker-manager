package stacks

import (
	"context"
	"fmt"
	"strings"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/selfupdate"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// SelfUpdater hands the agent's own Compose service to a helper container
// once the job has finished (#32, *selfupdate.Launcher): Docker Manager
// redeploys and updates its own project without the agent stopping itself
// in the middle of the job.
type SelfUpdater interface {
	// OwnService returns the agent's own service when its container
	// belongs to project.
	OwnService(ctx context.Context, project string) (string, bool, error)
	// Schedule starts a helper with the plan after the job succeeded.
	Schedule(p selfupdate.Plan)
}

// selfHandoff splits the requested services (all when empty) into those
// the job converges (rest; nil with a non-empty handoff means none) and
// those the helper converges after it: the agent's own service and the
// services depending on it. A deploy that would remove the agent as an
// orphan (its service is no longer defined) is refused.
func (s *Service) selfHandoff(ctx context.Context, p *compose.Project, project string, requested []string, removeOrphans bool) (rest, handoff []string, err error) {
	if s.opts.Self == nil {
		return requested, nil, nil
	}
	own, ok, err := s.opts.Self.OwnService(ctx, project)
	if err != nil || !ok {
		return requested, nil, err
	}
	if !selfupdate.HasService(p, own) {
		if removeOrphans {
			return nil, nil, &protection.Refusal{Code: protection.CodeProtected, Action: protection.Down,
				Reason: fmt.Sprintf("the definition no longer has the Docker Agent's own service %s: removing orphans would remove the agent", own)}
		}
		return requested, nil, nil
	}
	rest, handoff = selfupdate.Handoff(p, own, requested)
	return rest, handoff, nil
}

// scheduleSelf records the helper's plan: exactly the definition bytes the
// job loaded.
func (s *Service) scheduleSelf(sc *jobexec.StepContext, ref protocol.ProjectRef, dir string, snap protocol.SourceSnapshot,
	services []string, force bool, timeoutSeconds int) {
	content := make(map[string][]byte, len(snap.Files))
	for _, f := range snap.Files {
		content[f.Path] = f.Content
	}
	s.opts.Self.Schedule(selfupdate.Plan{JobID: sc.JobID, ProjectName: ref.ProjectName, Dir: dir, ConfigFiles: ref.ConfigFiles,
		EnvFiles: ref.EnvFiles, Profiles: ref.Profiles, Content: content, Services: services, ForceRecreate: force,
		StopTimeoutSeconds: timeoutSeconds})
}

// selfUpdateMessage explains the handoff in the job's output.
func selfUpdateMessage(services []string) string {
	return fmt.Sprintf("Docker Manager's own agent service (%s) is recreated by a helper container right after this job; "+
		"the environment reconnects within a minute.", strings.Join(services, ", "))
}
