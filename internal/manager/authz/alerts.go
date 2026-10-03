package authz

import (
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
)

// Alerts (#159) have no read capability of their own: an alert is shown
// to whoever may see its source, and dismissed with alert.dismiss scoped
// like that source.
//
//   - disk_health, raid: environment.system.read on the environment (the
//     System tab's disk and RAID sections);
//   - temperature, disk_space, memory: environment.metrics.read on the
//     environment (its host stats);
//   - environment_offline: the environment visible at all;
//   - job_failed: job.read on the failed job (its targets, or the kind's
//     own capabilities on every target);
//   - updates: update_policy.read on the update policy.
//
// Notifications (finished runs) are shown to whoever may read their job.

// CapAlertDismiss dismisses an alert for everyone.
const CapAlertDismiss = "alert.dismiss"

// alertJob rebuilds the job resource of a job alert (its kind and targets;
// the input is not kept).
func alertJob(a domain.Alert) Resource {
	return JobResource(domain.Job{ID: a.ResourceID, Kind: a.JobKind, EnvironmentID: a.EnvironmentID, Targets: a.Targets})
}

// alertPolicy is the update policy resource of an update alert (below its
// stack or container, like updates.Resource).
func alertPolicy(a domain.Alert) Resource {
	parents := []ResourceRef{}
	for _, t := range a.Targets {
		switch t.Type {
		case domain.TargetStack:
			parents = append(parents, ResourceRef{Type: catalog.TypeStack, ID: t.ID})
		case domain.TargetContainer:
			parents = append(parents, ResourceRef{Type: catalog.TypeContainer, ID: t.ID, EnvironmentID: a.EnvironmentID})
		}
	}
	return Resource{Type: catalog.TypeUpdatePolicy, ID: a.ResourceID, EnvironmentID: a.EnvironmentID, Parents: parents}
}

// AlertVisible reports whether c may see alert a.
func AlertVisible(c Checker, a domain.Alert) bool {
	switch a.Kind {
	case domain.NotifyDiskHealth, domain.NotifyRAID:
		return a.EnvironmentID != "" && c.Can("environment.system.read", EnvironmentResource(a.EnvironmentID)).Allowed
	case domain.NotifyTemperature, domain.NotifyDiskSpace, domain.NotifyMemory:
		return a.EnvironmentID != "" && c.Can("environment.metrics.read", EnvironmentResource(a.EnvironmentID)).Allowed
	case domain.NotifyEnvironmentOffline:
		return a.EnvironmentID != "" && ViewOf(c, EnvironmentResource(a.EnvironmentID)).Visible()
	case domain.NotifyJobFailed:
		return c.Can(CapJobRead, alertJob(a)).Allowed
	case domain.NotifyUpdates:
		return c.Can("update_policy.read", alertPolicy(a)).Allowed
	case domain.NotifyBackup:
		return c.Can("backup_policy.read", Instance()).Allowed
	}
	return c.Can("groups.manage", Instance()).Allowed
}

// AlertDismissResources are the resources alert.dismiss must be granted
// on to dismiss a: the environment (disks, RAID, offline), every target
// of the failed job, or the update policy.
func AlertDismissResources(a domain.Alert) []Resource {
	switch a.Kind {
	case domain.NotifyDiskHealth, domain.NotifyRAID, domain.NotifyTemperature, domain.NotifyDiskSpace, domain.NotifyMemory,
		domain.NotifyEnvironmentOffline:
		if a.EnvironmentID == "" {
			return []Resource{Instance()}
		}
		return []Resource{EnvironmentResource(a.EnvironmentID)}
	case domain.NotifyJobFailed:
		return alertJob(a).Targets
	case domain.NotifyUpdates:
		return []Resource{alertPolicy(a)}
	}
	return []Resource{Instance()}
}

// AlertDismissible reports whether c may see and dismiss alert a.
func AlertDismissible(c Checker, a domain.Alert) bool {
	if !AlertVisible(c, a) {
		return false
	}
	for _, r := range AlertDismissResources(a) {
		if !c.Can(CapAlertDismiss, r).Allowed {
			return false
		}
	}
	return true
}

// NotificationVisible reports whether c may see notification n: job.read
// on its run's job. The manager's own backup (no environment, no
// targets) is the owner's.
func NotificationVisible(c Checker, n domain.Notification) bool {
	return c.Can(CapJobRead, JobResource(domain.Job{ID: n.JobID, Kind: n.JobKind, EnvironmentID: n.EnvironmentID,
		Targets: n.Targets})).Allowed
}
