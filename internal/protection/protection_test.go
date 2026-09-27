package protection

import (
	"errors"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

func code(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return ""
}

// TestCheckMatrix pins what may happen to Docker Manager's own resources (#32):
// start/unpause always; the connected agent never stops, pauses, restarts,
// updates or goes away; the manager and Docker Manager's project restart only
// with a confirmation; volumes, images and networks are never removed or
// mounted elsewhere; nothing unprotected is refused; there is no override.
func TestCheckMatrix(t *testing.T) {
	agent := &protocol.Protection{Role: RoleAgent, Reason: "the agent", Self: true, RestartAllowed: RestartAllowed(RoleAgent, true)}
	manager := &protocol.Protection{Role: RoleManager, Reason: "the manager", Self: true, RestartAllowed: RestartAllowed(RoleManager, true)}
	proxy := &protocol.Protection{Role: RoleProject, Reason: "the proxy", RestartAllowed: RestartAllowed(RoleProject, false)}
	data := &protocol.Protection{Role: RoleManagerData, Reason: "data", RestartAllowed: RestartAllowed(RoleManagerData, false)}
	for _, c := range []struct {
		p         *protocol.Protection
		action    Action
		confirmed bool
		want      string
	}{
		{nil, Remove, false, ""},
		{nil, Restart, false, ""},
		{agent, Start, false, ""},
		{agent, Unpause, false, ""},
		{agent, Stop, false, CodeProtected},
		{agent, Pause, false, CodeProtected},
		{agent, Remove, true, CodeProtected},
		{agent, Update, false, CodeProtected},
		{agent, Restart, true, CodeProtected},
		{manager, Stop, true, CodeProtected},
		{manager, Remove, true, CodeProtected},
		{manager, Restart, false, CodeConfirmationRequired},
		{manager, Restart, true, ""},
		{proxy, Restart, false, CodeConfirmationRequired},
		{proxy, Restart, true, ""},
		{proxy, Stop, false, CodeProtected},
		{data, Remove, true, CodeProtected},
		{data, Mount, false, CodeProtected},
		// Docker Manager redeploys and updates itself; it never takes itself down.
		{&protocol.Protection{Role: RoleProject, Reason: "project"}, Deploy, false, ""},
		{agent, Deploy, false, ""},
		{&protocol.Protection{Role: RoleProject, Reason: "project"}, Down, false, CodeProtected},
	} {
		err := Check(c.p, c.action, c.confirmed)
		if code(err) != c.want {
			t.Errorf("%v %s confirmed=%v: %v, want %q", c.p, c.action, c.confirmed, err, c.want)
		}
		var r *Refusal
		if errors.As(err, &r) && (r.ErrorClass() != c.want || r.Recovery() == "" || r.Error() == "") {
			t.Errorf("refusal %+v", r)
		}
	}
	if RestartAllowed(RoleAgent, true) || !RestartAllowed(RoleAgent, false) || RestartAllowed(RoleStacks, false) {
		t.Fatal("RestartAllowed")
	}
}

// TestExclusion: bulk selections keep unprotected items and report the
// protected ones with their reason.
func TestExclusion(t *testing.T) {
	type item struct {
		name string
		p    *protocol.Protection
	}
	items := []item{{"web", nil}, {"docker-agent", &protocol.Protection{Role: RoleAgent, Reason: "the agent"}}, {"db", nil}}
	kept, excluded := Filter(items, func(i item) *protocol.Protection { return i.p })
	if len(kept) != 2 || kept[0].name != "web" || kept[1].name != "db" || len(excluded) != 1 || excluded[0].Item.name != "docker-agent" ||
		excluded[0].Reason != "the agent" {
		t.Fatalf("kept %+v excluded %+v", kept, excluded)
	}
	if Excluded(nil) || !Excluded(items[1].p) {
		t.Fatal("Excluded")
	}
}
