package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/backups/s3probe"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/restic"
)

// Fresh-manager import (#24). A new, empty manager (no owner yet) is given
// a destination, its S3 key pair (newly issued ones are fine) and the saved
// Recovery Key. It finds the repositories and backup sets through the
// portable manifests stored inside them, previews what a set holds and
// whether this build can run its database, and imports a set's manager
// state: the backup.import job restores the snapshot into a staging
// directory, opens the secret-key bundle with the Recovery Key, checks the
// database, and stages it with the recovered key; a controlled restart
// applies it (ApplyPendingRestore) and the restored manager finishes the
// restore (CompleteRestore: sessions, API tokens and agents are revoked,
// the repository points at the new destination and credentials, the index
// is reconciled with every manifest found).
//
// The Recovery Key and the S3 credentials are never stored by the import:
// they stay in memory for the job and are sealed with the restored secret
// key only to hand them to the restored manager.

const (
	importDirName = "backup-import"
	// PendingRestoreDir holds a staged manager-state restore, applied by the
	// next startup.
	PendingRestoreDir = "restore-pending"
	// AppliedRestoreFile marks an applied restore whose completion has not
	// finished yet.
	AppliedRestoreFile = "restore-applied.json"
	restoreMarkerFile  = "restore.json"
	restoredDBFile     = "docker-manager.db"
	restoredKeyFile    = "secret.key"
	// restoredDraftsFile holds the snapshot's template drafts.
	restoredDraftsFile = "templates.tar.gz"
	// importMaxSets bounds the sets a preview lists (newest first).
	importMaxSets = 20
	// importMaxManifests bounds the manifests handed to the restored
	// manager for reconciliation.
	importMaxManifests = 100

	sealImportCredentials = "backup_import/credentials"
	sealImportCurrent     = "backup_import/current"
	sealImportPrevious    = "backup_import/previous"
)

// Import error classes: stable codes of the setup import routes and of
// failed backup.import jobs. Each comes with recovery guidance.
const (
	ImportKeyRejected        = "backup_import_key_rejected"
	ImportNotFound           = "backup_import_not_found"
	ImportManifestCorrupt    = "backup_import_manifest_corrupt"
	ImportSchemaIncompatible = "backup_import_schema_incompatible"
	ImportKeyRotated         = "backup_import_key_rotated"
	ImportStateMissing       = "backup_import_state_missing"
	ImportUnreachable        = "backup_import_unreachable"
	ImportInProgress         = "backup_import_in_progress"
)

// Guidance texts.
const (
	guideKeyRejected = "Check the Recovery Key for typos (DYRK-…, 13 groups). If it was rotated, enter the newest key and the " +
		"previous one (previousRecoveryKey): repositories not used since the rotation still use the previous key. " +
		"If the Recovery Key is lost, nobody can decrypt these backups, Docker Manager included (restic encrypts them with it): " +
		"complete setup as a new instance and create new backups."
	guideNotFound = "Check the endpoint, bucket and prefix."
	guideManifest = "The manifest of this set is damaged. Choose another (older) set, or run restic check on the repository with " +
		"the Recovery Key to find damaged data."
	guideSchema = "This backup was written by a newer Docker Manager. Install a Docker Manager version at least as new as the one that " +
		"wrote it, then import again."
	guideRotated = "The manager state of this set was saved while another Recovery Key was current (a key rotation happened " +
		"after it). Enter that key too: the newest key as recoveryKey and the other as previousRecoveryKey."
	guideStateMissing = "This set has no readable manager-state snapshot (the manager repository is missing or the manager part " +
		"of the set failed). Choose a set whose manager state is present. If only host repositories remain, complete setup as " +
		"a new instance and restore their data with restic directly (docs/internal/architecture/backups.md, \"Host-only recovery\")."
	guideInProgress = "An import is already running on this manager; wait for it to finish (GET /api/v1/setup/status)."
)

func importRefusal(class, msg, guidance string) error { return backup.Refuse(class, msg, guidance) }

// ImportSource is what the owner supplies to a fresh manager.
type ImportSource struct {
	Destination backup.Destination
	Credentials backup.S3Credentials
	RecoveryKey string
	// PreviousRecoveryKey is the key before the last rotation (only
	// needed while repositories still use it).
	PreviousRecoveryKey string
}

type importSecrets struct {
	creds    backup.S3Credentials
	current  RecoveryKey
	previous *RecoveryKey
}

func (k importSecrets) keys() []RecoveryKey {
	out := []RecoveryKey{k.current}
	if k.previous != nil && !k.previous.Equal(k.current) {
		out = append(out, *k.previous)
	}
	return out
}

