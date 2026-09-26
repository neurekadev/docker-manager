package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func goodEnroll() EnrollRequest {
	return EnrollRequest{Protocol: Version, AgentVersion: "1.4.0", InstallID: "0190a6e0-1111-7000-8000-000000000001",
		Engine:   EngineInfo{ID: "4VQD:ABCD:ZK2M", Version: "28.5.2", APIVersion: "1.51", OS: "linux", Arch: "amd64"},
		Hostname: "nas-01", EnvironmentName: "NAS"}
}

func TestEnrollRequestValidation(t *testing.T) {
	if err := goodEnroll().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*EnrollRequest){
		"version":           func(r *EnrollRequest) { r.AgentVersion = "" },
		"install id":        func(r *EnrollRequest) { r.InstallID = "a b" },
		"engine id":         func(r *EnrollRequest) { r.Engine.ID = "" },
		"engine version":    func(r *EnrollRequest) { r.Engine.Version = "" },
		"api version":       func(r *EnrollRequest) { r.Engine.APIVersion = "1 51" },
		"min api version":   func(r *EnrollRequest) { r.Engine.MinAPIVersion = "x y" },
		"os too long":       func(r *EnrollRequest) { r.Engine.OS = strings.Repeat("l", 33) },
		"hostname control":  func(r *EnrollRequest) { r.Hostname = "a\nb" },
		"hostname long":     func(r *EnrollRequest) { r.Hostname = strings.Repeat("h", MaxHostname+1) },
		"name long":         func(r *EnrollRequest) { r.EnvironmentName = strings.Repeat("n", MaxEnvironmentName+1) },
		"name invalid utf8": func(r *EnrollRequest) { r.EnvironmentName = "\xff" },
	}
	for name, mut := range bad {
		r := goodEnroll()
		mut(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDecodeEnrollRequestStrict(t *testing.T) {
	b, _ := json.Marshal(goodEnroll())
	if r, err := DecodeEnrollRequest(b); err != nil || r != goodEnroll() {
		t.Fatalf("%+v %v", r, err)
	}
	for _, s := range []string{
		`{"protocol":"docker-manager.agent/v1","shell":"sh"}`,
		string(b) + `{}`,
		`[1]`,
		`nul`,
		`{"protocol":` + strings.Repeat(" ", MaxEnrollBody) + `"x"}`,
	} {
		if _, err := DecodeEnrollRequest([]byte(s)); err == nil {
			t.Errorf("accepted %.60q", s)
		}
	}
}

func TestEnrollResponseValidate(t *testing.T) {
	ok := EnrollResponse{AgentID: "a1", EnvironmentID: "e1", Credential: CredentialPrefix + "id_secret", SessionPath: SessionPath}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]EnrollResponse{
		"no credential":    {AgentID: "a1", EnvironmentID: "e1", SessionPath: SessionPath},
		"enrollment token": {AgentID: "a1", EnvironmentID: "e1", Credential: EnrollmentTokenPrefix + "x_y", SessionPath: SessionPath},
		"other path":       {AgentID: "a1", EnvironmentID: "e1", Credential: ok.Credential, SessionPath: "/evil"},
		"bad id":           {AgentID: "a 1", EnvironmentID: "e1", Credential: ok.Credential, SessionPath: SessionPath},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSeqTracker(t *testing.T) {
	var tr SeqTracker
	steps := []struct {
		seq    uint64
		res    SeqResult
		missed uint64
	}{
		{0, SeqDuplicate, 0},
		{1, SeqNext, 0},
		{2, SeqNext, 0},
		{2, SeqDuplicate, 0},
		{1, SeqDuplicate, 0},
		{5, SeqGap, 2},
		{6, SeqNext, 0},
		{4, SeqDuplicate, 0},
	}
	for i, s := range steps {
		res, missed := tr.Observe(s.seq)
		if res != s.res || missed != s.missed {
			t.Fatalf("step %d seq %d: %v %d, want %v %d", i, s.seq, res, missed, s.res, s.missed)
		}
	}
	if tr.Last() != 6 {
		t.Fatalf("last %d", tr.Last())
	}
	// A fresh session starts at 1 again.
	var next SeqTracker
	if res, missed := next.Observe(3); res != SeqGap || missed != 2 {
		t.Fatalf("first frame 3: %v %d", res, missed)
	}
}
