package testharness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// S3Client is a minimal SigV4 S3 client for fixture setup and assertions
// (bucket creation, object put/get/head). It is not a general S3 SDK.
type S3Client struct {
	Endpoint  string // http://host:port
	AccessKey string
	SecretKey string
	Region    string
	HTTP      *http.Client
	// Now is the signing time source; nil means time.Now (request signing
	// needs real time: servers reject skewed signatures).
	Now func() time.Time
}

// Do sends a signed request for /bucket[/key] and returns status and body.
func (c *S3Client) Do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	SignV4(req, c.AccessKey, c.SecretKey, region, "s3", now(), PayloadHash(body))
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, err
}

// CreateBucket creates a bucket (idempotent for buckets this user owns).
func (c *S3Client) CreateBucket(ctx context.Context, bucket string) error {
	status, body, err := c.Do(ctx, http.MethodPut, "/"+bucket, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && !bytes.Contains(body, []byte("BucketAlreadyOwnedByYou")) {
		return fmt.Errorf("create bucket %s: HTTP %d: %s", bucket, status, body)
	}
	return nil
}

// PutObject stores data at bucket/key.
func (c *S3Client) PutObject(ctx context.Context, bucket, key string, data []byte) error {
	status, body, err := c.Do(ctx, http.MethodPut, "/"+bucket+"/"+key, data)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("put %s/%s: HTTP %d: %s", bucket, key, status, body)
	}
	return nil
}

// GetObject reads bucket/key.
func (c *S3Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	status, body, err := c.Do(ctx, http.MethodGet, "/"+bucket+"/"+key, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get %s/%s: HTTP %d: %s", bucket, key, status, body)
	}
	return body, nil
}

// ListKeys lists object keys under prefix (first 1000).
func (c *S3Client) ListKeys(ctx context.Context, bucket, prefix string) ([]string, error) {
	status, body, err := c.Do(ctx, http.MethodGet, "/"+bucket+"?list-type=2&prefix="+awsEscape(prefix), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list %s: HTTP %d: %s", bucket, status, body)
	}
	var keys []string
	for _, part := range strings.Split(string(body), "<Key>")[1:] {
		if k, _, ok := strings.Cut(part, "</Key>"); ok {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