func (s *Service) importSource(src ImportSource) (importSecrets, error) {
	d := src.Destination
	if err := d.Validate(); err != nil {
		return importSecrets{}, fieldErr("endpoint", "%s", err.Error())
	}
	if src.Credentials.AccessKeyID == "" || src.Credentials.SecretAccessKey == "" {
		return importSecrets{}, fieldErr("secretAccessKey", "an S3 destination needs its access key ID and secret access key")
	}
	sec := importSecrets{creds: src.Credentials}
	cur, err := ParseRecoveryKey(src.RecoveryKey)
	if err != nil {
		return importSecrets{}, err
	}
	sec.current = cur
	if strings.TrimSpace(src.PreviousRecoveryKey) != "" {
		prev, err := ParseRecoveryKey(src.PreviousRecoveryKey)
		if err != nil {
			return importSecrets{}, &domain.FieldError{Field: "previousRecoveryKey", Message: "the previous Recovery Key is malformed; check it for typos"}
		}
		sec.previous = &prev
	}
	return sec, nil
}

// ImportLocation is one physical repository as seen by an import.
type ImportLocation struct {
	Scope           string
	EnvironmentID   string
	EnvironmentName string
	EngineID        string
	RepositoryID    string
	RepositoryName  string
	// Repository is the restic repository (never credentials).
	Repository string
	// Reachable: this manager can open it with the supplied destination
	// and credentials now.
	Reachable bool
	Found     bool
	// Key is "current" or "previous": which supplied key opened it.
	Key                string
	ResticRepositoryID string
	ErrorClass         string
	// Note explains an unreachable location and what makes it available.
	Note string

	repo restic.Repo
}

// ImportConnection is the result of an import connection test.
type ImportConnection struct {
	OK bool
	// KeyFingerprint identifies the supplied Recovery Key.
	KeyFingerprint string
	Manager        ImportLocation
	Locations      []ImportLocation
	// S3 capabilities (nil when not tested).
	CanRead, CanWrite, CanDelete, ObjectLock *bool
	// Sets counts the backup sets found (manifests in the manager scope).
	Sets     int
	Problems []string
}

// openImport opens scope at the source with the current key, then the
// previous one.
func (s *Service) openImport(ctx context.Context, src ImportSource, sec importSecrets, scope string) (restic.Repo, string, string, error) {
	loc := src.Destination.Location(scope, sec.creds)
	var first error
	for i, k := range sec.keys() {
		repo := s.opts.Restic.Open(loc, k.String())
		cfg, err := repo.Config(ctx)
		if err == nil {
			which := "current"
			if i > 0 {
				which = "previous"
			}
			return repo, which, cfg.ID, nil
		}
		if first == nil {
			first = err
		}
		if !restic.IsCode(err, restic.CodeKeyRejected) {
			break
		}
	}
	return nil, "", "", first
}

// importFailure turns a restic failure on repository into an import error.
func importFailure(err error, repository string) error {
	var ref *backup.Refusal
	if err == nil || errors.As(err, &ref) {
		return err
	}
	code := restic.CodeOf(err)
	switch code {
	case "":
		return err
	case restic.CodeRepositoryNotFound:
		return importRefusal(ImportNotFound, "no Docker Manager repository exists at "+repository, guideNotFound)
	case restic.CodeKeyRejected:
		return importRefusal(ImportKeyRejected, "the Recovery Key does not open "+repository, guideKeyRejected)
	}
	return importRefusal(ImportUnreachable, "the repository "+repository+" could not be read ("+code+")", restic.RecoveryFor(code))
}

func (s *Service) probeImportScope(ctx context.Context, src ImportSource, sec importSecrets, l ImportLocation) ImportLocation {
	repo, which, id, err := s.openImport(ctx, src, sec, l.Scope)
	switch {
	case err == nil:
		l.Found, l.Key, l.ResticRepositoryID, l.repo = true, which, id, repo
	case restic.IsCode(err, restic.CodeKeyRejected):
		l.Found, l.ErrorClass = true, restic.CodeKeyRejected
	default:
		l.ErrorClass = restic.CodeOf(err)
		if l.ErrorClass == "" {
			l.ErrorClass = restic.CodeFailed
		}
	}
	return l
}

// manifestScan is what one location's manifests say.
type manifestScan struct {
	snaps     []restic.Snapshot
	manifests []backup.Manifest
	// corrupt maps set IDs to manifest decoding failures.
	corrupt map[string]error
}

