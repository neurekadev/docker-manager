package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestRequestIDPropagatesToTheAgent (#34): the public API request ID
// reaches the agent on request, stream_open and job command frames (the
// job keeps it from enqueue to dispatch), handlers see it through
// logging.RequestID(ctx) and the agent's log lines carry request_id.
func TestRequestIDPropagatesToTheAgent(t *testing.T) {
	f := newFixture(t)
	seen := make(chan string, 8)
	logger, logs := testutil.CaptureLogger()
	var effects jobstest.Effects
	exec := jobstest.SimExecutor(jobspec.ContainerRestart, jobstest.SimOptions{Effects: &effects,
		Before: map[string]func(context.Context, *jobexec.StepContext) error{"restart": func(ctx context.Context, _ *jobexec.StepContext) error {
			seen <- "command:" + logging.RequestID(ctx)
			logging.FromContext(ctx).Info("restarting for the test")
			return nil
		}}})
	a := f.newAgent("ENG-R", "host-r", exec)
	a.logger = logger
	a.requests = map[string]session.RequestHandler{protocol.ReqContainerList: func(ctx context.Context, _ json.RawMessage) (any, error) {
		seen <- "request:" + logging.RequestID(ctx)
		logging.FromContext(ctx).Info("listing for the test")
		return map[string]any{}, nil
	}}
	a.streams = map[string]session.StreamHandler{protocol.StreamFilesDownload: func(ctx context.Context, s *streammux.Stream) error {
		seen <- "stream:" + logging.RequestID(ctx)
		return nil
	}}
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	f.waitOnline(r.EnvironmentID)
	a.waitState(session.StateOnline)

	ctx := logging.WithRequestID(f.ctx, "req-34-propagation")
	if _, err := f.svc.Hub().RequestEnvironment(ctx, r.EnvironmentID, protocol.ReqContainerList, nil, 0); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "request:req-34-propagation" {
		t.Fatalf("request handler saw %q", got)
	}
	st, err := f.svc.Hub().OpenStream(ctx, r.EnvironmentID, protocol.StreamFilesDownload, map[string]string{"path": "x"}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "stream:req-34-propagation" {
		t.Fatalf("stream handler saw %q", got)
	}
	st.Abort(protocol.CloseReasonCancelled, "", "")

	j, _, err := f.jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: alice, EnvironmentID: r.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.jobs.Get(f.ctx, j.ID)
	if err != nil || stored.RequestID != "req-34-propagation" {
		t.Fatalf("stored job request ID %q %v", stored.RequestID, err)
	}
	f.dispatch() // the dispatch runs without the request's context
	if got := <-seen; got != "command:req-34-propagation" {
		t.Fatalf("executor saw %q", got)
	}
	f.waitJob(j.ID, domain.JobSucceeded)
	for _, msg := range []string{"listing for the test", "restarting for the test"} {
		found := false
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, msg) {
				found = strings.Contains(line, `"request_id":"req-34-propagation"`)
			}
		}
		if !found {
			t.Errorf("agent log line %q lacks request_id:\n%s", msg, logs.String())
		}
	}

	// Work without a request (scheduled jobs, reconcilers) carries none.
	if _, err := f.svc.Hub().RequestEnvironment(f.ctx, r.EnvironmentID, protocol.ReqContainerList, nil, 0); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "request:" {
		t.Fatalf("request without an ID saw %q", got)
	}
}

// TestRequestIDNotSentToAgentsWithoutTheFeature: an agent that does not
// announce frame.request_id (a release predating it) never receives the
// field it would reject.
func TestRequestIDNotSentToAgentsWithoutTheFeature(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-OLD")
	s := f.raw(r.Credential)
	s.handshake(hello) // caps() announces no features
	f.waitOnline(r.EnvironmentID)
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Hub().RequestEnvironment(logging.WithRequestID(f.ctx, "req-old-agent"), r.EnvironmentID,
			protocol.ReqAgentCredentialRotate, map[string]string{}, 0)
		done <- err
	}()
	fr, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if fr.Type != protocol.TypeRequest || fr.RequestID != "" {
		t.Fatalf("frame to an old agent %+v", fr)
	}
	_ = s.c.CloseNow()
	<-done
}
