package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Environment migration: every chosen stack of an environment moves to
// another environment (environment.migrate). Stacks linked by a network or
// volume that one creates and another joins as external form a group; a
// group stops together (so shared data is consistent and nothing runs
// against a half-moved group) and its stacks then move one after the
// other as stack.migrate jobs, the creating stack first, so what the next
// one joins already exists on the destination. Networks made on the
// source outside any stack that a moving stack joins are created on the
// destination first. When a stack does not move, the run stops: that
// stack is back on its source (its own migration's compensation), the
// group's other stacks still on the source start again (start_group) and
// the stacks already moved stay on the destination. Running it again moves
// what is left.

// Environment finding codes.
const (
	FindingNoStacks            = "no_stacks"
	FindingNetworkCreated      = "network_created"
	FindingDependencyCycle     = "dependency_cycle"
	FindingNetworkNotCreatable = "network_not_creatable"
	FindingNetworkDenied       = "network_create_denied"
)

// Reasons a stack of the environment is left out.
const (
	// SkipDockerManager: Docker Manager's own stack moves with the manager.
	SkipDockerManager = "docker_manager"
	// SkipNotPermitted: the caller may not migrate it to the destination.
	SkipNotPermitted = "not_permitted"
	// SkipNotSelected: the request names other stacks.
	SkipNotSelected = "not_selected"
)

// Job error classes of environment migrations.
const (
	ClassStackNotMoved   = "stack_not_moved"
	ClassNetworkFailed   = "network_create_failed"
	ClassEnvironmentMove = "environment_changed"
)

// EnvironmentRequest is an environment migration preview or start.
type EnvironmentRequest struct {
	TargetEnvironmentID string
	// Stacks limits the migration to these stacks (empty: every stack of
	// the environment the caller may migrate).
	Stacks []string
	// TimeoutSeconds is the stop grace period of the source's containers.
	TimeoutSeconds int
	IdempotencyKey string
}

// EnvironmentStack is one stack's place and plan in an environment
// migration.
type EnvironmentStack struct {
	StackID string
	Name    string
	// Group is the index of its group in EnvironmentPlan.Groups.
	Group int
	// DependsOn are the stacks of its group that move before it (it joins
	// a network or volume they create).
	DependsOn []string
	// Plan is the stack's own preview, with the external networks and
	// volumes that stacks moving before it (or the migration itself)
	// create no longer counted as missing.
	Plan Plan
}

// SkippedStack is a stack of the environment that does not move.
type SkippedStack struct {
	StackID string
	Name    string
	Reason  string
}

