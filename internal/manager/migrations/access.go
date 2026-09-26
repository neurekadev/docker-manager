package migrations

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/permissions"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Permissions evaluates access changes (*permissions.Service).
type Permissions interface {
	MoveImpact(ctx context.Context, checks []permissions.MoveCheck) ([]permissions.AccessChange, error)
}

// AccessPreview is how a migration changes who may act on the stack or
// volume (#17: stack- and service-scoped rules follow the stack,
// environment rules and exact rules on per-environment resources do not).
type AccessPreview struct {
	// Complete is true when Changes lists every affected user (the caller
	// is the instance owner). Otherwise Changes holds the caller's own
	// change at most and OthersAffected counts the other users.
	Complete       bool
	Changes        []permissions.AccessChange
	OthersAffected int
	// Unavailable explains why the preview could not be computed.
	Unavailable string
}

// stackChecks lists what is evaluated for a stack moving from src to dst:
// every grantable stack capability on the stack, and every container
// capability on each service's containers (they keep their names).
func stackChecks(cat *catalog.Catalog, stackID, src, dst string, project *protocol.MigrationProjectFacts, volumes []VolumePlan) []permissions.MoveCheck {
	stackAt := func(env string) authz.Resource {
		return authz.Resource{Type: catalog.TypeStack, ID: stackID, EnvironmentID: env, Parents: []authz.ResourceRef{}}
	}
	var out []permissions.MoveCheck
	for _, c := range grantable(cat, catalog.TypeStack) {
		out = append(out, permissions.MoveCheck{Capability: c, Before: stackAt(src), After: stackAt(dst)})
	}
	containerCaps := grantable(cat, catalog.TypeContainer)
	for _, s := range project.Services {
		parents := []authz.ResourceRef{{Type: catalog.TypeService, ID: authz.ServiceID(stackID, s.Name)}, {Type: catalog.TypeStack, ID: stackID}}
		for _, name := range s.ContainerNames {
			for _, c := range containerCaps {
				out = append(out, permissions.MoveCheck{Capability: c, Label: c + " (" + s.Name + ")",
					Before: authz.Resource{Type: catalog.TypeContainer, ID: name, EnvironmentID: src, Parents: parents},
					After:  authz.Resource{Type: catalog.TypeContainer, ID: name, EnvironmentID: dst, Parents: parents}})
			}
		}
	}
	volCaps := grantable(cat, catalog.TypeVolume)
	for _, v := range volumes {
		if v.Action != VolumeCopy {
			continue
		}
		parents := []authz.ResourceRef{{Type: catalog.TypeStack, ID: stackID}}
		for _, c := range volCaps {
			out = append(out, permissions.MoveCheck{Capability: c, Label: c + " (" + v.Target + ")",
				Before: authz.Resource{Type: catalog.TypeVolume, ID: v.Source, EnvironmentID: src, Parents: parents},
				After:  authz.Resource{Type: catalog.TypeVolume, ID: v.Target, EnvironmentID: dst, Parents: parents}})
		}
	}
	return out
}

// volumeChecks lists what is evaluated for a standalone volume copied from
// src to dst (the copy is a new volume: nothing follows it).
func volumeChecks(cat *catalog.Catalog, src, dst, source, target string) []permissions.MoveCheck {
	var out []permissions.MoveCheck
	for _, c := range grantable(cat, catalog.TypeVolume) {
		out = append(out, permissions.MoveCheck{Capability: c,
			Before: authz.Resource{Type: catalog.TypeVolume, ID: source, EnvironmentID: src, Parents: []authz.ResourceRef{}},
			After:  authz.Resource{Type: catalog.TypeVolume, ID: target, EnvironmentID: dst, Parents: []authz.ResourceRef{}}})
	}
	return out
}

func grantable(cat *catalog.Catalog, typ string) []string {
	var out []string
	for _, k := range cat.OfType(typ) {
		if cp, ok := cat.Lookup(k); ok && !cp.OwnerOnly {
			out = append(out, k)
		}
	}
	return out
}

// accessPreview computes the preview for caller p: the owner sees every
// affected user, anyone else their own change and a count.
func accessPreview(ctx context.Context, perms Permissions, p authz.Principal, owner bool, checks []permissions.MoveCheck) AccessPreview {
	if perms == nil {
		return AccessPreview{Changes: []permissions.AccessChange{}, Unavailable: "the permission service is not available"}
	}
	all, err := perms.MoveImpact(ctx, checks)
	if err != nil {
		return AccessPreview{Changes: []permissions.AccessChange{}, Unavailable: "the access change could not be computed"}
	}
	if owner {
		if all == nil {
			all = []permissions.AccessChange{}
		}
		return AccessPreview{Complete: true, Changes: all}
	}
	out := AccessPreview{Changes: []permissions.AccessChange{}}
	for _, ch := range all {
		if ch.UserID == p.UserID {
			out.Changes = append(out.Changes, ch)
		} else {
			out.OthersAffected++
		}
	}
	return out
}
