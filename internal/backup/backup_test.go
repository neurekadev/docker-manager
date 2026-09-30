package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/restic"
)

func sampleManifest() Manifest {
	at := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	dest := Destination{Kind: KindS3, Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: "docker-manager", Region: "eu-central-1"}
	return Manifest{
		Kind: ManifestSet, SetID: "set-1", InstanceID: "inst-1", PolicyID: "pol-1", PolicyName: "Nightly",
		StartedAt: at, CreatedAt: at.Add(time.Minute), App: AppInfo{Version: "1.0.0", Commit: "abc"},
		Schema:       &SchemaInfo{Migrations: []string{"20260924000000_create_instance", "20260925214126_create_schedules"}},
		Completeness: StatePartial,
		Repositories: []RepositoryRef{{ID: "repo-1", Name: "Offsite", Destination: dest, KeyFingerprint: "rk_0011223344556677", KeyGeneration: 1}},
		Locations: []LocationRef{
			{RepositoryID: "repo-1", Scope: EnvironmentScope("env-b"), Repository: dest.Repository(EnvironmentScope("env-b")), EnvironmentID: "env-b"},
			{RepositoryID: "repo-1", Scope: ScopeManager, Repository: dest.Repository(ScopeManager)},
		},
		Members: []Member{
			{Item: StackItem("st-1"), Kind: MemberStack, Scope: EnvironmentScope("env-b"), RepositoryID: "repo-1", StackID: "st-1", State: StatePending},
			{Item: ItemManagerState, Kind: MemberManagerState, Scope: ScopeManager, RepositoryID: "repo-1", SnapshotID: "m1", State: StateComplete,
				Consistency: ConsistencySnapshot},
		},
	}
}

