package canary

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neurekadev/dockyard/internal/testutil"
)

// AssertClean fails t for every canary found in v (see ScanValue). where
// names the sink in the failure message, e.g. "audit rows".
func (s *Set) AssertClean(t testing.TB, where string, v any) {
	t.Helper()
	for _, l := range s.ScanValue(v) {
		t.Errorf("secret leak in %s: %s", where, l)
	}
}

// CaptureLogger returns a JSON slog logger (debug level) whose output is
// checked for canaries when the test ends. Pass it to the code under test
// (or into a context with logging.IntoContext).
func (s *Set) CaptureLogger(t testing.TB) *slog.Logger {
	t.Helper()
	logger, buf := testutil.CaptureLogger()
	s.CheckLogsAtCleanup(t, buf)
	return logger
}

// CheckLogsAtCleanup checks an existing log buffer when the test ends.
func (s *Set) CheckLogsAtCleanup(t testing.TB, buf *testutil.LogBuffer) {
	t.Helper()
	t.Cleanup(func() { s.AssertClean(t, "logs", buf.String()) })
}

// Writer returns a writer for captured output (job output, streamed logs)
// that is checked when the test ends.
func (s *Set) Writer(t testing.TB, where string) io.Writer {
	t.Helper()
	w := &syncBuffer{}
	t.Cleanup(func() { s.AssertClean(t, where, w.String()) })
	return w
}

// CheckRecorder checks the headers and body of a recorded response.
func (s *Set) CheckRecorder(t testing.TB, rec *httptest.ResponseRecorder) {
	t.Helper()
	s.AssertClean(t, "response headers", headerText(rec.Result().Header))
	s.AssertClean(t, "response body", rec.Body.Bytes())
}

// CheckResponse checks a client-side response and restores its body so
// the caller can still read it.
func (s *Set) CheckResponse(t testing.TB, resp *http.Response) {
	t.Helper()
	s.AssertClean(t, "response headers", headerText(resp.Header))
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Errorf("canary: read response body: %v", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	s.AssertClean(t, "response body", body)
}

// Handler wraps h so that every response (status line headers and body)
// is checked for canaries after the handler returns. The response is
// buffered, so use it for request/response APIs; check streams with Writer.
func (s *Set) Handler(t testing.TB, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		where := r.Method + " " + r.URL.Path
		s.AssertClean(t, where+" response headers", headerText(rec.Result().Header))
		s.AssertClean(t, where+" response body", rec.Body.Bytes())
		for k, vs := range rec.Result().Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func headerText(h http.Header) string {
	var b strings.Builder
	_ = h.Write(&b)
	return b.String()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
