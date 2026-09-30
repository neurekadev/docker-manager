package enroll

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func request() protocol.EnrollRequest {
	return protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: "1.4.0", InstallID: "i1",
		Engine: protocol.EngineInfo{ID: "E", Version: "28.5.2", APIVersion: "1.51"}}
}

func TestEnrollSuccessAndRequestShape(t *testing.T) {
	var gotAuth, gotUA, gotURL string
	var gotBody protocol.EnrollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotURL = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.URL.String()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"a1","environmentId":"e1","environmentName":"NAS","credential":"dya_c_s","sessionPath":"/agent/v1/session","reattached":false}`))
	}))
	defer srv.Close()
	resp, err := Enroll(testutil.Context(t), srv.Client(), srv.URL+protocol.EnrollPath, "dye_tok_secret", "docker-agent/1.4.0", request())
	if err != nil || resp.AgentID != "a1" || resp.Credential != "dya_c_s" {
		t.Fatalf("%+v %v", resp, err)
	}
	if gotAuth != "Bearer dye_tok_secret" || gotUA != "docker-agent/1.4.0" || gotURL != protocol.EnrollPath || gotBody != request() {
		t.Fatalf("request: %q %q %q %+v", gotAuth, gotUA, gotURL, gotBody)
	}
	if strings.Contains(gotURL, "dye_") {
		t.Fatal("token in the URL")
	}
}

func TestEnrollFailures(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		header    string
		code      string
		retryable bool
	}{
		{401, `{"code":"unauthenticated","message":"agent authentication failed"}`, "", "unauthenticated", false},
		{409, `{"code":"engine_already_enrolled","message":"one agent per Engine"}`, "", "engine_already_enrolled", false},
		{426, `{"code":"version_unsupported","message":"upgrade"}`, "", "version_unsupported", false},
		{422, `{"code":"validation_failed","message":"bad"}`, "", "validation_failed", false},
		{429, `{"code":"rate_limited","message":"slow down"}`, "7", "rate_limited", true},
		{503, `not json`, "", "", true},
		{502, ``, "", "", true},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		_, err := Enroll(testutil.Context(t), srv.Client(), srv.URL, "dye_t_s", "ua", request())
		srv.Close()
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status || e.Code != c.code || e.Retryable() != c.retryable {
			t.Errorf("%d: %v", c.status, err)
			continue
		}
		if c.header == "7" && e.RetryAfter != 7*time.Second {
			t.Errorf("retry after %v", e.RetryAfter)
		}
		if strings.Contains(e.Error(), "dye_t_s") {
			t.Errorf("token in the error: %v", e)
		}
	}
	// Network failure: retryable.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := Enroll(testutil.Context(t), http.DefaultClient, url, "dye_t_s", "ua", request())
	var e *Error
	if !errors.As(err, &e) || e.Status != 0 || !e.Retryable() {
		t.Fatalf("network failure: %v", err)
	}
	// A 201 that does not carry a credential is not trusted.
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"a1","environmentId":"e1","credential":"","sessionPath":"/agent/v1/session"}`))
	}))
	defer srv.Close()
	if _, err := Enroll(testutil.Context(t), srv.Client(), srv.URL, "dye_t_s", "ua", request()); err == nil {
		t.Fatal("accepted a response without a credential")
	}
}
