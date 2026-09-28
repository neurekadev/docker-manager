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
	"unicode/utf8"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// NewEnvironmentPolicy is the settings of an all-environments or a single
// environment update policy. Empty EnvironmentID means all environments.
type NewEnvironmentPolicy struct {
	EnvironmentID      string
	Name               string
	ExcludeStacks      []string
	ExcludeContainers  []string
	Check              *domain.UpdateSchedule
	Run                *domain.UpdateSchedule
	Window             *domain.UpdateWindow
	WaitTimeoutSeconds int
}

// ListEnvironmentPolicies returns the user-configured update policies.
func (s *Service) ListEnvironmentPolicies(ctx context.Context) ([]domain.EnvironmentUpdatePolicy, error) {
	return store.ListEnvironmentUpdatePolicies(ctx, s.db)
}

// GetEnvironmentPolicy returns one user-configured update policy.
func (s *Service) GetEnvironmentPolicy(ctx context.Context, id string) (domain.EnvironmentUpdatePolicy, error) {
	return store.GetEnvironmentUpdatePolicy(ctx, s.db, id)
}

func validExclusions(field string, values []string, global bool) ([]string, error) {
	if len(values) > 256 {
		return nil, fieldErr(field, "at most 256 exclusions")
	}
	out := slices.Clone(values)
	for _, value := range out {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return nil, fieldErr(field, "invalid exclusion %q", value)
		}
		if field == "excludeContainers" {
			if global && !strings.Contains(value, "/") {
				return nil, fieldErr(field, "global container exclusions must be environmentID/containerName")
			}
			if !global && strings.Contains(value, "/") {
				return nil, fieldErr(field, "single-environment container exclusions use container names")
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (s *Service) validateEnvironmentPolicy(ctx context.Context, p *domain.EnvironmentUpdatePolicy) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 100 {
		return fieldErr("name", "must be 1 to 100 characters")
	}
	if p.EnvironmentID != "" {
		env, err := s.opts.Environments.GetEnvironment(ctx, p.EnvironmentID)
		if err != nil {
			return err
		}
		if env.Status == domain.EnvironmentArchived {
			return fieldErr("environmentId", "archived environments cannot be configured")
		}
	}
	var err error
	if p.ExcludeStacks, err = validExclusions("excludeStacks", p.ExcludeStacks, p.EnvironmentID == ""); err != nil {
		return err
	}
	if p.ExcludeContainers, err = validExclusions("excludeContainers", p.ExcludeContainers, p.EnvironmentID == ""); err != nil {
		return err
	}
	if p.WaitTimeoutSeconds < 0 || p.WaitTimeoutSeconds > 3600 {
		return fieldErr("waitTimeoutSeconds", "must be between 0 and 3600")
	}
	if err := validWindow(p.Window); err != nil {
		return err
	}
	if err := validSchedule("checkSchedule", p.Check); err != nil {
		return err
	}
	return validSchedule("runSchedule", p.Run)
}

func (s *Service) scopeAvailable(ctx context.Context, db bun.IDB, env, except string) error {
	policies, err := store.ListEnvironmentUpdatePolicies(ctx, db)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.ID != except && (env == "" || p.EnvironmentID == "" || p.EnvironmentID == env) {
			return domain.ErrUpdateScopeOverlap
		}
	}
	legacy, err := store.ListUpdatePolicies(ctx, db, "", "", 0)
	if err != nil {
		return err
	}
	for _, p := range legacy {
		if p.ParentID == "" && (env == "" || p.EnvironmentID == env) {
			return domain.ErrUpdateScopeOverlap
		}
	}
	return nil
}

// CreateEnvironmentPolicy stores one non-overlapping scope. New policies do
// not check or run automatically until their schedules are enabled.
func (s *Service) CreateEnvironmentPolicy(ctx context.Context, in NewEnvironmentPolicy) (domain.EnvironmentUpdatePolicy, error) {
	check, err := s.defaultSchedule(ctx, scheduler.KindUpdateCheck, in.Check)
	if err != nil {
		return domain.EnvironmentUpdatePolicy{}, err
	}
	run, err := s.defaultSchedule(ctx, scheduler.KindUpdateRun, in.Run)
	if err != nil {
		return domain.EnvironmentUpdatePolicy{}, err
	}
	p := domain.EnvironmentUpdatePolicy{ID: ids.New(), EnvironmentID: in.EnvironmentID, Name: in.Name,
		ExcludeStacks: in.ExcludeStacks, ExcludeContainers: in.ExcludeContainers, Check: check, Run: run,
		Window: in.Window, WaitTimeoutSeconds: in.WaitTimeoutSeconds, Revision: 1}
	if err := s.validateEnvironmentPolicy(ctx, &p); err != nil {
		return p, err
	}
	p.CreatedAt, p.UpdatedAt = s.now(), s.now()
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.scopeAvailable(ctx, tx, p.EnvironmentID, ""); err != nil {
			return err
		}
		return store.InsertEnvironmentUpdatePolicy(ctx, tx, p)
	})
	if err == nil {
		s.notify()
	}
	return p, err
}

