package api

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
)

// OpenAPI extension keys carrying DockYard's contract metadata. They are
// documented in docs/api/README.md.
const (
	// ExtCapability is the capability an operation requires.
	ExtCapability = "x-dockyard-capability"
	// ExtCapabilityValues lists the concrete capability keys of a selector
	// capability such as stack.{action}.
	ExtCapabilityValues = "x-dockyard-capability-values"
	// ExtScope is the resource level the capability is checked at.
	ExtScope = "x-dockyard-scope"
	// ExtIdempotency says how an Idempotency-Key is honored (stored|job).
	ExtIdempotency = "x-dockyard-idempotency"
	// ExtAudit is the action key an audited operation records (#30).
	ExtAudit = "x-dockyard-audit"
)

// Security scheme names declared in components.securitySchemes.
const (
	// SecurityCookie is the browser session cookie (#16).
	SecurityCookie = "cookieSession"
	// SecurityBearer is an API token sent as Authorization: Bearer (#31).
	SecurityBearer = "bearerToken"
)

// Capability names the permission an operation requires. It is either one of
// the pseudo-capabilities below, a dotted capability key from the #17
// catalog (e.g. "container.restart"), or a selector whose last segment is a
// {placeholder} (e.g. "stack.{action}") when the request picks one of
// several concrete keys; selectors list those keys in CapabilityValues.
type Capability string

