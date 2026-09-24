package api

import (
	"context"
	"fmt"
	"regexp"

	"github.com/danielgtaylor/huma/v2"
)

// OpenAPI extension keys carrying DockYard's authorization metadata.
const (
	ExtCapability = "x-dockyard-capability"
	ExtScope      = "x-dockyard-scope"
)

// Capability names the permission an operation requires. It is either one of
// the pseudo-capabilities below or a dotted capability key from the #17
// catalog, e.g. "container.restart" or "stack.deploy".
type Capability string

// Pseudo-capabilities.
const (
	// CapabilityPublic: no authentication (health, setup status, login).
	CapabilityPublic Capability = "public"
	// CapabilityAuthenticated: any signed-in user or API token, no grant needed.
	CapabilityAuthenticated Capability = "authenticated"
)

// Scope is the resource level at which the capability is evaluated.
type Scope string

// Scopes.
const (
	ScopeNone        Scope = "none"        // not resource-bound (public/authenticated)
	ScopeInstance    Scope = "instance"    // instance-wide settings and manager resources
	ScopeEnvironment Scope = "environment" // one environment (path has {environmentId})
	ScopeResource    Scope = "resource"    // one resource (stack, container, job, ...)
)

var (
	operationIDRE   = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	capabilityKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

// Operation is a huma.Operation plus mandatory DockYard metadata.
type Operation struct {
	huma.Operation
	// Capability required to call the operation. Mandatory.
	Capability Capability
	// Scope at which Capability is checked. Mandatory.
	Scope Scope
}

// Validate checks the DockYard metadata rules.
func (op Operation) Validate() error {
	if !operationIDRE.MatchString(op.OperationID) {
		return fmt.Errorf("api: operation %s %s: OperationID %q must be kebab-case (e.g. get-stack)", op.Method, op.Path, op.OperationID)
	}
	if err := ValidateCapability(string(op.Capability)); err != nil {
		return fmt.Errorf("api: operation %s: %w", op.OperationID, err)
	}
	if err := ValidateScope(string(op.Scope)); err != nil {
		return fmt.Errorf("api: operation %s: %w", op.OperationID, err)
	}
	pseudo := op.Capability == CapabilityPublic || op.Capability == CapabilityAuthenticated
	if pseudo != (op.Scope == ScopeNone) {
		return fmt.Errorf("api: operation %s: scope %q does not fit capability %q (public/authenticated use scope none; capability keys need a real scope)", op.OperationID, op.Scope, op.Capability)
	}
	if op.Summary == "" {
		return fmt.Errorf("api: operation %s: Summary is required", op.OperationID)
	}
	return nil
}

// ValidateCapability checks a capability value.
func ValidateCapability(c string) error {
	switch Capability(c) {
	case CapabilityPublic, CapabilityAuthenticated:
		return nil
	}
	if !capabilityKeyRE.MatchString(c) {
		return fmt.Errorf("capability %q must be %q, %q or a dotted key like container.restart", c, CapabilityPublic, CapabilityAuthenticated)
	}
	return nil
}

// ValidateScope checks a scope value.
func ValidateScope(s string) error {
	switch Scope(s) {
	case ScopeNone, ScopeInstance, ScopeEnvironment, ScopeResource:
		return nil
	}
	return fmt.Errorf("scope %q must be one of none, instance, environment, resource", s)
}

// Register adds an operation to the API. It is the ONLY way DockYard code
// registers operations; it panics at startup when metadata is missing so a
// misdeclared route can never ship. Authorization enforcement against the
// declared capability arrives with #17.
func Register[I, O any](a huma.API, op Operation, handler func(context.Context, *I) (*O, error)) {
	if err := op.Validate(); err != nil {
		panic(err)
	}
	hop := op.Operation
	ext := make(map[string]any, len(hop.Extensions)+2)
	for k, v := range hop.Extensions {
		ext[k] = v
	}
	ext[ExtCapability] = string(op.Capability)
	ext[ExtScope] = string(op.Scope)
	hop.Extensions = ext
	huma.Register(a, hop, handler)
}
