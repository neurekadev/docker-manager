package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// Digest-driven update payloads (#20): the input and output of the
// update.run job kind. The manager decides what to update (the policy's
// candidates, their platform manifest digests and the registry connection
// of each reference); the agent pulls the unchanged tagged reference,
// verifies that the tag still names the candidate and recreates what
// changed. Nothing here carries a credential (#19: they arrive per dispatch
// in the command's secrets) and nothing ever writes a definition file.

// Update run stages (UpdateRunOutput.Stage): how far a run got.
const (
	UpdateStagePull     = "pull"
	UpdateStageRecreate = "recreate"
	UpdateStageConfirm  = "confirm"
	UpdateStageDone     = "done"
)

// Outcomes of one service or container (UpdateServiceResult.Outcome).
const (
	// UpdatePending: not reached yet.
	UpdatePending = "pending"
	// UpdatePulled: the new image is on the host; not applied yet.
	UpdatePulled = "pulled"
	// UpdateUpdated: recreated with the new image (and started again when
	// it ran before).
	UpdateUpdated = "updated"
	// UpdateUnchanged: the tag names the image the service already runs
	// (same host-platform image): nothing was recreated.
	UpdateUnchanged = "unchanged"
	// UpdateKeptStopped: the service was stopped before the update; it is
	// kept stopped and picks up the new image at its next deploy.
	UpdateKeptStopped = "kept_stopped"
	// UpdateFailed: the service's update failed (see the job error).
	UpdateFailed = "failed"
)

// Error classes of update.run failures (jobexec.ClassedError).
const (
	// UpdateClassSourceChanged: the definition on disk is not the applied
	// revision, or it changed while the update ran. DockYard never writes
	// it; the run stops before (or reports after) touching containers.
	UpdateClassSourceChanged = "source_changed"
	// UpdateClassCandidateChanged: after the pull the tag names another
	// digest than the checked candidate (it moved again): nothing is
	// recreated; run a check again.
	UpdateClassCandidateChanged = "candidate_changed"
	// UpdateClassUnhealthy: an updated service or container became
	// unhealthy.
	UpdateClassUnhealthy = "unhealthy"
	// UpdateClassExited: an updated service or container stopped right
	// after it was started.
	UpdateClassExited = "service_exited"
	// UpdateClassRecreated: the standalone container was replaced or
	// renamed since the manager planned the run.
	UpdateClassRecreated = "container_recreated"
)

// MaxUpdateServices bounds the services of one update run.
const MaxUpdateServices = 64

// UpdateService is one Compose service (or the standalone container) an
// update run applies.
type UpdateService struct {
	// Service is the Compose service name, or the container name.
	Service string `json:"service"`
	// Reference is the resolved tagged reference the service runs (its
	// literal text is never changed).
	Reference string `json:"reference"`
	// Digest is the candidate's platform manifest digest; IndexDigest the
	// tag's index digest for multi-platform images (the Engine records
	// either as the image's repository digest).
	Digest      string `json:"digest"`
	IndexDigest string `json:"indexDigest,omitempty"`
	// Platform to pull ("os/arch[/variant]"); empty: the Engine's.
	Platform string `json:"platform,omitempty"`
	// RegistryConnection is the connection selected for Reference (also
	// listed in UpdateRunInput.RegistryConnections); a missing credential
	// for it fails the run instead of pulling anonymously.
	RegistryConnection string `json:"registryConnection,omitempty"`
}

// UpdateContainer is a DockYard-managed standalone container an update
// recreates from its saved recreate specification (#6).
type UpdateContainer struct {
	Name string `json:"name"`
	// ID is the container the manager planned for: a container replaced
	// under the same name meanwhile is refused.
	ID   string        `json:"id"`
	Spec ContainerSpec `json:"spec"`
	// Ownership are the DockYard labels of the container (LabelManaged,
	// LabelSpec, LabelInstance), kept on the recreated container.
	Ownership map[string]string `json:"ownership,omitempty"`
}

// UpdateRunInput is the input of update.run: a stack (Stack) or a
// standalone container (Container).
type UpdateRunInput struct {
	PolicyID string `json:"policyId"`
	StackID  string `json:"stackId,omitempty"`
	// Stack locates the stack's project; ExpectSourceHash is the applied
	// revision's hash: the definition on disk must hash to it before,
	// during and after the run (an undeployed edit is never deployed by an
	// update).
	Stack            *ProjectRef      `json:"stack,omitempty"`
	ExpectSourceHash string           `json:"expectSourceHash,omitempty"`
	Container        *UpdateContainer `json:"container,omitempty"`
	Services         []UpdateService  `json:"services"`
	// WaitTimeoutSeconds bounds each dependency wait and the final health
	// confirmation (0: the agent's default).
	WaitTimeoutSeconds int `json:"waitTimeoutSeconds,omitempty"`
	// TimeoutSeconds bounds stop grace periods (0: the containers').
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// RegistryConnections is jobspec.CredentialRefs (#19).
	RegistryConnections []string `json:"registryConnections,omitempty"`
}

