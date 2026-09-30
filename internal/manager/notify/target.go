package notify

import "strings"

// serverHosted are the services whose address names the server messages
// go to as its host (not a token): the host is shown in lists as the
// channel's target. Every other part of an address, and the host of
// services that keep a webhook ID, bot token or user key there (Discord,
// Slack, Telegram, Pushover, ...), is treated as secret. A generic webhook
// (generic+https://hooks.example.com/...) names the webhook's host.
var serverHosted = map[string]bool{
	"smtp": true, "ntfy": true, "gotify": true, "matrix": true, "generic": true, "mattermost": true,
	"rocketchat": true, "homeassistant": true,
}

// targetOf returns the non-secret target of an address: the server's host
// name for server-hosted services, else "".
func targetOf(address string) string {
	u, service, err := parseAddress(address)
	if err != nil || !serverHosted[service] {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if service == "ntfy" && host == "" {
		host = "ntfy.sh"
	}
	if len(host) > 255 || strings.ContainsAny(host, "@/?#%") {
		return ""
	}
	return host
}
