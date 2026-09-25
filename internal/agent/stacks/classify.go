package stacks

import (
	"context"
	"errors"
	"strings"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/agent/regauth"
	"github.com/neurekadev/dockyard/internal/jobexec"
)

// stepError is a classified stack job failure (jobexec.ClassedError): the
// job's error class is the stable Engine, Compose or lifecycle code and the
// recovery tells the operator what to do (#7, #26).
type stepError struct {
	class    string
	recovery string
	err      error
}

func (e *stepError) Error() string      { return e.err.Error() }
func (e *stepError) Unwrap() error      { return e.err }
func (e *stepError) ErrorClass() string { return e.class }
func (e *stepError) Recovery() string   { return e.recovery }

var _ jobexec.ClassedError = (*stepError)(nil)

// Credential class of a missing registry credential (#19).
const classCredentialUnavailable = "credential_unavailable" //nolint:gosec // G101: an error class, not a credential

// classNothingToBuild: a stack build of a project (or services) without a
// build section (#33).
const classNothingToBuild = "nothing_to_build"

var stackRecoveries = map[string]string{
	string(engine.CodeDependencyFailed): "A dependency did not become healthy, or a one-shot service failed (see the message). Services " +
		"that started keep running; nothing was rolled back. Fix the service (or restore the last applied revision) and deploy or " +
		"start again.",
	string(engine.CodeInvalidProject): "The Compose definition does not load. Fix it (validate it first) and deploy again; " +
		"the last applied revision is unchanged.",
	string(engine.CodeUnsupportedFeature): "The Compose definition uses a feature DockYard does not support (see the support matrix). " +
		"Remove it and deploy again.",
	string(engine.CodeUnauthorized): "The registry refused the credentials (or requires them). Check the registry connection for " +
		"the image (or add one for a private image) and deploy again.",
	string(engine.CodeForbidden):           "The registry denied access. Check the account's permissions on the repository.",
	string(engine.CodeRateLimited):         "The registry's rate limit was reached. Wait before deploying again.",
	string(engine.CodeRegistryUnavailable): "The registry is unavailable. Retry later.",
	string(engine.CodeNotFound):            "An image or object does not exist. Check the image references and deploy again.",
	string(engine.CodeBuildFailed):         "An image build failed (see the message). Fix the build section or Dockerfile and deploy again.",
	string(engine.CodeEngineUnavailable):   "The Docker Engine is not reachable from the agent. Check that Docker is running, then retry.",
	string(engine.CodeTimeout): "The Engine did not answer in time, or a dependency did not reach its condition in time. Check the " +
		"host's load and the dependency's health check and logs, then retry.",
	lifecycle.CodeDependencyMissing: "A required dependency has no containers. Deploy the stack.",
	lifecycle.CodeNoContainers:      "The service has no containers on this Engine. Deploy the stack first.",
	classCredentialUnavailable: "The job named a registry connection but its credential was not delivered. Check the registry " +
		"connection and deploy again; DockYard never pulls anonymously instead.",
	classNothingToBuild: "Only services with a build section can be built. Add a build section to the Compose definition or " +
		"select services that have one.",
}

// classify turns adapter, lifecycle and credential errors into classified
// step failures; other errors are returned unchanged (step_failed).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var ce jobexec.ClassedError
	if errors.As(err, &ce) {
		return err
	}
	class := ""
	switch {
	case errors.Is(err, regauth.ErrMissing):
		class = classCredentialUnavailable
	case errors.Is(err, errNoBuildSections):
		class = classNothingToBuild
	case lifecycle.CodeOf(err) != "":
		class = lifecycle.CodeOf(err)
	case engine.CodeOf(err) != "" && engine.CodeOf(err) != engine.CodeEngineError:
		class = string(engine.CodeOf(err))
	}
	if class == "" {
		return err
	}
	rec := stackRecoveries[class]
	if strings.HasPrefix(class, "storage_") {
		rec = "The stack's project directory is not in a verified stack root of this agent: fix the agent's storage layout " +
			"(see its diagnostics) and retry."
	}
	return &stepError{class: class, recovery: rec, err: err}
}

// classified wraps a step so its failures are classified.
func classified(f jobexec.StepFunc) jobexec.StepFunc {
	return func(ctx context.Context, sc *jobexec.StepContext) error {
		return classify(f(ctx, sc))
	}
}