// scanManifests lists a location's snapshots and decodes (newest first) up
// to limit manifests that want accepts.
func scanManifests(ctx context.Context, repo restic.Repo, limit int, want func(restic.Snapshot) bool) (manifestScan, error) {
	snaps, err := repo.Snapshots(ctx, restic.SnapshotFilter{})
	if err != nil {
		return manifestScan{}, err
	}
	sc := manifestScan{snaps: snaps, corrupt: map[string]error{}}
	order := slices.Clone(snaps)
	slices.SortStableFunc(order, func(a, b restic.Snapshot) int { return b.Time.Compare(a.Time) })
	for _, sn := range order {
		if len(sc.manifests) >= limit {
			break
		}
		if !sn.HasTag(backup.TagManifest) || (want != nil && !want(sn)) {
			continue
		}
		var buf limitedBuffer
		buf.max = backup.MaxManifestSize + 1024
		if err := repo.Dump(ctx, sn.ID, "/"+backup.ManifestFile, &buf); err != nil {
			sc.corrupt[backup.SetOf(sn.Tags)] = err
			continue
		}
		m, err := backup.DecodeManifest(buf.b)
		if err != nil {
			sc.corrupt[backup.SetOf(sn.Tags)] = err
			continue
		}
		sc.manifests = append(sc.manifests, m)
	}
	return sc, nil
}