// Validate checks the input's shape.
func (in UpdateRunInput) Validate() error {
	switch {
	case (in.Stack == nil) == (in.Container == nil):
		return errors.New("an update run targets exactly one stack or one container")
	case len(in.Services) == 0 || len(in.Services) > MaxUpdateServices:
		return fmt.Errorf("an update run applies 1 to %d services", MaxUpdateServices)
	case in.WaitTimeoutSeconds < 0 || in.WaitTimeoutSeconds > 3600, in.TimeoutSeconds < 0 || in.TimeoutSeconds > 3600:
		return errors.New("timeouts must be between 0 and 3600 seconds")
	}
	if in.Stack != nil {
		if err := in.Stack.Validate(); err != nil {
			return err
		}
		if !validDigest(in.ExpectSourceHash, false) {
			return errors.New("the applied revision's source hash is required")
		}
	}
	if in.Container != nil {
		if !ValidDockerName(in.Container.Name) || in.Container.ID == "" {
			return errors.New("the container needs a name and an ID")
		}
		if len(in.Services) != 1 || in.Services[0].Service != in.Container.Name {
			return errors.New("a container update applies exactly the container itself")
		}
		if in.Container.Spec.Image != in.Services[0].Reference {
			return errors.New("a container update keeps the saved reference unchanged")
		}
	}
	seen := map[string]bool{}
	for _, s := range in.Services {
		if s.Service == "" || len(s.Service) > 128 || seen[s.Service] {
			return fmt.Errorf("invalid or duplicate service %q", s.Service)
		}
		seen[s.Service] = true
		if s.Reference == "" || strings.Contains(s.Reference, "@") || len(s.Reference) > 1024 {
			return fmt.Errorf("service %s: the reference must be a tagged reference", s.Service)
		}
		if !validDigest(s.Digest, true) || (s.IndexDigest != "" && !validDigest(s.IndexDigest, true)) {
			return fmt.Errorf("service %s: invalid candidate digest", s.Service)
		}
	}
	return nil
}

// validDigest checks "sha256:<64 hex>" (prefixed) or a bare 64-hex hash.
func validDigest(s string, prefixed bool) bool {
	if prefixed {
		var ok bool
		if s, ok = strings.CutPrefix(s, "sha256:"); !ok {
			return false
		}
	}
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// UpdateServiceResult reports one service or container of an update run.
type UpdateServiceResult struct {
	Service   string `json:"service"`
	Reference string `json:"reference"`
	// From* is what ran before; To* what the tag named after the pull.
	FromImageID string `json:"fromImageId,omitempty"`
	FromDigest  string `json:"fromDigest,omitempty"`
	ToImageID   string `json:"toImageId,omitempty"`
	ToDigest    string `json:"toDigest,omitempty"`
	WasRunning  bool   `json:"wasRunning"`
	Outcome     string `json:"outcome"`
	Message     string `json:"message,omitempty"`
}

// UpdateRunOutput is the result output of update.run (also on failure).
type UpdateRunOutput struct {
	Stage string `json:"stage"`
	// Source hashes of the definition on disk before the run, after the
	// pull and at the end: all equal, or the run failed with
	// source_changed (stacks only).
	SourceHashBefore    string                `json:"sourceHashBefore,omitempty"`
	SourceHashAfterPull string                `json:"sourceHashAfterPull,omitempty"`
	SourceHashAfter     string                `json:"sourceHashAfter,omitempty"`
	Services            []UpdateServiceResult `json:"services"`
	// Restarted are dependents restarted because they declare
	// depends_on restart: true on an updated service.
	Restarted []string `json:"restarted,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	// Before/After are the stack's service states (stacks only).
	Before []ServiceState `json:"before,omitempty"`
	After  []ServiceState `json:"after,omitempty"`
	// Quarantine: the failure is attributed to the new images (a
	// replaced container failed to start, to become healthy or to satisfy
	// its dependents): the manager quarantines the candidate digests so
	// they are not tried again automatically.
	Quarantine bool `json:"quarantine,omitempty"`
}

// Result returns the result of a service (nil when absent).
func (o *UpdateRunOutput) Result(service string) *UpdateServiceResult {
	for i := range o.Services {
		if o.Services[i].Service == service {
			return &o.Services[i]
		}
	}
	return nil
}
