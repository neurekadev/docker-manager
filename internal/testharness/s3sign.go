package testharness

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// EmptyPayloadHash is hex(SHA-256("")).
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// SignV4 signs req in place with AWS Signature Version 4 (header auth), as
// needed to set up buckets on the MinIO fixture without an SDK dependency.
// It signs the Host header, every X-Amz-* header present on req, and
// Content-Type/Content-MD5 when set. payloadHash is hex(SHA-256(body)) or
// "UNSIGNED-PAYLOAD"; it is also sent as X-Amz-Content-Sha256 when service
// is "s3".
func SignV4(req *http.Request, accessKey, secretKey, region, service string, now time.Time, payloadHash string) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	headers := map[string]string{"host": host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-type" || lk == "content-md5" {
			vals := make([]string, len(vs))
			for i, v := range vs {
				vals[i] = strings.Join(strings.Fields(v), " ")
			}
			headers[lk] = strings.Join(vals, ",")
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method,
		path,
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")

	scope := date + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signed, sig))
}

// PayloadHash returns hex(SHA-256(body)).
func PayloadHash(body []byte) string {
	s := sha256.Sum256(body)
	return hex.EncodeToString(s[:])
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// awsEscape is RFC 3986 percent-encoding (unreserved characters kept).
func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
