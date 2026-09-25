// Package canary seeds known secret values ("canaries") into tests and
// asserts they never leak into logs, audit records, job output or HTTP
// responses (#29, #12). Import it from _test.go files only.
//
//	c := canary.New()
//	pw := c.New(canary.Password, "owner password")
//	logger := c.CaptureLogger(t)           // checked when the test ends
//	h := c.Handler(t, server.Handler)      // every response is checked
//	... exercise the code with pw ...
//	c.AssertClean(t, "audit rows", rows)   // any value: JSON-marshaled
//
// Detection covers the raw value and the encodings a leak typically takes:
// JSON and HTML escaping, URL query/path escaping, hex, and base64/base64url
// at every byte alignment (so a canary inside "user:password" basic-auth or
// a Docker config "auth" blob is found too).
package canary

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Kind classifies a canary.
type Kind string

// Canary kinds.
const (
	Password           Kind = "password"
	APIToken           Kind = "api-token"
	RegistryCredential Kind = "registry-credential" //nolint:gosec // G101: a kind name, not a credential
	S3AccessKey        Kind = "s3-access-key"
	S3SecretKey        Kind = "s3-secret-key" //nolint:gosec // G101: a kind name, not a credential
	EnvValue           Kind = "env-value"
	TOTPSeed           Kind = "totp-seed"
	RecoveryKey        Kind = "recovery-key"
)

// Kinds lists every kind.
var Kinds = []Kind{Password, APIToken, RegistryCredential, S3AccessKey, S3SecretKey, EnvValue, TOTPSeed, RecoveryKey}

// MinLength is the shortest value Register accepts: shorter values produce
// false positives and weak encoded fragments.
const MinLength = 12

// Canary is one registered secret.
type Canary struct {
	Kind  Kind
	Name  string
	Value string
}

// Leak is a canary found in scanned data.
type Leak struct {
	Canary Canary
	// Form is how it appeared: "raw", "json", "html", "query-escaped",
	// "path-escaped", "hex", "HEX", "base64", "base64url".
	Form string
	// Offset is the byte offset of the match in the scanned data.
	Offset int
}

func (l Leak) String() string {
	return fmt.Sprintf("%s canary %q leaked (%s) at byte %d", l.Canary.Kind, l.Canary.Name, l.Form, l.Offset)
}

// Set is a collection of canaries. Safe for concurrent use.
type Set struct {
	mu       sync.Mutex
	canaries []Canary
	patterns []pattern
}

type pattern struct {
	canary Canary
	form   string
	needle string
}

// New returns an empty set.
func New() *Set { return &Set{} }

// New generates a distinctive value of kind, registers it under name and
// returns it.
func (s *Set) New(kind Kind, name string) string {
	v := Generate(kind)
	s.Register(kind, name, v)
	return v
}

// Register adds an externally chosen secret (e.g. a token the code under
// test generated) to the set. It panics on values shorter than MinLength.
func (s *Set) Register(kind Kind, name, value string) {
	if len(value) < MinLength {
		panic(fmt.Sprintf("canary: value for %q is shorter than %d bytes", name, MinLength))
	}
	c := Canary{Kind: kind, Name: name, Value: value}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canaries = append(s.canaries, c)
	s.patterns = append(s.patterns, patternsFor(c)...)
}

// All returns the registered canaries.
func (s *Set) All() []Canary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Canary(nil), s.canaries...)
}

// Scan returns every leak in data (at most one per canary and form).
func (s *Set) Scan(data []byte) []Leak {
	s.mu.Lock()
	pats := append([]pattern(nil), s.patterns...)
	s.mu.Unlock()
	text := string(data)
	var leaks []Leak
	seen := map[string]bool{}
	for _, p := range pats {
		key := p.canary.Name + "\x00" + p.form
		if seen[key] {
			continue
		}
		if i := strings.Index(text, p.needle); i >= 0 {
			seen[key] = true
			leaks = append(leaks, Leak{Canary: p.canary, Form: p.form, Offset: i})
		}
	}
	sort.SliceStable(leaks, func(i, j int) bool { return leaks[i].Offset < leaks[j].Offset })
	return leaks
}

