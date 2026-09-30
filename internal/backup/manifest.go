package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/humanize"
)

// The portable manifest (#24) ties one logical backup set together so a
// fresh manager can find and judge it without the old database. It is a
// small JSON document stored as its own snapshot (TagManifest, SetTag)
// inside the repositories themselves, so restic encrypts it with the
// Recovery Key:
//
//   - the manager scope holds the set manifest (Kind "set"): the
//     repositories and their physical locations, every planned member
//     with its snapshot (when known), the manager's schema version and
//     the set's completeness;
//   - every environment scope holds a host manifest (Kind "host") for the
//     members that environment wrote, so a host repository alone is
//     enough to find its snapshots.
//
// It never contains credentials: S3 locations are endpoint, bucket and
// prefix only, and the Recovery Key is identified by its fingerprint.
//
// Encoding: one header line "DOCKER-MANAGER-MANIFEST v1 length=<n> sha256=<hex>"
// followed by exactly n bytes of JSON. Decode detects truncation and
// corruption before parsing.

// Manifest format and version.
const (
	ManifestFormat  = "docker-manager-backup-manifest"
	ManifestVersion = 1
	manifestMagic   = "DOCKER-MANAGER-MANIFEST"
	// MaxManifestSize bounds an encoded manifest.
	MaxManifestSize = 4 << 20
)

// Manifest kinds.
const (
	ManifestSet  = "set"
	ManifestHost = "host"
)

// Completeness of a set or member.
const (
	StateComplete = "complete"
	StatePartial  = "partial"
	StateFailed   = "failed"
	StatePending  = "pending"
	StateMissing  = "missing"
	// StateSkipped: a member whose stack or volume no longer existed when
	// its turn came (removed after the run was planned, ClassItemGone). It
	// has no snapshot and is not a failure; a set whose members were all
	// skipped is skipped (nothing was left to back up).
	StateSkipped = "skipped"
)

// ClassItemGone is the error class of a skipped member: its volume or
// its stack's project directory was removed before its turn.
const ClassItemGone = "item_gone"

// Member kinds.
const (
	MemberManagerState = "manager_state"
	MemberStack        = "stack"
	MemberVolume       = "volume"
)

// Consistency of a member's data.
const (
	// ConsistencyLive: taken while containers ran (crash-consistent).
	ConsistencyLive = "live"
	// ConsistencyShutdown: taken with the affected containers stopped.
	ConsistencyShutdown = "shutdown"
	// ConsistencySnapshot: a consistent database snapshot (manager state).
	ConsistencySnapshot = "snapshot"
)

