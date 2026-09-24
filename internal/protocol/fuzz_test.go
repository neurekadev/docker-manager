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
		`{"type":"hello","id":"h1","payload":{"protocol":"dockyard.agent/v1","agentId":"a1","agentVersion":"0.0.0-edge","installId":"i1","engineId":"E:1"}}`,
		`{"type":"welcome","id":"w1","correlationId":"h1","payload":{"sessionId":"s1","managerVersion":"1.4.0","environmentId":"e1","agentStatus":"outdated","heartbeatIntervalMs":15000,"heartbeatTimeoutMs":45000,"limits":{"maxFrameBytes":1048576,"maxStreams":32,"streamWindowBytes":1048576,"maxChunkBytes":262144,"maxPaths":256}}}`,
		`{"type":"capabilities","id":"c0","payload":{"agentVersion":"1.3.2","protocols":["dockyard.agent/v1"],"os":"linux","arch":"arm64","engine":{"id":"E:1","version":"28.5.2","apiVersion":"1.51","os":"linux","arch":"arm64"},"commands":["stack.deploy"],"requests":["engine.info"],"streams":["container.logs"],"roots":[{"kind":"stacks","path":"/var/lib/dockyard/stacks","watch":"inotify"}]}}`,
		`{"type":"request","id":"q1","deadline":"2026-09-24T12:00:05Z","payload":{"name":"files.list","input":{"scope":{"kind":"volume","id":"data"},"path":"a/b"}}}`,
		`{"type":"request","id":"q2","deadline":"2026-09-24T12:00:05Z","jobId":"j1","payload":{"name":"engine.info"}}`,
		`{"type":"request","id":"q3","deadline":"2026-09-24T12:00:05Z","payload":{"name":"host.shell","input":{"cmd":"rm -rf /"}}}`,
		`{"type":"response","id":"r2","correlationId":"q1","payload":{"output":{"entries":[]}}}`,
		`{"type":"stream_open","id":"o1","payload":{"kind":"migration.send","direction":"agent_to_manager","jobId":"j9","maxBytes":1073741824}}`,
		`{"type":"stream_credit","id":"k1","correlationId":"o1","payload":{"bytes":1048576}}`,
		`{"type":"stream_close","id":"z1","correlationId":"o1","payload":{"reason":"eof","bytes":5,"sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}}`,
		`{"type":"fs_invalidation","id":"f2","payload":{"scope":{"kind":"stack","id":"s1"},"paths":["compose.yaml","../etc/passwd"],"at":"2026-09-24T12:00:00Z","seq":4}}`,
		`{"type":"rescan","id":"x2","payload":{"scope":{"kind":"volume","id":"data"},"path":".","maxEntries":10000}}`,
		`{"type":"job_report","id":"jr","payload":{"highWater":3,"jobs":[{"jobId":"j1","attempt":1,"fencingToken":3,"kind":"prune.run","status":"finished","result":{"outcome":"succeeded"}}]}}`,
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
		_ = ValidatePayload(frame) // must not panic on any accepted envelope
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