// Pseudo-capabilities.
const (
	// CapabilityPublic: no authentication (health, setup status, login).
	CapabilityPublic Capability = "public"
	// CapabilityAuthenticated: any signed-in user or API token, no grant needed.
	CapabilityAuthenticated Capability = "authenticated"
	// CapabilityOwner: the instance owner only. Owner-only surfaces (users,
	// groups, invitations, security policy, credential administration, #16/#17)
	// are never delegable through grants.
	CapabilityOwner Capability = "owner"
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

// IdempotencyMode says how an operation honors the Idempotency-Key header.
type IdempotencyMode string

// Idempotency modes.
const (
	// IdempotencyNone: the operation has no Idempotency-Key header.
	IdempotencyNone IdempotencyMode = ""
	// IdempotencyStored: the API stores the first 2xx response per
	// (principal, operation, key) and replays it (idempotency.go). For
	// dangerous non-job operations.
	IdempotencyStored IdempotencyMode = "stored"
	// IdempotencyJob: the handler passes the key to the job engine, which
	// returns the existing job for a repeated request (#26). For operations
	// answering 202 + job.
	IdempotencyJob IdempotencyMode = "job"
)

var (
	operationIDRE   = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	capabilityKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	selectorRE      = regexp.MustCompile(`^([a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*)\.\{[a-z][A-Za-z0-9]*\}$`)
)

// Operation is a huma.Operation plus mandatory DockYard metadata.
type Operation struct {
	huma.Operation
	// Capability required to call the operation. Mandatory.
	Capability Capability
	// CapabilityValues are the concrete keys of a selector Capability.
	CapabilityValues []Capability
	// Scope at which Capability is checked. Mandatory.
	Scope Scope
	// Idempotency declares how the Idempotency-Key header is honored. It
	// must be set exactly when the input embeds IdempotencyKeyParam.
	Idempotency IdempotencyMode
	// Audit decides whether calls are recorded in the audit trail (#30).
	// Every non-GET operation is audited by construction (there is no
	// opt-out); GET operations opt in with AuditAlways (downloads, exports).
	Audit AuditMode
	// AuditAction overrides the recorded action key. Default: Capability
	// when it is a dotted key; for pseudo-capabilities (public,
	// authenticated, owner) and selectors a key derived from the operation
	// ID (audit.ActionForOperation: create-invitation -> invitation.create).
	// Handlers of selector operations record the concrete key they
	// authorized with audit.SetAction.
	AuditAction string
	// SessionOnly marks an operation API tokens (#31) can never call: it
	// needs an interactive browser session (sign-in and factor flows, token
	// management, Recovery Key administration). Owner operations and
	// operations whose capability is owner-only in the #17 catalog are
	// session-only anyway, as are operations declaring a cookie-only
	// Security requirement. See AcceptsAPITokens.
	SessionOnly bool
}

// AcceptsAPITokens reports whether an API token may call op (#31). Owner
// operations (users, groups, invitations, security settings, registry
// and Git credential administration, other users' tokens, ...),
// operations with an owner-only catalog capability (ownership, manager
// backup and system restore), SessionOnly operations and operations
// declaring cookie-only security never accept tokens: Register refuses a
// token-authenticated call with 403 api_token_not_allowed before any
// handler runs, and documents cookie-only security in OpenAPI.
func (op Operation) AcceptsAPITokens() bool {
	switch {
	case op.Capability == CapabilityPublic:
		return true
	case op.Capability == CapabilityOwner, op.SessionOnly, ownerOnlyCapability(op.Capability, op.CapabilityValues):
		return false
	case op.Security != nil:
		for _, alt := range op.Security {
			if _, ok := alt[SecurityBearer]; ok {
				return true
			}
		}
		return false
	}
	return true
}

// ownerOnlyCapability reports whether c (or any value of a selector) is
// reserved to the instance owner in the permission catalog.
func ownerOnlyCapability(c Capability, values []Capability) bool {
	cat := catalog.Default()
	for _, k := range append([]Capability{c}, values...) {
		if cp, ok := cat.Lookup(string(k)); ok && cp.OwnerOnly {
			return true
		}
	}
	return false
}

// tokenGuard refuses API-token callers of operations that do not accept
// them (AcceptsAPITokens).
func tokenGuard(ctx huma.Context, next func(huma.Context)) {
	if p, ok := authz.PrincipalFrom(ctx.Context()); ok && p.Kind == authz.KindAPIToken {
		audit.SetErrorClass(ctx.Context(), CodeAPITokenNotAllowed)
		writeHumaError(ctx, NewError(http.StatusForbidden, CodeAPITokenNotAllowed,
			"API tokens cannot be used for this operation; it needs a signed-in browser session"))
		return
	}
	next(ctx)
}

// AuditMode says when an operation is audited.
type AuditMode string

// Audit modes.
const (
	// AuditDefault: audited unless the method is GET.
	AuditDefault AuditMode = ""
	// AuditAlways: audited also for GET (downloads, exports, other reads
	// that must be accountable).
	AuditAlways AuditMode = "always"
)

// Audited reports whether calls of op are recorded in the audit trail.
func (op Operation) Audited() bool {
	return op.Method != http.MethodGet || op.Audit == AuditAlways
}

// AuditActionKey is the action recorded for op.
func (op Operation) AuditActionKey() string {
	if op.AuditAction != "" {
		return op.AuditAction
	}
	if IsCapabilityKey(string(op.Capability)) {
		return string(op.Capability)
	}
	return audit.ActionForOperation(op.OperationID)
}

// Validate checks the DockYard metadata rules.
func (op Operation) Validate() error {
	if !operationIDRE.MatchString(op.OperationID) {
		return fmt.Errorf("api: operation %s %s: OperationID %q must be kebab-case (e.g. get-stack)", op.Method, op.Path, op.OperationID)
	}
	if err := ValidateMetadata(op.Capability, op.CapabilityValues, op.Scope); err != nil {
		return fmt.Errorf("api: operation %s: %w", op.OperationID, err)
	}
	switch op.Idempotency {
	case IdempotencyNone, IdempotencyStored, IdempotencyJob:
	default:
		return fmt.Errorf("api: operation %s: unknown idempotency mode %q", op.OperationID, op.Idempotency)
	}
	if op.Idempotency != IdempotencyNone && op.Capability == CapabilityPublic {
		return fmt.Errorf("api: operation %s: idempotency keys are scoped per principal and need an authenticated operation", op.OperationID)
	}
	if op.Summary == "" {
		return fmt.Errorf("api: operation %s: Summary is required", op.OperationID)
	}
	switch op.Audit {
	case AuditDefault, AuditAlways:
	default:
		return fmt.Errorf("api: operation %s: unknown audit mode %q", op.OperationID, op.Audit)
	}
	if op.AuditAction != "" {
		if !op.Audited() {
			return fmt.Errorf("api: operation %s: AuditAction is set but GET operations are audited only with Audit: AuditAlways", op.OperationID)
		}
		if !IsCapabilityKey(op.AuditAction) {
			return fmt.Errorf("api: operation %s: AuditAction %q must be a dotted key like invitation.create", op.OperationID, op.AuditAction)
		}
	}
	return nil
}

// ValidateMetadata checks a capability, its selector values and its scope
// together (shared by Register and the route inventory).
func ValidateMetadata(c Capability, values []Capability, s Scope) error {
	if err := ValidateCapability(string(c)); err != nil {
		return err
	}
	if err := ValidateScope(string(s)); err != nil {
		return err
	}
	if m := selectorRE.FindStringSubmatch(string(c)); m != nil {
		if len(values) < 2 {
			return fmt.Errorf("selector capability %q needs at least two CapabilityValues", c)
		}
		seen := map[Capability]bool{}
		for _, v := range values {
			if !capabilityKeyRE.MatchString(string(v)) || !strings.HasPrefix(string(v), m[1]+".") {
				return fmt.Errorf("capability value %q of selector %q must be a key below %s", v, c, m[1])
			}
			if seen[v] {
				return fmt.Errorf("duplicate capability value %q", v)
			}
			seen[v] = true
		}
	} else if len(values) > 0 {
		return fmt.Errorf("CapabilityValues are only allowed with a selector capability like stack.{action}, not %q", c)
	}
	switch c {
	case CapabilityPublic, CapabilityAuthenticated:
		if s != ScopeNone {
			return fmt.Errorf("scope %q does not fit capability %q (public/authenticated use scope none)", s, c)
		}
	case CapabilityOwner:
		if s != ScopeInstance {
			return fmt.Errorf("scope %q does not fit capability %q (owner uses scope instance)", s, c)
		}
	default:
		if s == ScopeNone {
			return fmt.Errorf("capability %q needs a real scope, not none", c)
		}
	}
	return nil
}

// ValidateCapability checks a capability value (a pseudo-capability, a
// dotted key or a selector).
func ValidateCapability(c string) error {
	switch Capability(c) {
	case CapabilityPublic, CapabilityAuthenticated, CapabilityOwner:
		return nil
	}
	if !capabilityKeyRE.MatchString(c) && !selectorRE.MatchString(c) {
		return fmt.Errorf("capability %q must be %q, %q, %q, a dotted key like container.restart or a selector like stack.{action}",
			c, CapabilityPublic, CapabilityAuthenticated, CapabilityOwner)
	}
	return nil
}

// IsCapabilityKey reports whether c is a grantable dotted key (not a
// pseudo-capability or selector).
func IsCapabilityKey(c string) bool { return capabilityKeyRE.MatchString(c) }

// ValidateScope checks a scope value.
func ValidateScope(s string) error {
	switch Scope(s) {
	case ScopeNone, ScopeInstance, ScopeEnvironment, ScopeResource:
		return nil
	}
	return fmt.Errorf("scope %q must be one of none, instance, environment, resource", s)
}

var (
	idempotencyParamType = reflect.TypeFor[IdempotencyKeyParam]()
	jobAcceptedType      = reflect.TypeFor[JobAccepted]()
)

// embeds reports whether struct type t embeds (at any depth) the type want.
func embeds(t, want reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.Anonymous {
			continue
		}
		if f.Type == want || embeds(f.Type, want) {
			return true
		}
	}
	return false
}