// UpdateEnvironmentPolicy replaces the settings of one scope after a
// revision check. The scope itself is immutable.
func (s *Service) UpdateEnvironmentPolicy(ctx context.Context, id string, revision int64, in NewEnvironmentPolicy) (domain.EnvironmentUpdatePolicy, error) {
	p, err := store.GetEnvironmentUpdatePolicy(ctx, s.db, id)
	if err != nil {
		return p, err
	}
	if p.Revision != revision {
		return p, domain.ErrRevisionMismatch
	}
	if in.EnvironmentID != p.EnvironmentID {
		return p, fieldErr("environmentId", "create another policy to change scope")
	}
	check, err := s.defaultSchedule(ctx, scheduler.KindUpdateCheck, in.Check)
	if err != nil {
		return p, err
	}
	run, err := s.defaultSchedule(ctx, scheduler.KindUpdateRun, in.Run)
	if err != nil {
		return p, err
	}
	p.Name, p.ExcludeStacks, p.ExcludeContainers = in.Name, in.ExcludeStacks, in.ExcludeContainers
	p.Check, p.Run, p.Window, p.WaitTimeoutSeconds = check, run, in.Window, in.WaitTimeoutSeconds
	if err := s.validateEnvironmentPolicy(ctx, &p); err != nil {
		return p, err
	}
	p.Revision, p.UpdatedAt = revision+1, s.now()
	if err := store.UpdateEnvironmentUpdatePolicy(ctx, s.db, p, revision); err != nil {
		return p, err
	}
	s.notify()
	return p, nil
}

// DeleteEnvironmentPolicy removes the scope and its managed target policies.
func (s *Service) DeleteEnvironmentPolicy(ctx context.Context, id string, revision int64) error {
	p, err := store.GetEnvironmentUpdatePolicy(ctx, s.db, id)
	if err != nil {
		return err
	}
	if p.Revision != revision {
		return domain.ErrRevisionMismatch
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		children, err := store.UpdatePoliciesForParent(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := store.DeleteUpdatePolicy(ctx, tx, child.ID, child.Revision); err != nil {
				return err
			}
		}
		return store.DeleteEnvironmentUpdatePolicy(ctx, tx, id, revision)
	})
	if err == nil {
		s.notify()
	}
	return err
}

// ManagedTarget is a target record of an environment policy and, when it
// is inactive, why the policy no longer covers it.
type ManagedTarget struct {
	Policy domain.UpdatePolicy
	// InactiveReason is domain.UpdateTargetExcluded or
	// domain.UpdateTargetMissing for an inactive record, "" otherwise.
	InactiveReason string
}