// NetworkCreation is a network the migration creates on the destination
// before any stack moves.
type NetworkCreation struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Internal   bool              `json:"internal,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	// UsedBy are the stacks that join it.
	UsedBy []string `json:"usedBy"`
}

// EnvironmentPlan is an environment migration's preview.
type EnvironmentPlan struct {
	SourceEnvironmentID string
	TargetEnvironmentID string
	// Stacks are the stacks that move, in the order they move.
	Stacks []EnvironmentStack
	// Groups are the stack IDs of each group in their order.
	Groups   [][]string
	Networks []NetworkCreation
	Skipped  []SkippedStack
	// Blockers and Warnings concern the whole migration; each stack's
	// plan has its own.
	Blockers []Finding
	Warnings []Finding
	// Data sums the stacks' transfers against the destination's space.
	Data DataPlan
	// Downtime: EstimatedSeconds is the longest a group is down (its
	// stacks stop together and start when each has moved); Basis also
	// states the whole migration's duration.
	Downtime Downtime
}

// Allowed reports whether the migration can start: it moves at least one
// stack and neither it nor any stack's plan has blockers.
func (p EnvironmentPlan) Allowed() bool {
	if len(p.Blockers) > 0 || len(p.Stacks) == 0 {
		return false
	}
	for _, s := range p.Stacks {
		if !s.Plan.Allowed() {
			return false
		}
	}
	return true
}

// EnvironmentBlockedError is ErrBlocked with the environment plan.
type EnvironmentBlockedError struct{ Plan EnvironmentPlan }

func (e *EnvironmentBlockedError) Error() string {
	var codes []string
	for _, b := range e.Plan.Blockers {
		codes = append(codes, b.Code)
	}
	for _, s := range e.Plan.Stacks {
		for _, b := range s.Plan.Blockers {
			codes = append(codes, s.Name+":"+b.Code)
		}
	}
	return ErrBlocked.Error() + ": " + strings.Join(codes, ", ")
}

// Unwrap returns ErrBlocked.
func (e *EnvironmentBlockedError) Unwrap() error { return ErrBlocked }

// envEntry is one stack of the environment with its preview.
type envEntry struct {
	stack   domain.Stack
	skipped string
	g       gathered
}

// planEnvironment computes an environment migration's plan from the
// stacks' previews (pure). sourceNetworks are the source's networks
// (nil: unknown); canCreateNetworks says whether the caller may create
// networks on the destination.
func planEnvironment(source, target string, entries []envEntry, sourceNetworks []protocol.NetworkInfo, canCreateNetworks bool) EnvironmentPlan {
	p := EnvironmentPlan{SourceEnvironmentID: source, TargetEnvironmentID: target, Stacks: []EnvironmentStack{}, Groups: [][]string{},
		Networks: []NetworkCreation{}, Skipped: []SkippedStack{}, Blockers: []Finding{}, Warnings: []Finding{}}
	byID := map[string]envEntry{}
	var links []stackLinks
	for _, e := range entries {
		switch {
		case e.skipped != "":
			p.Skipped = append(p.Skipped, SkippedStack{StackID: e.stack.ID, Name: e.stack.Name, Reason: e.skipped})
			continue
		case e.g.source != nil && e.g.source.Project != nil && e.g.source.Project.Protected:
			p.Skipped = append(p.Skipped, SkippedStack{StackID: e.stack.ID, Name: e.stack.Name, Reason: SkipDockerManager})
			continue
		}
		byID[e.stack.ID] = e
		l := stackLinks{ID: e.stack.ID, Name: e.stack.Name}
		if e.g.source != nil && e.g.source.Project != nil {
			l = linksOf(e.stack.ID, e.g.source.Project)
			l.Name = e.stack.Name
		}
		links = append(links, l)
	}
	if source == target {
		p.block(FindingSameEnvironment, "the destination is the environment itself")
		return p
	}
	if len(links) == 0 {
		p.block(FindingNoStacks, "the environment has no stack to migrate")
		return p
	}
	order := orderStacks(links)
	p.Groups = order.Groups
	for _, c := range order.Cycles {
		var names []string
		for _, id := range c {
			names = append(names, byID[id].stack.Name)
		}
		p.warn(FindingDependencyCycle, "stacks %s join each other's networks or volumes: they move in name order and one may fail to start until the others are there",
			strings.Join(names, ", "))
	}

	// What the moving stacks create, and the networks made outside any
	// stack that the migration creates first.
	created := map[string]bool{}
	for _, l := range links {
		for _, n := range l.Networks {
			created["n:"+n] = true
		}
		for _, v := range l.Volumes {
			created["v:"+v] = true
		}
	}
	missing := map[string][]string{}
	var missingOrder []string
	for _, l := range links {
		for _, f := range byID[l.ID].g.plan.Blockers {
			if f.Code == FindingExternalNetwork && !created["n:"+f.Resource] {
				if _, ok := missing[f.Resource]; !ok {
					missingOrder = append(missingOrder, f.Resource)
				}
				if !slices.Contains(missing[f.Resource], l.ID) {
					missing[f.Resource] = append(missing[f.Resource], l.ID)
				}
			}
		}
	}
	slices.Sort(missingOrder)
	creatable := map[string]bool{}
	for _, name := range missingOrder {
		i := slices.IndexFunc(sourceNetworks, func(n protocol.NetworkInfo) bool { return n.Name == name })
		if i < 0 {
			continue
		}
		n := sourceNetworks[i]
		if n.Builtin || (n.Driver != "" && n.Driver != "bridge") || n.Protection != nil {
			p.block(FindingNetworkNotCreatable, "the network %s (driver %s) is made on the source by hand; create it on the destination first", name, n.Driver).Resource = name
			continue
		}
		if !canCreateNetworks {
			p.block(FindingNetworkDenied, "the network %s must be created on the destination, but you may not create networks there", name).Resource = name
			continue
		}
		creatable[name] = true
		p.Networks = append(p.Networks, NetworkCreation{Name: name, Driver: n.Driver, Internal: n.Internal, Attachable: n.Attachable,
			Labels: protocol.WithoutOwnLabels(n.Labels), UsedBy: missing[name]})
	}
	if len(p.Networks) > 0 {
		var names []string
		for _, n := range p.Networks {
			names = append(names, n.Name)
		}
		p.warn(FindingNetworkCreated, "the networks %s are created on the destination first, with Docker's default addresses (fixed subnets are not copied)",
			strings.Join(names, ", "))
	}

	// Walk the stacks in their order: what an earlier stack creates, or the
	// migration creates first, exists when a later one moves.
	landed := map[string]bool{}
	for name := range creatable {
		landed["n:"+name] = true
	}
	var longest, total int64
	var free *DataPlan
	for gi, group := range order.Groups {
		var groupDown int64
		for _, id := range group {
			e := byID[id]
			plan := e.g.plan
			plan.Blockers = slices.DeleteFunc(slices.Clone(plan.Blockers), func(f Finding) bool {
				return (f.Code == FindingExternalNetwork && landed["n:"+f.Resource]) || (f.Code == FindingExternalVolume && landed["v:"+f.Resource])
			})
			l := links[slices.IndexFunc(links, func(l stackLinks) bool { return l.ID == id })]
			for _, n := range l.Networks {
				landed["n:"+n] = true
			}
			for _, v := range l.Volumes {
				landed["v:"+v] = true
			}
			p.Stacks = append(p.Stacks, EnvironmentStack{StackID: id, Name: e.stack.Name, Group: gi, DependsOn: order.DependsOn[id], Plan: plan})
			p.Data.ProjectBytes += plan.Data.ProjectBytes
			p.Data.VolumeBytes += plan.Data.VolumeBytes
			p.Data.ImageBytes += plan.Data.ImageBytes
			p.Data.TotalBytes += plan.Data.TotalBytes
			p.Data.Truncated = p.Data.Truncated || plan.Data.Truncated
			if e.g.target != nil && free == nil {
				free = &plan.Data
			}
			groupDown += plan.Downtime.EstimatedSeconds
		}
		longest = max(longest, groupDown)
		total += groupDown
	}
	p.Data.TargetStacksFree, p.Data.TargetVolumesFree = -1, -1
	if free != nil {
		p.Data.TargetStacksFree, p.Data.TargetVolumesFree = free.TargetStacksFree, free.TargetVolumesFree
		// Each stack's plan checks its own data; together they may not fit.
		if p.Data.TargetStacksFree >= 0 && p.Data.ProjectBytes > p.Data.TargetStacksFree {
			p.block(FindingInsufficientSpace, "the project directories need %s together but the destination's stacks volume has %s free",
				humanize.Bytes(p.Data.ProjectBytes), humanize.Bytes(p.Data.TargetStacksFree))
		}
		if p.Data.TargetVolumesFree >= 0 && p.Data.VolumeBytes+p.Data.ImageBytes > p.Data.TargetVolumesFree {
			p.block(FindingInsufficientSpace, "the volumes and images need %s together but the destination's Docker data root has %s free",
				humanize.Bytes(p.Data.VolumeBytes+p.Data.ImageBytes), humanize.Bytes(p.Data.TargetVolumesFree))
		}
	}
	p.Downtime = Downtime{EstimatedSeconds: longest, Basis: fmt.Sprintf(
		"The stacks of a group stop together and are down until each of them has moved: about %ds for the longest group. The whole migration takes about %ds; the other groups keep running until their turn.",
		longest, total)}
	sortFindings(p.Blockers)
	sortFindings(p.Warnings)
	return p
}

func (p *EnvironmentPlan) block(code, format string, args ...any) *Finding {
	p.Blockers = append(p.Blockers, Finding{Code: code, Message: fmt.Sprintf(format, args...)})
	return &p.Blockers[len(p.Blockers)-1]
}

func (p *EnvironmentPlan) warn(code, format string, args ...any) *Finding {
	p.Warnings = append(p.Warnings, Finding{Code: code, Message: fmt.Sprintf(format, args...)})
	return &p.Warnings[len(p.Warnings)-1]
}

// environmentAccess is what principal p may do in an environment
// migration to target.
type environmentAccess struct {
	stack    func(st domain.Stack) bool
	networks bool
}

func (s *Service) environmentAccess(ctx context.Context, p authz.Principal, target string) environmentAccess {
	if p.IsService() {
		return environmentAccess{stack: func(domain.Stack) bool { return true }, networks: true}
	}
	c := authz.For(ctx, s.opts.Authorizer, p)
	return environmentAccess{
		stack: func(st domain.Stack) bool {
			if !c.Can("stack.migrate", authz.Resource{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{}}).Allowed {
				return false
			}
			for _, ch := range DestinationStackCapabilities(st.ID, target) {
				if !c.Can(ch.Capability, ch.Resource).Allowed {
					return false
				}
			}
			return true
		},
		networks: c.Can("network.create", authz.InEnvironment(catalog.TypeNetwork, target)).Allowed,
	}
}

// PreviewEnvironment computes an environment migration's preview for
// caller p (owner: the instance owner, who sees every affected user).
func (s *Service) PreviewEnvironment(ctx context.Context, p authz.Principal, owner bool, source string, r EnvironmentRequest) (EnvironmentPlan, error) {
	plan, entries, err := s.previewEnvironment(ctx, p, source, r, true)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	for i := range plan.Stacks {
		e := entries[plan.Stacks[i].StackID]
		if e.g.source != nil && e.g.source.Project != nil {
			checks := stackChecks(s.cat, e.stack.ID, source, r.TargetEnvironmentID, e.g.source.Project, e.g.plan.Volumes)
			plan.Stacks[i].Plan.Access = accessPreview(ctx, s.opts.Permissions, p, owner, checks)
		}
	}
	return plan, nil
}

func (s *Service) previewEnvironment(ctx context.Context, p authz.Principal, source string, r EnvironmentRequest, measure bool) (EnvironmentPlan, map[string]envEntry, error) {
	src, err := s.envState(ctx, source, false)
	if err != nil {
		return EnvironmentPlan{}, nil, err
	}
	if _, err := s.envState(ctx, r.TargetEnvironmentID, true); err != nil {
		return EnvironmentPlan{}, nil, err
	}
	all, err := s.opts.Stacks.List(ctx, domain.StackFilter{EnvironmentID: source})
	if err != nil {
		return EnvironmentPlan{}, nil, err
	}
	slices.SortFunc(all, func(a, b domain.Stack) int { return strings.Compare(a.Name, b.Name) })
	access := s.environmentAccess(ctx, p, r.TargetEnvironmentID)
	entries := make([]envEntry, 0, len(all))
	byID := map[string]envEntry{}
	selected := map[string]bool{}
	for _, id := range r.Stacks {
		selected[id] = true
	}
	entries = entries[:len(all)]
	errs := make([]error, len(all))
	// The stacks' previews run a few at a time: a large environment is
	// checked in reasonable time without flooding either agent.
	sem := make(chan struct{}, previewParallelism)
	var wg sync.WaitGroup
	for i, st := range all {
		entries[i] = envEntry{stack: st}
		switch {
		case len(r.Stacks) > 0 && !selected[st.ID]:
			entries[i].skipped = SkipNotSelected
		case !access.stack(st):
			entries[i].skipped = SkipNotPermitted
		case source != r.TargetEnvironmentID:
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				entries[i].g, errs[i] = s.previewStack(ctx, st, StackRequest{TargetEnvironmentID: r.TargetEnvironmentID,
					TimeoutSeconds: r.TimeoutSeconds}, measure)
			}()
		}
	}
	wg.Wait()
	if err := firstError(errs); err != nil {
		return EnvironmentPlan{}, nil, err
	}
	for _, e := range entries {
		byID[e.stack.ID] = e
	}
	// The source's networks: which external networks were made by hand
	// (unknown when the list fails: those stay missing on the destination).
	var networks []protocol.NetworkInfo
	if src.online && source != r.TargetEnvironmentID {
		var out protocol.NetworkListOutput
		if err := s.call(ctx, source, protocol.ReqNetworkList, protocol.NetworkListInput{}, &out, s.opts.RequestTimeout); err != nil {
			s.log.Warn("could not list the source's networks for an environment migration", "environment_id", source, "error", err)
		}
		networks = out.Networks
	}
	return planEnvironment(source, r.TargetEnvironmentID, entries, networks, access.networks), byID, nil
}

// previewParallelism bounds the stack previews an environment preview runs
// at once.
const previewParallelism = 4

func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// environmentJobInput is the environment.migrate job input. The stacks the
// caller confirmed are the job's stack targets (any number: the input,
// the job output and the step journal stay small whatever the size of the
// environment; groups, networks and stopped services are in the
// environment_migrations record).
type environmentJobInput struct {
	Target         string `json:"target"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// targetStacks returns a job's stack targets, sorted.
