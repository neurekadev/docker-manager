package authz

import (
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/events"
)

// Event filtering (#17, #23): which bus events a subscriber may receive.
// Events carry identifiers and status, i.e. minimal-view fields, so an
// event is delivered when its resource is visible to the subscriber at
// least minimally; file invalidations need the root's file read capability
// because they name paths. Every bus event type must have a rule here
// (TestEveryEventTypeHasAVisibilityRule): a new event type without one is
// delivered to the owner only.

// eventRule decides visibility of one event type.
type eventRule func(c Checker, e events.Event) bool

func envVisible(c Checker, e events.Event) bool {
	id := e.EnvironmentID
	if e.ResourceType == events.ResourceEnvironment && e.ResourceID != "" {
		id = e.ResourceID
	}
	return id != "" && ViewOf(c, EnvironmentResource(id)).Visible()
}

func agentVisible(c Checker, e events.Event) bool {
	return ViewOf(c, Resource{Type: catalog.TypeAgent, ID: e.ResourceID, EnvironmentID: e.EnvironmentID, Parents: []ResourceRef{}}).Visible()
}

func enrollmentVisible(c Checker, _ events.Event) bool {
	return c.Can("agent.enroll", Instance()).Allowed
}

func dockerVisible(c Checker, e events.Event) bool {
	switch e.ResourceType {
	case events.ResourceContainer, events.ResourceImage, events.ResourceVolume, events.ResourceNetwork:
		return ViewOf(c, Resource{Type: e.ResourceType, ID: e.ResourceID, EnvironmentID: e.EnvironmentID}).Visible()
	}
	return false
}

// filesVisible: a file-scope invalidation names paths, so it needs the
// root's file read capability. Whole-environment overflow invalidations
// (ResourceID "*", no paths) only say "something changed" and follow the
// environment's visibility.
func filesVisible(c Checker, e events.Event) bool {
	if e.ResourceID == "*" {
		return len(e.Paths) == 0 && envVisible(c, e)
	}
	switch e.Attributes["scopeKind"] {
	case catalog.TypeStack:
		return c.Can("stack.files.read", Resource{Type: catalog.TypeStack, ID: e.Attributes["scopeId"], EnvironmentID: e.EnvironmentID}).Allowed
	case catalog.TypeVolume:
		return c.Can("volume.files.read", Resource{Type: catalog.TypeVolume, ID: e.Attributes["scopeId"], EnvironmentID: e.EnvironmentID}).Allowed
	case catalog.TypeTemplate:
		return c.Can("template.files.read", Resource{Type: catalog.TypeTemplate, ID: e.Attributes["scopeId"], Parents: []ResourceRef{}}).Allowed
	}
	return c.Can("stack.files.read", Instance()).Allowed && c.Can("volume.files.read", Instance()).Allowed
}

// metricsVisible: new host samples need the environment's host stats
// capability, container samples container.metrics.read on at least one of
// the sampled containers (#5). The event carries no values.
func metricsVisible(c Checker, e events.Event) bool {
	if e.EnvironmentID == "" {
		return false
	}
	if e.Attributes["host"] == "true" && c.Can("environment.metrics.read", EnvironmentResource(e.EnvironmentID)).Allowed {
		return true
	}
	return ContainerMetricsVisible(c, e) != ""
}

// ContainerMetricsVisible returns the first container of a metrics.sampled
// or metrics.live event whose metrics c may read ("" for none).
func ContainerMetricsVisible(c Checker, e events.Event) string {
	for _, name := range e.Members {
		if c.Can("container.metrics.read", Resource{Type: catalog.TypeContainer, ID: name, EnvironmentID: e.EnvironmentID}).Allowed {
			return name
		}
	}
	return ""
}

// inventoryVisible: the Engine inventory and the disk health report
// (inventory.updated with the attribute health, #143) are system
// information and host capacity.
func inventoryVisible(c Checker, e events.Event) bool {
	env := EnvironmentResource(e.EnvironmentID)
	return e.EnvironmentID != "" && (c.Can("environment.system.read", env).Allowed || c.Can("environment.metrics.read", env).Allowed)
}

