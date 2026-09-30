package audit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
)

// Redaction layer (#30): audit records never contain secret values,
// tokens, file contents or .env values. Everything a caller passes goes
// through here before it is hashed and stored:
//
//   - Details: values under sensitive keys (password, secret, token,
//     credential, key, env/environment, content, body, data, value, input,
//     output, ...) become "[REDACTED]" — unless the key names an identifier
//     or metadata (…Id, …Name, …Count, …Type, …At). Byte slices,
//     logging.Secret values and errors are always redacted. Strings that
//     look like secrets anywhere (bearer/basic credentials, Docker Manager
//     token prefixes, private keys, JWTs, cloud keys, URL passwords,
//     "password=…" pairs) are redacted whatever their key. Long strings are
//     omitted (file contents), nesting and sizes are bounded.
//   - User agent, IDs and targets: control characters removed, bounded,
//     secret-looking values redacted.
//   - Messages (error texts, job messages), request bodies, query strings
//     and headers are never recorded at all; only stable error classes.
//
// This is a safety net: callers still record only what accountability
// needs (IDs, names, paths where required, rule diffs).

// Redacted replaces a removed value.
const Redacted = "[REDACTED]"

// Bounds.
const (
	// MaxDetailsBytes bounds a record's canonical details.
	MaxDetailsBytes = 16 << 10
	// MaxDetailString is the longest string kept in details (longer ones
	// are replaced by an omission marker: file contents never fit).
	MaxDetailString = 1024
	maxDetailDepth  = 8
	maxDetailItems  = 200
	maxIDLen        = 256
	maxUserAgentLen = 256
	maxTargets      = 100
)

var (
	actionRE     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	errorClassRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	requestIDRE  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

	// secretValueRE matches strings that carry a credential in any field.
	secretValueRE = regexp.MustCompile(strings.Join([]string{
		`(?i)\bbearer\s+\S{8,}`,
		`(?i)\bbasic\s+[A-Za-z0-9+/=_-]{8,}`,
		`-----BEGIN [A-Z0-9 ]*(PRIVATE KEY|CERTIFICATE REQUEST)`,
		`\bdy[aet]?_[A-Za-z0-9_-]{8,}`,                                // Docker Manager tokens/credentials (dya_, dye_, dy_, dyt_)
		`\b(AKIA|ASIA)[0-9A-Z]{16}\b`,                                 // AWS access key IDs
		`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`,                 // JWT
		`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`, // GitHub tokens
		`\bglpat-[A-Za-z0-9_-]{16,}`,                                  // GitLab tokens
		`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`,               // URL with password
		// Notification channel addresses (#142): Shoutrrr service URLs
		// carry webhook tokens and passwords in any part.
		`(?i)\b(bark|discord|generic|gotify|googlechat|hangouts|homeassistant|ifttt|join|lark|matrix|mattermost|mqtts?|notifiarr|ntfy|opsgenie|pagerduty|pushbullet|pushover|rocketchat|signal|signalgrid|slack|smtp|teams|telegram|twilio|wecom|xmpps?|zulip)(\+[a-z]+)?://\S`,
		`(?i)\bdiscord(app)?\.com/api/webhooks/\S`, // Discord webhook URLs
		`(?i)\bhooks\.slack\.com/services/\S`,      // Slack webhook URLs
		`\b\d{6,12}:[A-Za-z0-9_-]{30,}`,            // Telegram bot tokens
		`(?i)[?&]sig=[A-Za-z0-9%_-]{16,}`,          // signed webhook URLs (Teams workflows)
		`(?i)(pass(word|wd|phrase)?|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?["']?\s*[=:]\s*\S`,
	}, "|"))
)

// sensitiveSubstrings mark a key as sensitive wherever they occur
// (normalized: lower case, separators removed).
var sensitiveSubstrings = []string{
	"password", "passwd", "passphrase", "secret", "token", "apikey", "accesskey", "privatekey",
	"signingkey", "encryptionkey", "recoverykey", "credential", "authorization", "cookie",
	"recoverycode", "bearer", "totp", "mnemonic", "dotenv", "envfile", "envvar", "environmentvariable",
	"filecontent", "composefile", "composeyaml",
}

// sensitiveKeys mark a key as sensitive when it equals one of them.
var sensitiveKeys = map[string]bool{
	"key": true, "keys": true, "otp": true, "seed": true, "pin": true, "code": true, "codes": true,
	"auth": true, "env": true, "environment": true, "content": true, "contents": true, "body": true,
	"data": true, "payload": true, "yaml": true, "compose": true, "definition": true, "input": true,
	"output": true, "stdin": true, "stdout": true, "stderr": true, "log": true, "logs": true,
	"value": true, "values": true, "config": true, "dockerconfigjson": true, "file": true, "files": true,
	"archive": true, "blob": true, "raw": true, "text": true, "script": true, "command": true, "cmd": true,
	"args": true, "labels": true, "annotations": true,
}

