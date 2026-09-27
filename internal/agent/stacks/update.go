package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/regauth"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/resources"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Digest-driven updates (#20): update.run pulls the unchanged tagged
// reference, checks that the tag still names the checked candidate and
// replaces what changed, preserving the prior running state. For a stack
// it recreates through the Compose SDK from exactly the applied definition
// bytes (their hash is asserted before, during and after the run: the
// agent never writes them); for a Docker Manager-managed standalone container it
// recreates from the saved specification. There is no automatic rollback:
// a failure after containers were replaced asks the manager to quarantine
// the candidate digests and tells the operator how to pin the previous
// digest in their own definition.

func (s *Service) updateExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.UpdateRun, Steps: map[string]jobexec.StepFunc{
		"pull_images":  classified(s.updatePull),
		"recreate":     classified(s.updateRecreate),
		"wait_healthy": classified(s.updateConfirm),
	}}
}

func updateInput(sc *jobexec.StepContext) (protocol.UpdateRunInput, error) {
	var in protocol.UpdateRunInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed update input: %w", err)
	}
	if err := in.Validate(); err != nil {
		return in, err
	}
	return in, nil
}

func updateOutput(sc *jobexec.StepContext) (protocol.UpdateRunOutput, error) {
	var o protocol.UpdateRunOutput
	if b := sc.Output(); len(b) > 0 {
		if err := json.Unmarshal(b, &o); err != nil {
			return o, err
		}
	}
	return o, nil
}

// Recovery guidance of update failures.
const (
	recoverySourceChanged = "The Compose definition on disk is not the revision Docker Manager last deployed (or it changed during the update). " +
		"Docker Manager never edits it: review the change, deploy the stack, then run the update again."
	recoveryCandidateChanged = "The tag moved to another digest after the check. Nothing was recreated; run an update check again and " +
		"review the new candidate."
	recoveryRecreated = "The container was replaced, renamed or is no longer Docker Manager-managed since the update was planned. " +
		"Refresh the inventory and check the policy's target."
)

// failureRecovery is the guidance after a failed update: no automatic
// rollback; how to go back by pinning the previous digest.
func failureRecovery(o protocol.UpdateRunOutput) string {
	var pins []string
	for _, r := range o.Services {
		if r.FromDigest != "" && (r.Outcome == protocol.UpdateFailed || r.Outcome == protocol.UpdateUpdated) {
			pins = append(pins, fmt.Sprintf("%s: %s@%s", r.Service, repositoryOf(r.Reference), r.FromDigest))
		}
	}
	msg := "The update failed and there is no automatic rollback; the candidate digest is quarantined so it is not retried " +
		"automatically. To go back, pin the previous digest in your own definition (Compose image: or the container's image) " +
		"and deploy it"
	if len(pins) > 0 {
		msg += " — previous digests: " + strings.Join(pins, "; ")
	}
	return msg + ". Fix the image or its configuration and a newer digest of the tag becomes a new candidate."
}

func repositoryOf(ref string) string {
	if r, err := imageref.Parse(ref); err == nil {
		return r.Name()
	}
	return ref
}

func updateRefusal(class, recovery, format string, args ...any) error {
	return &stepError{class: class, recovery: recovery, err: fmt.Errorf(format, args...)}
}

// updateFailure classifies err and replaces its recovery with the
// quarantine guidance (the output was journaled with Quarantine set).
func updateFailure(err error, o protocol.UpdateRunOutput) error {
	err = classify(err)
	class := "step_failed"
	var ce jobexec.ClassedError
	if errors.As(err, &ce) {
		class = ce.ErrorClass()
	}
	return &stepError{class: class, recovery: failureRecovery(o), err: err}
}

func (s *Service) lifecycleOptions(ctx context.Context, sc *jobexec.StepContext, in protocol.UpdateRunInput) lifecycle.Options {
	o := lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout,
		Progress: func(service, msg string) { sc.Progress(ctx, -1, strings.TrimPrefix(service+": "+msg, ": ")) }}
	if in.WaitTimeoutSeconds > 0 {
		o.WaitTimeout = time.Duration(in.WaitTimeoutSeconds) * time.Second
	}
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		o.StopTimeout = &d
	}
	return o
}

