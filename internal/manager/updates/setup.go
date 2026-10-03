package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// SetupChange changes the updates setup (nil fields are kept).
type SetupChange struct {
	ExcludeEnvironments *[]string
	ExcludeStacks       *[]string
	// ExcludeContainers are environmentID/containerName pairs.
	ExcludeContainers  *[]string
	Check              *domain.UpdateSchedule
	Run                *domain.UpdateSchedule
	Window             *domain.UpdateWindow
	ClearWindow        bool
	WaitTimeoutSeconds *int
}

// Setup returns the updates setup.
func (s *Service) Setup(ctx context.Context) (domain.UpdateSetup, error) {
	return store.GetUpdateSetup(ctx, s.db)
}

func validExclusions(field string, values []string) ([]string, error) {
	if len(values) > 256 {
		return nil, fieldErr(field, "at most 256 exclusions")
	}
	out := slices.Clone(values)
	for _, value := range out {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return nil, fieldErr(field, "invalid exclusion %q", value)
		}
		if field == "excludeContainers" && !strings.Contains(value, "/") {
			return nil, fieldErr(field, "container exclusions must be environmentID/containerName")
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// UpdateSetup changes the setup when the revision matches. Environments
// left out that no longer exist are dropped.
func (s *Service) UpdateSetup(ctx context.Context, revision int64, c SetupChange) (before, after domain.UpdateSetup, err error) {
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetUpdateSetup(ctx, tx)
		if err != nil {
			return err
		}
		before = cur
		if cur.Revision != revision {
			return domain.ErrRevisionMismatch
		}
		next := cur
		if c.ExcludeEnvironments != nil {
			envs, err := store.ListEnvironments(ctx, tx, domain.EnvironmentFilter{})
			if err != nil {
				return err
			}
			next.ExcludeEnvironments = []string{}
			for _, e := range envs {
				if slices.Contains(*c.ExcludeEnvironments, e.ID) {
					next.ExcludeEnvironments = append(next.ExcludeEnvironments, e.ID)
				}
			}
			slices.Sort(next.ExcludeEnvironments)
		}
		if c.ExcludeStacks != nil {
			if next.ExcludeStacks, err = validExclusions("excludeStacks", *c.ExcludeStacks); err != nil {
				return err
			}
		}
		if c.ExcludeContainers != nil {
			if next.ExcludeContainers, err = validExclusions("excludeContainers", *c.ExcludeContainers); err != nil {
				return err
			}
		}
		if c.Check != nil {
			next.Check = *c.Check
		}
		if c.Run != nil {
			next.Run = *c.Run
		}
		if c.ClearWindow {
			next.Window = nil
		}
		if c.Window != nil {
			w := *c.Window
			next.Window = &w
		}
		if c.WaitTimeoutSeconds != nil {
			next.WaitTimeoutSeconds = *c.WaitTimeoutSeconds
		}
		if next.WaitTimeoutSeconds < 0 || next.WaitTimeoutSeconds > 3600 {
			return fieldErr("waitTimeoutSeconds", "must be between 0 and 3600")
		}
		if err := validWindow(next.Window); err != nil {
			return err
		}
		if err := validSchedule("checkSchedule", next.Check); err != nil {
			return err
		}
		if err := validSchedule("runSchedule", next.Run); err != nil {
			return err
		}
		next.Revision, next.UpdatedAt = cur.Revision+1, s.now()
		after = next
		return store.UpdateUpdateSetup(ctx, tx, next, revision)
	})
	if err != nil {
		return before, after, err
	}
	s.notify()
	return before, after, nil
}

// ManagedTarget is a target record of the setup and, when it is inactive,
// why the setup no longer covers it.
type ManagedTarget struct {
	Policy domain.UpdatePolicy
	// InactiveReason is domain.UpdateTargetExcluded or
	// domain.UpdateTargetMissing for an inactive record, "" otherwise.
	InactiveReason string
}

// Targets returns the target records of the setup (inactive ones included,
// with their reason), after reconciling them.
func (s *Service) Targets(ctx context.Context) ([]ManagedTarget, error) {
	st, err := s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	return s.reconcile(ctx, st, nil)
}

// Permit refuses (with its error) an environment the caller may not act
// on; nil permits every one.
type Permit func(domain.Environment) error