func targetStacks(j domain.Job) []string {
	var out []string
	for _, t := range j.Targets {
		if t.Type == domain.TargetStack {
			out = append(out, t.ID)
		}
	}
	slices.Sort(out)
	return out
}

// StartEnvironment re-runs the preview and, without blockers, enqueues the
// environment.migrate job (the migration ID is the job ID).
func (s *Service) StartEnvironment(ctx context.Context, p authz.Principal, source string, r EnvironmentRequest) (domain.Job, domain.EnvironmentMigration, error) {
	plan, _, err := s.previewEnvironment(ctx, p, source, r, true)
	if err != nil {
		return domain.Job{}, domain.EnvironmentMigration{}, err
	}
	if !plan.Allowed() {
		return domain.Job{}, domain.EnvironmentMigration{}, &EnvironmentBlockedError{Plan: plan}
	}
	in := environmentJobInput{Target: r.TargetEnvironmentID, TimeoutSeconds: r.TimeoutSeconds}
	targets := make([]domain.JobTarget, 0, len(plan.Stacks))
	for _, st := range plan.Stacks {
		targets = append(targets, domain.JobTarget{Type: domain.TargetStack, ID: st.StackID})
	}
	j, created, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.EnvironmentMigrate, Principal: p, EnvironmentID: source,
		Targets: targets, Input: in, IdempotencyKey: r.IdempotencyKey})
	if err != nil {
		return domain.Job{}, domain.EnvironmentMigration{}, err
	}
	m := environmentRecord(j.ID, source, plan, s.now())
	if created {
		if err := s.ensureEnvironmentRecord(ctx, &m); err != nil {
			return j, m, err
		}
	} else if existing, err := store.GetEnvironmentMigration(ctx, s.db, j.ID); err == nil {
		m = existing
	}
	s.log.Info("environment migration queued", "migration_id", j.ID, "from", source, "to", r.TargetEnvironmentID, "stacks", len(plan.Stacks))
	return j, m, nil
}

