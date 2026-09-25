package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Session payloads, allowed request/stream names, limits and the agent
// version window. docs/protocol/agent-v1.md is the normative description;
// keep both in sync (TestProtocolDocListsNames).

// Session timing and limits the manager announces in welcome.
const (
	// HelloTimeout: the agent must send hello this soon after the upgrade.
	HelloTimeout = 10 * time.Second
	// HeartbeatInterval: each side sends a heartbeat at least this often.
	HeartbeatInterval = 15 * time.Second
	// HeartbeatTimeout: a side that heard nothing for this long closes the
	// session with CloseHeartbeatTimeout.
	HeartbeatTimeout = 45 * time.Second
	// MaxStreams is the number of concurrently open streams per session.
	MaxStreams = 32
	// StreamWindow is the initial per-stream credit in bytes.
	StreamWindow = 1 << 20
	// MaxChunk bounds the decoded bytes of one stream_data frame, keeping
	// the base64 frame well below MaxFrameSize.
	MaxChunk = 256 << 10
	// MaxPaths bounds the paths of one fs_invalidation frame; more changes
	// set Overflow and the manager rescans.
	MaxPaths = 256
)

// Request names: bounded operations that are not durable jobs. The agent
// rejects any other name with ErrUnsupportedRequest. Mutating requests are
// never re-sent automatically after a disconnect.
const (
	ReqEngineInfo              = "engine.info"
	ReqEngineDiskUsage         = "engine.disk_usage"
	ReqHostMetrics             = "host.metrics"
	ReqContainerList           = "container.list"
	ReqContainerInspect        = "container.inspect"
	ReqContainerStats          = "container.stats"
	ReqContainerLogs           = "container.logs"
	ReqContainerExecCreate     = "container.exec.create"
	ReqContainerExecResize     = "container.exec.resize"
	ReqContainerExecDelete     = "container.exec.delete"
	ReqImageList               = "image.list"
	ReqImageInspect            = "image.inspect"
	ReqImageTag                = "image.tag"
	ReqVolumeList              = "volume.list"
	ReqVolumeInspect           = "volume.inspect"
	ReqNetworkList             = "network.list"
	ReqNetworkInspect          = "network.inspect"
	ReqComposeDiscover         = "compose.discover"
	ReqComposeValidate         = "compose.validate"
	ReqComposeRead             = "compose.read"
	ReqComposeWrite            = "compose.write"
	ReqComposeServices         = "compose.services"
	ReqFilesList               = "files.list"
	ReqFilesStat               = "files.stat"
	ReqFilesRead               = "files.read"
	ReqFilesWrite              = "files.write"
	ReqFilesMkdir              = "files.mkdir"
	ReqFilesConflictPreview    = "files.conflict_preview"
	ReqFilesWatch              = "files.watch" // the watched file scopes (#23)
	ReqBackupSnapshots         = "backup.snapshots"
	ReqBackupContents          = "backup.contents"
	ReqBackupScopePreview      = "backup.scope_preview"
	ReqRestorePreview          = "restore.preview"
	ReqMaintenancePreview      = "maintenance.preview"
	ReqMigrationPreview        = "migration.preview"
	ReqImageLocalDigests       = "image.local_digests"
	ReqAgentCredentialRotate   = "agent.credential.rotate" //nolint:gosec // G101: a request name, not a credential
	ReqAgentDiagnostics        = "agent.diagnostics"
	ReqEngineCompatibilityInfo = "engine.compatibility"
	// ReqManagerIdentity tells the agent which manager instance it serves and
	// the ID of the manager's own container, so the agent recognizes a
	// co-located manager (#32).
	ReqManagerIdentity = "manager.identity"
)

// Job-linked migration requests (#35): the manager's stack.migrate and
// volume.migrate jobs stop and restart the source stack and commit or clean
// up what they wrote on the destination (docs/architecture/migrations.md).
const (
	ReqMigrationStop    = "migration.stop"
	ReqMigrationStart   = "migration.start"
	ReqMigrationCommit  = "migration.commit"
	ReqMigrationCleanup = "migration.cleanup"
)

// Stream kinds opened with stream_open.
const (
	StreamContainerLogs    = "container.logs"
	StreamContainerStats   = "container.stats"
	StreamContainerExec    = "container.exec"
	StreamFilesDownload    = "files.download"
	StreamFilesUpload      = "files.upload"
	StreamBackupFile       = "backup.file"
	StreamMigrationSend    = "migration.send"
	StreamMigrationReceive = "migration.receive"
)

