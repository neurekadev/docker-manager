package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/permissions"
)

// Authorization routes (#17): the permission catalog, the caller's
// effective permissions, groups, group and user rule documents, effective
// permissions of a user and owner-only "view as" previews. The flows live
// in internal/manager/permissions; this file is the transport contract.

const (
	tagPermissions = "Permissions"
	tagGroups      = "Groups"
)

// PermissionService is the authorization service as seen by the API
// (implemented by *permissions.Service). Management methods read the
// caller's session from ctx and enforce owner-only access and step-up.
type PermissionService interface {
	Catalog() *catalog.Catalog
	Mine(ctx context.Context, p authz.Principal) (permissions.Effective, error)
	ListGroups(ctx context.Context) ([]domain.GroupInfo, error)
	GetGroup(ctx context.Context, id string) (domain.GroupInfo, error)
	CreateGroup(ctx context.Context, name string) (domain.GroupInfo, error)
	RenameGroup(ctx context.Context, id string, revision int64, name string) (domain.GroupInfo, error)
	DeleteGroup(ctx context.Context, id string, revision int64) error
	GroupOrder(ctx context.Context) ([]string, error)
	ReorderGroups(ctx context.Context, expect, order []string) ([]domain.GroupInfo, error)
	SelectDefaultGroup(ctx context.Context, id string) (domain.GroupInfo, error)
	GroupPermissions(ctx context.Context, id string) (domain.PermissionDocument, error)
	ReplaceGroupPermissions(ctx context.Context, id string, revision int64, rules []domain.PermissionRule) (domain.PermissionDocument, error)
	UserPermissions(ctx context.Context, id string) (domain.PermissionDocument, error)
	ReplaceUserPermissions(ctx context.Context, id string, revision int64, rules []domain.PermissionRule) (domain.PermissionDocument, error)
	UserEffective(ctx context.Context, userID string) (permissions.Effective, error)
	Preview(ctx context.Context, req permissions.PreviewRequest) (permissions.Preview, error)
}

// --- catalog ---

// CatalogScopes are the scopes a capability can be granted at.
type CatalogScopes struct {
	Instance      bool     `json:"instance" doc:"All resources of the capability's type, including future ones."`
	Environment   bool     `json:"environment" doc:"All such resources in one environment."`
	ResourceTypes []string `json:"resourceTypes" doc:"Resource types a rule may target individually. A parent type (stack, service) covers its current and future children."`
}

// CatalogCapability is one grantable action.
type CatalogCapability struct {
	Key          string        `json:"key" example:"container.restart" doc:"Stable capability key."`
	ResourceType string        `json:"resourceType" example:"container" doc:"Grouping in editors."`
	Label        string        `json:"label" example:"Restart" doc:"Plain-language action name."`
	Description  string        `json:"description"`
	Scopes       CatalogScopes `json:"scopes"`
	Risk         string        `json:"risk" enum:"normal,high" doc:"high: shown distinctly (secrets, data loss, code execution, cross-resource visibility)."`
	OwnerOnly    bool          `json:"ownerOnly" doc:"Reserved to the instance owner: never grantable."`
	Advanced     bool          `json:"advanced" doc:"Less common: collapsed in editors until needed."`
	Since        int           `json:"since" doc:"Catalog version that introduced the key."`
}

// CatalogResourceType describes a resource type.
type CatalogResourceType struct {
	Key                 string   `json:"key" example:"container"`
	Label               string   `json:"label" example:"Containers"`
	Scopable            bool     `json:"scopable" doc:"Rules may target one resource of this type."`
	EnvironmentBound    bool     `json:"environmentBound"`
	NamedPerEnvironment bool     `json:"namedPerEnvironment" doc:"Identified by environment and name (rules name the environment)."`
	Parents             []string `json:"parents" doc:"Resource types that contain this one, nearest first."`
	ReadCapability      string   `json:"readCapability,omitempty" doc:"Shows the resource in full; any other capability shows only the minimal fields."`
	MinimalFields       string   `json:"minimalFields,omitempty" doc:"Fields visible with any other capability."`
}

// PermissionCatalog is the versioned capability catalog.
type PermissionCatalog struct {
	Version       int                   `json:"version" doc:"Catalog version; increases when capabilities are added. New capabilities are denied until a rule grants them."`
	ResourceTypes []CatalogResourceType `json:"resourceTypes"`
	Capabilities  []CatalogCapability   `json:"capabilities"`
}

