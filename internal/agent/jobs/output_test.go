package jobs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestReportBoundsResultOutputs (#7): finished entries carry their result
// output in the job_report, but never more than MaxReportOutputs in one
// frame; the rest are reported without output rather than making the
// report too large to send.
func TestReportBoundsResultOutputs(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", protocol.MaxResultOutput-16) + `"}`)
	n := MaxReportOutputs/len(big) + 2
	for i := range n {
		st := jobexec.State{JobID: "job-" + string(rune('a'+i)), Attempt: 1, FencingToken: uint64(i + 1), Kind: jobspec.StackDeploy,
			Output: big, Outcome: &protocol.ResultPayload{Outcome: "succeeded", Output: big}}
		if err := j.Accept(&st); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(testutil.Context(t), Options{StateDir: dir, Sender: &captureSender{}, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	rep := r.Report()
	with, total := 0, 0
	for _, e := range rep.Jobs {
		if e.Result == nil || e.Status != protocol.ReportFinished {
			t.Fatalf("entry %+v", e)
		}
		if len(e.Result.Output) > 0 {
			with++
			total += len(e.Result.Output)
		}
	}
	if with == 0 || with == len(rep.Jobs) || total > MaxReportOutputs {
		t.Errorf("%d of %d entries carry %d bytes of output (max %d)", with, len(rep.Jobs), total, MaxReportOutputs)
	}
	// The report fits in a frame.
	f, err := protocol.NewFrame(protocol.TypeJobReport, "r", "", protocol.JobRef{}, rep)
	if err != nil {
		t.Fatalf("report frame: %v", err)
	}
	if b, err := protocol.Encode(f); err != nil || len(b) > protocol.MaxFrameSize {
		t.Fatalf("report frame of %d bytes: %v", len(b), err)
	}
	// The journal itself keeps every output.
	for _, st := range r.Journal().Entries() {
		if len(st.Outcome.Output) == 0 {
			t.Fatal("the journal lost an output")
		}
	}
}
