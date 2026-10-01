package live

import (
	"github.com/neurekadev/docker-manager/internal/manager/events"
)

// Topics of the live stream (docs/internal/api/streams.md).
const (
	TopicEnvironments = "environments"
	TopicAgents       = "agents"
	TopicContainers   = "containers"
	TopicImages       = "images"
	TopicVolumes      = "volumes"
	TopicNetworks     = "networks"
	TopicStacks       = "stacks"
	TopicJobs         = "jobs"
	TopicFiles        = "files"
	TopicPolicies     = "policies"
	TopicBackups      = "backups"
	TopicRegistries   = "registries"
	TopicSettings     = "settings"
	TopicPermissions  = "permissions"
	TopicMetrics      = "metrics"
	TopicTemplates    = "templates"
	// TopicManager: the move of this manager to a new server (the owner's
	// move, everyone's move lock).
	TopicManager = "manager"
	// TopicAlerts: alerts (#159) and notifications (finished runs).
	TopicAlerts = "alerts"
)

// Topics returns every topic.
func Topics() []string {
	return []string{TopicEnvironments, TopicAgents, TopicContainers, TopicImages, TopicVolumes, TopicNetworks, TopicStacks,
		TopicJobs, TopicFiles, TopicPolicies, TopicBackups, TopicRegistries, TopicSettings, TopicPermissions, TopicMetrics,
		TopicTemplates, TopicManager, TopicAlerts}
}

// Invalidation kinds of the metrics topic: new stored samples (charts and
// the latest values refetch) and new live values (only the current CPU
// and memory refetch: GET …/metrics/containers, …/capacity, /overview).
const (
	KindMetrics     = "metrics"
	KindLiveMetrics = "live_metrics"
)

// Invalidation actions.
const (
	ActionCreated = "created"
	ActionUpdated = "updated"
	ActionDeleted = "deleted"
)

// topicOfType maps the resource types of events.ResourceChanged (the #17
// catalog types and the audit target types of API mutations).
var topicOfType = map[string]string{
	"environment": TopicEnvironments, "agent": TopicAgents, "agent_enrollment": TopicAgents, "enrollment": TopicAgents,
	"container": TopicContainers, "image": TopicImages, "build_definition": TopicImages, "build": TopicImages,
	"volume": TopicVolumes, "network": TopicNetworks, "stack": TopicStacks, "service": TopicStacks, "migration": TopicStacks,
	"job":           TopicJobs,
	"update_policy": TopicPolicies, "maintenance_policy": TopicPolicies, "backup_policy": TopicPolicies, "schedule": TopicPolicies,
	"schedule_default": TopicPolicies, "maintenance_default": TopicPolicies,
	"backup": TopicBackups, "backup_repository": TopicBackups, "restore": TopicBackups,
	"registry": TopicRegistries, "git_credential": TopicRegistries,
	"settings": TopicSettings, "setting": TopicSettings, "notification_channel": TopicSettings,
	"group": TopicPermissions, "user": TopicPermissions, "invitation": TopicPermissions, "permission": TopicPermissions,
	"api_token": TopicPermissions, "session": TopicPermissions,
	"template": TopicTemplates, "template_registry": TopicTemplates,
	"manager_move": TopicManager,
	"alert":        TopicAlerts,
	"notification": TopicAlerts,
}

// Classify returns the topic and invalidation kind of a bus event ("" when
// the live stream does not relay it).
func Classify(e events.Event) (topic, kind string) {
	switch e.Type {
	case events.EnvironmentCreated, events.EnvironmentUpdated, events.EnvironmentOnline, events.EnvironmentOffline,
		events.EnvironmentArchived, events.EnvironmentReattached:
		return TopicEnvironments, events.ResourceEnvironment
	case events.InventoryUpdated:
		return TopicEnvironments, "inventory"
	case events.AgentEnrolled, events.AgentUpdated, events.AgentRevoked, events.AgentCredentialRotated, events.AgentCapabilitiesUpdate:
		return TopicAgents, events.ResourceAgent
	case events.EnrollmentCreated, events.EnrollmentRevoked, events.EnrollmentUsed, events.EnrollmentRejected:
		return TopicAgents, events.ResourceEnrollment
	case events.DockerEvent:
		switch e.ResourceType {
		case events.ResourceContainer:
			return TopicContainers, e.ResourceType
		case events.ResourceImage:
			return TopicImages, e.ResourceType
		case events.ResourceVolume:
			return TopicVolumes, e.ResourceType
		case events.ResourceNetwork:
			return TopicNetworks, e.ResourceType
		}
		return "", ""
	case events.FilesInvalidated:
		return TopicFiles, events.ResourceFileScope
	case events.MetricsSampled:
		return TopicMetrics, KindMetrics
	case events.MetricsLive:
		return TopicMetrics, KindLiveMetrics
	case events.StackCreated, events.StackUpdated, events.StackRemoved, events.StackRevisionRecorded:
		return TopicStacks, events.ResourceStack
	case events.JobUpdated:
		return TopicJobs, events.ResourceJob
	case events.ManagerMoveUpdated:
		return TopicManager, events.ResourceManagerMove
	case events.ManagerMoveLockChanged:
		return TopicManager, events.ResourceManagerMoveLock
	case events.AlertUpdated:
		return TopicAlerts, events.ResourceAlert
	case events.NotificationCreated:
		return TopicAlerts, events.ResourceNotification
	case events.ResourceChanged:
		if t, ok := topicOfType[e.ResourceType]; ok {
			return t, e.ResourceType
		}
		return TopicSettings, e.ResourceType
	}
	return "", ""
}

// dockerDeleted are Docker actions that remove the object.
var dockerDeleted = map[string]bool{"destroy": true, "remove": true, "delete": true, "prune": true}

// ActionOf returns the invalidation action of a bus event.
func ActionOf(e events.Event) string {
	switch e.Type {
	case events.EnvironmentCreated, events.AgentEnrolled, events.EnrollmentCreated, events.StackCreated, events.NotificationCreated:
		return ActionCreated
	case events.AgentRevoked, events.EnrollmentRevoked, events.StackRemoved:
		return ActionDeleted
	case events.DockerEvent:
		switch a := e.Attributes["action"]; {
		case a == "create" || a == "pull" || a == "tag":
			return ActionCreated
		case dockerDeleted[a]:
			return ActionDeleted
		}
	case events.ResourceChanged:
		switch e.Attributes["op"] {
		case "create":
			return ActionCreated
		case "delete":
			return ActionDeleted
		}
	}
	return ActionUpdated
}