// syncSetup reconciles the target records of the setup and returns the
// active ones. permit refuses every one when it refuses one of the
// covered environments.
func (s *Service) syncSetup(ctx context.Context, st domain.UpdateSetup, permit Permit) ([]domain.UpdatePolicy, error) {
	all, err := s.reconcile(ctx, st, permit)
	if err != nil {
		return nil, err
	}
	active := make([]domain.UpdatePolicy, 0, len(all))
	for _, t := range all {
		if !t.Policy.Inactive {
			active = append(active, t.Policy)
		}
	}
	return active, nil
}

// targetKey identifies a target record: environment/type/target.
func targetKey(env string, typ domain.UpdateTargetType, id string) string {
	return fmt.Sprintf("%s/%s/%s", env, typ, id)
}

// reconcile discovers targets and keeps their records in step with the
// setup: activity (exclusions and vanished targets deactivate a record
// without erasing its history), schedules, window, wait timeout and the
// record's name, which follows the target's current name ("Automatic
// updates for zerobyte"; records that earlier versions named after their
// ID are renamed here, so no migration is needed). permit, when set,
// checks every covered environment first: a refusal ends the
// reconciliation with its error, so callers act only on environments the
// caller was checked for.
func (s *Service) reconcile(ctx context.Context, p domain.UpdateSetup, permit Permit) ([]ManagedTarget, error) {
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return nil, err
	}
	envs = slices.DeleteFunc(envs, func(e domain.Environment) bool { return slices.Contains(p.ExcludeEnvironments, e.ID) })
	if permit != nil {
		for _, env := range envs {
			if err := permit(env); err != nil {
				return nil, err
			}
		}
	}
	children, err := store.UpdatePoliciesForParent(ctx, s.db, p.ID)
	if err != nil {
		return nil, err
	}
	byTarget := map[string]domain.UpdatePolicy{}
	for _, child := range children {
		byTarget[targetKey(child.EnvironmentID, child.TargetType, child.TargetID)] = child
	}
	wanted := map[string]bool{}
	excluded := map[string]bool{}
	// labels are the targets' names as users know them.
	labels := map[string]string{}
	for _, env := range envs {
		stacks, err := s.opts.Stacks.List(ctx, domain.StackFilter{EnvironmentID: env.ID})
		if err != nil {
			return nil, err
		}
		for _, st := range stacks {
			key := targetKey(env.ID, domain.UpdateTargetStack, st.ID)
			labels[key] = StackLabel(st)
			if slices.Contains(p.ExcludeStacks, st.ID) {
				excluded[key] = true
				continue
			}
			wanted[key] = true
		}
		// Offline, or its containers cannot be listed now: its container
		// records stay as they are, and the other environments go on.
		keep := func() {
			for key, child := range byTarget {
				if child.EnvironmentID == env.ID && child.TargetType == domain.UpdateTargetContainer && !child.Inactive &&
					!containerExcluded(p, env.ID, child.TargetID) {
					wanted[key] = true
				}
			}
		}
		if !env.Online || s.opts.Resources == nil {
			keep()
			continue
		}
		found, skip, err := s.standaloneTargets(ctx, p, env.ID)
		if err != nil {
			s.log.Warn("could not list the containers of an environment; its container records stay as they are",
				"environment_id", env.ID, "error", err)
			keep()
			continue
		}
		for _, key := range found {
			wanted[key] = true
		}
		for _, key := range skip {
			excluded[key] = true
		}
	}
	names := &policyNames{s: s, taken: map[string]map[string]string{}}
	out := make([]ManagedTarget, 0, len(byTarget)+len(wanted))
	for key, child := range byTarget {
		shouldBeActive := wanted[key]
		name, err := s.childName(ctx, names, child, labels[key])
		if err != nil {
			return nil, err
		}
		if child.Inactive != shouldBeActive && child.Name == name && child.WaitTimeoutSeconds == p.WaitTimeoutSeconds &&
			child.Check.Cron == p.Check.Cron && child.Check.TimeZone == p.Check.TimeZone &&
			child.Run.Cron == p.Run.Cron && child.Run.TimeZone == p.Run.TimeZone &&
			windowsEqual(child.Window, p.Window) {
			out = append(out, managedTarget(p, child, excluded[key]))
			continue
		}
		child.Inactive, child.WaitTimeoutSeconds, child.Name = !shouldBeActive, p.WaitTimeoutSeconds, name
		child.Check = domain.UpdateSchedule{Cron: p.Check.Cron, TimeZone: p.Check.TimeZone}
		child.Run = domain.UpdateSchedule{Cron: p.Run.Cron, TimeZone: p.Run.TimeZone}
		child.Window = p.Window
		child.Revision++
		child.UpdatedAt = s.now()
		if err := store.UpdateUpdatePolicy(ctx, s.db, &child, child.Revision-1); err != nil {
			return nil, err
		}
		out = append(out, managedTarget(p, child, excluded[key]))
	}
	for key := range wanted {
		if _, ok := byTarget[key]; ok {
			continue
		}
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 {
			return nil, errors.New("updates: malformed target key")
		}
		child := domain.UpdatePolicy{ID: ids.New(), ParentID: p.ID, EnvironmentID: parts[0],
			TargetType: domain.UpdateTargetType(parts[1]), TargetID: parts[2],
			Check: p.Check, Run: p.Run, Window: p.Window, WaitTimeoutSeconds: p.WaitTimeoutSeconds,
			Revision: 1, CreatedAt: s.now(), UpdatedAt: s.now()}
		if child.Name, err = s.childName(ctx, names, child, labels[key]); err != nil {
			return nil, err
		}
		child.Check.Enabled, child.Run.Enabled = false, false
		if err := store.InsertUpdatePolicy(ctx, s.db, &child); err != nil {
			return nil, err
		}
		out = append(out, ManagedTarget{Policy: child})
	}
	slices.SortFunc(out, func(a, b ManagedTarget) int { return strings.Compare(a.Policy.ID, b.Policy.ID) })
	return out, nil
}

