package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Authorization persistence (#17): groups, the default group, and the
// group and user permission rules. internal/manager/permissions owns every
// state change; these functions map rows and enforce compare-and-set
// updates on the permission documents' revisions.

type ruleRow struct {
	Subject       string `bun:"subject"`
	Capability    string `bun:"capability,notnull"`
	ScopeKind     string `bun:"scope_kind,notnull"`
	EnvironmentID string `bun:"environment_id,notnull"`
	ResourceType  string `bun:"resource_type,notnull"`
	ResourceID    string `bun:"resource_id,notnull"`
	Effect        string `bun:"effect,notnull"`
	Position      int    `bun:"position,notnull"`
}

func (r ruleRow) toDomain() domain.PermissionRule {
	return domain.PermissionRule{Capability: r.Capability, Effect: domain.PermissionEffect(r.Effect),
		Scope: domain.PermissionScope{Kind: r.ScopeKind, EnvironmentID: r.EnvironmentID, ResourceType: r.ResourceType, ResourceID: r.ResourceID}}
}

// ruleTable describes one of the two rule tables.
type ruleTable struct {
	table, subjectCol, parent string
}

var (
	groupRules = ruleTable{table: "group_permission_rules", subjectCol: "group_id", parent: "groups"}
	userRules  = ruleTable{table: "user_permission_rules", subjectCol: "user_id", parent: "users"}
)

func (t ruleTable) load(ctx context.Context, db bun.IDB, subject string) ([]domain.PermissionRule, error) {
	var rows []ruleRow
	err := db.NewRaw(`SELECT `+t.subjectCol+` AS subject, capability, scope_kind, environment_id, resource_type, resource_id, effect, position
		FROM `+t.table+` WHERE `+t.subjectCol+` = ? ORDER BY position`, subject).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read %s: %w", t.table, err)
	}
	out := make([]domain.PermissionRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

func (t ruleTable) revision(ctx context.Context, db bun.IDB, subject string) (int64, bool, error) {
	var rev int64
	err := db.NewRaw(`SELECT permissions_revision FROM `+t.parent+` WHERE id = ?`, subject).Scan(ctx, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: read %s permissions revision: %w", t.parent, err)
	}
	return rev, true, nil
}

func (t ruleTable) document(ctx context.Context, db bun.IDB, subject string, notFound error) (domain.PermissionDocument, error) {
	rev, ok, err := t.revision(ctx, db, subject)
	if err != nil {
		return domain.PermissionDocument{}, err
	}
	if !ok {
		return domain.PermissionDocument{}, notFound
	}
	rules, err := t.load(ctx, db, subject)
	if err != nil {
		return domain.PermissionDocument{}, err
	}
	return domain.PermissionDocument{SubjectID: subject, Revision: rev, Rules: rules}, nil
}

// replace swaps the subject's rules if its permissions revision is still
// expect, bumping the revision. The caller runs it in a transaction.
func (t ruleTable) replace(ctx context.Context, db bun.IDB, subject string, expect int64, rules []domain.PermissionRule,
	now time.Time, notFound error) (domain.PermissionDocument, error) {
	res, err := db.NewRaw(`UPDATE `+t.parent+` SET permissions_revision = permissions_revision + 1, updated_at = ?
		WHERE id = ? AND permissions_revision = ?`, now.UTC(), subject, expect).Exec(ctx)
	if err != nil {
		return domain.PermissionDocument{}, fmt.Errorf("store: bump %s permissions revision: %w", t.parent, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, ok, err := t.revision(ctx, db, subject); err != nil {
			return domain.PermissionDocument{}, err
		} else if !ok {
			return domain.PermissionDocument{}, notFound
		}
		return domain.PermissionDocument{}, domain.ErrPermissionConflict
	}
	if _, err := db.NewRaw(`DELETE FROM `+t.table+` WHERE `+t.subjectCol+` = ?`, subject).Exec(ctx); err != nil {
		return domain.PermissionDocument{}, fmt.Errorf("store: clear %s: %w", t.table, err)
	}
	for i, r := range rules {
		if _, err := db.NewRaw(`INSERT INTO `+t.table+` (`+t.subjectCol+`, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, subject, r.Capability, r.Scope.Kind, r.Scope.EnvironmentID, r.Scope.ResourceType,
			r.Scope.ResourceID, string(r.Effect), i).Exec(ctx); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return domain.PermissionDocument{}, &domain.RuleError{Problems: []domain.RuleProblem{{Index: i, Message: "duplicates another rule (same capability and scope)"}}}
			}
			return domain.PermissionDocument{}, fmt.Errorf("store: insert %s: %w", t.table, err)
		}
	}
	return t.document(ctx, db, subject, notFound)
}

// GroupPermissions returns a group's rule document.
func GroupPermissions(ctx context.Context, db bun.IDB, groupID string) (domain.PermissionDocument, error) {
	return groupRules.document(ctx, db, groupID, domain.ErrGroupNotFound)
}

// ReplaceGroupPermissions swaps a group's rules (compare-and-set on the
// document revision: domain.ErrPermissionConflict when stale).
func ReplaceGroupPermissions(ctx context.Context, db bun.IDB, groupID string, expect int64, rules []domain.PermissionRule, now time.Time) (domain.PermissionDocument, error) {
	return groupRules.replace(ctx, db, groupID, expect, rules, now, domain.ErrGroupNotFound)
}

// UserPermissions returns a user's override rule document.
func UserPermissions(ctx context.Context, db bun.IDB, userID string) (domain.PermissionDocument, error) {
	return userRules.document(ctx, db, userID, domain.ErrUserNotFound)
}

// ReplaceUserPermissions swaps a user's override rules (compare-and-set).
func ReplaceUserPermissions(ctx context.Context, db bun.IDB, userID string, expect int64, rules []domain.PermissionRule, now time.Time) (domain.PermissionDocument, error) {
	return userRules.replace(ctx, db, userID, expect, rules, now, domain.ErrUserNotFound)
}

// PermissionSubject loads what evaluation needs about a user: account
// state, its override rules and its groups' rules (highest priority
// first). A missing user is returned with Exists false (deny everything),
// not as an error.
func PermissionSubject(ctx context.Context, db bun.IDB, userID string) (domain.PermissionSubject, error) {
	out := domain.PermissionSubject{UserID: userID}
	var row userRow
	err := db.NewSelect().Model(&row).Column("id", "is_owner", "status").Where("id = ?", userID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("store: read user for authorization: %w", err)
	}
	out.Exists, out.Owner, out.Active = true, row.IsOwner == 1, row.Status == string(domain.UserActive)
	if out.UserRules, err = userRules.load(ctx, db, userID); err != nil {
		return out, err
	}
	m, err := userGroups(ctx, db, []string{userID})
	if err != nil {
		return out, err
	}
	if out.Groups, err = GroupRuleSets(ctx, db, m[userID]); err != nil {
		return out, err
	}
	return out, nil
}

// GroupRuleSets returns the named groups with their names and rules, in
// the given order; groups that do not exist are left out.
func GroupRuleSets(ctx context.Context, db bun.IDB, groupIDs []string) ([]domain.GroupRules, error) {
	out := make([]domain.GroupRules, 0, len(groupIDs))
	for _, id := range groupIDs {
		var name string
		err := db.NewRaw(`SELECT name FROM groups WHERE id = ?`, id).Scan(ctx, &name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store: read group: %w", err)
		}
		rules, err := groupRules.load(ctx, db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.GroupRules{GroupID: id, Name: name, Rules: rules})
	}
	return out, nil
}

// SortGroups orders group IDs by the groups' priority, the highest first
// (groups that do not exist are left out).
func SortGroups(ctx context.Context, db bun.IDB, groupIDs []string) ([]string, error) {
	out := []string{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	if err := db.NewRaw(`SELECT id FROM groups WHERE id IN (?) ORDER BY position, id`, bun.List(groupIDs)).Scan(ctx, &out); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: order groups: %w", err)
	}
	return out, nil
}

type groupInfoRow struct {
	ID                  string    `bun:"id"`
	Name                string    `bun:"name"`
	Position            int       `bun:"position"`
	Revision            int64     `bun:"revision"`
	PermissionsRevision int64     `bun:"permissions_revision"`
	CreatedAt           time.Time `bun:"created_at"`
	UpdatedAt           time.Time `bun:"updated_at"`
	IsDefault           int       `bun:"is_default"`
	Members             int       `bun:"members"`
	Rules               int       `bun:"rules"`
	Allows              int       `bun:"allows"`
}

const groupInfoSelect = `SELECT g.id, g.name, g.position, g.revision, g.permissions_revision, g.created_at, g.updated_at,
	(SELECT count(*) FROM default_group d WHERE d.group_id = g.id) AS is_default,
	(SELECT count(*) FROM user_groups m WHERE m.group_id = g.id) AS members,
	(SELECT count(*) FROM group_permission_rules r WHERE r.group_id = g.id) AS rules,
	(SELECT count(*) FROM group_permission_rules r WHERE r.group_id = g.id AND r.effect = 'allow') AS allows
	FROM groups g`

func (r groupInfoRow) toDomain() domain.GroupInfo {
	return domain.GroupInfo{
		Group: domain.Group{ID: r.ID, Name: r.Name, Position: r.Position, Default: r.IsDefault == 1, Revision: r.Revision,
			CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()},
		PermissionsRevision: r.PermissionsRevision, MemberCount: r.Members, RuleCount: r.Rules, AllowCount: r.Allows,
	}
}

// ListGroups returns every group in priority order (the highest first).
func ListGroups(ctx context.Context, db bun.IDB) ([]domain.GroupInfo, error) {
	var rows []groupInfoRow
	if err := db.NewRaw(groupInfoSelect+` ORDER BY g.position, g.id`).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	out := make([]domain.GroupInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetGroupInfo returns one group with its counts.
func GetGroupInfo(ctx context.Context, db bun.IDB, id string) (domain.GroupInfo, error) {
	var rows []groupInfoRow
	if err := db.NewRaw(groupInfoSelect+` WHERE g.id = ?`, id).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.GroupInfo{}, fmt.Errorf("store: read group: %w", err)
	}
	if len(rows) != 1 {
		return domain.GroupInfo{}, domain.ErrGroupNotFound
	}
	return rows[0].toDomain(), nil
}

// CreateGroup inserts a group without rules, last in the order (the
// lowest priority).
func CreateGroup(ctx context.Context, db bun.IDB, id, name string, now time.Time) (domain.GroupInfo, error) {
	var last sql.NullInt64
	if err := db.NewRaw(`SELECT max(position) FROM groups`).Scan(ctx, &last); err != nil {
		return domain.GroupInfo{}, fmt.Errorf("store: read group order: %w", err)
	}
	pos := 0
	if last.Valid {
		pos = int(last.Int64) + 1
	}
	row := groupRow{ID: id, Name: name, Position: pos, Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "groups.name") {
			return domain.GroupInfo{}, domain.ErrGroupNameTaken
		}
		return domain.GroupInfo{}, fmt.Errorf("store: create group: %w", err)
	}
	return GetGroupInfo(ctx, db, id)
}

// RenameGroup renames a group if its revision is still expect.
func RenameGroup(ctx context.Context, db bun.IDB, id string, expect int64, name string, now time.Time) (domain.GroupInfo, error) {
	res, err := db.NewUpdate().Model((*groupRow)(nil)).Set("name = ?", name).Set("revision = revision + 1").
		Set("updated_at = ?", now.UTC()).Where("id = ?", id).Where("revision = ?", expect).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "groups.name") {
			return domain.GroupInfo{}, domain.ErrGroupNameTaken
		}
		return domain.GroupInfo{}, fmt.Errorf("store: rename group: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, err := GetGroupInfo(ctx, db, id); err != nil {
			return domain.GroupInfo{}, err
		}
		return domain.GroupInfo{}, domain.ErrRevisionConflict
	}
	return GetGroupInfo(ctx, db, id)
}

// DeleteGroup deletes a group that is not the default and has no members
// (the database refuses the default and any member), if its revision is
// still expect.
func DeleteGroup(ctx context.Context, db bun.IDB, id string, expect int64) error {
	g, err := GetGroupInfo(ctx, db, id)
	if err != nil {
		return err
	}
	switch {
	case g.Default:
		return domain.ErrGroupIsDefault
	case g.MemberCount > 0:
		return domain.ErrGroupNotEmpty
	case g.Revision != expect:
		return domain.ErrRevisionConflict
	}
	res, err := db.NewDelete().Model((*groupRow)(nil)).Where("id = ?", id).Where("revision = ?", expect).Exec(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return domain.ErrGroupNotEmpty
		}
		return fmt.Errorf("store: delete group: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionConflict
	}
	return nil
}

// GroupOrder returns every group's ID in priority order (the highest
// first).
func GroupOrder(ctx context.Context, db bun.IDB) ([]string, error) {
	out := []string{}
	if err := db.NewRaw(`SELECT id FROM groups ORDER BY position, id`).Scan(ctx, &out); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read group order: %w", err)
	}
	return out, nil
}

// ReorderGroups sets the groups' priority order (the highest first).
// order must name every group exactly once (domain.ErrGroupOrderStale
// otherwise). The caller runs it in a transaction.
func ReorderGroups(ctx context.Context, db bun.IDB, order []string, now time.Time) error {
	all, err := GroupOrder(ctx, db)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range order {
		seen[id] = true
	}
	if len(seen) != len(order) || len(order) != len(all) {
		return domain.ErrGroupOrderStale
	}
	for _, id := range all {
		if !seen[id] {
			return domain.ErrGroupOrderStale
		}
	}
	for i, id := range order {
		if _, err := db.NewRaw(`UPDATE groups SET position = ?, updated_at = ? WHERE id = ? AND position <> ?`, i, now.UTC(), id, i).
			Exec(ctx); err != nil {
			return fmt.Errorf("store: reorder groups: %w", err)
		}
	}
	return nil
}

// SetDefaultGroup makes id the default group for new users.
func SetDefaultGroup(ctx context.Context, db bun.IDB, id string) error {
	if _, err := GetGroupInfo(ctx, db, id); err != nil {
		return err
	}
	if _, err := db.NewUpdate().Model((*defaultGroupRow)(nil)).Set("group_id = ?", id).Where("singleton = 1").Exec(ctx); err != nil {
		return fmt.Errorf("store: set default group: %w", err)
	}
	return nil
}

// GroupMembers returns the IDs of a group's users.
func GroupMembers(ctx context.Context, db bun.IDB, groupID string) ([]string, error) {
	var ids []string
	if err := db.NewRaw(`SELECT user_id FROM user_groups WHERE group_id = ? ORDER BY user_id`, groupID).Scan(ctx, &ids); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list group members: %w", err)
	}
	return ids, nil
}

// ResourceRuleSubjects returns the groups and users with rules on one
// resource (ForgetResource invalidation).
func ResourceRuleSubjects(ctx context.Context, db bun.IDB, resourceType, environmentID, resourceID string) (groups, users []string, err error) {
	q := func(t ruleTable) ([]string, error) {
		var ids []string
		err := db.NewRaw(`SELECT DISTINCT `+t.subjectCol+` FROM `+t.table+` WHERE scope_kind = 'resource'
			AND resource_type = ? AND environment_id = ? AND resource_id = ?`, resourceType, environmentID, resourceID).Scan(ctx, &ids)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: read %s: %w", t.table, err)
		}
		return ids, nil
	}
	if groups, err = q(groupRules); err != nil {
		return nil, nil, err
	}
	users, err = q(userRules)
	return groups, users, err
}

// DeleteResourceRules removes every group and user rule on one resource,
// bumping the affected documents' revisions. It returns the number of
// rules removed.
func DeleteResourceRules(ctx context.Context, db bun.IDB, resourceType, environmentID, resourceID string, now time.Time) (int, error) {
	total := 0
	for _, t := range []ruleTable{groupRules, userRules} {
		if _, err := db.NewRaw(`UPDATE `+t.parent+` SET permissions_revision = permissions_revision + 1, updated_at = ?
			WHERE id IN (SELECT `+t.subjectCol+` FROM `+t.table+` WHERE scope_kind = 'resource' AND resource_type = ?
			AND environment_id = ? AND resource_id = ?)`, now.UTC(), resourceType, environmentID, resourceID).Exec(ctx); err != nil {
			return 0, fmt.Errorf("store: bump %s revisions: %w", t.parent, err)
		}
		res, err := db.NewRaw(`DELETE FROM `+t.table+` WHERE scope_kind = 'resource' AND resource_type = ? AND environment_id = ?
			AND resource_id = ?`, resourceType, environmentID, resourceID).Exec(ctx)
		if err != nil {
			return 0, fmt.Errorf("store: delete %s: %w", t.table, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// envRuleWhere selects the rules scoped to one environment (#34): its
// environment-scoped rules and the rules on resources named per
// environment (containers, images, volumes, networks) in it. Rules on
// resources with their own Docker Manager IDs (stacks, policies) are kept with
// those records.
const envRuleWhere = `environment_id = ? AND environment_id <> '' AND scope_kind IN ('environment', 'resource')`

// EnvironmentRules lists the group and user rules scoped to an
// environment, groups first, each in document order.
func EnvironmentRules(ctx context.Context, db bun.IDB, environmentID string) ([]domain.RemovedPermissionRule, error) {
	var out []domain.RemovedPermissionRule
	for _, t := range []struct {
		table ruleTable
		kind  string
	}{{groupRules, "group"}, {userRules, "user"}} {
		var rows []ruleRow
		err := db.NewRaw(`SELECT `+t.table.subjectCol+` AS subject, capability, scope_kind, environment_id, resource_type, resource_id,
			effect, position FROM `+t.table.table+` WHERE `+envRuleWhere+` ORDER BY `+t.table.subjectCol+`, position`, environmentID).
			Scan(ctx, &rows)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: read %s: %w", t.table.table, err)
		}
		for _, r := range rows {
			out = append(out, domain.RemovedPermissionRule{SubjectKind: t.kind, SubjectID: r.Subject, Rule: r.toDomain()})
		}
	}
	return out, nil
}

// DeleteEnvironmentRules removes the group and user rules scoped to an
// environment (EnvironmentRules), bumping the affected documents'
// revisions. It returns the number of rules removed.
func DeleteEnvironmentRules(ctx context.Context, db bun.IDB, environmentID string, now time.Time) (int, error) {
	total := 0
	for _, t := range []ruleTable{groupRules, userRules} {
		if _, err := db.NewRaw(`UPDATE `+t.parent+` SET permissions_revision = permissions_revision + 1, updated_at = ?
			WHERE id IN (SELECT `+t.subjectCol+` FROM `+t.table+` WHERE `+envRuleWhere+`)`, now.UTC(), environmentID).Exec(ctx); err != nil {
			return 0, fmt.Errorf("store: bump %s revisions: %w", t.parent, err)
		}
		res, err := db.NewRaw(`DELETE FROM `+t.table+` WHERE `+envRuleWhere, environmentID).Exec(ctx)
		if err != nil {
			return 0, fmt.Errorf("store: delete %s: %w", t.table, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
