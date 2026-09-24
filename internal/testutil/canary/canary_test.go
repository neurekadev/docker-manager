package canary

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// recorder captures failures instead of failing the real test.
type recorder struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestGenerateShapes(t *testing.T) {
	shapes := map[Kind]*regexp.Regexp{
		Password:           regexp.MustCompile(`^Canary-pw-[0-9a-f]{16}!#%$`),
		APIToken:           regexp.MustCompile(`^dyt_canary_[0-9a-f]{32}$`),
		RegistryCredential: regexp.MustCompile(`^canary-registry-[0-9a-f]{24}$`),
		S3AccessKey:        regexp.MustCompile(`^AKIACANARY[A-Z2-7]{10}$`),
		S3SecretKey:        regexp.MustCompile(`^canary/S3\+[A-Za-z0-9+/]{30}$`),
		EnvValue:           regexp.MustCompile(`^canary-env-[0-9a-f]{24}$`),
		TOTPSeed:           regexp.MustCompile(`^CANARY[A-Z2-7]{26}$`),
	}
	if len(shapes) != len(Kinds) {
		t.Fatalf("shape table covers %d of %d kinds", len(shapes), len(Kinds))
	}
	for _, k := range Kinds {
		a, b := Generate(k), Generate(k)
		if !shapes[k].MatchString(a) {
			t.Errorf("%s: %q does not match %s", k, a, shapes[k])
		}
		if a == b {
			t.Errorf("%s: two generated values are equal", k)
		}
		if len(a) < MinLength {
			t.Errorf("%s: %q shorter than MinLength", k, a)
		}
	}
	if _, err := base32.StdEncoding.DecodeString(Generate(TOTPSeed)); err != nil {
		t.Errorf("TOTP seed is not valid base32: %v", err)
	}
	if got := len(Generate(S3SecretKey)); got != 40 {
		t.Errorf("S3 secret length = %d, want 40", got)
	}
}

func TestScanFindsEncodedLeaks(t *testing.T) {
	s := New()
	pw := s.New(Password, "owner password")
	env := s.New(EnvValue, "DB_PASSWORD")
	tok := s.New(APIToken, "api token")
	special := `Canary<pw>&"q"-0123456789`
	s.Register(Password, "special password", special)
	sp := Canary{Password, "special password", special}

	basic := base64.StdEncoding.EncodeToString([]byte("admin:" + pw))
	dockerAuth := base64.URLEncoding.EncodeToString([]byte(`{"auths":{"r":{"auth":"x"}},"password":"` + env + `"}`))
	cases := []struct {
		name, data, wantForm string
		want                 Canary
	}{
		{"raw in log line", `{"msg":"login","password":"` + pw + `"}`, "raw", Canary{Password, "owner password", pw}},
		{"json-escaped", mustJSON(map[string]string{"v": special}), "json", sp},
		{"html-escaped", "<td>" + html.EscapeString(special) + "</td>", "html", sp},
		{"basic auth header (misaligned base64)", "Authorization: Basic " + basic, "base64", Canary{Password, "owner password", pw}},
		{"base64url blob (same fragment as base64 unless +/ occur)", dockerAuth, "base64", Canary{EnvValue, "DB_PASSWORD", env}},
		{"query string", "GET /api?token=" + url.QueryEscape(tok), "raw", Canary{APIToken, "api token", tok}},
		{"url-escaped password", "next=" + url.QueryEscape(pw), "query-escaped", Canary{Password, "owner password", pw}},
		{"hex dump", "bytes: " + hex.EncodeToString([]byte(env)), "hex", Canary{EnvValue, "DB_PASSWORD", env}},
		{"HEX dump", strings.ToUpper(hex.EncodeToString([]byte(tok))), "HEX", Canary{APIToken, "api token", tok}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			leaks := s.Scan([]byte(c.data))
			found := false
			for _, l := range leaks {
				if l.Canary == c.want && strings.HasPrefix(l.Form, c.wantForm) {
					found = true
				}
			}
			if !found {
				t.Errorf("Scan(%q) = %v, want %s leak of %q", c.data, leaks, c.wantForm, c.want.Name)
			}
		})
	}
}

func TestScanNoFalsePositives(t *testing.T) {
	s := New()
	pw := s.New(Password, "pw")
	for _, clean := range []string{
		"",
		"password=[REDACTED]",
		pw[:len(pw)-1], // truncated value is not the secret
		base64.StdEncoding.EncodeToString([]byte(pw[:6])), // short prefix only
		strings.ToUpper(pw),
	} {
		if leaks := s.Scan([]byte(clean)); len(leaks) != 0 {
			t.Errorf("Scan(%q) = %v, want none", clean, leaks)
		}
	}
}