// ManagedPolicies returns the target records of an environment policy
// (inactive ones included, with their reason), after reconciling them.
func (s *Service) ManagedPolicies(ctx context.Context, id string) ([]ManagedTarget, error) {
	p, err := s.GetEnvironmentPolicy(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.reconcile(ctx, p)
}

// syncEnvironmentPolicy reconciles the target records of a policy and
// returns the active ones.
func (s *Service) syncEnvironmentPolicy(ctx context.Context, p domain.EnvironmentUpdatePolicy) ([]domain.UpdatePolicy, error) {
	all, err := s.reconcile(ctx, p)
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

// reconcile discovers targets and keeps their records in step with a
// policy: activity (exclusions and vanished targets deactivate a record
// without erasing its history), schedules, window, wait timeout and the
// record's name, which follows the target's current name ("Automatic
// updates for zerobyte"; records that earlier versions named after their
// ID are renamed here, so no migration is needed).
func (s *Service) reconcile(ctx context.Context, p domain.EnvironmentUpdatePolicy) ([]ManagedTarget, error) {
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return nil, err
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
		if p.EnvironmentID != "" && p.EnvironmentID != env.ID {
			continue
		}
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
		if !env.Online || s.opts.Resources == nil {
			for key, child := range byTarget {
				if child.EnvironmentID == env.ID && child.TargetType == domain.UpdateTargetContainer && !child.Inactive {
					if containerExcluded(p, env.ID, child.TargetID) {
						continue
					}
					wanted[key] = true
				}
			}
			continue
		}
		containers, err := s.opts.Resources.ListContainers(ctx, env.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range containers {
			if c.Stack != nil || protocol.LabelValue(c.Labels, protocol.LabelManaged) != protocol.ManagedStandalone ||
				s.opts.Resources.ContainerProtection(c) != nil {
				continue
			}
			key := targetKey(env.ID, domain.UpdateTargetContainer, c.Name)
			if protocol.UpdateExcluded(c.Labels) || containerExcluded(p, env.ID, c.Name) {
				excluded[key] = true
				continue
			}
			m, _, err := s.opts.Resources.ManagedSpec(ctx, env.ID, c.Labels)
			if err != nil {
				return nil, err
			}
			if m != nil {
				wanted[key] = true
			}
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

// containerExcluded reports whether the policy's exclusions name the
// container (environmentID/name for all-environments policies).
func containerExcluded(p domain.EnvironmentUpdatePolicy, env, name string) bool {
	if p.EnvironmentID == "" {
		name = env + "/" + name
	}
	return slices.Contains(p.ExcludeContainers, name)
}

// managedTarget explains an inactive record: excluded (by the policy, or
// by the container's label when it was seen) or missing.
func managedTarget(p domain.EnvironmentUpdatePolicy, child domain.UpdatePolicy, seenExcluded bool) ManagedTarget {
	t := ManagedTarget{Policy: child}
	if !child.Inactive {
		return t
	}
	t.InactiveReason = domain.UpdateTargetMissing
	switch {
	case seenExcluded,
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

// EnvironmentTargetPreview presents one managed target's current candidate
// plan within an environment policy.
type EnvironmentTargetPreview struct {
	Policy  domain.UpdatePolicy
	Preview domain.UpdatePreview
}

// EnvironmentPreview is the combined plan of an environment policy. Its
// fingerprint changes if any child's candidates or applied source changes.
type EnvironmentPreview struct {
	Fingerprint string
	Targets     []EnvironmentTargetPreview
}

func (s *Service) previewEnvironment(ctx context.Context, p domain.EnvironmentUpdatePolicy) (EnvironmentPreview, error) {
	children, err := s.syncEnvironmentPolicy(ctx, p)
	if err != nil {
		return EnvironmentPreview{}, err
	}
	out := EnvironmentPreview{Targets: make([]EnvironmentTargetPreview, 0, len(children))}
	h := sha256.New()
	for _, child := range children {
		preview, err := s.Preview(ctx, child.ID, nil)
		if err != nil {
			return out, err
		}
		out.Targets = append(out.Targets, EnvironmentTargetPreview{Policy: child, Preview: preview})
		_, _ = fmt.Fprintf(h, "%s:%s\n", child.ID, preview.Fingerprint)
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

// PreviewEnvironment previews every active target without pulling images.
func (s *Service) PreviewEnvironment(ctx context.Context, id string) (EnvironmentPreview, error) {
	p, err := s.GetEnvironmentPolicy(ctx, id)
	if err != nil {
		return EnvironmentPreview{}, err
	}
	return s.previewEnvironment(ctx, p)
}

// CheckEnvironment enqueues a registry check for every currently covered
// stack and Docker Manager-managed standalone container.
func (s *Service) CheckEnvironment(ctx context.Context, principal authz.Principal, id, key string) ([]domain.Job, error) {
	p, err := s.GetEnvironmentPolicy(ctx, id)
	if err != nil {
		return nil, err
	}
	children, err := s.syncEnvironmentPolicy(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Job, 0, len(children))
	for i, child := range children {
		req := checkRequest(child)
		req.PolicyID = p.ID
		req.Principal, req.IdempotencyKey = principal, fmt.Sprintf("%s#%d", key, i)
		job, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			return out, err
		}
		out = append(out, job)
	}
	return out, nil
}

// RunEnvironment enqueues every runnable target after confirming the
// fingerprint returned by PreviewEnvironment.
func (s *Service) RunEnvironment(ctx context.Context, principal authz.Principal, id, expected, key string) ([]domain.Job, error) {
	p, err := s.GetEnvironmentPolicy(ctx, id)
	if err != nil {
		return nil, err
	}
	preview, err := s.previewEnvironment(ctx, p)
	if err != nil {
		return nil, err
	}
	if expected == "" || preview.Fingerprint != expected {
		return nil, &domain.UpdateError{Code: domain.UpdateErrPreviewStale, Message: "the environment update plan changed; preview again"}
	}
	var reqs []jobs.Request
	for _, target := range preview.Targets {
		pl, err := s.planRun(ctx, target.Policy, nil)
		if err != nil {
			return nil, err
		}
		if pl.drift {
			return nil, &domain.UpdateError{Code: domain.UpdateErrSourceDrift, Message: "a stack has undeployed changes; deploy it before updating"}
		}
		if len(pl.items) == 0 {
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
	out := make([]domain.Job, 0, len(reqs))
	for i, req := range reqs {
		req.Principal, req.IdempotencyKey = principal, fmt.Sprintf("%s#%d", key, i)
		job, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			return out, err
		}
		out = append(out, job)
	}
	return out, nil
}