func environmentRecord(id, source string, plan EnvironmentPlan, now time.Time) domain.EnvironmentMigration {
	m := domain.EnvironmentMigration{ID: id, SourceEnvironmentID: source, TargetEnvironmentID: plan.TargetEnvironmentID,
		State: domain.MigrationRunning, Groups: plan.Groups, CreatedAt: now, UpdatedAt: now}
	for _, st := range plan.Stacks {
		m.Stacks = append(m.Stacks, domain.EnvironmentMigrationStack{StackID: st.StackID, Name: st.Name, State: domain.EnvironmentStackPending})
	}
	for _, n := range plan.Networks {
		m.Networks = append(m.Networks, domain.EnvironmentNetwork{Name: n.Name, Driver: n.Driver, Internal: n.Internal, Attachable: n.Attachable,
			Labels: n.Labels})
	}
	return m
}

// ensureEnvironmentRecord inserts the record unless it exists (the
// executor and the request that queued its job may both create it).
func (s *Service) ensureEnvironmentRecord(ctx context.Context, m *domain.EnvironmentMigration) error {
	if _, err := store.GetEnvironmentMigration(ctx, s.db, m.ID); err == nil {
		return nil
	}
	err := store.InsertEnvironmentMigration(ctx, s.db, m)
	if _, gerr := store.GetEnvironmentMigration(ctx, s.db, m.ID); err != nil && gerr == nil {
		return nil // the other one created it first
	}
	return err
}

