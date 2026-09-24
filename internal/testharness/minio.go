//go:build integration

package testharness

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MinIOOptions configures StartMinIO.
type MinIOOptions struct {
	// Network attaches MinIO (alias "minio"); nil creates one.
	Network *Network
	// Buckets are created after startup.
	Buckets []string
}

// MinIO is an S3-compatible server with generated root credentials.
type MinIO struct {
	// Endpoint is the S3 endpoint from the test process
	// (http://127.0.0.1:<port>); InternalEndpoint is http://minio:9000 on
	// the network (for manager/agent containers).
	Endpoint         string
	InternalEndpoint string
	AccessKey        string
	SecretKey        string
	Region           string
	S3               *S3Client
	Network          *Network
	Container        *testcontainers.DockerContainer
}

// StartMinIO starts MinIO with random credentials and creates the buckets.
func StartMinIO(t testing.TB, opts MinIOOptions) *MinIO {
	t.Helper()
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	access := RandomSecret("AKDY", 8)
	secret := RandomSecret("", 20)
	c := run(t, MinIOImage,
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     access,
			"MINIO_ROOT_PASSWORD": secret,
			"MINIO_REGION":        "us-east-1",
		}),
		// The image runs as the unprivileged "minio" user, which cannot
		// write the root-owned /data volume; serve from its home instead.
		testcontainers.WithCmd("server", "/home/minio/data"),
		testcontainers.WithExposedPorts("9000/tcp"),
		opts.Network.option("minio"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(2*time.Minute)),
	)
	m := &MinIO{
		Endpoint:         "http://" + endpoint(t, c, "9000/tcp"),
		InternalEndpoint: "http://minio:9000",
		AccessKey:        access,
		SecretKey:        secret,
		Region:           "us-east-1",
		Network:          opts.Network,
		Container:        c,
	}
	m.S3 = &S3Client{Endpoint: m.Endpoint, AccessKey: access, SecretKey: secret, Region: m.Region}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, b := range opts.Buckets {
		if err := m.S3.CreateBucket(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// ResticRepository returns the restic repository URL for bucket/path as
// seen from the test process.
func (m *MinIO) ResticRepository(bucket, path string) string {
	return "s3:" + m.Endpoint + "/" + bucket + "/" + path
}

// ResticEnv returns the credential environment restic needs for this
// MinIO (append RESTIC_PASSWORD yourself).
func (m *MinIO) ResticEnv() []string {
	return []string{
		"AWS_ACCESS_KEY_ID=" + m.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + m.SecretKey,
		"AWS_DEFAULT_REGION=" + m.Region,
	}
}