func TestManifestRoundTripIsStable(t *testing.T) {
	m := sampleManifest()
	a, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	// Order of the input lists does not change the encoding.
	m2 := sampleManifest()
	slices.Reverse(m2.Members)
	slices.Reverse(m2.Locations)
	b, err := EncodeManifest(m2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("encoding depends on list order")
	}
	got, err := DecodeManifest(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.SetID != "set-1" || got.Schema.Latest() != "20260925214126_create_schedules" || len(got.Members) != 2 ||
		got.Members[0].Scope != EnvironmentScope("env-b") || got.Repositories[0].Destination.Bucket != "backups" {
		t.Errorf("decoded = %+v", got)
	}
	if strings.Contains(string(a), "secret") || strings.Contains(string(a), "AccessKey") {
		t.Errorf("manifest mentions credentials: %s", a)
	}
}

func TestManifestDetectsCorruptionAndTruncation(t *testing.T) {
	good, err := EncodeManifest(sampleManifest())
	if err != nil {
		t.Fatal(err)
	}
	flip := bytes.Clone(good)
	flip[len(flip)-10] ^= 0x01
	newer := bytes.Replace(bytes.Clone(good), []byte("DOCKER-MANAGER-MANIFEST v1"), []byte("DOCKER-MANAGER-MANIFEST v9"), 1)
	cases := map[string]struct {
		in   []byte
		want error
	}{
		"truncated body":   {good[:len(good)-20], ErrManifestTruncated},
		"header only":      {good[:30], ErrManifestTruncated},
		"flipped bit":      {flip, ErrManifestCorrupt},
		"trailing data":    {append(bytes.Clone(good), '}'), ErrManifestCorrupt},
		"not a manifest":   {[]byte("{\"format\":\"x\"}\n{}"), ErrManifestCorrupt},
		"empty":            {nil, ErrManifestCorrupt},
		"newer version":    {newer, ErrManifestUnsupported},
		"garbage checksum": {bytes.Replace(bytes.Clone(good), []byte("sha256="), []byte("sha256=00"), 1), ErrManifestCorrupt},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeManifest(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCompletenessAndMerge(t *testing.T) {
	set := sampleManifest()
	// Manager complete, stack pending: pending until the host reports.
	if _, state := Merge(set, nil); state != StatePending {
		t.Errorf("state without host = %s", state)
	}
	host := Manifest{Kind: ManifestHost, SetID: "set-1", Members: []Member{
		{Item: StackItem("st-1"), Kind: MemberStack, Scope: EnvironmentScope("env-b"), SnapshotID: "h1", State: StateComplete},
	}}
	other := Manifest{Kind: ManifestHost, SetID: "set-2", Members: []Member{
		{Item: StackItem("st-1"), Kind: MemberStack, Scope: EnvironmentScope("env-b"), SnapshotID: "zz", State: StateFailed},
	}}
	members, state := Merge(set, []Manifest{other, host})
	if state != StateComplete || (members[0].SnapshotID != "h1" && members[1].SnapshotID != "h1") {
		t.Errorf("merged = %+v %s", members, state)
	}
	for _, tc := range []struct {
		states []string
		snaps  []string
		want   string
	}{
		{[]string{StateComplete, StateComplete}, []string{"a", "b"}, StateComplete},
		{[]string{StateComplete, StateFailed}, []string{"a", ""}, StatePartial},
		{[]string{StateFailed, StateMissing}, []string{"", ""}, StateFailed},
		{[]string{StateComplete, StatePending}, []string{"a", ""}, StatePending},
		{[]string{StatePartial}, []string{"a"}, StatePartial},
		{nil, nil, StateFailed},
		// Skipped members (removed before their turn) count neither way.
		{[]string{StateComplete, StateSkipped}, []string{"a", ""}, StateComplete},
		{[]string{StateSkipped, StateFailed}, []string{"", ""}, StateFailed},
		{[]string{StateComplete, StateSkipped, StateFailed}, []string{"a", "", ""}, StatePartial},
		{[]string{StatePending, StateSkipped}, []string{"", ""}, StatePending},
		// Only skipped members: nothing was backed up, nothing failed.
		{[]string{StateSkipped, StateSkipped}, []string{"", ""}, StateSkipped},
	} {
		var ms []Member
		for i, s := range tc.states {
			ms = append(ms, Member{State: s, SnapshotID: tc.snaps[i]})
		}
		if got := Completeness(ms); got != tc.want {
			t.Errorf("Completeness(%v) = %s, want %s", tc.states, got, tc.want)
		}
	}
}

func day(d int, h int) time.Time { return time.Date(2026, 3, d, h, 0, 0, 0, time.UTC) }

func TestRetentionRules(t *testing.T) {
	var snaps []RetentionSnapshot
	for d := 1; d <= 20; d++ {
		snaps = append(snaps, RetentionSnapshot{ID: "a" + time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC).Format("0102") + "-02", Time: day(d, 2), Item: "stack/a"})
		snaps = append(snaps, RetentionSnapshot{ID: "a" + time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC).Format("0102") + "-14", Time: day(d, 14), Item: "stack/a"})
	}
	snaps = append(snaps, RetentionSnapshot{ID: "b-only", Time: day(1, 3), Item: "volume/b"})

	p := Plan(RetentionRules{Daily: 7, Weekly: 2, Last: 3}, snaps, time.UTC)
	kept := map[string][]string{}
	for _, d := range p.Decisions {
		if d.Keep {
			kept[d.ID] = d.Reasons
		}
	}
	// Daily keeps the newest snapshot of the 7 newest days (14:00 runs).
	for d := 14; d <= 20; d++ {
		id := "a" + time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC).Format("0102") + "-14"
		if _, ok := kept[id]; !ok {
			t.Errorf("daily snapshot %s removed", id)
		}
	}
	// Last keeps the three newest, including the 02:00 run of day 20.
	if r := kept["a0320-02"]; !slices.Contains(r, "last") {
		t.Errorf("a0320-02 reasons = %v, want last", r)
	}
	if r := kept["a0320-14"]; !slices.Contains(r, "newest") {
		t.Errorf("newest reasons = %v", r)
	}
	// A lone snapshot of another item is always kept.
	if _, ok := kept["b-only"]; !ok {
		t.Error("the only snapshot of an item was removed")
	}
	if slices.Contains(p.Remove(), "a0320-14") || !slices.Contains(p.Remove(), "a0301-02") {
		t.Errorf("remove = %v", p.Remove())
	}
	if p.Kept()+len(p.Remove()) != len(snaps) {
		t.Errorf("kept %d + removed %d != %d", p.Kept(), len(p.Remove()), len(snaps))
	}
}

func TestRetentionLastWithinAndNoRules(t *testing.T) {
	snaps := []RetentionSnapshot{{ID: "1", Time: day(1, 1), Item: "x"}, {ID: "2", Time: day(1, 2), Item: "x"}, {ID: "3", Time: day(1, 3), Item: "x"}}
	// Last keeps the newest N whatever the other rules say.
	p := Plan(RetentionRules{Last: 2, Yearly: 1}, snaps, time.UTC)
	if got := p.Remove(); !slices.Equal(got, []string{"1"}) {
		t.Errorf("remove = %v", got)
	}
	// No rules: keep everything.
	if got := Plan(RetentionRules{}, snaps, time.UTC).Remove(); len(got) != 0 {
		t.Errorf("empty rules removed %v", got)
	}
	// Within keeps everything newer than N days before the newest.
	p = Plan(RetentionRules{WithinDays: 1}, []RetentionSnapshot{
		{ID: "old", Time: day(1, 0), Item: "x"}, {ID: "mid", Time: day(9, 12), Item: "x"}, {ID: "new", Time: day(10, 6), Item: "x"}}, time.UTC)
	if got := p.Remove(); !slices.Equal(got, []string{"old"}) {
		t.Errorf("within remove = %v", got)
	}
	// The rules always keep an item's newest snapshot.
	if errs := (RetentionRules{Daily: 3}).Validate(); len(errs) != 0 {
		t.Errorf("valid rules refused: %v", errs)
	}
	if got := Plan(RetentionRules{Yearly: 1}, snaps, time.UTC).Remove(); !slices.Equal(got, []string{"2", "1"}) {
		t.Errorf("yearly 1 removed %v", got)
	}
	if errs := (RetentionRules{Daily: -1}).Validate(); errs["daily"] == "" {
		t.Error("negative rule accepted")
	}
}

// TestRetentionExpiresDeletedItems: the rules alone would keep a deleted
// item's last snapshots forever (they judge it by its own snapshots);
// Expire removes all of them, whatever the rules keep, and leaves other items to
// the rules.
func TestRetentionExpiresDeletedItems(t *testing.T) {
	snaps := []RetentionSnapshot{
		{ID: "g1", Time: day(1, 1), Item: "volume/gone"}, {ID: "g2", Time: day(2, 1), Item: "volume/gone"},
		{ID: "k1", Time: day(1, 1), Item: "volume/kept"}, {ID: "k2", Time: day(2, 1), Item: "volume/kept"},
	}
	rules := RetentionRules{Last: 5}
	if got := Plan(rules, snaps, time.UTC).Remove(); len(got) != 0 {
		t.Fatalf("the rules removed %v", got)
	}
	p := Plan(rules, snaps, time.UTC).Expire([]string{"volume/gone"})
	if got := p.Remove(); !slices.Equal(got, []string{"g2", "g1"}) {
		t.Errorf("expired removal = %v", got)
	}
	for _, d := range p.Decisions {
		if d.Item == "volume/gone" && !slices.Equal(d.Reasons, []string{ReasonDeleted}) {
			t.Errorf("expired decision %+v", d)
		}
	}
	// Expiry works without rules too (keep everything else).
	if got := Plan(RetentionRules{}, snaps, time.UTC).Expire([]string{"volume/gone"}).Remove(); len(got) != 2 {
		t.Errorf("expiry without rules removed %v", got)
	}
}

func TestRetentionUsesPolicyTimeZone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zone data")
	}
	// 23:30 UTC on March 1 is March 2 in Berlin: two different local days.
	snaps := []RetentionSnapshot{{ID: "a", Time: time.Date(2026, 3, 1, 22, 30, 0, 0, time.UTC), Item: "x"},
		{ID: "b", Time: time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC), Item: "x"}}
	if got := Plan(RetentionRules{Daily: 2}, snaps, berlin).Remove(); len(got) != 0 {
		t.Errorf("berlin days: removed %v", got)
	}
	if got := Plan(RetentionRules{Daily: 2}, snaps, time.UTC).Remove(); !slices.Equal(got, []string{"a"}) {
		t.Errorf("utc days: removed %v", got)
	}
}