// Stream directions: who sends stream_data.
const (
	DirAgentToManager = "agent_to_manager"
	DirManagerToAgent = "manager_to_agent"
	DirBoth           = "both"
)

var requestNames = []string{
	ReqEngineInfo, ReqEngineDiskUsage, ReqHostMetrics, ReqContainerList, ReqContainerInspect,
	ReqContainerStats, ReqContainerLogs, ReqContainerExecCreate, ReqContainerExecResize,
	ReqContainerExecDelete, ReqImageList, ReqImageInspect, ReqImageTag, ReqVolumeList,
	ReqVolumeInspect, ReqNetworkList, ReqNetworkInspect, ReqComposeDiscover, ReqComposeValidate,
	ReqComposeRead, ReqComposeWrite, ReqComposeServices,
	ReqFilesList, ReqFilesStat, ReqFilesRead, ReqFilesWrite, ReqFilesMkdir, ReqFilesConflictPreview, ReqFilesWatch,
	ReqBackupSnapshots, ReqBackupContents, ReqBackupScopePreview, ReqRestorePreview,
	ReqMaintenancePreview, ReqMigrationPreview, ReqMigrationStop, ReqMigrationStart, ReqMigrationCommit,
	ReqMigrationCleanup, ReqImageLocalDigests, ReqAgentCredentialRotate,
	ReqAgentDiagnostics, ReqEngineCompatibilityInfo, ReqManagerIdentity,
}

// mutatingRequests change state on the agent or Engine.
var mutatingRequests = []string{
	ReqContainerExecCreate, ReqContainerExecResize, ReqContainerExecDelete, ReqImageTag,
	ReqFilesWrite, ReqFilesMkdir, ReqAgentCredentialRotate, ReqComposeWrite, ReqManagerIdentity,
	ReqMigrationStop, ReqMigrationStart, ReqMigrationCommit, ReqMigrationCleanup,
}

var streamKinds = map[string]string{
	StreamContainerLogs: DirAgentToManager, StreamContainerStats: DirAgentToManager,
	StreamContainerExec: DirBoth, StreamFilesDownload: DirAgentToManager,
	StreamFilesUpload: DirManagerToAgent, StreamBackupFile: DirAgentToManager,
	StreamMigrationSend: DirAgentToManager, StreamMigrationReceive: DirManagerToAgent,
}

// RequestNames returns every allowed request name.
func RequestNames() []string { return slices.Clone(requestNames) }

// IsMutatingRequest reports whether a request changes state.
func IsMutatingRequest(name string) bool { return slices.Contains(mutatingRequests, name) }

