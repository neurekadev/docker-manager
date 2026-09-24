package protocol

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestCommandFrameRoundTrip(t *testing.T) {
	ref := JobRef{JobID: "0190a6e0-0000-7000-8000-000000000001", Attempt: 2, FencingToken: 7}
	f, err := NewCommandFrame(ref, deadline, CommandPayload{Kind: "stack.deploy", Input: json.RawMessage(`{"a":1}`), CompletedSteps: []string{"resolve_sources"}})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != "cmd.0190a6e0-0000-7000-8000-000000000001.2" {
		t.Fatalf("command frame id = %q", f.ID)
	}
	b, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if g.Ref() != ref {
		t.Fatalf("ref = %+v", g.Ref())
	}
	p, err := DecodePayload[CommandPayload](g)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "stack.deploy" || string(p.Input) != `{"a":1}` || !reflect.DeepEqual(p.CompletedSteps, []string{"resolve_sources"}) {
		t.Fatalf("payload = %+v", p)
	}
}

func TestJobReportAndAckFrames(t *testing.T) {
	report := JobReportPayload{HighWater: 9, Jobs: []JobReportEntry{
		{JobID: "j1", Attempt: 1, FencingToken: 9, Kind: "backup.run", Status: ReportFinished,
			Result: &ResultPayload{Outcome: "interrupted", InterruptedStep: "snapshot", Recovery: "check",
				Compensations: []CompensationPayload{{Name: "start_containers", Done: true}}}},
		{JobID: "j2", Attempt: 3, FencingToken: 8, Kind: "prune.run", Status: ReportRunning, CurrentStep: "delete_candidates"},
	}}
	f, err := NewFrame(TypeJobReport, "r1", "", JobRef{}, report)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(f)
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload[JobReportPayload](g)
	if err != nil || !reflect.DeepEqual(got, report) {
		t.Fatalf("report round trip: %+v %v", got, err)
	}

	ack, err := NewFrame(TypeAck, "a1", "r1", JobRef{}, AckPayload{Accepted: true, Forget: []string{"j1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFrame(TypeAck, "a2", "", JobRef{}, AckPayload{}); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("ack without correlation accepted: %v", err)
	}
	ap, err := DecodePayload[AckPayload](ack)
	if err != nil || !ap.Accepted || ap.Forget[0] != "j1" {
		t.Fatalf("ack payload %+v %v", ap, err)
	}
}

func TestDecodePayloadStrict(t *testing.T) {
	f := &Frame{Type: TypeAck, ID: "a", CorrelationID: "c", Payload: json.RawMessage(`{"accepted":true,"surprise":1}`)}
	if _, err := DecodePayload[AckPayload](f); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("unknown payload field accepted: %v", err)
	}
	if _, err := DecodePayload[AckPayload](&Frame{Type: TypeAck}); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("missing payload accepted: %v", err)
	}
	if _, err := NewCommandFrame(JobRef{JobID: "j"}, deadline, CommandPayload{Kind: "x"}); err == nil {
		t.Fatal("command without attempt/token accepted")
	}
}
