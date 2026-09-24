package protocol

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var deadline = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func validCommand() *Frame {
	return &Frame{
		Type:         TypeCommand,
		ID:           "f-1",
		JobID:        "0190a6e0-0000-7000-8000-000000000001",
		Attempt:      1,
		FencingToken: 42,
		Deadline:     &deadline,
		Payload:      json.RawMessage(`{"name":"container.restart"}`),
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := validCommand()
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"type":"command"`, `"jobId"`, `"fencingToken":42`, `"attempt":1`, `"deadline":"2026-09-24T12:00:00Z"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("encoding %s lacks %s", b, key)
		}
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(f *Frame){
		"unknown type":        func(f *Frame) { f.Type = "shell" },
		"empty id":            func(f *Frame) { f.ID = "" },
		"bad id chars":        func(f *Frame) { f.ID = "a b" },
		"long id":             func(f *Frame) { f.ID = strings.Repeat("a", 129) },
		"bad correlation":     func(f *Frame) { f.CorrelationID = "x/y" },
		"command no job":      func(f *Frame) { f.JobID = "" },
		"command attempt 0":   func(f *Frame) { f.Attempt = 0 },
		"command no fencing":  func(f *Frame) { f.FencingToken = 0 },
		"command no deadline": func(f *Frame) { f.Deadline = nil },
		"zero deadline":       func(f *Frame) { f.Deadline = &time.Time{} },
		"payload array":       func(f *Frame) { f.Payload = json.RawMessage(`[1]`) },
		"payload null":        func(f *Frame) { f.Payload = json.RawMessage(`null`) },
		"payload invalid":     func(f *Frame) { f.Payload = json.RawMessage(`{"a":`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := validCommand()
			mutate(f)
			if err := f.Validate(); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("Validate = %v, want ErrInvalidFrame", err)
			}
			if _, err := Encode(f); err == nil {
				t.Fatal("Encode accepted invalid frame")
			}
		})
	}
	for _, typ := range []Type{TypeAck, TypeProgress, TypeResult, TypeCancel, TypeStreamData, TypeStreamClose,
		TypeWelcome, TypeResponse, TypeStreamCredit} {
		f := &Frame{Type: typ, ID: "x", Payload: json.RawMessage(`{}`)}
		if err := f.Validate(); err == nil {
			t.Errorf("%s without correlationId accepted", typ)
		}
		f.CorrelationID = "c"
		if err := f.Validate(); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
	for typ := range needsPayload {
		f := &Frame{Type: typ, ID: "x", CorrelationID: "c", Deadline: &deadline}
		if err := f.Validate(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s without payload accepted", typ)
		}
	}
	req := &Frame{Type: TypeRequest, ID: "q1", Deadline: &deadline, Payload: json.RawMessage(`{"name":"engine.info"}`)}
	if err := req.Validate(); err != nil {
		t.Errorf("request: %v", err)
	}
	for name, mutate := range map[string]func(*Frame){
		"request without deadline": func(f *Frame) { f.Deadline = nil },
		"request without payload":  func(f *Frame) { f.Payload = nil },
		"request with job id":      func(f *Frame) { f.JobID = "j" },
		"request with fencing":     func(f *Frame) { f.FencingToken = 3 },
	} {
		f := *req
		mutate(&f)
		if err := f.Validate(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s accepted", name)
		}
	}
	for _, typ := range Types() {
		if !knownTypes[typ] {
			t.Errorf("Types() returned unknown %s", typ)
		}
	}
	if len(Types()) != len(knownTypes) {
		t.Error("Types() and knownTypes disagree")
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"not json":       `nope`,
		"unknown field":  `{"type":"hello","id":"a","extra":1}`,
		"trailing data":  `{"type":"hello","id":"a"} {}`,
		"trailing brace": `{"type":"hello","id":"a"}}`,
		"wrong types":    `{"type":"hello","id":7}`,
		"negative token": `{"type":"hello","id":"a","fencingToken":-1}`,
		"array":          `[]`,
	}
	for name, in := range cases {
		if _, err := Decode([]byte(in)); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s: Decode = %v, want ErrInvalidFrame", name, err)
		}
	}
	big := `{"type":"hello","id":"a","payload":{"x":"` + strings.Repeat("a", MaxFrameSize) + `"}}`
	if _, err := Decode([]byte(big)); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("oversized frame: %v", err)
	}
	f := &Frame{Type: TypeEvent, ID: "a", Payload: json.RawMessage(`{"x":"` + strings.Repeat("a", MaxFrameSize) + `"}`)}
	if _, err := Encode(f); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("oversized encode: %v", err)
	}
	if f, err := Decode([]byte(" {\"type\":\"heartbeat\",\"id\":\"h1\"}\n")); err != nil || f.Type != TypeHeartbeat {
		t.Errorf("whitespace-framed heartbeat: %v", err)
	}
}

func TestWebSocketHelpers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Version}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		f, err := ReadFrame(r.Context(), c)
		if err != nil {
			return
		}
		_ = WriteFrame(r.Context(), c, &Frame{Type: TypeAck, ID: "ack-1", CorrelationID: f.ID})
	}))
	defer srv.Close()

	ctx := t.Context()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{Version}})
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()
	if c.Subprotocol() != Version {
		t.Fatalf("subprotocol = %q", c.Subprotocol())
	}
	if err := WriteFrame(ctx, c, validCommand()); err != nil {
		t.Fatal(err)
	}
	ack, err := ReadFrame(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Type != TypeAck || ack.CorrelationID != "f-1" {
		t.Fatalf("ack = %+v", ack)
	}
}
