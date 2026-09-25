package permissions

import (
	"context"
	"slices"

	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// MoveCheck is one capability evaluated on a resource before and after it
// moves to another environment (environment migration, #35).
type MoveCheck struct {
	Capability string
	// Before and After are the resource at its source and at its
	// destination; set Parents (possibly empty) so the Locator is not
	// consulted for the position the resource does not have yet.
	Before, After authz.Resource
	// Label names the check in results (e.g. "container.logs.read (web)");
	// empty uses Capability.
	Label string
}

// AccessChange is how a move changes one user's access.
type AccessChange struct {
	UserID   string
	Username string
	// Gained and Lost are the labels of the checks whose decision changes.
	Gained []string
	Lost   []string
}

// MoveImpact evaluates checks for every active user and returns those whose
// decisions differ between Before and After, ordered by user ID. The owner
// bypasses every rule and never changes. Resource-scoped rules on a stack
// or service follow it (its ID does not change); environment rules and
// exact rules on per-environment resources (containers, volumes) do not,
// which is what the result shows. API tokens are bounded by their user's
// permissions and change with them. It changes nothing; callers decide who
// may see the result.
func (s *Service) MoveImpact(ctx context.Context, checks []MoveCheck) ([]AccessChange, error) {
	var out []AccessChange
	after := ""
	for {
		users, err := store.ListUsers(ctx, s.db, after, 200)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			ps, err := store.PermissionSubject(ctx, s.db, u.ID)
			if err != nil {
				return nil, err
			}
			if !ps.Exists || !ps.Active || ps.Owner {
				continue
			}
			c := s.checker(ctx, subjectFrom(ps))
			var ch AccessChange
			for _, mc := range checks {
				label := mc.Label
				if label == "" {
					label = mc.Capability
				}
				before := c.Decide(mc.Capability, mc.Before).Allowed
				now := c.Decide(mc.Capability, mc.After).Allowed
				switch {
				case now && !before && !slices.Contains(ch.Gained, label):
					ch.Gained = append(ch.Gained, label)
				case before && !now && !slices.Contains(ch.Lost, label):
					ch.Lost = append(ch.Lost, label)
				}
			}
			if len(ch.Gained)+len(ch.Lost) > 0 {
				ch.UserID, ch.Username = u.ID, u.Username
				out = append(out, ch)
			}
		}
		if len(users) < 200 {
			return out, nil
		}
		after = users[len(users)-1].ID
	}
}