// ScanValue scans any value: strings and byte slices as-is, errors by
// message, fmt.Stringers by String(), everything else JSON-marshaled
// (falling back to %+v), e.g. audit rows or API DTOs.
func (s *Set) ScanValue(v any) []Leak { return s.Scan(render(v)) }

func render(v any) []byte {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return x
	case string:
		return []byte(x)
	case error:
		return []byte(x.Error())
	case fmt.Stringer:
		return []byte(x.String())
	}
	if b, err := json.Marshal(v); err == nil {
		// Also include %+v: unexported fields are invisible to JSON.
		return append(b, []byte("\n"+fmt.Sprintf("%+v", v))...)
	}
	return []byte(fmt.Sprintf("%+v", v))
}

// Generate returns a new random value shaped like a real secret of kind.
// Every value contains "canary" (in some case) so humans recognize it.
func Generate(kind Kind) string {
	switch kind {
	case Password:
		return "Canary-pw-" + randHex(8) + "!#%"
	case APIToken:
		return "dyt_canary_" + randHex(16)
	case RegistryCredential:
		return "canary-registry-" + randHex(12)
	case S3AccessKey:
		// 20 upper-case characters like AWS access key IDs.
		return "AKIACANARY" + randBase32(10)
	case S3SecretKey:
		// 40 characters from the base64 alphabet like AWS secret keys.
		return "canary/S3+" + strings.TrimRight(base64.StdEncoding.EncodeToString(randBytes(23)), "=")[:30]
	case EnvValue:
		return "canary-env-" + randHex(12)
	case TOTPSeed:
		// RFC 4648 base32, 32 characters (160 bits) like typical TOTP seeds.
		return "CANARY" + randBase32(26)
	case RecoveryKey:
		// Shaped like a DockYard Recovery Key (#10): grouped base32.
		return "DYRK-CANARY-" + randBase32(4) + "-" + randBase32(4) + "-" + randBase32(4) + "-" + randBase32(4)
	}
	return "canary-" + string(kind) + "-" + randHex(12)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return b
}

func randHex(n int) string { return hex.EncodeToString(randBytes(n)) }

func randBase32(n int) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randBytes(n))[:n]
}

// patternsFor derives the needles to search for.
func patternsFor(c Canary) []pattern {
	v := c.Value
	add := func(out []pattern, form, needle string) []pattern {
		if needle == "" {
			return out
		}
		for _, p := range out {
			if p.needle == needle {
				return out
			}
		}
		return append(out, pattern{canary: c, form: form, needle: needle})
	}
	var out []pattern
	out = add(out, "raw", v)
	if j, err := json.Marshal(v); err == nil {
		out = add(out, "json", strings.Trim(string(j), `"`))
	}
	out = add(out, "html", html.EscapeString(v))
	out = add(out, "query-escaped", url.QueryEscape(v))
	out = add(out, "path-escaped", url.PathEscape(v))
	out = add(out, "hex", hex.EncodeToString([]byte(v)))
	out = add(out, "HEX", strings.ToUpper(hex.EncodeToString([]byte(v))))
	for _, frag := range base64Fragments([]byte(v)) {
		out = add(out, "base64", frag)
		out = add(out, "base64url", strings.NewReplacer("+", "-", "/", "_").Replace(frag))
	}
	return out
}

// base64Fragments returns, for each of the three byte alignments a value
// can have inside a larger base64-encoded blob, the run of base64
// characters that encode only bytes of the value.
func base64Fragments(v []byte) []string {
	var out []string
	for k := range 3 {
		enc := base64.StdEncoding.EncodeToString(append(make([]byte, k), v...))
		start := (8*k + 5) / 6        // first char with no prefix bits
		end := (8 * (k + len(v))) / 6 // chars before this hold only value bits
		if end > len(enc) {
			end = len(enc)
		}
		if end-start >= 8 {
			out = append(out, enc[start:end])
		}
	}
	return out
}