type limitedBuffer struct {
	b   []byte
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b)+len(p) > l.max {
		return 0, errors.New("too large")
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

// importRepositoryID is the Docker Manager repository the imported manager
// state lives in (the manager member of the newest set manifest).
func importRepositoryID(sets []backup.Manifest) string {
	for _, m := range sets {
		for _, mem := range m.Members {
			if mem.Kind == backup.MemberManagerState && mem.RepositoryID != "" {
				return mem.RepositoryID
			}
		}
	}
	return ""
}

func repositoryRefOf(sets []backup.Manifest, id string) backup.RepositoryRef {
	for _, m := range sets {
		for _, r := range m.Repositories {
			if r.ID == id {
				return r
			}
		}
	}
	return backup.RepositoryRef{ID: id}
}

// otherRepositoryNote explains a location in a repository other than the
// one imported from: an S3 repository is checked after the import, a
// local folder of an earlier version is not imported at all.
func otherRepositoryNote(rr backup.RepositoryRef) string {
	if rr.Destination.Kind != "" && rr.Destination.Kind != backup.KindS3 {
		return "Kept in a local folder (" + rr.Name + "), which Docker Manager no longer supports: " +
			"it is not imported; restore its data with restic."
	}
	return "Kept in another repository (" + rr.Name + "): its credentials are restored with the manager state; " +
		"it is checked after the import."
}

// listImportScopes lists the scope directories at the destination.
func (s *Service) listImportScopes(ctx context.Context, src ImportSource, sec importSecrets) []string {
	d := src.Destination
	dirs, _ := s3probe.ListDirs(ctx, s.opts.HTTPClient, s3probe.Target{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: d.Prefix,
		Region: d.Region, PathStyle: d.PathStyle, AccessKeyID: sec.creds.AccessKeyID, SecretAccessKey: sec.creds.SecretAccessKey}, s.opts.Clock.Now)
	var scopes []string
	for _, d := range dirs {
		if sc := backup.ScopeOfDir(d); sc != "" && sc != backup.ScopeManager {
			scopes = append(scopes, sc)
		}
	}
	return scopes
}

// importHosts lists the host locations the sets name, plus scopes found at
// the destination, and opens the reachable ones.
func (s *Service) importHosts(ctx context.Context, src ImportSource, sec importSecrets, sets []backup.Manifest) []ImportLocation {
	repoID := importRepositoryID(sets)
	seen := map[string]bool{}
	var out []ImportLocation
	for _, m := range sets {
		for _, l := range m.Locations {
			key := l.RepositoryID + "\x00" + l.Scope
			if l.Scope == backup.ScopeManager || seen[key] {
				continue
			}
			seen[key] = true
			rr := repositoryRefOf(sets, l.RepositoryID)
			il := ImportLocation{Scope: l.Scope, EnvironmentID: l.EnvironmentID, EnvironmentName: l.EnvironmentName, EngineID: l.EngineID,
				RepositoryID: l.RepositoryID, RepositoryName: rr.Name, Repository: l.Repository}
			if l.RepositoryID == repoID {
				il.Reachable, il.Repository = true, src.Destination.Repository(l.Scope)
			} else {
				il.Note = otherRepositoryNote(rr)
			}
			out = append(out, il)
		}
	}
	for _, scope := range s.listImportScopes(ctx, src, sec) {
		if seen[repoID+"\x00"+scope] {
			continue
		}
		seen[repoID+"\x00"+scope] = true
		env, _ := backup.ScopeEnvironment(scope)
		out = append(out, ImportLocation{Scope: scope, EnvironmentID: env, RepositoryID: repoID, Repository: src.Destination.Repository(scope),
			Reachable: true, Note: "Found at the destination; no manifest of the listed sets names it."})
	}
	for i := range out {
		if out[i].Reachable {
			out[i] = s.probeImportScope(ctx, src, sec, out[i])
		}
	}
	return out
}

func isSetManifest(sn restic.Snapshot) bool { return sn.Hostname == ManagerHost }

// TestImport checks a source: S3 access, whether the Recovery Key opens
// the manager repository, and which host repositories exist.
func (s *Service) TestImport(ctx context.Context, src ImportSource) (ImportConnection, error) {
	sec, err := s.importSource(src)
	if err != nil {
		return ImportConnection{}, err
	}
	out := ImportConnection{OK: true, KeyFingerprint: sec.current.Fingerprint()}
	problem := func(p string) { out.OK, out.Problems = false, append(out.Problems, p) }
	// note records what limits the import without preventing it.
	note := func(p string) { out.Problems = append(out.Problems, p) }
	if src.Destination.Kind == backup.KindS3 {
		d := src.Destination
		p := s3probe.Probe(ctx, s.opts.HTTPClient, s3probe.Target{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: d.Prefix, Region: d.Region,
			PathStyle: d.PathStyle, AccessKeyID: sec.creds.AccessKeyID, SecretAccessKey: sec.creds.SecretAccessKey}, s.opts.Clock.Now)
		out.CanRead, out.CanWrite, out.CanDelete, out.ObjectLock = p.CanRead, p.CanWrite, p.CanDelete, p.ObjectLock
		if p.Class == s3probe.ClassAddressNotAllowed {
			// restic is not run against a refused address either.
			problem("S3 access: " + p.Message + " (" + p.Class + "). Docker Manager does not connect to loopback, link-local or multicast addresses.")
			return out, nil
		}
		if p.Class != "" && (p.CanRead == nil || !*p.CanRead) {
			problem("S3 access: " + p.Message + " (" + p.Class + "). The import needs read access; new backups need write and delete.")
		} else if p.Class != "" {
			note("S3 access: " + p.Message + " (" + p.Class + "). The import can read; new backups need write and delete.")
		}
	}
	out.Manager = s.probeImportScope(ctx, src, sec, ImportLocation{Scope: backup.ScopeManager, Repository: src.Destination.Repository(backup.ScopeManager),
		Reachable: true})
	var sets []backup.Manifest
	switch {
	case out.Manager.repo != nil:
		scan, err := scanManifests(ctx, out.Manager.repo, importMaxSets, isSetManifest)
		if err != nil {
			problem("The manager repository's snapshots could not be listed (" + restic.CodeOf(err) + ").")
		}
		sets = scan.manifests
		out.Sets = len(sets)
		if len(scan.corrupt) > 0 {
			note(fmt.Sprintf("%d backup set manifest(s) are damaged; those sets cannot be imported. %s", len(scan.corrupt), guideManifest))
		}
		if out.Sets == 0 && len(scan.corrupt) == 0 {
			problem("The manager repository holds no backup set yet.")
		}
		if out.Manager.Key == "previous" {
			note("The manager repository still uses the previous Recovery Key (the rotation did not reach it). The import works; " +
				"the restored manager moves it to the newest key at its next backup.")
		}
	case out.Manager.ErrorClass == restic.CodeKeyRejected:
		problem("The Recovery Key does not open the manager repository. " + guideKeyRejected)
	case out.Manager.ErrorClass == restic.CodeRepositoryNotFound:
		problem("No manager repository (docker-manager) at this destination. " + guideNotFound)
	default:
		problem("The manager repository could not be read (" + out.Manager.ErrorClass + "): " + restic.RecoveryFor(out.Manager.ErrorClass))
	}
	out.Locations = s.importHosts(ctx, src, sec, sets)
	for _, l := range out.Locations {
		switch {
		case !l.Reachable:
		case l.ErrorClass == restic.CodeRepositoryNotFound:
			note("Host repository " + l.Scope + " (" + l.EnvironmentName + ") is missing at this destination: its snapshots are reported missing.")
		case l.ErrorClass == restic.CodeKeyRejected:
			note("The Recovery Key does not open host repository " + l.Scope + ". If a rotation did not reach it yet, enter the previous key too.")
		case l.ErrorClass != "":
			note("Host repository " + l.Scope + " could not be read (" + l.ErrorClass + ").")
		case l.Key == "previous":
			note("Host repository " + l.Scope + " still uses the previous Recovery Key (partially rotated keys): the restored manager " +
				"moves it to the newest key the next time it uses it; keep both keys until then.")
		}
	}
	out.Manager.repo = nil
	for i := range out.Locations {
		out.Locations[i].repo = nil
	}
	return out, nil
}

// ImportPreview lists the importable sets.
type ImportPreview struct {
	KeyFingerprint string
	Manager        ImportLocation
	Locations      []ImportLocation
	Sets           []ImportSet
	Problems       []string
}

// ImportSet is one backup set found at the source.
type ImportSet struct {
	SetID        string
	PolicyName   string
	InstanceID   string
	CreatedAt    time.Time
	AppVersion   string
	Completeness string
	// SchemaLatest is the newest migration of the set's database;
	// SchemaCompatible reports whether this build can run it.
	SchemaLatest        string
	SchemaCompatible    bool
	RepositoryID        string
	ManagerSnapshotID   string
	ManagerSnapshotTime time.Time
	// KeyBundle (the selected set only): "ok", "previous_key",
	// "mismatch", "corrupt" or "missing".
	KeyBundle string
	// HostOnly: known only from host manifests (no manager-state set
	// manifest was found for it).
	HostOnly   bool
	Importable bool
	// BlockerClass is the import error class when not importable.
	BlockerClass string
	Problems     []string
	Members      []ImportMember
}

// ImportMember is one planned snapshot of a set and where it is.
type ImportMember struct {
	backup.Member
	EnvironmentName string
	// Located: "found" (listed in its repository), "missing" (its
	// repository was read and the snapshot is not there), "unverified"
	// (its repository is not reachable from this manager yet) or
	// "not_backed_up" (the member has no snapshot).
	Located string
}

// Located values.
const (
	LocatedFound       = "found"
	LocatedMissing     = "missing"
	LocatedUnverified  = "unverified"
	LocatedNotBackedUp = "not_backed_up"
)

// PreviewImport lists the sets at the source (newest first) with their
// completeness, where each member's snapshot is, and whether this build
// can import them. With setID it also opens that set's secret-key bundle.
func (s *Service) PreviewImport(ctx context.Context, src ImportSource, setID string) (ImportPreview, error) {
	pv, _, err := s.previewImport(ctx, src, setID)
	return pv, err
}

func (s *Service) previewImport(ctx context.Context, src ImportSource, setID string) (ImportPreview, importSecrets, error) {
	sec, err := s.importSource(src)
	if err != nil {
		return ImportPreview{}, sec, err
	}
	pv := ImportPreview{KeyFingerprint: sec.current.Fingerprint()}
	pv.Manager = s.probeImportScope(ctx, src, sec, ImportLocation{Scope: backup.ScopeManager,
		Repository: src.Destination.Repository(backup.ScopeManager), Reachable: true})
	var mscan manifestScan
	if pv.Manager.repo != nil {
		if mscan, err = scanManifests(ctx, pv.Manager.repo, importMaxSets, isSetManifest); err != nil {
			return pv, sec, importFailure(err, pv.Manager.Repository)
		}
	}
	pv.Locations = s.importHosts(ctx, src, sec, mscan.manifests)
	if pv.Manager.repo == nil {
		switch pv.Manager.ErrorClass {
		case restic.CodeKeyRejected:
			if !anyReachable(pv.Locations) {
				return pv, sec, importRefusal(ImportKeyRejected, "the Recovery Key does not open the manager repository", guideKeyRejected)
			}
		case restic.CodeRepositoryNotFound:
			pv.Problems = append(pv.Problems, "No manager repository at this destination: sets are listed from host repositories only "+
				"and cannot be imported. "+guideStateMissing)
		default:
			return pv, sec, importRefusal(ImportUnreachable, "the manager repository could not be read ("+pv.Manager.ErrorClass+")",
				restic.RecoveryFor(pv.Manager.ErrorClass))
		}
	}
	// Host snapshots and manifests of the reachable host locations.
	hostSnaps := map[string]map[string]bool{}
	var hostManifests []backup.Manifest
	for _, l := range pv.Locations {
		if l.repo == nil {
			continue
		}
		scan, err := scanManifests(ctx, l.repo, importMaxSets, nil)
		if err != nil {
			pv.Problems = append(pv.Problems, "Host repository "+l.Scope+" could not be listed ("+restic.CodeOf(err)+").")
			continue
		}
		ids := map[string]bool{}
		for _, sn := range scan.snaps {
			ids[sn.ID] = true
		}
		hostSnaps[l.RepositoryID+"\x00"+l.Scope] = ids
		hostManifests = append(hostManifests, scan.manifests...)
	}
	managerSnaps := map[string]bool{}
	for _, sn := range mscan.snaps {
		managerSnaps[sn.ID] = true
	}
	located := func(mem backup.Member) string {
		if mem.SnapshotID == "" {
			return LocatedNotBackedUp
		}
		if mem.Scope == backup.ScopeManager {
			if pv.Manager.repo == nil {
				return LocatedUnverified
			}
			if managerSnaps[mem.SnapshotID] {
				return LocatedFound
			}
			return LocatedMissing
		}
		ids, ok := hostSnaps[mem.RepositoryID+"\x00"+mem.Scope]
		if !ok {
			for _, l := range pv.Locations {
				if l.RepositoryID == mem.RepositoryID && l.Scope == mem.Scope && l.Reachable && l.ErrorClass == restic.CodeRepositoryNotFound {
					return LocatedMissing
				}
			}
			return LocatedUnverified
		}
		if ids[mem.SnapshotID] {
			return LocatedFound
		}
		return LocatedMissing
	}
	envNames := map[string]string{}
	for _, l := range pv.Locations {
		if l.EnvironmentName != "" {
			envNames[l.EnvironmentID] = l.EnvironmentName
		}
	}
	known := s.knownMigrations()
	seen := map[string]bool{}
	build := func(m backup.Manifest, members []backup.Member, completeness string, hostOnly bool) ImportSet {
		set := ImportSet{SetID: m.SetID, PolicyName: m.PolicyName, InstanceID: m.InstanceID, CreatedAt: m.CreatedAt, AppVersion: m.App.Version,
			Completeness: completeness, HostOnly: hostOnly}
		if m.Schema != nil {
			set.SchemaLatest = m.Schema.Latest()
			set.SchemaCompatible = schemaKnown(m.Schema.Migrations, known)
		}
		for _, mem := range members {
			im := ImportMember{Member: mem, EnvironmentName: envNames[mem.EnvironmentID], Located: located(mem)}
			if mem.Kind == backup.MemberManagerState {
				set.RepositoryID, set.ManagerSnapshotID, set.ManagerSnapshotTime = mem.RepositoryID, mem.SnapshotID, mem.SnapshotTime
				if im.Located != LocatedFound {
					set.ManagerSnapshotID = ""
				}
			}
			set.Members = append(set.Members, im)
		}
		switch {
		case hostOnly || set.ManagerSnapshotID == "":
			set.BlockerClass = ImportStateMissing
			set.Problems = append(set.Problems, "No readable manager-state snapshot: this set cannot be imported.")
		case m.Schema == nil || !set.SchemaCompatible:
			set.BlockerClass = ImportSchemaIncompatible
			set.Problems = append(set.Problems, "Written by a newer Docker Manager ("+m.App.Version+"): this build cannot run its database.")
		}
		for _, im := range set.Members {
			if im.Kind != backup.MemberManagerState && im.Located == LocatedMissing {
				set.Problems = append(set.Problems, "The snapshot of "+memberLabel(im)+" is missing from its repository.")
			}
		}
		switch completeness {
		case backup.StateComplete:
		case backup.StateSkipped:
			set.Problems = append(set.Problems, "Nothing was backed up in this set: every item was removed before its turn.")
		default:
			set.Problems = append(set.Problems, "The set is "+completeness+": some members have no snapshot.")
		}
		set.Importable = set.BlockerClass == ""
		return set
	}
	for _, m := range mscan.manifests {
		members, completeness := backup.Merge(m, hostManifests)
		pv.Sets = append(pv.Sets, build(m, members, completeness, false))
		seen[m.SetID] = true
	}
	for setIDc, err := range mscan.corrupt {
		if setIDc == "" || seen[setIDc] {
			continue
		}
		seen[setIDc] = true
		pv.Sets = append(pv.Sets, ImportSet{SetID: setIDc, BlockerClass: ImportManifestCorrupt,
			Problems: []string{"The manifest of this set is damaged (" + manifestErrorText(err) + ")."}})
	}
	// Sets known only from host manifests (the manager part is missing).
	hostSets := map[string][]backup.Manifest{}
	for _, h := range hostManifests {
		if !seen[h.SetID] {
			hostSets[h.SetID] = append(hostSets[h.SetID], h)
		}
	}
	for id, hs := range hostSets {
		base := hs[0]
		var members []backup.Member
		for _, h := range hs {
			members = append(members, h.Members...)
		}
		base.SetID = id
		pv.Sets = append(pv.Sets, build(base, members, backup.Completeness(members), true))
	}
	slices.SortStableFunc(pv.Sets, func(a, b ImportSet) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(pv.Sets) > importMaxSets {
		pv.Sets = pv.Sets[:importMaxSets]
	}
	if setID != "" {
		i := slices.IndexFunc(pv.Sets, func(x ImportSet) bool { return x.SetID == setID })
		if i < 0 {
			return pv, sec, importRefusal(ImportNotFound, "no backup set "+setID+" at this destination",
				"Choose one of the sets the preview lists (the newest are listed).")
		}
		set := &pv.Sets[i]
		if set.Importable && pv.Manager.repo != nil {
			set.KeyBundle = s.checkBundle(ctx, pv.Manager.repo, *set, sec)
			switch set.KeyBundle {
			case "ok", "previous_key":
			case "mismatch":
				set.Importable, set.BlockerClass = false, ImportKeyRotated
				set.Problems = append(set.Problems, "The manager secret key in this snapshot is sealed under another Recovery Key.")
			default:
				set.Importable, set.BlockerClass = false, ImportStateMissing
				set.Problems = append(set.Problems, "The manager-state snapshot has no readable secret-key bundle ("+set.KeyBundle+").")
			}
		}
	}
	for i := range pv.Locations {
		pv.Locations[i].repo = nil
	}
	pv.Manager.repo = nil
	return pv, sec, nil
}

func anyReachable(ls []ImportLocation) bool {
	return slices.ContainsFunc(ls, func(l ImportLocation) bool { return l.repo != nil })
}

func manifestErrorText(err error) string {
	switch {
	case errors.Is(err, backup.ErrManifestTruncated):
		return "truncated"
	case errors.Is(err, backup.ErrManifestUnsupported):
		return "written by a newer Docker Manager"
	case errors.Is(err, backup.ErrManifestCorrupt):
		return "checksum mismatch or unreadable"
	}
	return "unreadable"
}

func memberLabel(m ImportMember) string {
	switch m.Kind {
	case backup.MemberStack:
		return "stack " + m.StackName
	case backup.MemberVolume:
		return "volume " + m.Volume
	}
	return m.Item
}

func (s *Service) knownMigrations() map[string]bool {
	known := map[string]bool{}
	if s.opts.KnownMigrations != nil {
		for _, m := range s.opts.KnownMigrations() {
			known[m] = true
		}
	}
	return known
}

// schemaKnown reports whether every applied migration is known.
func schemaKnown(applied []string, known map[string]bool) bool {
	if len(known) == 0 {
		return true
	}
	for _, m := range applied {
		if !known[m] {
			return false
		}
	}
	return true
}

// stateFile is the snapshot path of a file of a manager-state snapshot
// whose recorded paths are paths.
func stateFile(paths []string, name string) string {
	dir := ""
	if len(paths) > 0 {
		dir = snapshotPath(paths[0])
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// snapshotPath maps a recorded path (slash separated; on Windows with a
// drive letter) to the form restic stores it in.
func snapshotPath(p string) string {
	if len(p) >= 3 && p[1] == ':' && p[2] == '/' {
		return "/" + p[:1] + p[2:]
	}
	return p
}

func managerMember(set ImportSet) (ImportMember, bool) {
	for _, m := range set.Members {
		if m.Kind == backup.MemberManagerState {
			return m, true
		}
	}
	return ImportMember{}, false
}

// checkBundle opens the set's secret-key bundle with the supplied keys.
func (s *Service) checkBundle(ctx context.Context, repo restic.Repo, set ImportSet, sec importSecrets) string {
	mem, ok := managerMember(set)
	if !ok {
		return "missing"
	}
	var buf limitedBuffer
	buf.max = 64 << 10
	if err := repo.Dump(ctx, set.ManagerSnapshotID, stateFile(mem.Paths, BundleFile), &buf); err != nil {
		return "missing"
	}
	for i, k := range sec.keys() {
		_, _, err := OpenKeyBundle(buf.b, k)
		switch {
		case err == nil && i == 0:
			return "ok"
		case err == nil:
			return "previous_key"
		case errors.Is(err, ErrBundleCorrupt):
			return "corrupt"
		}
	}
	return "mismatch"
}

// importInput is the input of backup.import (no secrets).
type importInput struct {
	SetID             string             `json:"setId"`
	RepositoryID      string             `json:"repositoryId"`
	Destination       backup.Destination `json:"destination"`
	ManagerSnapshotID string             `json:"managerSnapshotId"`
	StatePaths        []string           `json:"statePaths"`
	KeyFingerprint    string             `json:"keyFingerprint"`
}

// StartImport queues the import of a set's manager state (backup.import).
// The job restarts the manager when it succeeds.
func (s *Service) StartImport(ctx context.Context, src ImportSource, setID, idempotencyKey string) (domain.Job, error) {
	if strings.TrimSpace(setID) == "" {
		return domain.Job{}, fieldErr("setId", "choose the backup set to import")
	}
	if running, err := s.importRunning(ctx); err != nil {
		return domain.Job{}, err
	} else if running {
		return domain.Job{}, importRefusal(ImportInProgress, "an import is already running", guideInProgress)
	}
	pv, sec, err := s.previewImport(ctx, src, setID)
	if err != nil {
		return domain.Job{}, err
	}
	var set ImportSet
	for _, x := range pv.Sets {
		if x.SetID == setID {
			set = x
		}
	}
	if !set.Importable {
		return domain.Job{}, importBlocker(set)
	}
	mem, _ := managerMember(set)
	in := importInput{SetID: set.SetID, RepositoryID: set.RepositoryID, Destination: src.Destination, ManagerSnapshotID: set.ManagerSnapshotID,
		StatePaths: mem.Paths, KeyFingerprint: sec.current.Fingerprint()}
	s.importMu.Lock()
	defer s.importMu.Unlock()
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.BackupImport, Principal: authz.Service(),
		Targets: []domain.JobTarget{repoTarget(set.RepositoryID)}, Input: in, IdempotencyKey: idempotencyKey})
	if err != nil {
		return j, err
	}
	s.imports[j.ID] = &sec
	audit.SetDetail(ctx, "setId", set.SetID)
	audit.SetDetail(ctx, "destination", src.Destination.Base())
	audit.SetDetail(ctx, "keyFingerprint", sec.current.Fingerprint())
	return j, nil
}

func importBlocker(set ImportSet) error {
	switch set.BlockerClass {
	case ImportSchemaIncompatible:
		return importRefusal(ImportSchemaIncompatible, "this set was written by a newer Docker Manager ("+set.AppVersion+")", guideSchema)
	case ImportKeyRotated:
		return importRefusal(ImportKeyRotated, "the manager state of this set is sealed under another Recovery Key", guideRotated)
	case ImportManifestCorrupt:
		return importRefusal(ImportManifestCorrupt, "the manifest of this set is damaged", guideManifest)
	}
	return importRefusal(ImportStateMissing, "this set has no readable manager state", guideStateMissing)
}

// importRunning reports whether an import job is queued or running.
func (s *Service) importRunning(ctx context.Context) (bool, error) {
	list, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.BackupImport}})
	if err != nil {
		return false, err
	}
	for _, j := range list {
		if !j.State.Terminal() {
			return true, nil
		}
	}
	return false, nil
}

