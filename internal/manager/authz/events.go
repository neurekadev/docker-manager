package authz

import (
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/events"
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
	}
	return c.Can("stack.files.read", Instance()).Allowed && c.Can("volume.files.read", Instance()).Allowed
}

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