// GetEnvironmentMigration returns an environment migration (with each
// moved stack's source removal read from its stack migration).
func (s *Service) GetEnvironmentMigration(ctx context.Context, id string) (domain.EnvironmentMigration, error) {
	m, err := store.GetEnvironmentMigration(ctx, s.db, id)
	if err != nil {
		return m, err
	}
	s.withRemovals(ctx, &m)
	return m, nil
}

// EnvironmentMigrations lists the latest migrations away from an
// environment, newest first.
func (s *Service) EnvironmentMigrations(ctx context.Context, source string, limit int) ([]domain.EnvironmentMigration, error) {
	ms, err := store.ListEnvironmentMigrations(ctx, s.db, source, limit)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		s.withRemovals(ctx, &ms[i])
	}
	return ms, nil
}

func (s *Service) withRemovals(ctx context.Context, m *domain.EnvironmentMigration) {
	for i, st := range m.Stacks {
		if st.State != domain.EnvironmentStackMoved || st.MigrationID == "" {
			continue
		}
		if child, err := store.GetMigration(ctx, s.db, st.MigrationID); err == nil {
			m.Stacks[i].SourceRemoved = child.State == domain.MigrationSourceRemoved
		}
	}
}

func (s *Service) updateEnvironmentRecord(ctx context.Context, id string, fn func(m *domain.EnvironmentMigration)) error {
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		m, err := store.GetEnvironmentMigration(ctx, tx, id)
		if err != nil {
			return err
		}
		fn(&m)
		m.UpdatedAt = s.now()
		return store.UpdateEnvironmentMigration(ctx, tx, &m)
	})
}

// groupStartArgs are the start_group compensation's arguments: one stack
// the job stopped.
type groupStartArgs struct {
	MigrationID string              `json:"migrationId"`
	StackID     string              `json:"stackId"`
	Source      string              `json:"source"`
	Stack       protocol.ProjectRef `json:"stack"`
	Services    []string            `json:"services"`
}

func (s *Service) environmentExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.EnvironmentMigrate, Steps: map[string]jobexec.StepFunc{
		"prepare":         s.environmentPrepare,
		"create_networks": s.environmentNetworks,
		"migrate":         s.environmentMigrate,
		"finalize":        s.environmentFinalize,
	}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompStartGroup: s.startGroup}}
}

func environmentInput(sc *jobexec.StepContext) (environmentJobInput, error) {
	var in environmentJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed environment.migrate input: %w", err)
	}
	if in.Target == "" {
		return in, errors.New("incomplete environment.migrate input")
	}
	return in, nil
}

func (s *Service) environmentPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := environmentInput(sc)
	if err != nil {
		return err
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 2, "checking every stack and the destination")
	confirmed := targetStacks(j)
	if len(confirmed) == 0 {
		return errors.New("environment.migrate has no stack targets")
	}
	plan, _, err := s.previewEnvironment(ctx, principalOf(j), j.EnvironmentID, EnvironmentRequest{TargetEnvironmentID: in.Target,
		Stacks: confirmed, TimeoutSeconds: in.TimeoutSeconds}, false)
	if err != nil {
		return err
	}
	var moving []string
	for _, st := range plan.Stacks {
		moving = append(moving, st.StackID)
	}
	slices.Sort(moving)
	if !slices.Equal(moving, confirmed) {
		return &classed{class: ClassEnvironmentMove, err: errors.New("the environment's stacks changed since the migration was requested"),
			recovery: "Nothing was changed. Preview the migration again."}
	}
	if !plan.Allowed() {
		b := firstBlocker(plan)
		return &classed{class: ClassBlocked, err: fmt.Errorf("%s: %s", b.Code, b.Message),
			recovery: "Nothing was changed. Preview the migration again and resolve its blockers."}
	}
	m := environmentRecord(sc.JobID, j.EnvironmentID, plan, s.now())
	if err := s.ensureEnvironmentRecord(ctx, &m); err != nil {
		return err
	}
	return s.updateEnvironmentRecord(ctx, sc.JobID, func(r *domain.EnvironmentMigration) {
		r.Groups, r.Networks = m.Groups, m.Networks
	})
}

func firstBlocker(p EnvironmentPlan) Finding {
	if len(p.Blockers) > 0 {
		return p.Blockers[0]
	}
	for _, s := range p.Stacks {
		if len(s.Plan.Blockers) > 0 {
			b := s.Plan.Blockers[0]
			b.Message = s.Name + ": " + b.Message
			return b
		}
	}
	return Finding{Code: FindingNoStacks, Message: "the environment has no stack to migrate"}
}

