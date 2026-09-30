package s3probe_test

import (
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/manager/backups/s3probe"
	"github.com/neurekadev/docker-manager/internal/manager/backups/s3probe/s3probetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

func TestProbeCapabilitiesAndObjectLock(t *testing.T) {
	set := canary.New()
	access := set.New(canary.S3AccessKey, "access")
	secret := set.New(canary.S3SecretKey, "secret")
	fake := s3probetest.New(t, access, "backups")
	now := func() time.Time { return time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC) }
	target := s3probe.Target{Endpoint: fake.URL, Bucket: "backups", Prefix: "docker-manager", PathStyle: true, AccessKeyID: access, SecretAccessKey: secret}

	r := s3probe.Probe(testutil.Context(t), fake.Client(), target, now)
	if r.Class != "" || !*r.CanWrite || !*r.CanRead || !*r.CanDelete || r.ObjectLock == nil || *r.ObjectLock {
		t.Fatalf("result = %+v", r)
	}
	if fake.Objects() != 0 {
		t.Error("the probe object was left behind")
	}
	for _, req := range fake.Requests {
		if !strings.HasPrefix(strings.Fields(req)[1], "/backups/") {
			t.Errorf("request outside the bucket: %s", req)
		}
		set.AssertClean(t, "request line", req)
	}

	fake.ObjectLock, fake.DenyDelete = true, true
	r = s3probe.Probe(testutil.Context(t), fake.Client(), target, now)
	if r.Class != "access_denied" || *r.CanDelete || r.ObjectLock == nil || !*r.ObjectLock {
		t.Errorf("locked bucket: %+v", r)
	}
	set.AssertClean(t, "message", r.Message)

	bad := target
	bad.AccessKeyID = "AKIAWRONG000000000000"
	if r := s3probe.Probe(testutil.Context(t), fake.Client(), bad, now); r.Class != "access_denied" || *r.CanWrite {
		t.Errorf("wrong key: %+v", r)
	}
	missing := target
	missing.Bucket = "nope"
	if r := s3probe.Probe(testutil.Context(t), fake.Client(), missing, now); r.Class != "bucket_not_found" {
		t.Errorf("missing bucket: %+v", r)
	}
	gone := target
	gone.Endpoint = "http://127.0.0.1:1"
	if r := s3probe.Probe(testutil.Context(t), nil, gone, now); r.Class != "unreachable" {
		t.Errorf("unreachable: %+v", r)
	}
}