// Register adds an operation to the API. It is the ONLY way DockYard code
// registers operations; it panics at startup when metadata is missing or
// inconsistent so a misdeclared route can never ship:
//
//   - operation ID, summary, capability and scope are mandatory (Validate);
//   - Idempotency is declared exactly when the input embeds
//     IdempotencyKeyParam (stored mode installs the response store);
//   - 202 Accepted is used exactly by operations returning JobAccepted;
//   - non-public operations get the cookie and bearer security
//     requirements and a documented 401 (declare 403 explicitly where the
//     route answers it; routes that hide existence answer 404, lists filter);
//   - every non-GET operation (and every GET declaring AuditAlways) is
//     audited (#30): one record per call with the action, actor, client IP,
//     user agent, path-parameter targets, outcome and error class, request
//     ID and job ID (audit_http.go). Handlers enrich it via the audit
//     package's context helpers.
//
// Authorization enforcement against the declared capability arrives with #17.
func Register[I, O any](a huma.API, op Operation, handler func(context.Context, *I) (*O, error)) {
	if err := op.Validate(); err != nil {
		panic(err)
	}
	hasKey := embeds(reflect.TypeFor[I](), idempotencyParamType)
	if hasKey != (op.Idempotency != IdempotencyNone) {
		panic(fmt.Errorf("api: operation %s: declare Idempotency exactly when the input embeds IdempotencyKeyParam (embeds=%v, mode=%q)",
			op.OperationID, hasKey, op.Idempotency))
	}
	accepted := reflect.TypeFor[O]() == jobAcceptedType
	if accepted && op.DefaultStatus == 0 {
		op.DefaultStatus = http.StatusAccepted
	}
	if accepted != (op.DefaultStatus == http.StatusAccepted) {
		panic(fmt.Errorf("api: operation %s: 202 Accepted is reserved for operations returning JobAccepted (202 + job + Location)", op.OperationID))
	}

	hop := op.Operation
	ext := make(map[string]any, len(hop.Extensions)+4)
	for k, v := range hop.Extensions {
		ext[k] = v
	}
	ext[ExtCapability] = string(op.Capability)
	ext[ExtScope] = string(op.Scope)
	if len(op.CapabilityValues) > 0 {
		vals := make([]string, len(op.CapabilityValues))
		for i, v := range op.CapabilityValues {
			vals[i] = string(v)
		}
		ext[ExtCapabilityValues] = vals
	}
	if op.Idempotency != IdempotencyNone {
		ext[ExtIdempotency] = string(op.Idempotency)
	}
	hop.Extensions = ext

	if op.Capability != CapabilityPublic {
		tokens := op.AcceptsAPITokens()
		switch {
		case hop.Security == nil && tokens:
			hop.Security = []map[string][]string{{SecurityCookie: {}}, {SecurityBearer: {}}}
		case hop.Security == nil:
			hop.Security = []map[string][]string{{SecurityCookie: {}}}
		case !tokens:
			for _, alt := range hop.Security {
				if _, ok := alt[SecurityBearer]; ok {
					panic(fmt.Errorf("api: operation %s: owner, owner-only and session-only operations never accept API tokens; do not declare the bearer scheme", op.OperationID))
				}
			}
		}
		hop.Errors = withStatus(hop.Errors, http.StatusUnauthorized)
		if !tokens {
			hop.Errors = withStatus(hop.Errors, http.StatusForbidden)
			hop.Middlewares = append(huma.Middlewares{tokenGuard}, hop.Middlewares...)
		}
	}
	if op.Idempotency == IdempotencyStored {
		hop.Errors = withStatus(hop.Errors, http.StatusConflict)
		hop.Middlewares = append(slices.Clone(hop.Middlewares), idempotencyMiddleware(hop.OperationID, hop.MaxBodyBytes))
	}
	if op.Audited() {
		// Outermost operation middleware: it sees the final status of
		// everything below it (validation, idempotency replays, panics).
		ext[ExtAudit] = op.AuditActionKey()
		hop.Middlewares = append(huma.Middlewares{auditMiddleware(op)}, hop.Middlewares...)
		handler = auditHandler(handler)
	}
	huma.Register(a, hop, handler)
}

func withStatus(errs []int, status int) []int {
	if slices.Contains(errs, status) {
		return errs
	}
	out := append(slices.Clone(errs), status)
	slices.Sort(out)
	return out
}
