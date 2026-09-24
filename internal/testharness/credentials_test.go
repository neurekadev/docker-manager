package testharness

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestHtpasswdLine(t *testing.T) {
	line, err := HtpasswdLine("alice", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	user, hash, ok := strings.Cut(line, ":")
	if !ok || user != "alice" {
		t.Fatalf("line = %q", line)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")); err != nil {
		t.Errorf("hash does not verify: %v", err)
	}
	for _, bad := range []string{"", "a:b", "a\nb"} {
		if _, err := HtpasswdLine(bad, "x"); err == nil {
			t.Errorf("HtpasswdLine(%q) succeeded", bad)
		}
	}
}

func TestRegistryAuthEncoding(t *testing.T) {
	enc := RegistryAuth("bob", "pw+/=", "registry.test:5000")
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["username"] != "bob" || got["password"] != "pw+/=" || got["serveraddress"] != "registry.test:5000" {
		t.Errorf("decoded = %v", got)
	}
}

func TestRandomSecret(t *testing.T) {
	a, b := RandomSecret("pw-", 8), RandomSecret("pw-", 8)
	if a == b || !strings.HasPrefix(a, "pw-") || len(a) != 3+16 {
		t.Errorf("RandomSecret = %q, %q", a, b)
	}
}

// Vectors from the AWS Signature Version 4 test suite (get-vanilla and
// get-vanilla-query-order-key-case).
func TestSignV4MatchesAWSTestSuite(t *testing.T) {
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	cases := []struct {
		url, want string
	}{
		{"https://example.amazonaws.com/", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"https://example.amazonaws.com/?Param2=value2&Param1=value1", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		SignV4(req, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service", now, EmptyPayloadHash)
		want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=" + c.want
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.url, got, want)
		}
	}
}

func TestSignV4S3SetsContentHash(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, "http://127.0.0.1:9000/bucket", nil)
	if err != nil {
		t.Fatal(err)
	}
	SignV4(req, "AK", "SK", "us-east-1", "s3", time.Unix(0, 0), EmptyPayloadHash)
	if req.Header.Get("X-Amz-Content-Sha256") != EmptyPayloadHash {
		t.Error("missing X-Amz-Content-Sha256")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;x-amz-content-sha256;x-amz-date,") {
		t.Errorf("Authorization = %s", req.Header.Get("Authorization"))
	}
	if PayloadHash(nil) != EmptyPayloadHash {
		t.Error("PayloadHash(nil) mismatch")
	}
}
