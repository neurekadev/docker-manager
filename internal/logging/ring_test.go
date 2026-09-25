package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRingKeepsNewestLinesAndTees(t *testing.T) {
	ring := NewRing(3)
	var out bytes.Buffer
	log := slog.New(Tee(New(&out, slog.LevelDebug, FormatText).Handler(), ring.Handler(slog.LevelInfo))).With("component", "test")
	log.Debug("debug only in the process log")
	for i := range 5 {
		log.Info("line", "n", i, "token", Secret("dya_secret_value"))
	}
	lines, dropped := ring.Lines()
	if len(lines) != 3 || dropped != 2 {
		t.Fatalf("lines %d dropped %d", len(lines), dropped)
	}
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		if m["n"] != float64(i+2) || m["component"] != "test" || m["token"] != "[REDACTED]" {
			t.Errorf("line %d: %v", i, m)
		}
	}
	if !strings.Contains(out.String(), "debug only") || strings.Contains(out.String(), "dya_secret_value") {
		t.Fatalf("process log %q", out.String())
	}
	for _, l := range lines {
		if bytes.Contains(l, []byte("debug only")) {
			t.Fatal("ring took a line below its level")
		}
	}
}

func TestRingTruncatesLongLines(t *testing.T) {
	ring := NewRing(2)
	_, _ = ring.Write([]byte(strings.Repeat("x", DefaultRingLineMax+100) + "\n"))
	lines, _ := ring.Lines()
	if len(lines) != 1 || len(lines[0]) > DefaultRingLineMax+20 || !bytes.HasSuffix(lines[0], []byte("(truncated)")) {
		t.Fatalf("got %d lines, first %d bytes", len(lines), len(lines[0]))
	}
}
