package domain

import (
	"errors"
	"time"
)

// Authorization (#17): permission rules of groups and users. The catalog
// of capability keys lives in internal/manager/authz/catalog; evaluation in
// internal/manager/authz/policy.

// PermissionEffect is a rule's effect.
type PermissionEffect string

// Effects.
const (
	PermissionAllow PermissionEffect = "allow"
	PermissionDeny  PermissionEffect = "deny"
)

// Permission scope kinds.
const (
	ScopeKindInstance    = "instance"
	ScopeKindEnvironment = "environment"
	ScopeKindResource    = "resource"
)

// PermissionScope is where a rule applies: all resources (instance), one
// environment, or one resource (type + ID; the environment too for
// resources named per environment: containers, images, volumes, networks).
type PermissionScope struct {
	Kind          string
	EnvironmentID string
	ResourceType  string
	ResourceID    string
}

// PermissionRule allows or denies one capability at one scope. User rules
// are overrides: a capability/scope without a user rule inherits the group.
type PermissionRule struct {
	Capability string
	Scope      PermissionScope
	Effect     PermissionEffect
}

// PermissionDocument is the full rule list of a group or a user with its
// optimistic revision (the ETag of GET/PUT .../permissions).
type PermissionDocument struct {
	// SubjectID is the group or user ID.
	SubjectID string
	Revision  int64
	Rules     []PermissionRule
}

// GroupInfo is a group with its member count.
type GroupInfo struct {
	Group
	PermissionsRevision int64
	// MemberCount counts the group's accounts except the owner, whom group
	// rules never govern.
	MemberCount int
	// RuleCount and AllowCount summarize its rules (the default-group
	// warning: a default group with allow rules grants access to new users).
	RuleCount  int
	AllowCount int
}

// GroupPatch renames a group.
type GroupPatch struct {
	Name *string
}

// PermissionSubject is everything evaluation needs about one user: the
// account state, its group and both rule sets.
type PermissionSubject struct {
	UserID     string
	Exists     bool
	Owner      bool
	Active     bool
	GroupID    string
	UserRules  []PermissionRule
	GroupRules []PermissionRule
}

// PermissionChange is the before/after of a permission document (audit
// diff, invalidation).
type PermissionChange struct {
	Before, After PermissionDocument
	// AffectedUsers are the users whose effective access may have changed.
	AffectedUsers []string
	At            time.Time
}

// Authorization errors.
var (
	ErrGroupNameTaken     = errors.New("group name taken")
	ErrGroupIsDefault     = errors.New("the default group cannot be deleted")
	ErrGroupNotEmpty      = errors.New("the group still has members")
	ErrPermissionConflict = errors.New("permission document changed")
)

// RuleError lists invalid rules of a submitted permission document.
type RuleError struct {
	// Field is the request member holding the rules (rules, userRules,
	// groupRules, tokenScope).
	Field    string
	Problems []RuleProblem
}

// RuleProblem is one invalid rule.
type RuleProblem struct {
	Index   int
	Message string
}

func (e *RuleError) Error() string { return "invalid permission rules" }
