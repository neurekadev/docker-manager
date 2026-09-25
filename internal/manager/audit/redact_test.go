package audit_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

func TestSensitiveKeys(t *testing.T) {
	for _, k := range []string{"password", "newPassword", "db_password", "Secret", "clientSecret", "token", "apiToken",
		"refresh-token", "apiKey", "accessKey", "privateKey", "credential", "credentials", "Authorization", "cookie",
		"key", "env", "environment", "envVars", "dotenv", "content", "contents", "body", "data", "value", "values",
		"input", "output", "logs", "totp", "otp", "seed", "code", "recoveryCodes", "fileContent", "composeFile", "auth",
		"dockerConfigJson", "labels", "command", "args",
		// Not a metadata suffix: redacted (the old revocation detail key).
		"apiTokensRevoked"} {
		if !audit.SensitiveKey(k) {
			t.Errorf("SensitiveKey(%q) = false", k)
		}
	}
	for _, k := range []string{"tokenId", "tokenIds", "secretName", "credentialId", "passwordChangedAt", "keyCount",
		"environmentId", "name", "path", "kind", "state", "effect", "capability", "scope", "tokenScope", "status",
		"format", "reason", "deletedRecords", "items", "diff", "before", "after", "rules", "contentType",
		// Counts of revoked credentials (auth revocations, manager restore).
		"apiTokenCount", "agentsRevoked"} {
		if audit.SensitiveKey(k) {
			t.Errorf("SensitiveKey(%q) = true", k)
		}
	}
}

func TestLooksSecret(t *testing.T) {
	for _, s := range []string{
		"Bearer abcdefghijkl", "basic dXNlcjpwYXNzd29yZA==", "-----BEGIN OPENSSH PRIVATE KEY-----", "dy_0123456789abcdef",
		"dya_credentialvalue", "dye_enrollmentvalue", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig",
		"ghp_abcdefghijklmnopqrstuvwxyz", "https://user:pa55word@registry.example.com/v2", "password=hunter2",
		"DB_PASSWORD: x", "token: abc", "glpat-abcdefghijklmnopqrst",
	} {
		if !audit.LooksSecret(s) {
			t.Errorf("LooksSecret(%q) = false", s)
		}
	}
	for _, s := range []string{"stack.deploy", "web-1", "/data/app/config.yml", "Mozilla/5.0 (X11; Linux x86_64)",
		"0190a6e0-0000-7000-8000-000000000001", "https://registry.example.com/v2", "dynamic_frontend_app", "nginx:1.27"} {
		if audit.LooksSecret(s) {
			t.Errorf("LooksSecret(%q) = true", s)
		}
	}
}

type ruleDTO struct {
	Capability string `json:"capability"`
	Effect     string `json:"effect"`
	Secret     string `json:"-"`
	hidden     string
}

func TestRedactDetails(t *testing.T) {
	long := strings.Repeat("x", audit.MaxDetailString+1)
	in := map[string]any{
		"password": "hunter2hunter2",
		"nested":   map[string]any{"env": map[string]any{"DB_URL": "postgres://u:p@db/x"}, "name": "web", "note": "Bearer abcdefghijk"},
		"tokens":   []string{"a", "b"},
		"names":    []string{"a", "b"},
		"blob":     []byte("file bytes"),
		"sealed":   logging.Secret("s3cr3t-s3cr3t"),
		"err":      errors.New("password=hunter2"),
		"fn":       func() {},
		"long":     long,
		"invalid":  string([]byte{0xff, 0xfe}),
		"raw":      json.RawMessage(`{"a":1}`),
		"rawOK":    json.RawMessage(`{"value":"x","count":1}`),
		"rule":     ruleDTO{Capability: "stack.read", Effect: "allow", Secret: "zzz", hidden: "h"},
		"nilPtr":   (*ruleDTO)(nil),
		"n":        3,
		"ok":       true,
		"deep":     map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{"e": map[string]any{"f": map[string]any{"g": map[string]any{"h": "x"}}}}}}}},
	}
	out := audit.RedactDetails(in)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, leak := range []string{"hunter2", "postgres://", "abcdefghijk", "file bytes", "s3cr3t", "zzz", "xxxxxxxx"} {
		if strings.Contains(text, leak) {
			t.Errorf("redacted details contain %q: %s", leak, text)
		}
	}
	nested := out["nested"].(map[string]any)
	rule := out["rule"].(map[string]any)
	rawOK := out["rawOK"].(map[string]any)
	if out["password"] != audit.Redacted || nested["env"] != audit.Redacted || nested["name"] != "web" || nested["note"] != audit.Redacted ||
		out["tokens"] != audit.Redacted || len(out["names"].([]any)) != 2 || out["blob"] != audit.Redacted ||
		out["sealed"] != audit.Redacted || out["err"] != audit.Redacted || out["fn"] != audit.Redacted ||
		!strings.HasPrefix(out["long"].(string), "[OMITTED") || out["invalid"] != audit.Redacted || out["raw"] != audit.Redacted ||
		rawOK["value"] != audit.Redacted || rawOK["count"] != float64(1) || rule["capability"] != "stack.read" || len(rule) != 2 ||
		out["nilPtr"] != nil || out["n"] != 3 || out["ok"] != true || !strings.Contains(text, "nested too deeply") {
		t.Fatalf("redacted %s", text)
	}

	// Sizes are bounded.
	many := map[string]any{}
	for i := range 300 {
		many[strings.Repeat("k", 3)+string(rune('a'+i%26))+strings.Repeat("z", i/26)] = strings.Repeat("v", 200)
	}
	c, err := audit.CanonicalDetails(many)
	if err != nil || len(c) > audit.MaxDetailsBytes || !strings.Contains(string(c), "_omitted") {
		t.Fatalf("bounded details %d %v %.80s", len(c), err, c)
	}
	if c, _ := audit.CanonicalDetails(nil); string(c) != "{}" {
		t.Fatalf("empty details %s", c)
	}
}