// stackVisible: stack events carry identity and status (minimal fields),
// so they reach everyone who sees the stack at least minimally.
func stackVisible(c Checker, e events.Event) bool {
	return ViewOf(c, Resource{Type: catalog.TypeStack, ID: e.ResourceID, EnvironmentID: e.EnvironmentID, Parents: []ResourceRef{}}).Visible()
}

// jobVisible: job.read on the job's targets, or the kind's own capability
// on every target (#17, the jobs API rule).
func jobVisible(c Checker, e events.Event) bool {
	return e.Job != nil && c.Can("job.read", JobResource(*e.Job)).Allowed
}

// changedVisible: a mutation of a catalog resource reaches everyone who
// sees the resource at least minimally; settings reach settings.read
// holders; anything else (users, groups, invitations, API tokens of other
// users) only the owner.
func changedVisible(c Checker, e events.Event) bool {
	switch e.ResourceType {
	case catalog.TypeSettings:
		return c.Can("settings.read", Instance()).Allowed
	case catalog.TypeEnvironment:
		return ViewOf(c, EnvironmentResource(e.ResourceID)).Visible()
	case catalog.TypeInstance, catalog.TypeAdministration, catalog.TypeAPIToken, catalog.TypeAudit:
		return c.Can("groups.manage", Instance()).Allowed
	case "template_registry":
		// Registries' cached templates are browsed with template.read.
		return c.Can("template.read", Instance()).Allowed
	case "notification_channel":
		// Channels are the owner's (notification_channel.manage).
		return c.Can("notification_channel.manage", Instance()).Allowed
	}
	if _, ok := catalog.Default().Type(e.ResourceType); !ok {
		return c.Can("groups.manage", Instance()).Allowed
	}
	return ViewOf(c, Resource{Type: e.ResourceType, ID: e.ResourceID, EnvironmentID: e.EnvironmentID}).Visible()
}

// managerMoveVisible: the move of the manager (its state, the new server,
// the progress) is the owner's (the manager.move capability).
func managerMoveVisible(c Checker, _ events.Event) bool {
	return c.Can("manager.move", Instance()).Allowed
}

// moveLockVisible: the move lock (read-only while the manager moves) is
// what every signed-in user reads in GET /auth/session; the event names
// no move.
func moveLockVisible(Checker, events.Event) bool { return true }

var eventRules = map[string]eventRule{
	events.EnvironmentCreated:      envVisible,
	events.EnvironmentUpdated:      envVisible,
	events.EnvironmentOnline:       envVisible,
	events.EnvironmentOffline:      envVisible,
	events.EnvironmentArchived:     envVisible,
	events.EnvironmentReattached:   envVisible,
	events.EnvironmentResync:       envVisible,
	events.AgentEnrolled:           agentVisible,
	events.AgentUpdated:            agentVisible,
	events.AgentRevoked:            agentVisible,
	events.AgentCredentialRotated:  agentVisible,
	events.AgentCapabilitiesUpdate: agentVisible,
	events.EnrollmentCreated:       enrollmentVisible,
	events.EnrollmentRevoked:       enrollmentVisible,
	events.EnrollmentUsed:          enrollmentVisible,
	events.EnrollmentRejected:      enrollmentVisible,
	events.DockerEvent:             dockerVisible,
	events.FilesInvalidated:        filesVisible,
	events.MetricsSampled:          metricsVisible,
	events.MetricsLive:             metricsVisible,
	events.InventoryUpdated:        inventoryVisible,
	events.StackCreated:            stackVisible,
	events.StackUpdated:            stackVisible,
	events.StackRemoved:            stackVisible,
	events.StackRevisionRecorded:   stackVisible,
	events.JobUpdated:              jobVisible,
	events.ResourceChanged:         changedVisible,
	events.ManagerMoveUpdated:      managerMoveVisible,
	events.ManagerMoveLockChanged:  moveLockVisible,
}

// HasEventRule reports whether an event type has a visibility rule.
func HasEventRule(eventType string) bool {
	_, ok := eventRules[eventType]
	return ok
}

// EventVisible reports whether the subscriber checked by c may receive e.
// Unknown event types reach only principals holding every capability
// (the owner).
func EventVisible(c Checker, e events.Event) bool {
	if r, ok := eventRules[e.Type]; ok {
		return r(c, e)
	}
	return c.Can("groups.manage", Instance()).Allowed
}