func newCatalog(c *catalog.Catalog) PermissionCatalog {
	out := PermissionCatalog{Version: c.Version(), ResourceTypes: []CatalogResourceType{}, Capabilities: []CatalogCapability{}}
	for _, t := range c.Types() {
		out.ResourceTypes = append(out.ResourceTypes, CatalogResourceType{Key: t.Key, Label: t.Label, Scopable: t.Scopable,
			EnvironmentBound: t.EnvironmentBound, NamedPerEnvironment: t.NamedPerEnvironment, Parents: append([]string{}, t.Parents...),
			ReadCapability: t.Read, MinimalFields: t.Minimal})
	}
	for _, cp := range c.Capabilities() {
		out.Capabilities = append(out.Capabilities, CatalogCapability{Key: cp.Key, ResourceType: cp.Type, Label: cp.Label,
			Description: cp.Description, Risk: string(cp.Risk), OwnerOnly: cp.OwnerOnly, Advanced: cp.Advanced, Since: cp.Since,
			Scopes: CatalogScopes{Instance: cp.Instance, Environment: cp.Environment, ResourceTypes: append([]string{}, cp.Resources...)}})
	}
	return out
}

// --- rules ---

// PermissionScope is where a rule applies.
type PermissionScope struct {
	Kind          string `json:"kind" enum:"instance,environment,resource" doc:"instance: all resources of the capability's type (also future ones); environment: all of them in one environment; resource: one resource (a stack or service also covers its current and future children)."`
	EnvironmentID string `json:"environmentId,omitempty" maxLength:"128" doc:"environment scopes; and resource scopes of types named per environment (container, image, volume, network)."`
	ResourceType  string `json:"resourceType,omitempty" maxLength:"64" example:"container"`
	ResourceID    string `json:"resourceId,omitempty" maxLength:"1024" doc:"Docker Manager ID (stack, agent, policy, ...), Docker name (container, image, volume, network) or <stackId>/<service> for services."`
}

// PermissionRule allows or denies one capability at one scope.
type PermissionRule struct {
	Capability string          `json:"capability" maxLength:"128" example:"container.restart"`
	Scope      PermissionScope `json:"scope"`
	Effect     string          `json:"effect" enum:"allow,deny"`
}

func newRule(r domain.PermissionRule) PermissionRule {
	return PermissionRule{Capability: r.Capability, Effect: string(r.Effect), Scope: PermissionScope{Kind: r.Scope.Kind,
		EnvironmentID: r.Scope.EnvironmentID, ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}}
}

func (r PermissionRule) domain() domain.PermissionRule {
	return domain.PermissionRule{Capability: r.Capability, Effect: domain.PermissionEffect(r.Effect), Scope: domain.PermissionScope{
		Kind: r.Scope.Kind, EnvironmentID: r.Scope.EnvironmentID, ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}}
}

func domainRules(in []PermissionRule) []domain.PermissionRule {
	out := make([]domain.PermissionRule, 0, len(in))
	for _, r := range in {
		out = append(out, r.domain())
	}
	return out
}

// PermissionDocument is the full rule list of a group or a user.
type PermissionDocument struct {
	SubjectID      string           `json:"subjectId" doc:"Group or user ID."`
	Revision       int64            `json:"revision" doc:"Document revision (the ETag); PUT needs it in If-Match."`
	CatalogVersion int              `json:"catalogVersion"`
	Rules          []PermissionRule `json:"rules" doc:"Group documents: allow/deny rules (no rule = deny). User documents: overrides (no rule = inherit the group)."`
}

func newDocument(d domain.PermissionDocument, version int) PermissionDocument {
	out := PermissionDocument{SubjectID: d.SubjectID, Revision: d.Revision, CatalogVersion: version, Rules: []PermissionRule{}}
	for _, r := range d.Rules {
		out.Rules = append(out.Rules, newRule(r))
	}
	return out
}

// --- groups ---

