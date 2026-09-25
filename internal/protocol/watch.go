package protocol

// Scoped file watching (#23, docs/protocol/agent-v1.md, "fs_invalidation
// and rescan"): the manager declares the complete set of file scopes an
// agent watches with the files.watch request (every stack of the
// environment, plus the volumes with open file views); the agent watches
// them with inotify where it can, reconciles the rest by bounded periodic
// scans, and reports changes as fs_invalidation frames. rescan asks for a
// bounded reconciliation of one watched scope.

// Watch modes of a watched scope (also Root.Watch).
const (
	// WatchInotify: kernel notifications (inotify on Linux) for every
	// directory of the scope, plus a slow safety reconciliation.
	WatchInotify = "inotify"
	// WatchPoll: bounded reconciliation scans at least every 60 s
	// (remote or unsupported filesystems, watch limit reached, notifications
	// unavailable).
	WatchPoll = "poll"
	// WatchUnavailable: the scope cannot be watched (see Reason).
	WatchUnavailable = "unavailable"
)

// Reasons of a scope that is polled or unavailable.
const (
	WatchReasonLimit        = "watch_limit"
	WatchReasonRemote       = "remote_filesystem"
	WatchReasonNotify       = "notify_unavailable"
	WatchReasonNotFound     = "not_found"
	WatchReasonUnsupported  = "unsupported_volume"
	WatchReasonForbidden    = "forbidden_path"
	WatchReasonScanTruncate = "scan_truncated"
)

// MaxWatchScopes bounds the scopes of one files.watch request.
const MaxWatchScopes = 4096

// FilesWatchInput replaces the agent's watch set: scopes not listed are no
// longer watched.
type FilesWatchInput struct {
	Scopes []FileScope `json:"scopes"`
}

// Validate checks the input's shape.
func (in FilesWatchInput) Validate() error {
	if len(in.Scopes) > MaxWatchScopes {
		return invalid("files.watch lists %d scopes, max %d", len(in.Scopes), MaxWatchScopes)
	}
	seen := make(map[ScopeRef]bool, len(in.Scopes))
	for _, s := range in.Scopes {
		if err := s.Validate(); err != nil {
			return err
		}
		ref := ScopeRef{Kind: s.Kind, ID: s.ID}
		if seen[ref] {
			return invalid("files.watch lists scope %s:%s twice", s.Kind, s.ID)
		}
		seen[ref] = true
	}
	return nil
}

// WatchStatus is how the agent watches one scope.
type WatchStatus struct {
	Scope ScopeRef `json:"scope"`
	// Mode is inotify, poll or unavailable.
	Mode string `json:"mode"`
	// Watches is the number of kernel watches the scope uses.
	Watches int `json:"watches"`
	// Entries is the size of the scope's last reconciliation scan.
	Entries int `json:"entries"`
	// Reason explains poll and unavailable modes (and truncated scans).
	Reason string `json:"reason,omitempty"`
}

// FilesWatchOutput reports the resulting watch set.
type FilesWatchOutput struct {
	Scopes []WatchStatus `json:"scopes"`
	// WatchLimit is the agent's kernel watch budget; WatchesUsed how much
	// of it the watch set uses.
	WatchLimit  int `json:"watchLimit"`
	WatchesUsed int `json:"watchesUsed"`
}