// StreamKinds returns every allowed stream kind, sorted.
func StreamKinds() []string {
	out := make([]string, 0, len(streamKinds))
	for k := range streamKinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// StreamDirection returns the fixed direction of a stream kind.
func StreamDirection(kind string) (string, bool) {
	d, ok := streamKinds[kind]
	return d, ok
}

// Error codes carried by error frames (ErrorPayload.Code).
const (
	CodeInvalidFrame       = "invalid_frame"
	CodeUnsupportedRequest = "unsupported_request"
	CodeUnsupportedStream  = "unsupported_stream"
	CodeVersionUnsupported = "version_unsupported"
	CodeUnauthorized       = "unauthorized"
	CodeForbiddenPath      = "forbidden_path"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeDeadlineExceeded   = "deadline_exceeded"
	CodeBusy               = "busy"
	CodeStreamLimit        = "stream_limit"
	CodeTooLarge           = "too_large"
	CodeEngineUnavailable  = "engine_unavailable"
	CodeEngineError        = "engine_error"
	// CodeInvalidArgument: the Engine rejected the input (#6).
	CodeInvalidArgument = "invalid_argument"
	// CodeUnsupportedAPIVersion: the Engine's API version is too old for
	// the operation (#6, #21).
	CodeUnsupportedAPIVersion = "unsupported_api_version"
	CodeCancelled             = "cancelled"
	CodeInternal              = "internal"
	// File scope codes (#15): the manager maps them to specific public
	// errors (409 file_exists, 409 file_type_mismatch, ...).
	CodeAlreadyExists     = "already_exists"
	CodeNotDirectory      = "not_directory"
	CodeIsDirectory       = "is_directory"
	CodeUnsupportedFile   = "unsupported_file"
	CodeUnsupportedVolume = "unsupported_volume"
	CodeDigestMismatch    = "digest_mismatch"
	// Backup codes (#10): the restic error classes of internal/restic and
	// the agent's own refusals of a repository location.
	CodeRepositoryNotFound     = "repository_not_found"
	CodeRecoveryKeyRejected    = "recovery_key_rejected"
	CodeRepositoryLocked       = "repository_locked"
	CodeRepositoryDamaged      = "repository_damaged"
	CodeStorageAccessDenied    = "storage_access_denied"
	CodeStorageUnreachable     = "storage_unreachable"
	CodeSnapshotNotFound       = "snapshot_not_found"
	CodeResticUnavailable      = "restic_unavailable"
	CodeResticFailed           = "restic_failed"
	CodePathNotAllowed         = "path_not_allowed"
	CodeRepositoryInsideSource = "repository_inside_source"
)

var errorCodes = []string{
	CodeInvalidFrame, CodeUnsupportedRequest, CodeUnsupportedStream, CodeVersionUnsupported,
	CodeUnauthorized, CodeForbiddenPath, CodeNotFound, CodeConflict, CodeDeadlineExceeded,
	CodeBusy, CodeStreamLimit, CodeTooLarge, CodeEngineUnavailable, CodeEngineError,
	CodeInvalidArgument, CodeUnsupportedAPIVersion, CodeCancelled, CodeInternal,
	CodeAlreadyExists, CodeNotDirectory, CodeIsDirectory, CodeUnsupportedFile, CodeUnsupportedVolume, CodeDigestMismatch,
	CodeRepositoryNotFound, CodeRecoveryKeyRejected, CodeRepositoryLocked, CodeRepositoryDamaged, CodeStorageAccessDenied,
	CodeStorageUnreachable, CodeSnapshotNotFound, CodeResticUnavailable, CodeResticFailed, CodePathNotAllowed,
	CodeRepositoryInsideSource,
}

// ErrorCodes returns every error frame code.
func ErrorCodes() []string { return slices.Clone(errorCodes) }

// HelloPayload is the agent's first frame after the upgrade.
type HelloPayload struct {
	// Protocol must equal Version.
	Protocol     string `json:"protocol"`
	AgentID      string `json:"agentId"`
	AgentVersion string `json:"agentVersion"`
	// InstallID is generated once per agent state volume.
	InstallID string `json:"installId"`
	// EngineID is the Docker Engine ID the agent controls.
	EngineID string `json:"engineId"`
	// PreviousSessionID is the session this agent had before reconnecting.
	PreviousSessionID string `json:"previousSessionId,omitempty"`
}

// SessionLimits are announced by the manager in welcome.
type SessionLimits struct {
	MaxFrameBytes     int `json:"maxFrameBytes"`
	MaxStreams        int `json:"maxStreams"`
	StreamWindowBytes int `json:"streamWindowBytes"`
	MaxChunkBytes     int `json:"maxChunkBytes"`
	MaxPaths          int `json:"maxPaths"`
}

// DefaultLimits returns the v1 session limits.
func DefaultLimits() SessionLimits {
	return SessionLimits{MaxFrameBytes: MaxFrameSize, MaxStreams: MaxStreams, StreamWindowBytes: StreamWindow,
		MaxChunkBytes: MaxChunk, MaxPaths: MaxPaths}
}

// Agent version status in welcome (current, outdated). The public API
// also reports VersionUnsupported for a stored agent version the manager
// would refuse now (AgentCompatibility), e.g. after a manager upgrade.
const (
	VersionCurrent     = "current"
	VersionOutdated    = "outdated"
	VersionUnsupported = "unsupported"
)

// WelcomePayload establishes the session.
type WelcomePayload struct {
	SessionID      string `json:"sessionId"`
	ManagerVersion string `json:"managerVersion"`
	EnvironmentID  string `json:"environmentId"`
	// AgentStatus is current or outdated (inside the N-1 window).
	AgentStatus         string        `json:"agentStatus"`
	HeartbeatIntervalMs int64         `json:"heartbeatIntervalMs"`
	HeartbeatTimeoutMs  int64         `json:"heartbeatTimeoutMs"`
	Limits              SessionLimits `json:"limits"`
}

// EngineInfo describes the controlled Docker Engine.
type EngineInfo struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	APIVersion    string `json:"apiVersion"`
	MinAPIVersion string `json:"minApiVersion,omitempty"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Rootless      bool   `json:"rootless,omitempty"`
}

// Root is a filesystem root the agent may serve and watch.
type Root struct {
	// Kind is stacks (the stacks volume), volumes (the Docker volume
	// directory at its identical host path, #28) or bind (a registered root).
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Watch is inotify, poll or none (#23 support matrix).
	Watch string `json:"watch"`
}

// CapabilitiesPayload is sent by the agent after welcome and whenever its
// capabilities change (for example the Engine was upgraded).
type CapabilitiesPayload struct {
	AgentVersion string     `json:"agentVersion"`
	Protocols    []string   `json:"protocols"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	Engine       EngineInfo `json:"engine"`
	// Commands are the job kinds this agent has executors for.
	Commands []string `json:"commands"`
	// Requests and Streams are the subsets of RequestNames/StreamKinds served.
	Requests []string `json:"requests"`
	Streams  []string `json:"streams"`
	Features []string `json:"features,omitempty"`
	Roots    []Root   `json:"roots,omitempty"`
	// Transport says how the agent reaches the manager; the host page flags
	// plain-HTTP connections (#27).
	Transport TransportInfo `json:"transport"`
	// Diagnostics explain why parts of the agent are unavailable, e.g. stack
	// operations refused because the storage layout check failed (#28). The
	// host page shows them.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Diagnostic areas.
const (
	DiagnosticEngine  = "engine"
	DiagnosticStorage = "storage"
)

// Diagnostic is a stable code plus a human-readable message.
type Diagnostic struct {
	// Area is engine or storage.
	Area string `json:"area"`
	// Code is stable snake_case, e.g. storage_path_mismatch (#28) or
	// unsupported_api_version (#21).
	Code    string `json:"code"`
	Message string `json:"message"`
	// Path is the affected root, if any.
	Path string `json:"path,omitempty"`
}

// MaxDiagnosticMessage bounds Diagnostic.Message (bytes).
const MaxDiagnosticMessage = 2048

// HeartbeatPayload is optional on heartbeat frames.
type HeartbeatPayload struct {
	Seq    uint64    `json:"seq"`
	SentAt time.Time `json:"sentAt"`
}

// EventPayload relays one Docker Engine or agent event. Attributes are an
// allowlist (names, image, exit code, health); never environment variables,
// labels with secrets, or file contents.
type EventPayload struct {
	Source     string            `json:"source"` // engine | agent
	Type       string            `json:"type"`   // container | image | volume | network | daemon | agent
	Action     string            `json:"action"`
	ResourceID string            `json:"resourceId,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	At         time.Time         `json:"at"`
	// Seq increases per session; the manager deduplicates and detects gaps.
	Seq uint64 `json:"seq"`
}

// ScopeRef names a watched file scope.
type ScopeRef struct {
	Kind string `json:"kind"` // stack | volume
	ID   string `json:"id"`   // stack ID or volume name
}

// FSInvalidationPayload reports changed paths under a scope root. It never
// carries file contents.
type FSInvalidationPayload struct {
	Scope ScopeRef `json:"scope"`
	// Paths are root-relative, slash-separated and cleaned; at most MaxPaths.
	Paths []string `json:"paths,omitempty"`
	// Overflow: more changes happened than listed (or the watch overflowed);
	// the manager rescans the scope.
	Overflow bool      `json:"overflow,omitempty"`
	At       time.Time `json:"at"`
	Seq      uint64    `json:"seq"`
}

// RescanPayload asks the agent for a bounded reconciliation of a scope
// subtree; answered by a response carrying RescanResult.
type RescanPayload struct {
	Scope      ScopeRef `json:"scope"`
	Path       string   `json:"path"`
	MaxEntries int      `json:"maxEntries"`
	Reason     string   `json:"reason,omitempty"`
}

// RescanResult is the output of a rescan response.
type RescanResult struct {
	Scope     ScopeRef `json:"scope"`
	Path      string   `json:"path"`
	Entries   int      `json:"entries"`
	Truncated bool     `json:"truncated"`
	// Changed are root-relative paths that differ from the agent's last
	// report; the manager invalidates them like an fs_invalidation.
	Changed []string `json:"changed,omitempty"`
}

// RequestPayload names a request and carries its input.
type RequestPayload struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ResponsePayload carries a request's output.
type ResponsePayload struct {
	Output json.RawMessage `json:"output,omitempty"`
}

// StreamOpenPayload opens a stream; the frame ID is the stream ID and
// every stream_data/stream_close/stream_credit frame correlates with it.
type StreamOpenPayload struct {
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	// JobID links the stream to a job (migration transfer relay, #35).
	JobID string          `json:"jobId,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// WindowBytes is the initial credit the opener grants the sender
	// (defaults to the session's StreamWindowBytes when zero).
	WindowBytes int64 `json:"windowBytes,omitempty"`
	// MaxBytes bounds the stream's total size (0 = the kind's default).
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// StreamDataPayload is one chunk; Data is base64 in JSON.
type StreamDataPayload struct {
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data,omitempty"`
	// Channel distinguishes stdout/stderr for container streams.
	Channel string `json:"channel,omitempty"`
}

// Stream close reasons.
const (
	CloseReasonEOF       = "eof"
	CloseReasonCancelled = "cancelled"
	CloseReasonError     = "error"
	CloseReasonTimeout   = "timeout"
	CloseReasonLimit     = "limit"
)

// StreamClosePayload ends a stream (either side).
type StreamClosePayload struct {
	Reason  string `json:"reason"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// ExitCode of an exec session's process.
	ExitCode *int `json:"exitCode,omitempty"`
	// Bytes and SHA256 cover the data sent, for transfer verification.
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	// Result is a kind-specific outcome sent with the final eof close of
	// the side that commits the transfer (files.upload: the written
	// entry). At most MaxCloseResult bytes.
	Result json.RawMessage `json:"result,omitempty"`
}

// MaxCloseResult bounds StreamClosePayload.Result (bytes).
const MaxCloseResult = 16 << 10

// StreamCreditPayload grants the stream sender Bytes more bytes.
type StreamCreditPayload struct {
	Bytes int64 `json:"bytes"`
}

// CancelPayload is optional on cancel frames.
type CancelPayload struct {
	Reason string `json:"reason,omitempty"`
}

// ErrorPayload answers a frame that failed (correlationId = that frame)
// or, without correlationId, reports a session-level problem before close.
type ErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// ErrRequestTimeout means the agent did not answer a request in time (the
// manager's request deadline passed; agents.ErrRequestTimeout).
var ErrRequestTimeout = errors.New("agents: the agent did not answer in time")

// CodedError is an error frame an agent answered a request with, as the
// manager returns it (agents.RequestError).
type CodedError interface {
	error
	ProtocolCode() string
	ProtocolMessage() string
}

// Payload validation errors.
var (
	ErrUnsupportedRequest = errors.New("protocol: unsupported request")
	ErrUnsupportedStream  = errors.New("protocol: unsupported stream kind")
)

var (
	versionRE = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFrame, fmt.Sprintf(format, args...))
}

// Validate checks a hello payload.
func (p HelloPayload) Validate() error {
	switch {
	case p.Protocol != Version:
		return invalid("hello protocol %q, want %q", p.Protocol, Version)
	case !idRE.MatchString(p.AgentID), !idRE.MatchString(p.InstallID), !idRE.MatchString(p.EngineID):
		return invalid("hello agentId, installId and engineId must match %s", idRE)
	case !versionRE.MatchString(p.AgentVersion):
		return invalid("hello agentVersion is malformed")
	case p.PreviousSessionID != "" && !idRE.MatchString(p.PreviousSessionID):
		return invalid("hello previousSessionId is malformed")
	}
	return nil
}

// Validate checks a welcome payload.
func (p WelcomePayload) Validate() error {
	switch {
	case !idRE.MatchString(p.SessionID), !idRE.MatchString(p.EnvironmentID):
		return invalid("welcome sessionId and environmentId must match %s", idRE)
	case !versionRE.MatchString(p.ManagerVersion):
		return invalid("welcome managerVersion is malformed")
	case p.AgentStatus != VersionCurrent && p.AgentStatus != VersionOutdated:
		return invalid("welcome agentStatus %q", p.AgentStatus)
	case p.HeartbeatIntervalMs <= 0 || p.HeartbeatTimeoutMs <= p.HeartbeatIntervalMs:
		return invalid("welcome heartbeat timeout must exceed a positive interval")
	case p.Limits.MaxFrameBytes <= 0 || p.Limits.MaxFrameBytes > MaxFrameSize || p.Limits.MaxStreams <= 0 ||
		p.Limits.StreamWindowBytes <= 0 || p.Limits.MaxChunkBytes <= 0 || p.Limits.MaxChunkBytes > MaxChunk || p.Limits.MaxPaths <= 0:
		return invalid("welcome limits out of range")
	}
	return nil
}

// Validate checks a capabilities payload.
func (p CapabilitiesPayload) Validate() error {
	if !versionRE.MatchString(p.AgentVersion) || !slices.Contains(p.Protocols, Version) {
		return invalid("capabilities need agentVersion and protocols including %s", Version)
	}
	if p.Engine.ID == "" || p.Engine.APIVersion == "" {
		return invalid("capabilities need engine.id and engine.apiVersion")
	}
	if err := p.Transport.Validate(); err != nil {
		return err
	}
	for _, c := range p.Commands {
		if !nameRE.MatchString(c) {
			return invalid("command kind %q is malformed", c)
		}
	}
	for _, r := range p.Requests {
		if !slices.Contains(requestNames, r) {
			return fmt.Errorf("%w: %q", ErrUnsupportedRequest, r)
		}
	}
	for _, s := range p.Streams {
		if _, ok := streamKinds[s]; !ok {
			return fmt.Errorf("%w: %q", ErrUnsupportedStream, s)
		}
	}
	for _, r := range p.Roots {
		if !slices.Contains([]string{"stacks", "volumes", "bind"}, r.Kind) || !IsAbsHostPath(r.Path) ||
			!slices.Contains([]string{"inotify", "poll", "none"}, r.Watch) {
			return invalid("root %+v is malformed", r)
		}
	}
	for _, d := range p.Diagnostics {
		if (d.Area != DiagnosticEngine && d.Area != DiagnosticStorage) || !diagCodeRE.MatchString(d.Code) ||
			d.Message == "" || len(d.Message) > MaxDiagnosticMessage || (d.Path != "" && !IsAbsHostPath(d.Path)) {
			return invalid("diagnostic %q is malformed", d.Code)
		}
	}
	return nil
}

var diagCodeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Validate checks an event payload.
func (p EventPayload) Validate() error {
	if p.Source != "engine" && p.Source != "agent" {
		return invalid("event source %q", p.Source)
	}
	if !slices.Contains([]string{"container", "image", "volume", "network", "daemon", "agent"}, p.Type) || p.Action == "" || p.At.IsZero() {
		return invalid("event needs a known type, an action and a timestamp")
	}
	return nil
}

func (s ScopeRef) validate() error {
	if (s.Kind != "stack" && s.Kind != "volume") || s.ID == "" || len(s.ID) > 255 {
		return invalid("scope %+v is malformed", s)
	}
	return nil
}

// ValidRelativePath reports whether p is a clean, slash-separated path
// relative to a scope root that cannot escape it ("." is the root itself).
func ValidRelativePath(p string) bool {
	if p == "." {
		return true
	}
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || path.Clean(p) != p {
		return false
	}
	return p != ".." && !strings.HasPrefix(p, "../")
}

// Validate checks an fs_invalidation payload.
func (p FSInvalidationPayload) Validate() error {
	if err := p.Scope.validate(); err != nil {
		return err
	}
	if len(p.Paths) > MaxPaths {
		return invalid("fs_invalidation lists %d paths, max %d (set overflow instead)", len(p.Paths), MaxPaths)
	}
	if len(p.Paths) == 0 && !p.Overflow {
		return invalid("fs_invalidation needs paths or overflow")
	}
	for _, rp := range p.Paths {
		if !ValidRelativePath(rp) {
			return invalid("fs_invalidation path %q escapes or is not clean", rp)
		}
	}
	return nil
}

// Validate checks a rescan payload.
func (p RescanPayload) Validate() error {
	if err := p.Scope.validate(); err != nil {
		return err
	}
	if !ValidRelativePath(p.Path) || p.MaxEntries <= 0 {
		return invalid("rescan needs a clean relative path and maxEntries > 0")
	}
	return nil
}

// Validate checks a request payload.
func (p RequestPayload) Validate() error {
	if !slices.Contains(requestNames, p.Name) {
		return fmt.Errorf("%w: %q", ErrUnsupportedRequest, p.Name)
	}
	return nil
}

// Validate checks a stream_open payload.
func (p StreamOpenPayload) Validate() error {
	dir, ok := streamKinds[p.Kind]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedStream, p.Kind)
	}
	if p.Direction != dir {
		return invalid("stream %s has direction %s, not %q", p.Kind, dir, p.Direction)
	}
	if p.JobID != "" && !idRE.MatchString(p.JobID) {
		return invalid("stream jobId is malformed")
	}
	if p.WindowBytes < 0 || p.MaxBytes < 0 {
		return invalid("stream window and max bytes must not be negative")
	}
	return nil
}

// Validate checks a stream_data payload.
func (p StreamDataPayload) Validate() error {
	if len(p.Data) > MaxChunk {
		return invalid("stream chunk of %d bytes exceeds %d", len(p.Data), MaxChunk)
	}
	if p.Channel != "" && p.Channel != "stdout" && p.Channel != "stderr" && p.Channel != "stdin" {
		return invalid("stream channel %q", p.Channel)
	}
	return nil
}

// Validate checks a stream_close payload.
func (p StreamClosePayload) Validate() error {
	if !slices.Contains([]string{CloseReasonEOF, CloseReasonCancelled, CloseReasonError, CloseReasonTimeout, CloseReasonLimit}, p.Reason) {
		return invalid("stream_close reason %q", p.Reason)
	}
	if p.Reason == CloseReasonError && !slices.Contains(errorCodes, p.Code) {
		return invalid("stream_close with reason error needs a known code")
	}
	if p.Bytes < 0 {
		return invalid("stream_close bytes must not be negative")
	}
	if len(p.Result) > MaxCloseResult {
		return invalid("stream_close result exceeds %d bytes", MaxCloseResult)
	}
	if len(p.Result) > 0 && (p.Reason != CloseReasonEOF || !json.Valid(p.Result)) {
		return invalid("stream_close result needs reason eof and valid JSON")
	}
	return nil
}

// Validate checks a stream_credit payload.
func (p StreamCreditPayload) Validate() error {
	if p.Bytes <= 0 || p.Bytes > 64*StreamWindow {
		return invalid("stream_credit bytes %d out of range", p.Bytes)
	}
	return nil
}

// Validate checks an error payload.
func (p ErrorPayload) Validate() error {
	if !slices.Contains(errorCodes, p.Code) {
		return invalid("error code %q is not in the protocol catalog", p.Code)
	}
	return nil
}

var outcomes = []string{"succeeded", "failed", "partial", "cancelled", "interrupted"}

func validateResult(p ResultPayload) error {
	if !slices.Contains(outcomes, p.Outcome) {
		return invalid("result outcome %q", p.Outcome)
	}
	return nil
}

type validator interface{ Validate() error }

func decodeValidate[T validator](f *Frame) error {
	v, err := DecodePayload[T](f)
	if err != nil {
		return err
	}
	return v.Validate()
}

// ValidatePayload strictly decodes f's payload into the type-specific
// struct and checks its invariants (allowed request and stream names, path
// containment, limits). Receivers call it after Decode and before acting on
// a frame; frames whose payload is optional pass when it is absent.
func ValidatePayload(f *Frame) error {
	if len(f.Payload) == 0 && !needsPayload[f.Type] {
		switch f.Type {
		case TypeCommand, TypeAck, TypeProgress, TypeResult, TypeJobReport:
			return invalid("%s frame has no payload", f.Type)
		}
		return nil
	}
	switch f.Type {
	case TypeHello:
		return decodeValidate[HelloPayload](f)
	case TypeWelcome:
		return decodeValidate[WelcomePayload](f)
	case TypeCapabilities:
		return decodeValidate[CapabilitiesPayload](f)
	case TypeHeartbeat:
		_, err := DecodePayload[HeartbeatPayload](f)
		return err
	case TypeEvent:
		return decodeValidate[EventPayload](f)
	case TypeFSInvalidation:
		return decodeValidate[FSInvalidationPayload](f)
	case TypeRescan:
		return decodeValidate[RescanPayload](f)
	case TypeRequest:
		return decodeValidate[RequestPayload](f)
	case TypeResponse:
		_, err := DecodePayload[ResponsePayload](f)
		return err
	case TypeStreamOpen:
		return decodeValidate[StreamOpenPayload](f)
	case TypeStreamData:
		return decodeValidate[StreamDataPayload](f)
	case TypeStreamClose:
		return decodeValidate[StreamClosePayload](f)
	case TypeStreamCredit:
		return decodeValidate[StreamCreditPayload](f)
	case TypeCancel:
		_, err := DecodePayload[CancelPayload](f)
		return err
	case TypeError:
		return decodeValidate[ErrorPayload](f)
	case TypeCommand:
		p, err := DecodePayload[CommandPayload](f)
		if err == nil && !nameRE.MatchString(p.Kind) {
			err = invalid("command kind %q is malformed", p.Kind)
		}
		return err
	case TypeAck:
		_, err := DecodePayload[AckPayload](f)
		return err
	case TypeProgress:
		p, err := DecodePayload[ProgressPayload](f)
		if err == nil && (p.Percent < -1 || p.Percent > 100) {
			err = invalid("progress percent %d out of range", p.Percent)
		}
		return err
	case TypeResult:
		p, err := DecodePayload[ResultPayload](f)
		if err == nil {
			err = validateResult(p)
		}
		return err
	case TypeJobReport:
		p, err := DecodePayload[JobReportPayload](f)
		if err != nil {
			return err
		}
		for _, e := range p.Jobs {
			if e.Status != ReportRunning && e.Status != ReportFinished {
				return invalid("job_report status %q", e.Status)
			}
			if e.Status == ReportFinished {
				if e.Result == nil {
					return invalid("finished job_report entry %s lacks result", e.JobID)
				}
				if err := validateResult(*e.Result); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return invalid("unknown type %q", f.Type)
}

// Agent version window (#34): the manager supports agents of its own and the
// previous minor release. Agents newer than the manager are refused (upgrade
// the manager first).

// ErrVersionUnsupported means the agent is outside the compatibility window.
var ErrVersionUnsupported = errors.New("protocol: agent version outside the supported window")

type semver struct{ major, minor, patch int }

func parseSemver(v string) (semver, bool) {
	v, _, _ = strings.Cut(v, "+")
	core, _, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		n[i] = x
	}
	return semver{n[0], n[1], n[2]}, true
}

// CheckAgentVersion classifies an agent version against the manager's:
// VersionCurrent (same major.minor), VersionOutdated (previous minor, still
// supported; the UI flags it), or ErrVersionUnsupported with an upgrade
// message. Identical version strings (e.g. two 0.0.0-edge builds) are
// current.
func CheckAgentVersion(managerVersion, agentVersion string) (string, error) {
	if managerVersion == agentVersion {
		return VersionCurrent, nil
	}
	m, okM := parseSemver(managerVersion)
	a, okA := parseSemver(agentVersion)
	switch {
	case !okM || !okA:
		return "", fmt.Errorf("%w: cannot compare agent %q with manager %q; run the same DockYard build on both", ErrVersionUnsupported, agentVersion, managerVersion)
	case a.major != m.major:
		return "", fmt.Errorf("%w: agent %s and manager %s differ in major version; upgrade the older one", ErrVersionUnsupported, agentVersion, managerVersion)
	case a.minor > m.minor:
		return "", fmt.Errorf("%w: agent %s is newer than manager %s; upgrade the manager first", ErrVersionUnsupported, agentVersion, managerVersion)
	case a.minor == m.minor:
		return VersionCurrent, nil
	case a.minor == m.minor-1:
		return VersionOutdated, nil
	}
	return "", fmt.Errorf("%w: agent %s is older than the previous minor release of manager %s; upgrade the agent", ErrVersionUnsupported, agentVersion, managerVersion)
}

// UpgradeGuide is where the upgrade procedure of each deploy method is
// documented (#34).
const UpgradeGuide = "docs/operations/upgrades.md"

// AgentCompatibility classifies a stored agent version for display (#34):
// VersionCurrent, VersionOutdated (previous minor release: works, upgrade
// it) or VersionUnsupported (the manager refuses its sessions), with
// plain-language upgrade instructions for the last two ("" when current).
// There is no in-app self-update in v1 (#25): the operator upgrades the
// agent where it runs, always after the manager.
func AgentCompatibility(managerVersion, agentVersion string) (status, instructions string) {
	const how = "On the agent's host, pull the agent image the manager runs with and recreate the container " +
		"(e.g. `docker compose pull && docker compose up -d` in the agent's Compose project); upgrade the manager first, " +
		"then its agents. See " + UpgradeGuide + "."
	st, err := CheckAgentVersion(managerVersion, agentVersion)
	switch {
	case err != nil:
		reason := strings.TrimPrefix(err.Error(), ErrVersionUnsupported.Error()+": ")
		return VersionUnsupported, "The manager refuses this agent: " + reason + ". " + how
	case st == VersionOutdated:
		return VersionOutdated, "This agent runs the previous minor release (" + agentVersion + ", manager " + managerVersion +
			"): it still works, but support ends with the next manager release. " + how
	}
	return VersionCurrent, ""
}