// Group is a permission group. A user can be in several groups; one group
// is the default for new users. Groups are ordered by priority.
type Group struct {
	ID                  string    `json:"id" example:"0190a6e0-0000-7000-8000-00000000000a"`
	Name                string    `json:"name" example:"Restricted"`
	Position            int       `json:"position" doc:"Priority order, 0 first: for a member of several groups, the first group with a rule matching an action decides."`
	Default             bool      `json:"default" doc:"New users join this group."`
	MemberCount         int       `json:"memberCount" doc:"Accounts in the group (the owner is in none: group rules never apply to it)."`
	RuleCount           int       `json:"ruleCount"`
	GrantsAccess        bool      `json:"grantsAccess" doc:"The group has at least one allow rule."`
	Revision            int64     `json:"revision" doc:"Group revision (the ETag of the group; name changes)."`
	PermissionsRevision int64     `json:"permissionsRevision" doc:"Revision of the group's permission document."`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func newGroup(g domain.GroupInfo) Group {
	return Group{ID: g.ID, Name: g.Name, Position: g.Position, Default: g.Default, MemberCount: g.MemberCount, RuleCount: g.RuleCount,
		GrantsAccess: g.AllowCount > 0, Revision: g.Revision, PermissionsRevision: g.PermissionsRevision,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}
}

// --- effective permissions ---

// EffectivePermission is the decision for one capability at one scope.
type EffectivePermission struct {
	Capability string          `json:"capability"`
	Scope      PermissionScope `json:"scope"`
	Allowed    bool            `json:"allowed"`
	Source     string          `json:"source" enum:"owner,user_rule,group_rule,default_deny,token_scope,unknown_capability,owner_only,inactive_account" doc:"What decided: a user override, a group rule, or nothing (default deny)."`
	Rule       *PermissionRule `json:"rule,omitempty" doc:"The deciding rule."`
	GroupID    string          `json:"groupId,omitempty" doc:"The group of the deciding rule (group_rule)."`
	Reason     string          `json:"reason" doc:"Plain-language explanation."`
}

// EffectivePermissions lists what a user may do.
type EffectivePermissions struct {
	UserID         string                `json:"userId,omitempty"`
	Owner          bool                  `json:"owner" doc:"The instance owner may do everything; entries are empty."`
	GroupIDs       []string              `json:"groupIds" doc:"The user's groups, highest priority first."`
	GroupID        string                `json:"groupId,omitempty" deprecated:"true" doc:"Deprecated: the first of groupIds."`
	CatalogVersion int                   `json:"catalogVersion"`
	Entries        []EffectivePermission `json:"entries" doc:"One entry per capability and scope named by the user's or group's rules, with the effective decision there. Anything not listed is denied."`
}

func newEffective(e permissions.Effective, version int) EffectivePermissions {
	out := EffectivePermissions{UserID: e.UserID, Owner: e.Owner, GroupIDs: groupIDs(e.GroupIDs), GroupID: firstGroup(e.GroupIDs),
		CatalogVersion: version, Entries: []EffectivePermission{}}
	for _, en := range e.Entries {
		ep := EffectivePermission{Capability: en.Capability, Allowed: en.Allowed, Source: en.Source, GroupID: en.GroupID, Reason: en.Reason,
			Scope: newRule(domain.PermissionRule{Scope: en.Scope}).Scope}
		if en.Rule != nil {
			r := newRule(*en.Rule)
			ep.Rule = &r
		}
		out.Entries = append(out.Entries, ep)
	}
	return out
}

// VisibleEnvironment is one environment the caller can see.
type VisibleEnvironment struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	View    string   `json:"view" enum:"minimal,full" doc:"minimal: name and status only (a grant inside it); full: environment.read."`
	Actions []string `json:"actions" doc:"Granted environment capabilities."`
}

// MyPermissions is the caller's effective permissions and a minimal summary
// of the resources they can see.
type MyPermissions struct {
	EffectivePermissions
	Environments []VisibleEnvironment `json:"environments" doc:"Environments visible to the caller (at most 500). A new Restricted user sees none: show an empty state, not an error."`
}

// --- previews ---

// ResourceRefDTO identifies a resource.
type ResourceRefDTO struct {
	Type          string `json:"type" maxLength:"64"`
	ID            string `json:"id" maxLength:"1024"`
	EnvironmentID string `json:"environmentId,omitempty" maxLength:"128"`
}

// ResourceDTO is a resource a capability is checked on.
type ResourceDTO struct {
	Type          string           `json:"type" maxLength:"64" example:"container" doc:"Catalog resource type, or instance."`
	ID            string           `json:"id,omitempty" maxLength:"1024" doc:"Empty: any (new) resource of the type in environmentId."`
	EnvironmentID string           `json:"environmentId,omitempty" maxLength:"128"`
	Parents       []ResourceRefDTO `json:"parents,omitempty" maxItems:"8" doc:"Containing resources, nearest first (service <stackId>/<name>, stack). Omitted: resolved by the manager."`
}