// definitionHash reads the stack's definition as on disk now.
func (s *Service) definitionHash(ctx context.Context, ref protocol.ProjectRef) (string, error) {
	dir, err := s.resolve(ref)
	if err != nil {
		return "", err
	}
	files, _ := definitionFiles(ctx, ref, dir)
	snap, err := readSources(dir, files)
	if err != nil {
		return "", err
	}
	return snap.Hash, nil
}

// pull pulls a service's unchanged tagged reference and checks that the
// tag now names the candidate (its platform manifest or index digest).
func (s *Service) pullCandidate(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, u protocol.UpdateService, r *protocol.UpdateServiceResult) error {
	auth, err := regauth.ForReference(sc.Secrets, u.Reference, u.RegistryConnection != "")
	if err != nil {
		return err
	}
	sc.Progress(ctx, -1, "pulling "+u.Reference)
	if _, err := eng.PullImage(ctx, u.Reference, engine.PullOptions{Auth: auth, Platform: u.Platform}); err != nil {
		return err
	}
	img, err := eng.InspectImage(ctx, u.Reference)
	if err != nil {
		return err
	}
	digests := imageref.DigestsFor(u.Reference, img.RepoDigests)
	r.ToImageID = img.ID
	if len(digests) > 0 {
		r.ToDigest = digests[0]
	}
	if !slices.Contains(digests, u.Digest) && (u.IndexDigest == "" || !slices.Contains(digests, u.IndexDigest)) {
		r.Outcome = protocol.UpdateFailed
		r.Message = "the tag moved again after the check"
		return updateRefusal(protocol.UpdateClassCandidateChanged, recoveryCandidateChanged,
			"%s now names %s, not the checked candidate %s", u.Reference, strings.Join(digests, ", "), u.Digest)
	}
	switch {
	case slices.Contains(digests, u.Digest):
		r.ToDigest = u.Digest
	default:
		r.ToDigest = u.IndexDigest
	}
	r.Outcome = protocol.UpdatePulled
	return nil
}

