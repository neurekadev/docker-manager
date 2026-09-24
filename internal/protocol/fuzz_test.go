package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

// FuzzDecodeFrame checks that Decode never panics and that every accepted
// frame survives an Encode/Decode round trip unchanged. The extended CI
// workflow runs it with -fuzz; plain `go test` runs the seed corpus.
func FuzzDecodeFrame(f *testing.F) {
	seeds := []string{
		`{"type":"hello","id":"h1","payload":{"protocol":"dockyard.agent/v1","agentVersion":"0.0.0-edge"}}`,
		`{"type":"heartbeat","id":"hb-1"}`,
		`{"type":"command","id":"c1","jobId":"j1","attempt":1,"fencingToken":18446744073709551615,"deadline":"2026-09-24T12:00:00.123456789+02:00","payload":{"name":"container.restart"}}`,
		`{"type":"ack","id":"a1","correlationId":"c1"}`,
		`{"type":"progress","id":"p1","correlationId":"c1","jobId":"j1","payload":{"percent":50}}`,
		`{"type":"result","id":"r1","correlationId":"c1","payload":{"ok":true,"data":[1,2,3]}}`,
		`{"type":"stream_data","id":"s1","correlationId":"o1","payload":{"seq":1,"data":"aGVsbG8="}}`,
		`{"type":"error","id":"e1","payload":{"code":"unsupported","message":"<nope>"}}`,
		`{"type":"fs_invalidation","id":"f1","payload":{}}`,
		`{"type":"cancel","id":"x1","correlationId":"c1","deadline":null}`,
		`{"type":"hello","id":"a","extra":1}`,
		`{"type":"hello","id":"a"}}`,
		`{"type":"command","id":"c1"}`,
		`{"TYPE":"hello","ID":"a"}`,
		`[]`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := Decode(data)
		if err != nil {
			return
		}
		enc, err := Encode(frame)
		if err != nil {
			t.Fatalf("accepted frame does not re-encode: %v\ninput: %q", err, data)
		}
		again, err := Decode(enc)
		if err != nil {
			t.Fatalf("re-encoded frame rejected: %v\nencoded: %s", err, enc)
		}
		if !equalFrames(frame, again) {
			t.Fatalf("round trip changed frame:\nfirst:  %+v\nsecond: %+v", frame, again)
		}
	})
}

func equalFrames(a, b *Frame) bool {
	if a.Type != b.Type || a.ID != b.ID || a.CorrelationID != b.CorrelationID || a.JobID != b.JobID ||
		a.Attempt != b.Attempt || a.FencingToken != b.FencingToken {
		return false
	}
	if (a.Deadline == nil) != (b.Deadline == nil) || (a.Deadline != nil && !a.Deadline.Equal(*b.Deadline)) {
		return false
	}
	if (len(a.Payload) == 0) != (len(b.Payload) == 0) {
		return false
	}
	if len(a.Payload) == 0 {
		return true
	}
	var pa, pb any
	if json.Unmarshal(a.Payload, &pa) != nil || json.Unmarshal(b.Payload, &pb) != nil {
		return false
	}
	return reflect.DeepEqual(pa, pb)
}