func (r ResourceDTO) resource() authz.Resource {
	out := authz.Resource{Type: r.Type, ID: r.ID, EnvironmentID: r.EnvironmentID}
	if r.Parents != nil {
		out.Parents = []authz.ResourceRef{}
		for _, p := range r.Parents {
			out.Parents = append(out.Parents, authz.ResourceRef(p))
		}
	}
	return out
}

func newResourceDTO(r authz.Resource) ResourceDTO {
	out := ResourceDTO{Type: r.Type, ID: r.ID, EnvironmentID: r.EnvironmentID}
	for _, p := range r.Parents {
		out.Parents = append(out.Parents, ResourceRefDTO(p))
	}
	return out
}

// PreviewCheck asks for one decision.
type PreviewCheck struct {
	Capability string      `json:"capability" maxLength:"128"`
	Resource   ResourceDTO `json:"resource"`
}

// PreviewDecision is the decision of one check with its reason.
type PreviewDecision struct {
	Capability string          `json:"capability"`
	Resource   ResourceDTO     `json:"resource"`
	Allowed    bool            `json:"allowed"`
	Source     string          `json:"source" doc:"owner, user_rule, group_rule, default_deny, token_scope, unknown_capability, owner_only, inactive_account or job_targets."`
	Rule       *PermissionRule `json:"rule,omitempty"`
	GroupID    string          `json:"groupId,omitempty" doc:"The group of the deciding rule (group_rule)."`
	Reason     string          `json:"reason"`
}

// PermissionPreview is the result of a view-as evaluation.
type PermissionPreview struct {
	Effective EffectivePermissions `json:"effective"`
	Checks    []PreviewDecision    `json:"checks"`
}

// --- inputs/outputs ---