// environmentNetworks creates the networks made by hand on the source
// that moving stacks join, before anything stops (a network.create job
// each on the destination, the initiator's).
func (s *Service) environmentNetworks(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := environmentInput(sc)
	if err != nil {
		return err
	}
	rec, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
	if err != nil {
		return err
	}
	if len(rec.Networks) == 0 {
		return nil
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	var existing protocol.NetworkListOutput
	if err := s.call(ctx, in.Target, protocol.ReqNetworkList, protocol.NetworkListInput{}, &existing, s.opts.RequestTimeout); err != nil {
		return &classed{class: ClassNetworkFailed, err: fmt.Errorf("list the destination's networks: %w", err),
			recovery: "Nothing was stopped. Check the destination environment's agent, then migrate again."}
	}
	for _, n := range rec.Networks {
		if slices.ContainsFunc(existing.Networks, func(e protocol.NetworkInfo) bool { return e.Name == n.Name }) {
			continue
		}
		sc.Progress(ctx, 5, "creating the network "+n.Name+" on the destination")
		nj, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.NetworkCreate, Principal: principalOf(j), EnvironmentID: in.Target,
			Targets:        []domain.JobTarget{{Type: domain.TargetNetwork, ID: n.Name}},
			Input:          protocol.NetworkCreateInput{Name: n.Name, Driver: n.Driver, Internal: n.Internal, Attachable: n.Attachable, Labels: n.Labels},
			IdempotencyKey: "environment-" + sc.JobID + "-network-" + n.Name})
		if err != nil {
			return &classed{class: ClassNetworkFailed, err: fmt.Errorf("create the network %s: %w", n.Name, err),
				recovery: "Nothing was stopped. Create the network on the destination by hand, then migrate again."}
		}
		done, err := s.waitJob(ctx, nj.ID)
		if err != nil {
			return err
		}
		if done.State != domain.JobSucceeded {
			return &classed{class: ClassNetworkFailed, err: fmt.Errorf("the network %s was not created (job %s ended %s)", n.Name, nj.ID, done.State),
				recovery: "Nothing was stopped. See the network job for the cause, then migrate again."}
		}
		sc.Item(ctx, "network:"+n.Name, domain.ItemSucceeded, "created on the destination")
	}
	return nil
}

// runningServices returns the services of a project that run.
func (s *Service) runningServices(ctx context.Context, env, project string) ([]string, error) {
	var live protocol.ComposeServicesOutput
	if err := s.call(ctx, env, protocol.ReqComposeServices, protocol.ComposeServicesInput{ProjectName: project}, &live, s.opts.RequestTimeout); err != nil {
		return nil, err
	}
	running := []string{}
	for _, c := range live.Containers {
		if c.State == "running" && !c.OneOff && !slices.Contains(running, c.Service) {
			running = append(running, c.Service)
		}
	}
	slices.Sort(running)
	return running, nil
}

func (s *Service) environmentMigrate(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := environmentInput(sc)
	if err != nil {
		return err
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	p := principalOf(j)
	start, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
	if err != nil {
		return err
	}
	total := len(start.Stacks)
	done := 0
	for _, group := range start.Groups {
		rec, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
		if err != nil {
			return err
		}
		var pending []domain.Stack
		for _, id := range group {
			if e, ok := rec.Stack(id); ok && e.State == domain.EnvironmentStackMoved {
				done++
				continue
			}
			st, err := s.opts.Stacks.Get(ctx, id)
			if err != nil {
				return err
			}
			pending = append(pending, st)
		}
		if len(pending) == 0 {
			continue
		}
		if sc.CancelRequested() {
			return fmt.Errorf("before the group of %s: %w", pending[0].Name, jobexec.ErrStepCancelled)
		}
		// The whole group stops first, in reverse order (the stacks that
		// join the others' networks first).
		for i := len(pending) - 1; i >= 0; i-- {
			if err := s.stopGroupMember(ctx, sc, in, pending[i]); err != nil {
				return err
			}
		}
		for i, st := range pending {
			// This job holds no stack locks (each stack's own migration
			// does): what another job started meanwhile of the stacks still
			// waiting stops again, so a shared volume is not written while
			// it is copied.
			if i > 0 {
				if err := s.restop(ctx, sc, in, pending[i:]); err != nil {
					return err
				}
			}
			sc.Progress(ctx, 10+85*done/max(total, 1), "migrating "+st.Name)
			if err := s.moveGroupMember(ctx, sc, p, in, st); err != nil {
				return err
			}
			done++
		}
		// The group moved: nothing of it is started on the source again.
		if err := sc.ReleaseCompensation(ctx, jobspec.CompStartGroup); err != nil {
			return err
		}
	}
	return nil
}

// stopGroupMember stops a stack of the group on the source, after
// registering the compensation that starts what ran again.
func (s *Service) stopGroupMember(ctx context.Context, sc *jobexec.StepContext, in environmentJobInput, st domain.Stack) error {
	rec, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
	if err != nil {
		return err
	}
	if e, _ := rec.Stack(st.ID); e.Stopped {
		return nil
	}
	ref := protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name, ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
	running, err := s.runningServices(ctx, st.EnvironmentID, st.Name)
	if err != nil {
		return s.groupFailure(st.Name, err)
	}
	if err := sc.AddCompensation(ctx, jobspec.CompStartGroup, groupStartArgs{MigrationID: sc.JobID, StackID: st.ID, Source: st.EnvironmentID,
		Stack: ref, Services: running}); err != nil {
		return err
	}
	if err := s.updateEnvironmentRecord(ctx, sc.JobID, func(m *domain.EnvironmentMigration) {
		m.SetStack(st.ID, func(e *domain.EnvironmentMigrationStack) { e.Stopped, e.StoppedServices = true, running })
	}); err != nil {
		return err
	}
	sc.Progress(ctx, -1, fmt.Sprintf("stopping %s (%d services) on the source", st.Name, len(running)))
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: sc.JobID, Stack: ref,
		TimeoutSeconds: in.TimeoutSeconds}, &res, s.opts.StopTimeout); err != nil {
		return s.groupFailure(st.Name, err)
	}
	return nil
}

