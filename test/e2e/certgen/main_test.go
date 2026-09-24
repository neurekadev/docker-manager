package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGeneratedPKIServesVerifiedTLS: a server using the generated leaf is
// trusted by a client that trusts only the generated CA, for localhost and
// 127.0.0.1; a second run keeps the existing files.
func TestGeneratedPKIServesVerifiedTLS(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	ca, _ := os.ReadFile(filepath.Join(dir, CAFile))
	if err := Generate(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, CAFile)); string(again) != string(ca) {
		t.Fatal("existing CA was replaced")
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("CA not PEM")
	}
	for _, name := range []string{"localhost", "127.0.0.1"} {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: name, MinVersion: tls.VersionTLS12}}}
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
	}
}