// TestSecretCanariesNeverStored seeds every canary kind into every field a
// caller can pass and asserts that neither the stored rows nor the log
// mirror contain any of them (#29 canaries, #30 Done-when).
func TestSecretCanariesNeverStored(t *testing.T) {
	c := canary.New()
	values := map[canary.Kind]string{}
	for _, k := range canary.Kinds {
		values[k] = c.New(k, string(k))
	}
	mirror, buf := testutil.CaptureLogger()
	f := newFixture(t, func(o *audit.Options) { o.Mirror = mirror })
	details := map[string]any{
		"password": values[canary.Password], "token": values[canary.APIToken], "registryCredential": values[canary.RegistryCredential],
		"accessKey": values[canary.S3AccessKey], "secretKey": values[canary.S3SecretKey], "totpSeed": values[canary.TOTPSeed],
		"env":         map[string]any{"DB_PASSWORD": values[canary.EnvValue]},
		"environment": []string{"API_KEY=" + values[canary.EnvValue]},
		"content":     "services:\n  db:\n    environment:\n      PASSWORD: " + values[canary.Password],
		"note":        "Authorization: Bearer " + values[canary.APIToken],
		"url":         "https://robot:" + values[canary.RegistryCredential] + "@registry.example.com",
		"diff": map[string]any{
			"before": map[string]any{"value": values[canary.EnvValue]},
			"after":  map[string]any{"secretAccessKey": values[canary.S3SecretKey], "accessKeyId": "AKIA" + strings.Repeat("A", 16)},
		},
	}
	for _, k := range canary.Kinds {
		details["input_"+string(k)] = map[string]any{"input": values[k]}
	}
	f.record(domain.AuditEvent{
		Action: "registry.update", Details: details,
		UserAgent: "curl/8 Bearer " + values[canary.APIToken],
		Targets:   []domain.AuditTarget{{Type: "api_token", ID: values[canary.APIToken]}, {Type: "registry", ID: "reg-1"}},
		JobID:     "Bearer " + values[canary.APIToken], ErrorClass: values[canary.Password],
	})
	rows := dumpRows(t, f)
	if !strings.Contains(rows, "reg-1") {
		t.Fatalf("record missing: %s", rows)
	}
	c.AssertClean(t, "audit_events rows", rows)
	c.AssertClean(t, "audit records", f.all())
	c.AssertClean(t, "audit mirror log", buf.String())
	if rep := f.verify(); !rep.OK {
		t.Fatalf("verify %+v", rep)
	}
}

// dumpRows renders every column of every audit row.
func dumpRows(t *testing.T, f *fixture) string {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx, `SELECT * FROM audit_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var sb strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(vals)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func FuzzCanonicalDetails(f *testing.F) {
	f.Add("password", "hunter2", "note")
	f.Add("env", `{"A":"b"}`, "value")
	f.Add("name", "Bearer abcdefghijkl", "path")
	f.Add("x", strings.Repeat("y", 2000), "")
	f.Add("", "\x00\xff", "tokenId")
	f.Fuzz(func(t *testing.T, key, value, key2 string) {
		b, err := audit.CanonicalDetails(map[string]any{key: value, key2: map[string]any{key: []any{value}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > audit.MaxDetailsBytes || !json.Valid(b) {
			t.Fatalf("invalid canonical details %q", b)
		}
		if audit.LooksSecret(value) && strings.Contains(string(b), value) {
			t.Fatalf("secret-shaped value kept: %q", b)
		}
	})
}