// restop stops the given stacks of a group again (the services recorded
// when the group stopped are the ones started again on failure).
func (s *Service) restop(ctx context.Context, sc *jobexec.StepContext, in environmentJobInput, stacks []domain.Stack) error {
	for i := len(stacks) - 1; i >= 0; i-- {
		st := stacks[i]
		ref := protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name, ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
		var res protocol.MigrationLifecycleOutput
		if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: sc.JobID, Stack: ref,
			TimeoutSeconds: in.TimeoutSeconds}, &res, s.opts.StopTimeout); err != nil {
			return s.groupFailure(st.Name, err)
		}
	}
	return nil
}

func (s *Service) groupFailure(name string, err error) error {
	rec := "The stacks of this group that were stopped are started again on the source; stacks moved before stay on the destination. "
	if errors.Is(err, jobs.ErrAgentOffline) || errors.Is(err, protocol.ErrRequestTimeout) {
		return &classed{class: domain.ErrorAgentOffline, err: fmt.Errorf("stop %s on the source: %w (%w)", name, jobexec.ErrStepInterrupted, err),
			recovery: "The source environment's agent is unavailable. " + rec + "Migrate again once it is back."}
	}
	return &classed{class: ClassSourceStopFailed, err: fmt.Errorf("stop %s on the source: %w", name, err),
		recovery: rec + "Check the stack's containers, then migrate again."}
}

// moveGroupMember runs a stack's own migration and waits for it.
func (s *Service) moveGroupMember(ctx context.Context, sc *jobexec.StepContext, p authz.Principal, in environmentJobInput, st domain.Stack) error {
	rec, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
	if err != nil {
		return err
	}
	e, _ := rec.Stack(st.ID)
	childID := e.MigrationID
	if childID == "" || e.State != domain.EnvironmentStackMoving {
		child, _, err := s.StartStack(ctx, p, st, StackRequest{TargetEnvironmentID: in.Target, TimeoutSeconds: in.TimeoutSeconds,
			IdempotencyKey: "environment-" + sc.JobID + "-" + st.ID})
		var be *BlockedError
		switch {
		case errors.As(err, &be):
			b := be.Plan.Blockers[0]
			s.markStack(ctx, sc.JobID, st.ID, "", domain.EnvironmentStackFailed)
			sc.Item(ctx, st.Name, domain.ItemFailed, b.Message)
			return &classed{class: ClassStackNotMoved, err: fmt.Errorf("%s: %s: %s", st.Name, b.Code, b.Message),
				recovery: "The stacks of this group still on the source are started again; stacks moved before stay on the destination. Fix the cause, then migrate again to move the rest."}
		case err != nil:
			return err
		}
		childID = child.ID
		s.markStack(ctx, sc.JobID, st.ID, childID, domain.EnvironmentStackMoving)
	}
	child, err := s.waitChild(ctx, sc, childID)
	if err != nil {
		return err
	}
	if child.State == domain.JobSucceeded {
		s.markStack(ctx, sc.JobID, st.ID, childID, domain.EnvironmentStackMoved)
		sc.Item(ctx, st.Name, domain.ItemSucceeded, "migrated (migration "+childID+")")
		return nil
	}
	s.markStack(ctx, sc.JobID, st.ID, childID, domain.EnvironmentStackFailed)
	sc.Item(ctx, st.Name, domain.ItemFailed, fmt.Sprintf("its migration (job %s) ended %s", childID, child.State))
	if child.State == domain.JobCancelled {
		return fmt.Errorf("the migration of %s was cancelled: %w", st.Name, jobexec.ErrStepCancelled)
	}
	msg := string(child.State)
	if child.ErrorClass != "" {
		msg += " (" + child.ErrorClass + ")"
	}
	return &classed{class: ClassStackNotMoved, err: fmt.Errorf("the migration of %s (job %s) ended %s", st.Name, childID, msg),
		recovery: st.Name + " is back on the source. The other stacks of its group still on the source are started again; stacks moved before stay on the destination. See its migration job for the cause, then migrate again to move the rest."}
}