// standaloneTargets lists the target keys of an online environment's
// Docker Manager-managed standalone containers: the covered ones and the
// excluded ones.
func (s *Service) standaloneTargets(ctx context.Context, p domain.UpdateSetup, envID string) (found, excluded []string, err error) {
	containers, err := s.opts.Resources.ListContainers(ctx, envID)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range containers {
		if c.Stack != nil || protocol.LabelValue(c.Labels, protocol.LabelManaged) != protocol.ManagedStandalone ||
			s.opts.Resources.ContainerProtection(c) != nil {
			continue
		}
		key := targetKey(envID, domain.UpdateTargetContainer, c.Name)
		if protocol.UpdateExcluded(c.Labels) || containerExcluded(p, envID, c.Name) {
			excluded = append(excluded, key)
			continue
		}
		m, _, err := s.opts.Resources.ManagedSpec(ctx, envID, c.Labels)
		if err != nil {
			return nil, nil, err
		}
		if m != nil {
			found = append(found, key)
		}
	}
	return found, excluded, nil
}

// containerExcluded reports whether the setup's exclusions name the
// container (environmentID/name).
func containerExcluded(p domain.UpdateSetup, env, name string) bool {
	return slices.Contains(p.ExcludeContainers, env+"/"+name)
}

// managedTarget explains an inactive record: excluded (by the setup, its
// environment being left out, or the container's label when it was seen)
// or missing.
func managedTarget(p domain.UpdateSetup, child domain.UpdatePolicy, seenExcluded bool) ManagedTarget {
	t := ManagedTarget{Policy: child}
	if !child.Inactive {
		return t
	}
	t.InactiveReason = domain.UpdateTargetMissing
	switch {
	case seenExcluded, slices.Contains(p.ExcludeEnvironments, child.EnvironmentID),
		child.TargetType == domain.UpdateTargetStack && slices.Contains(p.ExcludeStacks, child.TargetID),
		child.TargetType == domain.UpdateTargetContainer && containerExcluded(p, child.EnvironmentID, child.TargetID):
		t.InactiveReason = domain.UpdateTargetExcluded
	}
	return t
}

// StackLabel is a stack's name as users know it: its display name when
// set, else its Compose project name.
func StackLabel(st domain.Stack) string {
	if n := strings.TrimSpace(st.DisplayName); n != "" {
		return n
	}
	return st.Name
}

// Target record names.
const (
	targetNamePrefix = "Automatic updates for "
	maxPolicyName    = 100
)

// targetPolicyName is the i-th name of a target record: "Automatic
// updates for zerobyte", then "... zerobyte (stack)" when another policy
// of the environment holds it, then "... zerobyte (stack 2)", ... Names
// never contain IDs.
func targetPolicyName(label string, typ domain.UpdateTargetType, i int) string {
	suffix := ""
	switch {
	case i == 1:
		suffix = " (" + string(typ) + ")"
	case i > 1:
		suffix = fmt.Sprintf(" (%s %d)", typ, i)
	}
	return targetNamePrefix + truncateName(label, maxPolicyName-utf8.RuneCountInString(targetNamePrefix+suffix)) + suffix
}