func TestDestinations(t *testing.T) {
	s3 := Destination{Kind: KindS3, Endpoint: "http://minio:9000", Bucket: "docker-manager", Prefix: "site-a", PathStyle: true}
	if err := s3.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := s3.Repository(EnvironmentScope("e1")); got != "s3:http://minio:9000/docker-manager/site-a/docker-manager-env-e1" {
		t.Errorf("s3 repository = %s", got)
	}
	loc := s3.Location(ScopeManager, S3Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"})
	if loc.S3 == nil || !loc.S3.PathStyle || loc.Repository != "s3:http://minio:9000/docker-manager/site-a/docker-manager" {
		t.Errorf("location = %#v", loc)
	}
	local := Destination{Kind: KindLocal, Path: "/backups/docker-manager"}
	if err := local.Validate(); err != nil || local.Repository(ScopeManager) != "/backups/docker-manager/docker-manager" {
		t.Errorf("local: %v %s", err, local.Repository(ScopeManager))
	}
	for _, bad := range []Destination{
		{Kind: KindLocal, Path: "relative"}, {Kind: KindLocal, Path: "/"}, {Kind: KindLocal, Path: "/a/../b"},
		{Kind: KindS3, Endpoint: "ftp://x", Bucket: "bkt"}, {Kind: KindS3, Endpoint: "https://u:p@x", Bucket: "bkt"},
		{Kind: KindS3, Endpoint: "https://x", Bucket: "B"}, {Kind: KindS3, Endpoint: "https://x", Bucket: "bkt", Prefix: "/a"},
		{Kind: KindS3, Endpoint: "https://x", Bucket: "bkt", Prefix: "a/../b"}, {Kind: "nfs"},
		{Kind: KindLocal, Path: "/backups", Compression: "fastest"}, {Kind: KindS3, Endpoint: "https://x", Bucket: "bkt", Compression: "MAX"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for _, s := range []string{ScopeManager, EnvironmentScope("0190-abc")} {
		if !ValidScope(s) || ScopeOfDir(ScopeDir(s)) != s {
			t.Errorf("scope %s round trip", s)
		}
	}
	if ValidScope("env:") || ValidScope("env:../x") || ScopeOfDir("other") != "" {
		t.Error("invalid scopes accepted")
	}
	if !Within("/a/b", "/a") || Within("/ab", "/a") || !Within("/a", "/a") {
		t.Error("Within")
	}
	tags := []string{TagDockerManager, SetTag("s1"), PolicyTag("p1"), ItemTag(StackItem("st"))}
	if SetOf(tags) != "s1" || PolicyOf(tags) != "p1" || ItemOf(tags) != "stack/st" {
		t.Errorf("tag values")
	}
}

// TestScopeEnvironment: only environment scopes name an environment; the
// manager scope (and anything else) names none, even if the caller ignores
// the second result.
func TestDestinationCompression(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"", ""}, {restic.CompressionAuto, ""}, {restic.CompressionMax, restic.CompressionMax}, {restic.CompressionOff, restic.CompressionOff},
	} {
		for _, d := range []Destination{
			{Kind: KindLocal, Path: "/backups", Compression: tc.mode},
			{Kind: KindS3, Endpoint: "https://s3.example.com", Bucket: "bkt", Compression: tc.mode},
		} {
			if err := d.Validate(); err != nil {
				t.Errorf("%s %q: %v", d.Kind, tc.mode, err)
			}
			if got := d.Location(ScopeManager, S3Credentials{}).Compression; got != tc.want {
				t.Errorf("%s %q: location compression = %q, want %q", d.Kind, tc.mode, got, tc.want)
			}
		}
	}
	// Auto is never written out: older agents and manifests see the
	// destination they know.
	b, err := json.Marshal(Destination{Kind: KindLocal, Path: "/backups"})
	if err != nil || strings.Contains(string(b), "compression") {
		t.Errorf("auto destination = %s (%v)", b, err)
	}
	b, _ = json.Marshal(Destination{Kind: KindLocal, Path: "/backups", Compression: restic.CompressionMax})
	if !strings.Contains(string(b), `"compression":"max"`) {
		t.Errorf("max destination = %s", b)
	}
}

func TestScopeEnvironment(t *testing.T) {
	for scope, want := range map[string]string{EnvironmentScope("e1"): "e1", ScopeManager: "", "env:": "", "other": ""} {
		if got, ok := ScopeEnvironment(scope); got != want || ok != (want != "") {
			t.Errorf("ScopeEnvironment(%q) = %q, %v", scope, got, ok)
		}
	}
}
