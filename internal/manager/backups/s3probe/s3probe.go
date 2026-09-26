// Package s3probe tests what an S3 key pair may do on a bucket (#10):
// read, write and delete below the repository prefix, and whether the
// bucket enforces Object Lock (which can refuse retention pruning). It
// signs requests with AWS Signature Version 4 and needs no SDK.
package s3probe

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Target is the bucket and prefix to probe.
type Target struct {
	Endpoint        string
	Bucket          string
	Prefix          string
	Region          string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
}

// Result is the outcome of a probe. Each capability is nil when it could
// not be determined.
type Result struct {
	CanWrite   *bool
	CanRead    *bool
	CanDelete  *bool
	ObjectLock *bool
	// Class is "" when every capability works, otherwise the first failure:
	// access_denied, bucket_not_found, unreachable, invalid_response.
	Class   string
	Message string
}

// Probe writes, reads and deletes a random object below the prefix and
// reads the bucket's Object Lock configuration. Messages never contain the
// credentials.
func Probe(ctx context.Context, client *http.Client, t Target, now func() time.Time) Result {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	p := prober{c: client, t: t, now: now}
	var r Result
	name := make([]byte, 8)
	_, _ = rand.Read(name)
	key := ".docker-manager-probe-" + hex.EncodeToString(name)
	if t.Prefix != "" {
		key = t.Prefix + "/" + key
	}
	body := []byte("docker-manager connection test\n")
	status, _, err := p.do(ctx, http.MethodPut, key, nil, body)
	r.CanWrite = boolPtr(err == nil && status/100 == 2)
	if fail := classify(status, err); fail != "" {
		r.Class, r.Message = fail, message("write", status, err)
	}
	if *r.CanWrite {
		status, got, err := p.do(ctx, http.MethodGet, key, nil, nil)
		r.CanRead = boolPtr(err == nil && status/100 == 2 && bytes.Equal(got, body))
		if fail := classify(status, err); fail != "" && r.Class == "" {
			r.Class, r.Message = fail, message("read", status, err)
		}
		status, _, err = p.do(ctx, http.MethodDelete, key, nil, nil)
		r.CanDelete = boolPtr(err == nil && status/100 == 2)
		if fail := classify(status, err); fail != "" && r.Class == "" {
			r.Class, r.Message = fail, message("delete", status, err)
		}
	}
	status, got, err := p.do(ctx, http.MethodGet, "", url.Values{"object-lock": {""}}, nil)
	switch {
	case err == nil && status == http.StatusOK:
		var cfg struct {
			Enabled string `xml:"ObjectLockEnabled"`
		}
		if xml.Unmarshal(got, &cfg) == nil {
			r.ObjectLock = boolPtr(strings.EqualFold(cfg.Enabled, "Enabled"))
		}
	case err == nil && status == http.StatusNotFound:
		// ObjectLockConfigurationNotFoundError: no lock.
		r.ObjectLock = boolPtr(false)
	}
	return r
}

// ListDirs lists the "directories" directly below the target's prefix
// (ListObjectsV2 with a delimiter; at most 1000). A fresh manager uses it to
// discover the scope repositories of a destination (#24). The class is ""
// on success, otherwise one of Probe's classes.
func ListDirs(ctx context.Context, client *http.Client, t Target, now func() time.Time) ([]string, string) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	p := prober{c: client, t: t, now: now}
	prefix := ""
	if t.Prefix != "" {
		prefix = t.Prefix + "/"
	}
	status, body, err := p.do(ctx, http.MethodGet, "", url.Values{"list-type": {"2"}, "delimiter": {"/"}, "prefix": {prefix}}, nil)
	if class := classify(status, err); class != "" {
		return nil, class
	}
	var res struct {
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}
	if xml.Unmarshal(body, &res) != nil {
		return nil, "invalid_response"
	}
	out := make([]string, 0, len(res.CommonPrefixes))
	for _, cp := range res.CommonPrefixes {
		name := strings.TrimSuffix(strings.TrimPrefix(cp.Prefix, prefix), "/")
		if name != "" && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, ""
}

func boolPtr(b bool) *bool { return &b }

func classify(status int, err error) string {
	switch {
	case err != nil:
		return "unreachable"
	case status/100 == 2:
		return ""
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return "access_denied"
	case status == http.StatusNotFound:
		return "bucket_not_found"
	}
	return "invalid_response"
}

func message(op string, status int, err error) string {
	if err != nil {
		return op + ": the endpoint could not be reached"
	}
	return fmt.Sprintf("%s: HTTP %d", op, status)
}

type prober struct {
	c   *http.Client
	t   Target
	now func() time.Time
}

func (p prober) url(key string, q url.Values) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSuffix(p.t.Endpoint, "/"))
	if err != nil {
		return nil, err
	}
	if p.t.PathStyle {
		u.Path = "/" + p.t.Bucket
		if key != "" {
			u.Path += "/" + key
		} else {
			u.Path += "/"
		}
	} else {
		u.Host = p.t.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	u.RawQuery = canonicalQuery(q)
	return u, nil
}

func (p prober) do(ctx context.Context, method, key string, q url.Values, body []byte) (int, []byte, error) {
	u, err := p.url(key, q)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = int64(len(body))
	region := p.t.Region
	if region == "" {
		region = "us-east-1"
	}
	sign(req, p.t.AccessKeyID, p.t.SecretAccessKey, region, p.now(), payloadHash(body))
	resp, err := p.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func payloadHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sign adds an AWS Signature Version 4 Authorization header (service s3).
func sign(req *http.Request, accessKey, secretKey, region string, now time.Time, hash string) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", hash)
	host := req.URL.Host
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headers := map[string]string{"host": host, "x-amz-content-sha256": hash, "x-amz-date": amzDate}
	sort.Strings(signed)
	var canonHeaders strings.Builder
	for _, h := range signed {
		canonHeaders.WriteString(h + ":" + strings.TrimSpace(headers[h]) + "\n")
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, req.URL.RawQuery, canonHeaders.String(), strings.Join(signed, ";"), hash}, "\n")
	scope := date + "/" + region + "/s3/aws4_request"
	crHash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(crHash[:])
	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
