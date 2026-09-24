package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevelAndFormat(t *testing.T) {
	for in, want := range map[string]slog.Level{"": slog.LevelInfo, "DEBUG": slog.LevelDebug, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("expected error for unknown level")
	}
	if f, err := ParseFormat(""); err != nil || f != FormatJSON {
		t.Errorf("ParseFormat default = %q, %v", f, err)
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestJSONOutputAndSecretRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo, FormatJSON)
	l.Info("hello", "token", Secret("s3cr3t"))
	if strings.Contains(buf.String(), "s3cr3t") {
		t.Fatalf("secret leaked: %s", buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if rec["token"] != "[REDACTED]" {
		t.Fatalf("token = %v", rec["token"])
	}
	if s := fmt.Sprintf("%v", Secret("x")); s != "[REDACTED]" {
		t.Fatalf("fmt redaction = %q", s)
	}
}

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	if FromContext(ctx) != slog.Default() {
		t.Fatal("expected default logger")
	}
	l := Discard()
	ctx = IntoContext(WithRequestID(ctx, "req-1"), l)
	if FromContext(ctx) != l || RequestID(ctx) != "req-1" {
		t.Fatal("context round trip failed")
	}
}