type catalogOutput struct{ Body PermissionCatalog }
type myPermissionsOutput struct{ Body MyPermissions }
type groupsOutput struct {
	ETagHeader
	Body struct {
		Items []Group `json:"items" doc:"In priority order, the highest first."`
	}
}
type reorderGroupsInput struct {
	IfMatchParam
	Body struct {
		GroupIDs []string `json:"groupIds" maxItems:"256" doc:"Every group's ID once, the highest priority first."`
	}
}
type groupOutput struct {
	ETagHeader
	Body Group
}
type groupIDInput struct {
	GroupID string `path:"groupId" maxLength:"64" doc:"Group ID."`
}
type createGroupInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"64" example:"Operators"`
	}
}
type updateGroupInput struct {
	GroupID string `path:"groupId" maxLength:"64" doc:"Group ID."`
	IfMatchParam
	Body struct {
		Name *string `json:"name,omitempty" example:"Operators" minLength:"1" maxLength:"64"`
	}
}
type deleteGroupInput struct {
	GroupID string `path:"groupId" maxLength:"64" doc:"Group ID."`
	IfMatchParam
}
type defaultSelectionOutput struct {
	Body struct {
		Group   Group  `json:"group"`
		Warning string `json:"warning,omitempty" doc:"Set when the new default group grants access: every newly invited user gets it."`
	}
}
type documentOutput struct {
	ETagHeader
	Body PermissionDocument
}
type replaceDocumentBody struct {
	Rules []PermissionRule `json:"rules" maxItems:"2000" doc:"The complete new rule list. Duplicate rules for the same capability and scope are rejected."`
}
type replaceGroupDocumentInput struct {
	GroupID string `path:"groupId" maxLength:"64" doc:"Group ID."`
	IfMatchParam
	Body replaceDocumentBody
}
type userDocumentInput struct {
	UserID string `path:"userId" maxLength:"64" doc:"User ID."`
}
type replaceUserDocumentInput struct {
	UserID string `path:"userId" maxLength:"64" doc:"User ID."`
	IfMatchParam
	Body replaceDocumentBody
}
type effectiveOutput struct{ Body EffectivePermissions }
type previewInput struct {
	Body struct {
		UserID       string            `json:"userId,omitempty" maxLength:"64" doc:"Preview this user."`
		GroupIDs     *[]string         `json:"groupIds,omitempty" maxItems:"256" doc:"Preview the user in exactly these groups (a membership change), or a member of these groups without overrides when userId is absent."`
		GroupID      string            `json:"groupId,omitempty" maxLength:"64" deprecated:"true" doc:"Deprecated: the same as groupIds with this one group."`
		RulesGroupID string            `json:"rulesGroupId,omitempty" maxLength:"64" doc:"The previewed group whose rules groupRules replace; default: the only previewed group."`
		GroupRules   *[]PermissionRule `json:"groupRules,omitempty" maxItems:"2000" doc:"Unsaved group rules to preview instead of the stored ones."`
		UserRules    *[]PermissionRule `json:"userRules,omitempty" maxItems:"2000" doc:"Unsaved user overrides to preview."`
		TokenScope   *[]PermissionRule `json:"tokenScope,omitempty" maxItems:"2000" doc:"Preview an API token with this scope (#31, allow rules only): token scope ∩ the user's effective permissions."`
		Checks       []PreviewCheck    `json:"checks,omitempty" maxItems:"500" doc:"Decisions to explain (capability + resource)."`
	}
}
type previewOutput struct{ Body PermissionPreview }

// --- handlers ---

type permissionsAPI struct {
	svc  PermissionService
	deps Deps
}

func (h *permissionsAPI) service() (PermissionService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "the permission service is not available")
	}
	return h.svc, nil
}

// permissionError maps permission service errors.
func permissionError(err error) error {
	var re *domain.RuleError
	var fe *domain.FieldError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re):
		details := make([]ErrorDetail, 0, len(re.Problems))
		for _, p := range re.Problems {
			details = append(details, Field(fmt.Sprintf("body.%s[%d]", re.Field, p.Index), p.Message))
		}
		return Invalid("invalid permission rules", details...)
	case errors.As(err, &fe):
		return Invalid(fe.Message, Field("body."+fe.Field, fe.Message))
	case errors.Is(err, domain.ErrGroupNotFound):
		return NotFound("group not found")
	case errors.Is(err, domain.ErrUserNotFound):
		return NotFound("user not found")
	case errors.Is(err, domain.ErrGroupNameTaken):
		return Conflict(CodeGroupNameTaken, "another group already uses this name")
	case errors.Is(err, domain.ErrGroupIsDefault):
		return Conflict(CodeDefaultGroupProtected, "the default group cannot be deleted; choose another default group first")
	case errors.Is(err, domain.ErrGroupNotEmpty):
		return Conflict(CodeGroupNotEmpty, "the group still has members; remove them from it first")
	case errors.Is(err, domain.ErrGroupOrderStale):
		return Invalid("name every group once", Field("body.groupIds", "every group's ID exactly once"))
	case errors.Is(err, domain.ErrOwnerProtected):
		return Conflict(CodeOwnerProtected, "the instance owner's capabilities are protected and cannot be changed by rules")
	}
	return identityError(err)
}

// groupOrderETag is the ETag of the group order: a digest of every
// group's ID in priority order.
func groupOrderETag(order []string) string {
	sum := sha256.Sum256([]byte(strings.Join(order, "\n")))
	return ETag("order-" + hex.EncodeToString(sum[:8]))
}

func newGroupsOutput(gs []domain.GroupInfo) *groupsOutput {
	out := &groupsOutput{}
	out.Body.Items = make([]Group, 0, len(gs))
	order := make([]string, 0, len(gs))
	for _, g := range gs {
		out.Body.Items = append(out.Body.Items, newGroup(g))
		order = append(order, g.ID)
	}
	out.ETag = groupOrderETag(order)
	return out
}

func (h *permissionsAPI) staleGroup(ctx context.Context, svc PermissionService, id string) error {
	g, err := svc.GetGroup(ctx, id)
	if err != nil {
		return permissionError(err)
	}
	return stale(g.Revision)
}

func registerPermissions(a huma.API, deps Deps) {
	h := &permissionsAPI{svc: deps.Permissions, deps: deps}
	ownerErrs := []int{http.StatusForbidden, http.StatusNotFound}
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}
	stepUp := " Requires a recent step-up (403 step_up_required)."

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-permission-catalog", Method: http.MethodGet, Path: BasePath + "/permission-catalog",
			Summary: "Get the permission catalog",
			Description: "The versioned catalog of capability keys with their resource grouping, plain-language labels, compatible scopes, " +
				"risk hints and owner-only flags. There are no generic read/write grants: every action has its own key.",
			Tags: []string{tagPermissions},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*catalogOutput, error) {
		if _, ok := authz.PrincipalFrom(ctx); !ok {
			return nil, Unauthenticated("authentication required")
		}
		return &catalogOutput{Body: newCatalog(catalog.Default())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-my-permissions", Method: http.MethodGet, Path: BasePath + "/me/permissions",
			Summary: "Get my effective permissions",
			Description: "The caller's effective permissions (one entry per capability and scope named by their rules, with the deciding rule) " +
				"and the environments they can see. Use it to show or hide actions; the server still decides on every request.",
			Tags: []string{tagPermissions},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.mine)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-groups", Method: http.MethodGet, Path: BasePath + "/groups",
			Summary: "List groups", Description: "Every group with member and rule counts, in priority order (the highest first). The ETag " +
				"is the order's: PUT /group-order needs it in If-Match. " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: []int{http.StatusForbidden},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*groupsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		gs, err := svc.ListGroups(ctx)
		if err != nil {
			return nil, permissionError(err)
		}
		return newGroupsOutput(gs), nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "replace-group-order", Method: http.MethodPut, Path: BasePath + "/group-order",
			Summary: "Reorder the groups",
			Description: "Sets the groups' priority order, the highest first: for a member of several groups, the first group with a rule " +
				"matching an action decides (user overrides still come first). Name every group once (422 otherwise). Members of several " +
				"groups get their new access at once: their open requests and streams end. Requires If-Match with the ETag of " +
				"GET /groups (412 when the order changed meanwhile)." + stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *reorderGroupsInput) (*groupsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.GroupOrder(ctx)
		if err != nil {
			return nil, permissionError(err)
		}
		if err := in.CheckIfMatch(groupOrderETag(cur)); err != nil {
			return nil, err
		}
		gs, err := svc.ReorderGroups(ctx, cur, in.Body.GroupIDs)
		if errors.Is(err, domain.ErrRevisionConflict) {
			latest, gerr := svc.GroupOrder(ctx)
			if gerr != nil {
				return nil, permissionError(gerr)
			}
			return nil, CheckIfMatch(groupOrderETag(cur), groupOrderETag(latest), true)
		}
		if err != nil {
			return nil, permissionError(err)
		}
		return newGroupsOutput(gs), nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-group", Method: http.MethodPost, Path: BasePath + "/groups",
			Summary: "Create a group", DefaultStatus: http.StatusCreated,
			Description: "Creates a group without grants (no access); edit its rules with PUT /groups/{groupId}/permissions. " +
				"409 group_name_taken." + stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *createGroupInput) (*groupOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		g, err := svc.CreateGroup(ctx, in.Body.Name)
		if err != nil {
			return nil, permissionError(err)
		}
		return &groupOutput{ETagHeader: ETagHeader{ETag: RevisionETag(g.Revision)}, Body: newGroup(g)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-group", Method: http.MethodGet, Path: BasePath + "/groups/{groupId}",
			Summary: "Get a group", Description: ownerOnly, Tags: []string{tagGroups}, Security: cookieOnly, Errors: ownerErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *groupIDInput) (*groupOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		g, err := svc.GetGroup(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		return &groupOutput{ETagHeader: ETagHeader{ETag: RevisionETag(g.Revision)}, Body: newGroup(g)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-group", Method: http.MethodPatch, Path: BasePath + "/groups/{groupId}",
			Summary: "Rename a group", Description: "Renames the group (the default group too). Requires If-Match with the group's ETag." +
				stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *updateGroupInput) (*groupOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.GetGroup(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		if in.Body.Name == nil {
			return &groupOutput{ETagHeader: ETagHeader{ETag: RevisionETag(cur.Revision)}, Body: newGroup(cur)}, nil
		}
		g, err := svc.RenameGroup(ctx, cur.ID, cur.Revision, *in.Body.Name)
		if errors.Is(err, domain.ErrRevisionConflict) {
			return nil, h.staleGroup(ctx, svc, cur.ID)
		}
		if err != nil {
			return nil, permissionError(err)
		}
		return &groupOutput{ETagHeader: ETagHeader{ETag: RevisionETag(g.Revision)}, Body: newGroup(g)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-group", Method: http.MethodDelete, Path: BasePath + "/groups/{groupId}",
			Summary: "Delete a group", DefaultStatus: http.StatusNoContent,
			Description: "Deletes an empty, non-default group and its rules. 409 default_group_protected for the current default group " +
				"(choose another default first); 409 group_not_empty while users are in it (remove them first: Docker Manager never changes " +
				"memberships implicitly, so deleting a group never changes anyone's access). Requires If-Match." + stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *deleteGroupInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.GetGroup(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		err = svc.DeleteGroup(ctx, cur.ID, cur.Revision)
		if errors.Is(err, domain.ErrRevisionConflict) {
			return nil, h.staleGroup(ctx, svc, cur.ID)
		}
		if err != nil {
			return nil, permissionError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-group-default-selection", Method: http.MethodPost, Path: BasePath + "/groups/{groupId}/default-selection",
			Summary: "Make a group the default",
			Description: "New users (invitation redemptions) join this group from now on; existing members are not moved. The response " +
				"warns when the group grants access, because every newly invited user gets it." + stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: ownerErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *groupIDInput) (*defaultSelectionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		g, err := svc.SelectDefaultGroup(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		out := &defaultSelectionOutput{}
		out.Body.Group = newGroup(g)
		if g.AllowCount > 0 {
			out.Body.Warning = fmt.Sprintf("the default group %q grants access (%d allow rules): every newly invited user gets it", g.Name, g.AllowCount)
		}
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-group-permissions", Method: http.MethodGet, Path: BasePath + "/groups/{groupId}/permissions",
			Summary: "Get a group's rules", Description: "The group's allow/deny rules and the document revision (ETag). " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: ownerErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *groupIDInput) (*documentOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		doc, err := svc.GroupPermissions(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		return &documentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(doc.Revision)}, Body: newDocument(doc, svc.Catalog().Version())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "replace-group-permissions", Method: http.MethodPut, Path: BasePath + "/groups/{groupId}/permissions",
			Summary: "Replace a group's rules",
			Description: "Replaces the complete rule list. Rules name catalog capabilities at scopes they support; duplicates for the same " +
				"capability and scope are rejected (422). Members' effective access changes at once: their open requests and streams end " +
				"and their stored idempotent responses are dropped. Audited with the before/after rules. Requires If-Match with the " +
				"document ETag." + stepUp + " " + ownerOnly,
			Tags: []string{tagGroups}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *replaceGroupDocumentInput) (*documentOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.GroupPermissions(ctx, in.GroupID)
		if err != nil {
			return nil, permissionError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		doc, err := svc.ReplaceGroupPermissions(ctx, in.GroupID, cur.Revision, domainRules(in.Body.Rules))
		if errors.Is(err, domain.ErrPermissionConflict) {
			latest, gerr := svc.GroupPermissions(ctx, in.GroupID)
			if gerr != nil {
				return nil, permissionError(gerr)
			}
			return nil, stale(latest.Revision)
		}
		if err != nil {
			return nil, permissionError(err)
		}
		return &documentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(doc.Revision)}, Body: newDocument(doc, svc.Catalog().Version())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-user-permissions", Method: http.MethodGet, Path: BasePath + "/users/{userId}/permissions",
			Summary: "Get a user's overrides", Description: "The user's allow/deny overrides (absent capability/scope = inherit the group). " + ownerOnly,
			Tags: []string{tagPermissions}, Security: cookieOnly, Errors: ownerErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userDocumentInput) (*documentOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		doc, err := svc.UserPermissions(ctx, in.UserID)
		if err != nil {
			return nil, permissionError(err)
		}
		return &documentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(doc.Revision)}, Body: newDocument(doc, svc.Catalog().Version())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "replace-user-permissions", Method: http.MethodPut, Path: BasePath + "/users/{userId}/permissions",
			Summary: "Replace a user's overrides",
			Description: "Replaces the user's override list: an allow or deny beats every group rule; removing a rule resets it to inherit " +
				"(an empty list resets everything). The owner's capabilities are protected (409 owner_protected). The user's open requests " +
				"and streams end at once. Audited with the before/after rules. Requires If-Match with the document ETag." + stepUp + " " + ownerOnly,
			Tags: []string{tagPermissions}, Security: cookieOnly, Errors: editErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *replaceUserDocumentInput) (*documentOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.UserPermissions(ctx, in.UserID)
		if err != nil {
			return nil, permissionError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		doc, err := svc.ReplaceUserPermissions(ctx, in.UserID, cur.Revision, domainRules(in.Body.Rules))
		if errors.Is(err, domain.ErrPermissionConflict) {
			latest, gerr := svc.UserPermissions(ctx, in.UserID)
			if gerr != nil {
				return nil, permissionError(gerr)
			}
			return nil, stale(latest.Revision)
		}
		if err != nil {
			return nil, permissionError(err)
		}
		return &documentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(doc.Revision)}, Body: newDocument(doc, svc.Catalog().Version())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-user-effective-permissions", Method: http.MethodGet, Path: BasePath + "/users/{userId}/effective-permissions",
			Summary: "Get a user's effective permissions",
			Description: "The decision for every capability and scope named by the user's overrides or group rules, with the deciding rule " +
				"(user override beats group rule; a higher group beats a lower one; within one rule set the exact resource beats environment " +
				"beats all). " + ownerOnly,
			Tags: []string{tagPermissions}, Security: cookieOnly, Errors: ownerErrs,
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userDocumentInput) (*effectiveOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		e, err := svc.UserEffective(ctx, in.UserID)
		if err != nil {
			return nil, permissionError(err)
		}
		return &effectiveOutput{Body: newEffective(e, svc.Catalog().Version())}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-permission-preview", Method: http.MethodPost, Path: BasePath + "/permission-previews",
			Summary: "Preview permissions (view as)",
			Description: "Evaluates what a user (or a member of some groups) could do, optionally with unsaved group rules, user overrides, " +
				"other groups or an API token scope (#31) applied, and explains each requested check with its deciding rule. Nothing is " +
				"stored and no session is impersonated. " + ownerOnly,
			Tags: []string{tagPermissions}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.preview)
}

func (h *permissionsAPI) mine(ctx context.Context, _ *struct{}) (*myPermissionsOutput, error) {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return nil, Unauthenticated("authentication required")
	}
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	e, err := svc.Mine(ctx, p)
	if err != nil {
		return nil, Internal(err)
	}
	out := &myPermissionsOutput{Body: MyPermissions{EffectivePermissions: newEffective(e, svc.Catalog().Version()), Environments: []VisibleEnvironment{}}}
	if h.deps.Agents != nil {
		envs, err := h.deps.Agents.ListEnvironments(ctx, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}, Limit: 500})
		if err != nil {
			return nil, Internal(err)
		}
		c := authz.For(ctx, h.deps.Authorizer, p)
		for _, env := range envs {
			v := authz.ViewOf(c, authz.EnvironmentResource(env.ID))
			if !v.Visible() {
				continue
			}
			actions := v.Actions
			if actions == nil {
				actions = []string{}
			}
			out.Body.Environments = append(out.Body.Environments, VisibleEnvironment{ID: env.ID, Name: env.Name, View: v.Level.String(), Actions: actions})
		}
	}
	return out, nil
}

func (h *permissionsAPI) preview(ctx context.Context, in *previewInput) (*previewOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	req := permissions.PreviewRequest{UserID: in.Body.UserID, GroupIDs: in.Body.GroupIDs, RulesGroupID: in.Body.RulesGroupID}
	if req.GroupIDs == nil && in.Body.GroupID != "" {
		req.GroupIDs = &[]string{in.Body.GroupID}
	}
	opt := func(rs *[]PermissionRule) *[]domain.PermissionRule {
		if rs == nil {
			return nil
		}
		d := domainRules(*rs)
		return &d
	}
	req.GroupRules, req.UserRules, req.TokenScope = opt(in.Body.GroupRules), opt(in.Body.UserRules), opt(in.Body.TokenScope)
	for _, c := range in.Body.Checks {
		req.Checks = append(req.Checks, permissions.PreviewCheck{Capability: c.Capability, Resource: c.Resource.resource()})
	}
	res, err := svc.Preview(ctx, req)
	if err != nil {
		return nil, permissionError(err)
	}
	out := &previewOutput{Body: PermissionPreview{Effective: newEffective(res.Effective, svc.Catalog().Version()), Checks: []PreviewDecision{}}}
	for _, c := range res.Checks {
		d := PreviewDecision{Capability: c.Capability, Resource: newResourceDTO(c.Resource), Allowed: c.Allowed, Source: c.Source,
			GroupID: c.GroupID, Reason: c.Reason}
		if c.Rule != nil {
			r := newRule(*c.Rule)
			d.Rule = &r
		}
		out.Body.Checks = append(out.Body.Checks, d)
	}
	return out, nil
}