// ImportStatus is the state of the newest import on this manager.
type ImportStatus struct {
	JobID      string
	State      domain.JobState
	ErrorClass string
	// Message is the failure's recovery guidance (never secrets).
	Message string
	// RestartPending: the manager state is staged and the manager is
	// restarting to apply it.
	RestartPending bool
}

// LatestImport returns the newest import job's state (nil when none).
func (s *Service) LatestImport(ctx context.Context) (*ImportStatus, error) {
	list, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.BackupImport}, Limit: 1})
	if err != nil || len(list) == 0 {
		return nil, err
	}
	j := list[0]
	st := &ImportStatus{JobID: j.ID, State: j.State, ErrorClass: j.ErrorClass, Message: j.Recovery}
	if _, err := os.Stat(filepath.Join(s.opts.DataDir, PendingRestoreDir, restoreMarkerFile)); err == nil {
		st.RestartPending = true
	}
	return st, nil
}

func (s *Service) importSecretsFor(jobID string) (importSecrets, bool) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	sec, ok := s.imports[jobID]
	if !ok {
		return importSecrets{}, false
	}
	return *sec, true
}

func (s *Service) importStaging(jobID string) string {
	return filepath.Join(s.opts.DataDir, importDirName, jobID)
}

// jsonBytes encodes v (errors only for unsupported types).
func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