func TestBase64FragmentsEveryAlignment(t *testing.T) {
	s := New()
	v := s.New(S3SecretKey, "s3 secret")
	for prefix := range 6 {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding} {
			blob := enc.EncodeToString([]byte(strings.Repeat("p", prefix) + v + "suffix"))
			if len(s.Scan([]byte(blob))) == 0 {
				t.Errorf("prefix %d: canary not found in %s", prefix, blob)
			}
		}
	}
}

func TestRegisterRejectsShortValues(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register accepted a short value")
		}
	}()
	New().Register(Password, "short", "abc")
}

type auditRow struct {
	Action string
	Detail map[string]any
	secret string //nolint:unused // checked through %+v rendering
}

func TestAssertCleanValue(t *testing.T) {
	s := New()
	key := s.New(S3AccessKey, "s3 key")
	rec := &recorder{TB: t}
	s.AssertClean(rec, "audit rows", []auditRow{{Action: "backup.update", Detail: map[string]any{"accessKey": "[REDACTED]"}}})
	if len(rec.errors) != 0 {
		t.Fatalf("clean rows reported: %v", rec.errors)
	}
	s.AssertClean(rec, "audit rows", []auditRow{{Action: "backup.update", Detail: map[string]any{"accessKey": key}}})
	s.AssertClean(rec, "audit rows", auditRow{Action: "x", secret: key})
	s.AssertClean(rec, "error", errors.New("dial failed for "+key))
	if len(rec.errors) != 3 || !strings.Contains(rec.errors[0], "secret leak in audit rows") || !strings.Contains(rec.errors[0], `"s3 key"`) {
		t.Errorf("errors = %v", rec.errors)
	}
}

func TestCaptureLoggerChecksAtCleanup(t *testing.T) {
	s := New()
	seed := s.New(TOTPSeed, "totp seed")
	rec := &recorder{TB: t}
	logger := s.CaptureLogger(rec)
	logger.Info("totp enrolled", "user", "alice")
	rec.finish()
	if len(rec.errors) != 0 {
		t.Fatalf("clean logs reported: %v", rec.errors)
	}

	rec = &recorder{TB: t}
	logger = s.CaptureLogger(rec)
	logger.Debug("totp enrolled", "seed", seed)
	if len(rec.errors) != 0 {
		t.Fatal("checked before cleanup")
	}
	rec.finish()
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "secret leak in logs") {
		t.Errorf("errors = %v", rec.errors)
	}
}

func TestWriterChecksJobOutput(t *testing.T) {
	s := New()
	cred := s.New(RegistryCredential, "registry password")
	rec := &recorder{TB: t}
	w := s.Writer(rec, "job output")
	_, _ = io.WriteString(w, "pulling image...\n")
	_, _ = io.WriteString(w, "login with "+cred+"\n")
	rec.finish()
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "job output") {
		t.Errorf("errors = %v", rec.errors)
	}
}

func TestHTTPHelpers(t *testing.T) {
	s := New()
	tok := s.New(APIToken, "token")
	leaky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/header":
			w.Header().Set("X-Debug", tok)
		case "/body":
			_, _ = io.WriteString(w, `{"token":"`+tok+`"}`)
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "clean")
		}
	})

	rec := &recorder{TB: t}
	srv := httptest.NewServer(s.Handler(rec, leaky))
	defer srv.Close()
	for _, p := range []string{"/clean", "/header", "/body"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+p, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if p == "/clean" && resp.StatusCode != http.StatusTeapot {
			t.Errorf("wrapped status = %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if len(rec.errors) != 2 || !strings.Contains(rec.errors[0], "GET /header response headers") || !strings.Contains(rec.errors[1], "GET /body response body") {
		t.Errorf("handler errors = %v", rec.errors)
	}

	rec = &recorder{TB: t}
	r := httptest.NewRecorder()
	leaky.ServeHTTP(r, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/body", nil))
	s.CheckRecorder(rec, r)
	if len(rec.errors) != 1 {
		t.Errorf("CheckRecorder errors = %v", rec.errors)
	}

	rec = &recorder{TB: t}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/body", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	s.CheckResponse(rec, resp)
	body, _ := io.ReadAll(resp.Body)
	if len(rec.errors) != 1 || !strings.Contains(string(body), "token") {
		t.Errorf("CheckResponse errors = %v, body restored = %q", rec.errors, body)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