// fitsTargetName reports whether name is one of label's record names.
func fitsTargetName(name, label string, typ domain.UpdateTargetType) bool {
	if name == targetPolicyName(label, typ, 0) || name == targetPolicyName(label, typ, 1) {
		return true
	}
	marker := " (" + string(typ) + " "
	i := strings.LastIndex(name, marker)
	if i < 0 || !strings.HasSuffix(name, ")") || i+len(marker) >= len(name)-1 {
		return false
	}
	n, err := strconv.Atoi(name[i+len(marker) : len(name)-1])
	return err == nil && n > 1 && name == targetPolicyName(label, typ, n)
}

// policyNames tracks the policy names taken per environment (a name is
// unique per environment) during one reconciliation.
type policyNames struct {
	s *Service
	// taken maps environment -> name key -> policy ID.
	taken map[string]map[string]string
}

func (n *policyNames) in(ctx context.Context, env string) (map[string]string, error) {
	if t, ok := n.taken[env]; ok {
		return t, nil
	}
	t := map[string]string{}
	after := ""
	for {
		page, err := store.ListUpdatePolicies(ctx, n.s.db, env, after, 500)
		if err != nil {
			return nil, err
		}
		for _, p := range page {
			t[store.NameKey(p.Name)] = p.ID
		}
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].ID
	}
	n.taken[env] = t
	return t, nil
}

// childName returns the name a target record should have: its current
// one while it still fits its target's label, else the first free name for
// the label. Without a label (a deleted stack) the current name stays,
// unless it contains the record's ID (names of earlier versions): that one
// becomes "Automatic updates for a removed stack".
func (s *Service) childName(ctx context.Context, names *policyNames, child domain.UpdatePolicy, label string) (string, error) {
	if label == "" {
		if child.TargetType == domain.UpdateTargetContainer {
			label = child.TargetID
		} else if st, err := s.opts.Stacks.Get(ctx, child.TargetID); err == nil {
			label = StackLabel(st)
		} else if !errors.Is(err, domain.ErrStackNotFound) {
			return "", err
		}
	}
	fallback := false
	if label == "" {
		if child.Name != "" && !strings.Contains(child.Name, child.ID) {
			return child.Name, nil
		}
		label, fallback = "a removed "+string(child.TargetType), true
	}
	if child.Name != "" && !fallback && fitsTargetName(child.Name, label, child.TargetType) {
		return child.Name, nil
	}
	taken, err := names.in(ctx, child.EnvironmentID)
	if err != nil {
		return "", err
	}
	for i := 0; ; i++ {
		if fallback && i == 1 {
			continue
		}
		name := targetPolicyName(label, child.TargetType, i)
		if owner, ok := taken[store.NameKey(name)]; ok && owner != child.ID {
			continue
		}
		if child.Name != "" {
			delete(taken, store.NameKey(child.Name))
		}
		taken[store.NameKey(name)] = child.ID
		return name, nil
	}
}

func windowsEqual(a, b *domain.UpdateWindow) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Start == b.Start && a.End == b.End && slices.Equal(a.Days, b.Days)
}

// TargetPreview presents one covered target's current candidate plan.
type TargetPreview struct {
	Policy  domain.UpdatePolicy
	Preview domain.UpdatePreview
}

// SetupPreview is the combined plan of the setup. Its fingerprint changes
// if any target's candidates or applied source changes.
type SetupPreview struct {
	Fingerprint string
	Targets     []TargetPreview
}

