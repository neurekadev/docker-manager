package protocol

import "net/url"

// TransportInfo describes how the agent reaches the manager. The agent
// reports it as the "transport" field of its capabilities
// (CapabilitiesPayload) so the manager can flag insecure connections on the
// host page (#27). internal/agent/transport builds it.
type TransportInfo struct {
	// ManagerURL is the origin the agent dials: the public HTTPS origin, or
	// the internal URL on the manager's Docker network.
	ManagerURL string `json:"managerUrl"`
	// PlainHTTP is true when the agent uses an http:// manager URL
	// (DOCKER_AGENT_MANAGER_ALLOW_HTTP=true). The host page shows it as a warning.
	PlainHTTP bool `json:"plainHttp"`
	// CustomCA is true when DOCKER_AGENT_MANAGER_CA_FILE adds private CA roots.
	CustomCA bool `json:"customCa"`
}

// Flagged reports whether the host page must warn about this transport.
func (t TransportInfo) Flagged() bool { return t.PlainHTTP }

// Validate checks that ManagerURL is an http(s) origin and that PlainHTTP
// matches its scheme (an agent cannot hide a plain-HTTP connection).
func (t TransportInfo) Validate() error {
	u, err := url.Parse(t.ManagerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return invalid("transport.managerUrl %q is not an http(s) origin", t.ManagerURL)
	}
	if t.PlainHTTP != (u.Scheme == "http") {
		return invalid("transport.plainHttp must be true exactly for http:// manager URLs")
	}
	return nil
}