// updatePull records the state before the update, pulls every candidate
// and asserts that the definition is (still) the applied revision.
func (s *Service) updatePull(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := updateInput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	if in.Container != nil {
		return s.containerPull(ctx, sc, eng, in)
	}
	before, err := s.definitionHash(ctx, *in.Stack)
	if err != nil {
		return err
	}
	out := protocol.UpdateRunOutput{Stage: protocol.UpdateStagePull, SourceHashBefore: before}
	if before != in.ExpectSourceHash {
		_ = sc.SetOutput(ctx, out)
		return updateRefusal(protocol.UpdateClassSourceChanged, recoverySourceChanged,
			"the definition on disk (%s) is not the applied revision (%s)", short(before), short(in.ExpectSourceHash))
	}
	if out.Before, err = serviceStates(ctx, eng, in.Stack.ProjectName); err != nil {
		return err
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	for _, u := range in.Services {
		for _, c := range list {
			if c.Labels[lifecycle.ComposeServiceLabel] == u.Service && protocol.UpdateExcluded(c.Labels) {
				return updateRefusal(protocol.UpdateClassRecreated, "Remove the update exclusion label and check again.",
					"service %s has docker-manager.update.exclude=true", u.Service)
			}
		}
	}
	for _, u := range in.Services {
		r := protocol.UpdateServiceResult{Service: u.Service, Reference: u.Reference, Outcome: protocol.UpdatePending}
		for _, c := range list {
			if c.Labels[lifecycle.ComposeServiceLabel] != u.Service {
				continue
			}
			if r.FromImageID == "" {
				r.FromImageID = c.ImageID
			}
			r.WasRunning = r.WasRunning || c.State == "running"
		}
		if r.FromImageID != "" {
			if img, err := eng.InspectImage(ctx, r.FromImageID); err == nil {
				r.FromDigest = imageref.DigestFor(u.Reference, img.RepoDigests)
			}
		}
		out.Services = append(out.Services, r)
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	var pullErr error
	for i, u := range in.Services {
		if pullErr = s.pullCandidate(ctx, sc, eng, u, &out.Services[i]); pullErr != nil {
			break
		}
		sc.Item(ctx, u.Service, domain.ItemSucceeded, "pulled "+u.Reference)
	}
	after, herr := s.definitionHash(ctx, *in.Stack)
	out.SourceHashAfterPull = after
	if err := sc.SetOutput(ctx, out); err != nil {
		return errors.Join(pullErr, err)
	}
	switch {
	case pullErr != nil:
		return pullErr
	case herr != nil:
		return herr
	case after != before:
		return updateRefusal(protocol.UpdateClassSourceChanged, recoverySourceChanged,
			"the definition on disk changed during the pull (%s -> %s); Docker Manager did not write it", short(before), short(after))
	}
	return nil
}

// updateRecreate replaces the changed services' containers through the
// Compose SDK, loaded from the applied bytes, and starts what ran before
// in dependency order (internal/agent/lifecycle.Update).
func (s *Service) updateRecreate(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := updateInput(sc)
	if err != nil {
		return err
	}
	out, err := updateOutput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	if in.Container != nil {
		return s.containerRecreate(ctx, sc, eng, in, out)
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	dir, err := s.resolve(*in.Stack)
	if err != nil {
		return err
	}
	p, snap, err := s.loadSnapshot(ctx, c, protocol.StackJobInput{Stack: *in.Stack}, dir)
	if err != nil && !errors.Is(err, errSourcesChanged) {
		return err
	}
	if err != nil || snap.Hash != out.SourceHashBefore {
		return updateRefusal(protocol.UpdateClassSourceChanged, recoverySourceChanged,
			"the definition on disk changed before the recreate (%s -> %s); Docker Manager did not write it", short(out.SourceHashBefore), short(snap.Hash))
	}
	var nodes []lifecycle.Service
	for _, svc := range p.Services {
		n := lifecycle.Service{Name: svc.Name}
		for _, d := range svc.DependsOn {
			n.DependsOn = append(n.DependsOn, lifecycle.Dependency{Service: d.Service, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
		}
		nodes = append(nodes, n)
	}
	g, err := lifecycle.NewGraph(nodes)
	if err != nil {
		return err
	}
	var changed, wasRunning []string
	for i := range out.Services {
		r := &out.Services[i]
		switch r.ToImageID {
		case "":
			return fmt.Errorf("service %s was not pulled", r.Service)
		case r.FromImageID:
			r.Outcome, r.Message = protocol.UpdateUnchanged, "the tag names the image the service already runs"
		default:
			changed = append(changed, r.Service)
		}
	}
	for _, st := range out.Before {
		if st.Running > 0 {
			wasRunning = append(wasRunning, st.Service)
		}
	}
	// Docker Manager's own project (#32): the agent's own service is
	// recreated by a helper container after the job.
	var handoff []string
	if len(changed) > 0 {
		if changed, handoff, err = s.selfHandoff(ctx, p, in.Stack.ProjectName, changed, false); err != nil {
			return err
		}
	}
	out.Stage = protocol.UpdateStageRecreate
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	creds := regauth.All(sc.Secrets)
	recreate := func(ctx context.Context, services []string) error {
		return c.Create(ctx, p, compose.CreateOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc), Auth: creds},
			Services: services, StopTimeout: s.lifecycleOptions(ctx, sc, in).StopTimeout})
	}
	rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
	var rep lifecycle.UpdateReport
	var uerr error
	if len(changed) > 0 {
		rep, uerr = lifecycle.Update(ctx, g, rt, changed, wasRunning, recreate, s.lifecycleOptions(ctx, sc, in))
	}
	for i := range out.Services {
		r := &out.Services[i]
		switch {
		case slices.Contains(rep.KeptStopped, r.Service):
			r.Outcome, r.Message = protocol.UpdateKeptStopped, "stopped before the update: kept stopped; it picks up the new image at its next deploy"
		case slices.Contains(rep.Recreated, r.Service):
			r.Outcome = protocol.UpdateUpdated
		case uerr != nil && slices.Contains(changed, r.Service):
			r.Outcome = protocol.UpdateFailed
		}
	}
	out.Restarted = rep.Restarted
	out.Warnings = append(out.Warnings, rep.Warnings...)
	if uerr != nil {
		code := lifecycle.CodeOf(uerr)
		touched := code != lifecycle.CodeConflict && code != lifecycle.CodeDependencyMissing && ctx.Err() == nil
		out.Quarantine = touched
		out.After, _ = serviceStates(ctx, eng, in.Stack.ProjectName)
		if err := sc.SetOutput(ctx, out); err != nil {
			return errors.Join(uerr, err)
		}
		if !touched {
			return uerr
		}
		return updateFailure(uerr, out)
	}
	for i := range out.Services {
		if r := &out.Services[i]; slices.Contains(handoff, r.Service) {
			r.Outcome, r.Message = protocol.UpdateUpdated, "recreated by a helper container right after this job (the agent restarts)"
		}
	}
	if len(handoff) > 0 {
		out.Warnings = append(out.Warnings, selfUpdateMessage(handoff))
	}
	for _, r := range out.Services {
		if r.Outcome != protocol.UpdatePulled {
			sc.Item(ctx, r.Service, itemStatus(r.Outcome), r.Outcome)
		}
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	if len(handoff) > 0 {
		s.scheduleSelf(sc, *in.Stack, dir, snap, handoff, false, in.TimeoutSeconds)
	}
	return nil
}

func itemStatus(outcome string) string {
	switch outcome {
	case protocol.UpdateFailed:
		return domain.ItemFailed
	case protocol.UpdateKeptStopped, protocol.UpdateUnchanged:
		return domain.ItemSkipped
	}
	return domain.ItemSucceeded
}

// updateConfirm waits until every recreated or restarted service runs and
// is healthy, then asserts the definition bytes are unchanged.
func (s *Service) updateConfirm(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := updateInput(sc)
	if err != nil {
		return err
	}
	out, err := updateOutput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	if in.Container != nil {
		return s.containerConfirm(ctx, sc, eng, in, out)
	}
	var confirm []string
	for _, r := range out.Services {
		if r.Outcome == protocol.UpdateUpdated {
			confirm = append(confirm, r.Service)
		}
	}
	confirm = append(confirm, out.Restarted...)
	out.Stage = protocol.UpdateStageConfirm
	rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
	cerr := lifecycle.Confirm(ctx, rt, confirm, s.lifecycleOptions(ctx, sc, in))
	out.After, _ = serviceStates(ctx, eng, in.Stack.ProjectName)
	after, herr := s.definitionHash(ctx, *in.Stack)
	out.SourceHashAfter = after
	if cerr != nil {
		for i := range out.Services {
			var le *lifecycle.Error
			if errors.As(cerr, &le) && le.Service == out.Services[i].Service {
				out.Services[i].Outcome, out.Services[i].Message = protocol.UpdateFailed, le.Message
			}
		}
		out.Quarantine = ctx.Err() == nil
		if err := sc.SetOutput(ctx, out); err != nil {
			return errors.Join(cerr, err)
		}
		return updateFailure(cerr, out)
	}
	if herr == nil && after == out.SourceHashBefore {
		out.Stage = protocol.UpdateStageDone
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	switch {
	case herr != nil:
		return herr
	case after != out.SourceHashBefore:
		return updateRefusal(protocol.UpdateClassSourceChanged, recoverySourceChanged,
			"the definition on disk changed during the update (%s -> %s); Docker Manager did not write it", short(out.SourceHashBefore), short(after))
	}
	for _, r := range out.Services {
		if r.Outcome == protocol.UpdateUpdated {
			sc.Item(ctx, r.Service, domain.ItemSucceeded, "updated to "+r.ToDigest)
		}
	}
	sc.Progress(ctx, 100, "update applied")
	return nil
}

// Standalone containers.

// checkStandalone refuses a container that is not the planned
// Docker Manager-managed standalone container, or that is Docker Manager's own.
func (s *Service) checkStandalone(ctx context.Context, eng engine.Engine, in protocol.UpdateRunInput, d engine.ContainerDetails) error {
	c := in.Container
	if d.Name != c.Name || d.Labels[protocol.LabelManaged] != protocol.ManagedStandalone ||
		d.Labels[protocol.LabelSpec] != c.Ownership[protocol.LabelSpec] || d.Labels[protocol.ComposeProjectLabel] != "" {
		return updateRefusal(protocol.UpdateClassRecreated, recoveryRecreated, "container %s is not the planned Docker Manager-managed standalone container", c.Name)
	}
	if s.opts.Guard == nil {
		return nil
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return err
	}
	return protection.Check(s.opts.Guard.Identify(ctx, eng, cs).Container(d.ID), protection.Update, false)
}

func (s *Service) containerPull(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, in protocol.UpdateRunInput) error {
	c := in.Container
	d, err := eng.InspectContainer(ctx, c.ID)
	if engine.IsCode(err, engine.CodeNotFound) {
		return updateRefusal(protocol.UpdateClassRecreated, recoveryRecreated, "container %s no longer exists", c.Name)
	}
	if err != nil {
		return err
	}
	if err := s.checkStandalone(ctx, eng, in, d); err != nil {
		return err
	}
	if protocol.UpdateExcluded(d.Labels) {
		return updateRefusal(protocol.UpdateClassRecreated, "Remove the update exclusion label and check again.",
			"container %s has docker-manager.update.exclude=true", c.Name)
	}
	u := in.Services[0]
	r := protocol.UpdateServiceResult{Service: c.Name, Reference: u.Reference, FromImageID: d.ImageID, WasRunning: d.State.Running,
		Outcome: protocol.UpdatePending}
	if img, err := eng.InspectImage(ctx, d.ImageID); err == nil {
		r.FromDigest = imageref.DigestFor(u.Reference, img.RepoDigests)
	}
	out := protocol.UpdateRunOutput{Stage: protocol.UpdateStagePull, Services: []protocol.UpdateServiceResult{r}}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	perr := s.pullCandidate(ctx, sc, eng, u, &out.Services[0])
	if err := sc.SetOutput(ctx, out); err != nil {
		return errors.Join(perr, err)
	}
	return perr
}

// asideName is the temporary name of the replaced container.
func asideName(c *protocol.UpdateContainer) string {
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	return c.Name + "-docker-manager-update-" + id
}

// containerRecreate replaces the container: the old one is stopped and
// renamed aside, the new one is created from the saved specification with
// the unchanged reference (anonymous volumes are carried over), connected
// and started when the old one ran; the old one is removed only once its
// replacement exists. A failed create puts the old container back.
func (s *Service) containerRecreate(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, in protocol.UpdateRunInput, out protocol.UpdateRunOutput) error {
	c := in.Container
	r := &out.Services[0]
	if r.ToImageID == "" {
		return fmt.Errorf("container %s was not pulled", c.Name)
	}
	if r.ToImageID == r.FromImageID {
		r.Outcome, r.Message = protocol.UpdateUnchanged, "the tag names the image the container already runs"
		sc.Item(ctx, c.Name, domain.ItemSkipped, r.Outcome)
		return sc.SetOutput(ctx, out)
	}
	out.Stage = protocol.UpdateStageRecreate
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	fail := func(err error) error {
		r.Outcome = protocol.UpdateFailed
		out.Quarantine = ctx.Err() == nil
		if serr := sc.SetOutput(ctx, out); serr != nil {
			return errors.Join(err, serr)
		}
		return updateFailure(err, out)
	}
	// A repeated attempt finds the replacement already created.
	if cur, err := eng.InspectContainer(ctx, c.Name); err == nil && cur.ID != c.ID && cur.ImageID == r.ToImageID {
		return s.finishReplacement(ctx, sc, eng, in, &out, cur.ID, fail)
	}
	old, err := eng.InspectContainer(ctx, c.ID)
	if err != nil {
		return err
	}
	if err := s.checkStandalone(ctx, eng, in, withName(old, c.Name)); err != nil {
		return err
	}
	opts := s.lifecycleOptions(ctx, sc, in)
	if old.State.Running {
		sc.Progress(ctx, -1, "stopping "+c.Name)
		if err := eng.StopContainer(ctx, old.ID, opts.StopTimeout); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
			return err
		}
	}
	if old.Name != asideName(c) {
		if err := eng.RenameContainer(ctx, old.ID, asideName(c)); err != nil {
			return err
		}
	}
	spec := resources.EngineSpec(c.Spec, c.Ownership)
	inheritAnonymous(&spec, old)
	sc.Progress(ctx, -1, "creating "+c.Name+" from its saved specification")
	id, _, cerr := eng.CreateContainer(ctx, spec)
	if cerr == nil {
		for _, n := range c.Spec.Networks[min(1, len(c.Spec.Networks)):] {
			if err := eng.ConnectNetwork(ctx, n.Name, id, n.Aliases...); err != nil {
				cerr = err
				_ = eng.RemoveContainer(context.WithoutCancel(ctx), id, engine.RemoveOptions{Force: true})
				break
			}
		}
	}
	if cerr != nil {
		// Put the old container back (no rollback of anything else).
		bctx := context.WithoutCancel(ctx)
		if err := eng.RenameContainer(bctx, old.ID, c.Name); err != nil {
			out.Warnings = append(out.Warnings, "could not rename the old container back: "+err.Error())
		} else if old.State.Running {
			if err := eng.StartContainer(bctx, old.ID); err != nil {
				out.Warnings = append(out.Warnings, "could not start the old container again: "+err.Error())
			}
		}
		return fail(cerr)
	}
	return s.finishReplacement(ctx, sc, eng, in, &out, id, fail)
}

func withName(d engine.ContainerDetails, name string) engine.ContainerDetails {
	d.Name = name
	return d
}

// finishReplacement removes the old container and starts the new one when
// the old one ran.
func (s *Service) finishReplacement(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, in protocol.UpdateRunInput,
	out *protocol.UpdateRunOutput, id string, fail func(error) error) error {
	c := in.Container
	r := &out.Services[0]
	if err := eng.RemoveContainer(ctx, c.ID, engine.RemoveOptions{Force: true}); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
		out.Warnings = append(out.Warnings, "the replaced container could not be removed: "+err.Error())
	}
	if r.WasRunning {
		if err := eng.StartContainer(ctx, id); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
			return fail(err)
		}
	} else {
		r.Message = "stopped before the update: recreated and kept stopped"
	}
	r.Outcome = protocol.UpdateUpdated
	return sc.SetOutput(ctx, *out)
}

// inheritAnonymous reuses the old container's anonymous volumes for the
// specification's anonymous volume mounts (same target), like Compose.
func inheritAnonymous(spec *engine.ContainerSpec, old engine.ContainerDetails) {
	for i, m := range spec.Mounts {
		if m.Type != "volume" || m.Source != "" {
			continue
		}
		for _, om := range old.Mounts {
			if om.Type == "volume" && om.Destination == m.Target && om.Name != "" {
				spec.Mounts[i].Source = om.Name
			}
		}
	}
}

func (s *Service) containerConfirm(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, in protocol.UpdateRunInput, out protocol.UpdateRunOutput) error {
	r := &out.Services[0]
	if r.Outcome != protocol.UpdateUpdated || !r.WasRunning {
		out.Stage = protocol.UpdateStageDone
		return sc.SetOutput(ctx, out)
	}
	out.Stage = protocol.UpdateStageConfirm
	err := lifecycle.Confirm(ctx, containerRuntime{eng: eng, name: in.Container.Name}, []string{in.Container.Name}, s.lifecycleOptions(ctx, sc, in))
	if err != nil {
		r.Outcome, r.Message = protocol.UpdateFailed, err.Error()
		out.Quarantine = ctx.Err() == nil
		if serr := sc.SetOutput(ctx, out); serr != nil {
			return errors.Join(err, serr)
		}
		return updateFailure(err, out)
	}
	out.Stage = protocol.UpdateStageDone
	sc.Item(ctx, r.Service, domain.ItemSucceeded, "updated to "+r.ToDigest)
	sc.Progress(ctx, 100, "update applied")
	return sc.SetOutput(ctx, out)
}

// containerRuntime observes one standalone container as a lifecycle
// service (health confirmation).
type containerRuntime struct {
	eng  engine.Engine
	name string
}

func (r containerRuntime) Start(ctx context.Context, _ string) error {
	return r.eng.StartContainer(ctx, r.name)
}

func (r containerRuntime) Stop(ctx context.Context, _ string, timeout *time.Duration) error {
	return r.eng.StopContainer(ctx, r.name, timeout)
}

func (r containerRuntime) State(ctx context.Context, _ string) (lifecycle.State, error) {
	d, err := r.eng.InspectContainer(ctx, r.name)
	if engine.IsCode(err, engine.CodeNotFound) {
		return lifecycle.State{}, nil
	}
	if err != nil {
		return lifecycle.State{}, err
	}
	st := lifecycle.State{Exists: true, ExitCode: d.State.ExitCode}
	switch {
	case d.State.Running || d.State.Restarting || d.State.Paused:
		st.Running = true
	case d.State.Status == "exited" || d.State.Status == "dead":
		st.Exited = true
	}
	if h := d.State.Health; h != nil && h.Status != "" && h.Status != "none" {
		st.Health = h.Status
		if st.Health != "healthy" && st.Health != "unhealthy" {
			st.Health = "starting"
		}
	}
	return st, nil
}