func (s *Service) previewSetup(ctx context.Context, p domain.UpdateSetup, permit Permit) (SetupPreview, error) {
	children, err := s.syncSetup(ctx, p, permit)
	if err != nil {
		return SetupPreview{}, err
	}
	out := SetupPreview{Targets: make([]TargetPreview, 0, len(children))}
	h := sha256.New()
	for _, child := range children {
		preview, err := s.Preview(ctx, child.ID, nil)
		if err != nil {
			return out, err
		}
		out.Targets = append(out.Targets, TargetPreview{Policy: child, Preview: preview})
		_, _ = fmt.Fprintf(h, "%s:%s\n", child.ID, preview.Fingerprint)
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

// PreviewSetup previews every covered target without pulling images
// (permit: see reconcile).
func (s *Service) PreviewSetup(ctx context.Context, permit Permit) (SetupPreview, error) {
	p, err := s.Setup(ctx)
	if err != nil {
		return SetupPreview{}, err
	}
	return s.previewSetup(ctx, p, permit)
}

// CheckSetup enqueues a registry check for every currently covered stack
// and Docker Manager-managed standalone container (permit: see reconcile).
func (s *Service) CheckSetup(ctx context.Context, principal authz.Principal, key string, permit Permit) ([]domain.Job, error) {
	p, err := s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	children, err := s.syncSetup(ctx, p, permit)
	if err != nil {
		return nil, err
	}
	reqs := make([]jobs.Request, 0, len(children))
	for _, child := range children {
		req := checkRequest(child)
		req.PolicyID = p.ID
		reqs = append(reqs, req)
	}
	if err := tooMany(reqs); err != nil {
		return nil, err
	}
	return s.enqueueAll(ctx, principal, key, reqs)
}

// tooMany refuses a manual start beyond the scheduler's bound of one run,
// so manual and scheduled runs agree.
func tooMany(reqs []jobs.Request) error {
	if len(reqs) > scheduler.MaxJobsPerRun {
		return &domain.FieldError{Field: "excludeEnvironments",
			Message: fmt.Sprintf("%d targets; one run handles at most %d: leave some out", len(reqs), scheduler.MaxJobsPerRun)}
	}
	return nil
}

// rollbackTimeout bounds cancelling one job of a start that failed part
// way.
const rollbackTimeout = 10 * time.Second

// enqueueAll enqueues reqs for principal (job keys key#i) all or none:
// when one fails, the jobs already queued are cancelled (also when the
// request was cancelled), and the key of such a start is refused
// afterwards (replaying it would report the cancelled jobs).
func (s *Service) enqueueAll(ctx context.Context, principal authz.Principal, key string, reqs []jobs.Request) ([]domain.Job, error) {
	out := make([]domain.Job, 0, len(reqs))
	for i, req := range reqs {
		req.Principal, req.IdempotencyKey = principal, jobKey(key, i)
		job, created, err := s.opts.Jobs.Enqueue(ctx, req)
		if err == nil && !created && job.State.Terminal() {
			err = domain.ErrJobIdempotencyConflict
		}
		if err != nil {
			// The request may be cancelled already: cancel what was
			// queued regardless, all at once, each within a bound of its
			// own (so the rollback takes about one bound, and one slow
			// cancel does not skip the others).
			base := context.WithoutCancel(ctx)
			var wg sync.WaitGroup
			for _, j := range out {
				wg.Go(func() {
					cctx, cancel := context.WithTimeout(base, rollbackTimeout)
					defer cancel()
					if _, cerr := s.opts.Jobs.Cancel(cctx, j.ID); cerr != nil && !errors.Is(cerr, domain.ErrJobFinished) {
						s.log.Warn("could not cancel a job of a failed start", "job_id", j.ID, "error", cerr)
					}
				})
			}
			wg.Wait()
			return nil, err
		}
		out = append(out, job)
	}
	return out, nil
}

// jobKey is the idempotency key of the i-th job of a request with key
// ("" without one).
func jobKey(key string, i int) string {
	if key == "" {
		return ""
	}
	return fmt.Sprintf("%s#%d", key, i)
}

// RunSetup enqueues every runnable target after confirming the
// fingerprint returned by PreviewSetup (permit: see reconcile).
func (s *Service) RunSetup(ctx context.Context, principal authz.Principal, expected, key string, permit Permit) ([]domain.Job, error) {
	p, err := s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	preview, err := s.previewSetup(ctx, p, permit)
	if err != nil {
		return nil, err
	}
	if expected == "" || preview.Fingerprint != expected {
		return nil, &domain.UpdateError{Code: domain.UpdateErrPreviewStale, Message: "the update plan changed; preview again"}
	}
	var reqs []jobs.Request
	for _, target := range preview.Targets {
		pl, err := s.planRun(ctx, target.Policy, nil)
		if err != nil {
			return nil, err
		}
		// A stack with undeployed changes waits until it is deployed (an
		// update never deploys an edit); the other targets go ahead.
		if pl.drift || len(pl.items) == 0 {
			continue
		}
		req, err := s.request(ctx, pl)
		if err != nil {
			return nil, err
		}
		req.PolicyID = p.ID
		reqs = append(reqs, req)
	}
	if len(reqs) == 0 {
		return nil, &domain.UpdateError{Code: domain.UpdateErrNoCandidates, Message: "no checked target has a new digest"}
	}
	if err := tooMany(reqs); err != nil {
		return nil, err
	}
	return s.enqueueAll(ctx, principal, key, reqs)
}