// metadataSuffixes mark a key as naming metadata about a value, not the
// value itself (tokenId, secretName, credentialCount, passwordChangedAt).
var metadataSuffixes = []string{"id", "ids", "name", "names", "count", "type", "types", "kind", "at", "prefix", "scope", "scopes"}

func normalizeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// SensitiveKey reports whether values under key k are redacted.
func SensitiveKey(k string) bool {
	n := normalizeKey(k)
	for _, s := range metadataSuffixes {
		if strings.HasSuffix(n, s) && len(n) > len(s) {
			return false
		}
	}
	if sensitiveKeys[n] {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// LooksSecret reports whether s contains a credential-shaped value.
func LooksSecret(s string) bool { return secretValueRE.MatchString(s) }

// RedactDetails returns a redacted, bounded copy of details as a JSON
// compatible value tree (map[string]any, []any, string, float64/int64,
// bool, nil).
func RedactDetails(details map[string]any) map[string]any {
	out := make(map[string]any, len(details))
	for k, v := range details {
		out[cleanKey(k)] = redactValue(k, v, 0)
	}
	return out
}

func cleanKey(k string) string {
	k = stripControl(k)
	if LooksSecret(k) {
		return Redacted
	}
	if len(k) > 128 {
		k = truncateUTF8(k, 128)
	}
	return k
}

func redactValue(key string, v any, depth int) any {
	if SensitiveKey(key) {
		if v == nil {
			return nil
		}
		return Redacted
	}
	if depth >= maxDetailDepth {
		return "[OMITTED: nested too deeply]"
	}
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return x
	case json.Number:
		return x
	case string:
		return redactString(x)
	case logging.Secret, []byte, error:
		return Redacted
	case json.RawMessage:
		var tree any
		if err := json.Unmarshal(x, &tree); err != nil {
			return Redacted
		}
		return redactValue(key, tree, depth)
	case map[string]any:
		m := make(map[string]any, len(x))
		n := 0
		for _, k := range sortedKeys(x) {
			if n == maxDetailItems {
				m["_omitted"] = fmt.Sprintf("%d more members", len(x)-n)
				break
			}
			m[cleanKey(k)] = redactValue(k, x[k], depth+1)
			n++
		}
		return m
	case []any:
		return redactSlice(key, x, depth)
	case []string:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = e
		}
		return redactSlice(key, s, depth)
	}
	// Other types (structs, typed maps and slices, stringers): take their
	// JSON form, so unexported fields and json:"-" members never appear.
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Redacted
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return Redacted
	}
	return redactValue(key, tree, depth)
}

func redactSlice(key string, x []any, depth int) []any {
	n := min(len(x), maxDetailItems)
	s := make([]any, 0, n+1)
	for _, e := range x[:n] {
		// Elements inherit the slice's key: []any under "tokens" stays redacted.
		s = append(s, redactValue(key, e, depth+1))
	}
	if len(x) > n {
		s = append(s, fmt.Sprintf("[OMITTED: %d more items]", len(x)-n))
	}
	return s
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func redactString(s string) string {
	if !utf8.ValidString(s) {
		return Redacted
	}
	if len(s) > MaxDetailString {
		return fmt.Sprintf("[OMITTED: %d bytes]", len(s))
	}
	if LooksSecret(s) {
		return Redacted
	}
	return s
}

// CanonicalDetails redacts details and returns its canonical JSON (sorted
// keys, compact), bounded by MaxDetailsBytes.
func CanonicalDetails(details map[string]any) ([]byte, error) {
	if len(details) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(RedactDetails(details))
	if err != nil {
		return nil, fmt.Errorf("%w: details: %v", ErrInvalidEvent, err)
	}
	if len(b) > MaxDetailsBytes {
		b, _ = json.Marshal(map[string]any{"_omitted": fmt.Sprintf("details exceeded %d bytes", MaxDetailsBytes)})
	}
	return b, nil
}

func stripControl(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cleanID bounds an identifier-like field and redacts secret-shaped values.
func cleanID(s string) string {
	s = strings.TrimSpace(stripControl(s))
	if s == "" {
		return ""
	}
	if LooksSecret(s) {
		return Redacted
	}
	return truncateUTF8(s, maxIDLen)
}

func cleanUserAgent(s string) string {
	s = strings.TrimSpace(stripControl(s))
	if LooksSecret(s) {
		return Redacted
	}
	return truncateUTF8(s, maxUserAgentLen)
}

// cleanTargets bounds, cleans and de-duplicates targets (order kept).
func cleanTargets(in []domain.AuditTarget) []domain.AuditTarget {
	out := make([]domain.AuditTarget, 0, min(len(in), maxTargets))
	seen := map[domain.AuditTarget]bool{}
	for _, t := range in {
		c := domain.AuditTarget{Type: cleanTargetType(t.Type), ID: cleanID(t.ID), EnvironmentID: cleanID(t.EnvironmentID)}
		if c.Type == "" || c.ID == "" || seen[c] {
			continue
		}
		if len(out) == maxTargets {
			break
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

var targetTypeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func cleanTargetType(s string) string {
	if !targetTypeRE.MatchString(s) {
		return ""
	}
	return s
}