func (s *Service) markStack(ctx context.Context, id, stackID, childID string, state domain.EnvironmentStackState) {
	if err := s.updateEnvironmentRecord(ctx, id, func(m *domain.EnvironmentMigration) {
		m.SetStack(stackID, func(e *domain.EnvironmentMigrationStack) {
			if childID != "" {
				e.MigrationID = childID
			}
			e.State = state
		})
	}); err != nil {
		s.log.Warn("could not record an environment migration's stack", "migration_id", id, "stack_id", stackID, "error", err)
	}
}

// waitChild waits for a stack migration; a cancellation of this job
// cancels it (its compensation puts the stack back) and waits for its end.
func (s *Service) waitChild(ctx context.Context, sc *jobexec.StepContext, id string) (domain.Job, error) {
	ch, cancel := s.opts.Jobs.Subscribe(id)
	defer cancel()
	t := s.clk.NewTicker(time.Second)
	defer t.Stop()
	cancelled := false
	for {
		j, err := s.opts.Jobs.Get(ctx, id)
		if err != nil {
			return j, err
		}
		if j.State.Terminal() {
			return j, nil
		}
		if !cancelled && sc.CancelRequested() {
			cancelled = true
			if _, err := s.opts.Jobs.Cancel(ctx, id); err != nil {
				s.log.Warn("could not cancel a stack migration of an environment migration", "migration_id", sc.JobID, "child", id, "error", err)
			}
		}
		select {
		case <-ch:
		case <-t.C():
		case <-ctx.Done():
			return j, ctx.Err()
		}
	}
}

func (s *Service) environmentFinalize(ctx context.Context, sc *jobexec.StepContext) error {
	rec, err := store.GetEnvironmentMigration(ctx, s.db, sc.JobID)
	if err != nil {
		return err
	}
	moved := 0
	for _, st := range rec.Stacks {
		if st.State == domain.EnvironmentStackMoved {
			moved++
		}
	}
	sc.Progress(ctx, 100, fmt.Sprintf("%d stacks migrated; their sources stay stopped until their removal is confirmed", moved))
	s.log.Info("environment migrated", "migration_id", sc.JobID, "from", rec.SourceEnvironmentID, "to", rec.TargetEnvironmentID, "stacks", moved)
	return nil
}

// startGroup is the start_group compensation: start the services the job
// stopped of a stack that is still on the source. A stack whose own
// migration still runs is awaited first (its compensation puts it back).
func (s *Service) startGroup(ctx context.Context, raw json.RawMessage) error {
	var a groupStartArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("malformed compensation arguments: %w", err)
	}
	if ms, err := store.ListStackMigrations(ctx, s.db, a.StackID); err == nil {
		for _, m := range ms {
			if m.State == domain.MigrationRunning {
				wctx, cancel := context.WithTimeout(ctx, s.opts.StopTimeout)
				_, _ = s.waitJob(wctx, m.ID)
				cancel()
			}
		}
	}
	st, err := s.opts.Stacks.Get(ctx, a.StackID)
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.EnvironmentID != a.Source || len(a.Services) == 0 {
		return nil
	}
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, a.Source, protocol.ReqMigrationStart, protocol.MigrationStartInput{MigrationID: a.MigrationID, Stack: a.Stack,
		Services: a.Services}, &res, s.opts.StopTimeout); err != nil {
		if errors.Is(err, jobs.ErrAgentOffline) {
			return errors.New("the source environment's agent is offline: start the stack once it reconnects")
		}
		return fmt.Errorf("start the source's services: %w", err)
	}
	s.log.Info("environment migration rolled back a stack: source services started", "migration_id", a.MigrationID, "stack_id", a.StackID,
		"services", len(a.Services))
	return nil
}

// onEnvironmentMigrationFinished records an environment migration's end.
func (s *Service) onEnvironmentMigrationFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	m, err := store.GetEnvironmentMigration(ctx, db, j.ID)
	if errors.Is(err, domain.ErrEnvironmentMigrationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch j.State {
	case domain.JobSucceeded:
		m.State = domain.MigrationCompleted
	case domain.JobCancelled:
		m.State = domain.MigrationCancelled
	case domain.JobInterrupted:
		m.State = domain.MigrationInterrupted
	default:
		m.State = domain.MigrationFailed
	}
	// A stack whose migration was still running ended with it.
	for i, st := range m.Stacks {
		if st.State != domain.EnvironmentStackMoving || st.MigrationID == "" {
			continue
		}
		child, err := store.GetMigration(ctx, db, st.MigrationID)
		if err == nil && (child.State == domain.MigrationCompleted || child.State == domain.MigrationSourceRemoved) {
			m.Stacks[i].State = domain.EnvironmentStackMoved
		} else {
			m.Stacks[i].State = domain.EnvironmentStackFailed
		}
	}
	now := s.now()
	m.UpdatedAt, m.FinishedAt = now, &now
	return store.UpdateEnvironmentMigration(ctx, db, &m)
}
