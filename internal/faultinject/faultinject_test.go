package faultinject

import (
	"context"
	"testing"
)

func TestParseSpec(t *testing.T) {
	got, err := ParseSpec(" engine.dispatch.after_commit:crash, agent.step.before.snapshot:error,x:block,")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Action{"engine.dispatch.after_commit": Crash, "agent.step.before.snapshot": Error, "x": Block}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}
	if m, err := ParseSpec(""); err != nil || len(m) != 0 {
		t.Fatalf("empty spec: %v %v", m, err)
	}
	for _, bad := range []string{"nocolon", ":crash", "a:explode", "Bad.Name:crash", "a b:crash"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("ParseSpec(%q) accepted", bad)
		}
	}
}

func TestPointIsNoOpWithoutArming(t *testing.T) {
	t.Setenv(EnvVar, "")
	if err := Point(context.Background(), "engine.test.unarmed"); err != nil {
		t.Fatalf("unarmed point returned %v", err)
	}
	if !ValidName("agent.step.before.stop_containers") || ValidName("a:b") {
		t.Fatal("ValidName")
	}
}