// Manifest is one set (or one host's part of it).
type Manifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	SetID      string    `json:"setId"`
	InstanceID string    `json:"instanceId"`
	PolicyID   string    `json:"policyId,omitempty"`
	PolicyName string    `json:"policyName,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	CreatedAt  time.Time `json:"createdAt"`
	App        AppInfo   `json:"app"`
	// Schema is the manager database schema of the set's manager-state
	// snapshot (set manifests with a manager member).
	Schema *SchemaInfo `json:"schema,omitempty"`
	// Completeness is the set's (or host part's) state when written.
	Completeness string          `json:"completeness"`
	Repositories []RepositoryRef `json:"repositories"`
	Locations    []LocationRef   `json:"locations"`
	Members      []Member        `json:"members"`
}

// AppInfo identifies the Docker Manager build that wrote a manifest.
type AppInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// SchemaInfo is a manager database schema: the applied migrations.
type SchemaInfo struct {
	Migrations []string `json:"migrations"`
}

// Latest returns the newest applied migration.
func (s SchemaInfo) Latest() string {
	if len(s.Migrations) == 0 {
		return ""
	}
	return slices.Max(s.Migrations)
}

// RepositoryRef describes a backup repository without credentials.
type RepositoryRef struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Destination Destination `json:"destination"`
	// Executor of a local repository: "manager" or the environment ID.
	Executor string `json:"executor,omitempty"`
	// KeyFingerprint identifies the Recovery Key the repository used
	// when the manifest was written; KeyGeneration counts rotations.
	KeyFingerprint string `json:"keyFingerprint"`
	KeyGeneration  int    `json:"keyGeneration"`
}

// LocationRef is one physical restic repository of the set.
type LocationRef struct {
	RepositoryID       string `json:"repositoryId"`
	Scope              string `json:"scope"`
	Repository         string `json:"repository"`
	ResticRepositoryID string `json:"resticRepositoryId,omitempty"`
	EnvironmentID      string `json:"environmentId,omitempty"`
	EnvironmentName    string `json:"environmentName,omitempty"`
	EngineID           string `json:"engineId,omitempty"`
	KeyFingerprint     string `json:"keyFingerprint,omitempty"`
}

// Member is one planned snapshot of the set.
type Member struct {
	Item          string    `json:"item"`
	Kind          string    `json:"kind"`
	Scope         string    `json:"scope"`
	RepositoryID  string    `json:"repositoryId"`
	EnvironmentID string    `json:"environmentId,omitempty"`
	StackID       string    `json:"stackId,omitempty"`
	StackName     string    `json:"stackName,omitempty"`
	Volume        string    `json:"volume,omitempty"`
	SnapshotID    string    `json:"snapshotId,omitempty"`
	SnapshotTime  time.Time `json:"snapshotTime,omitzero"`
	Paths         []string  `json:"paths,omitempty"`
	Volumes       []string  `json:"volumes,omitempty"`
	// ProjectPath is the stack's project directory and VolumePaths each
	// volume's data directory as stored in the snapshot (restores map
	// them to the current places).
	ProjectPath string            `json:"projectPath,omitempty"`
	VolumePaths map[string]string `json:"volumePaths,omitempty"`
	Consistency string            `json:"consistency,omitempty"`
	State       string            `json:"state"`
	ErrorClass  string            `json:"errorClass,omitempty"`
	Bytes       int64             `json:"bytes,omitempty"`
	// RequiredCapabilities are what an agent needs to restore the member
	// (e.g. "files", "compose").
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
}

// Manifest decoding errors.
var (
	ErrManifestTruncated   = errors.New("manifest is truncated")
	ErrManifestCorrupt     = errors.New("manifest is corrupt (checksum mismatch or unreadable)")
	ErrManifestUnsupported = errors.New("manifest was written by a newer Docker Manager (unsupported version)")
)

// Normalize sorts the manifest's lists (stable encoding).
func (m *Manifest) Normalize() {
	slices.SortFunc(m.Repositories, func(a, b RepositoryRef) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(m.Locations, func(a, b LocationRef) int {
		return strings.Compare(a.RepositoryID+"\x00"+a.Scope, b.RepositoryID+"\x00"+b.Scope)
	})
	slices.SortFunc(m.Members, func(a, b Member) int {
		return strings.Compare(a.Scope+"\x00"+a.Item, b.Scope+"\x00"+b.Item)
	})
}

// Completeness derives the state of a set of members. Skipped members
// (removed before their turn) are left out of the judgement: complete when
// every other member is complete; pending while the others are complete
// and some are still pending; failed when no member has a snapshot;
// partial otherwise (a partial set is never reported as complete); skipped
// when every member was skipped (nothing was backed up, and nothing
// failed).
func Completeness(members []Member) string {
	if len(members) == 0 {
		return StateFailed
	}
	counted := make([]Member, 0, len(members))
	for _, m := range members {
		if m.State != StateSkipped {
			counted = append(counted, m)
		}
	}
	if len(counted) == 0 {
		return StateSkipped
	}
	members = counted
	complete, withSnapshot, pending := 0, 0, 0
	for _, m := range members {
		switch m.State {
		case StateComplete:
			complete++
		case StatePending:
			pending++
		}
		if m.SnapshotID != "" {
			withSnapshot++
		}
	}
	switch {
	case complete == len(members):
		return StateComplete
	case pending > 0 && complete+pending == len(members):
		return StatePending
	case withSnapshot == 0 && pending == 0:
		return StateFailed
	}
	return StatePartial
}

// Validate checks a decoded manifest's structure.
func (m Manifest) Validate() error {
	if m.Format != ManifestFormat {
		return fmt.Errorf("%w: format %q", ErrManifestCorrupt, m.Format)
	}
	if m.Version > ManifestVersion {
		return fmt.Errorf("%w: version %d", ErrManifestUnsupported, m.Version)
	}
	if m.Version < 1 || (m.Kind != ManifestSet && m.Kind != ManifestHost) || m.SetID == "" || m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: missing set, kind or time", ErrManifestCorrupt)
	}
	for _, mem := range m.Members {
		if !ValidScope(mem.Scope) || mem.Item == "" {
			return fmt.Errorf("%w: member with invalid scope or item", ErrManifestCorrupt)
		}
	}
	return nil
}

// EncodeManifest serializes m (normalized) with its integrity header.
func EncodeManifest(m Manifest) ([]byte, error) {
	m.Format, m.Version = ManifestFormat, ManifestVersion
	m.Normalize()
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxManifestSize {
		return nil, fmt.Errorf("manifest too large (%s)", humanize.Bytes(int64(len(body))))
	}
	sum := sha256.Sum256(body)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s v%d length=%d sha256=%s\n", manifestMagic, ManifestVersion, len(body), hex.EncodeToString(sum[:]))
	buf.Write(body)
	return buf.Bytes(), nil
}

// DecodeManifest parses and verifies an encoded manifest.
func DecodeManifest(b []byte) (Manifest, error) {
	if len(b) > MaxManifestSize+256 {
		return Manifest{}, fmt.Errorf("%w: too large", ErrManifestCorrupt)
	}
	header, body, ok := bytes.Cut(b, []byte("\n"))
	if !ok {
		if bytes.HasPrefix(b, []byte(manifestMagic)) {
			return Manifest{}, ErrManifestTruncated
		}
		return Manifest{}, fmt.Errorf("%w: no header", ErrManifestCorrupt)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 4 || fields[0] != manifestMagic || !strings.HasPrefix(fields[1], "v") {
		return Manifest{}, fmt.Errorf("%w: bad header", ErrManifestCorrupt)
	}
	version, err := strconv.Atoi(strings.TrimPrefix(fields[1], "v"))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: bad version", ErrManifestCorrupt)
	}
	if version > ManifestVersion {
		return Manifest{}, fmt.Errorf("%w: version %d", ErrManifestUnsupported, version)
	}
	lengthText, ok1 := strings.CutPrefix(fields[2], "length=")
	sumText, ok2 := strings.CutPrefix(fields[3], "sha256=")
	length, err := strconv.Atoi(lengthText)
	if !ok1 || !ok2 || err != nil || length < 0 || length > MaxManifestSize {
		return Manifest{}, fmt.Errorf("%w: bad header", ErrManifestCorrupt)
	}
	switch {
	case len(body) < length:
		return Manifest{}, ErrManifestTruncated
	case len(body) > length:
		return Manifest{}, fmt.Errorf("%w: trailing data", ErrManifestCorrupt)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != strings.ToLower(sumText) {
		return Manifest{}, ErrManifestCorrupt
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestCorrupt, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Merge overlays host manifests on a set manifest: a member the set
// manifest lists as pending or missing takes the host's result. It returns
// the merged members and the recomputed completeness.
func Merge(set Manifest, hosts []Manifest) ([]Member, string) {
	byKey := map[string]Member{}
	for _, h := range hosts {
		if h.SetID != set.SetID {
			continue
		}
		for _, m := range h.Members {
			byKey[m.Scope+"\x00"+m.Item] = m
		}
	}
	out := make([]Member, 0, len(set.Members))
	for _, m := range set.Members {
		if h, ok := byKey[m.Scope+"\x00"+m.Item]; ok && (m.SnapshotID == "" || m.State == StatePending || m.State == StateMissing) {
			m = h
		}
		out = append(out, m)
	}
	return out, Completeness(out)
}
