package password

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Length limits in Unicode code points (after NFKC normalization).
const (
	// FloorMinLength is the shortest password ever accepted (NIST SP
	// 800-63B-4 minimum for a password used with a second factor).
	FloorMinLength = 8
	// DefaultStrictMinLength is the strict policy's default minimum (the
	// NIST minimum for a password that is the only factor).
	DefaultStrictMinLength = 15
	// MaxMinLength bounds the configurable minimum.
	MaxMinLength = 64
	// MaxLength bounds accepted passwords (long passphrases fit easily;
	// the bound keeps Argon2id input and request bodies small).
	MaxLength = 256
)

// Policy is the instance password policy (security settings, #16). There
// are deliberately no composition rules and no expiry (NIST SP 800-63B):
// length, a blocklist of common and breached passwords, context words and
// throttling do the work.
type Policy struct {
	// Strict enables the configurable MinLength; otherwise FloorMinLength applies.
	Strict bool
	// MinLength is the strict minimum (FloorMinLength..MaxMinLength).
	MinLength int
}

// EffectiveMinLength is the minimum length this policy enforces.
func (p Policy) EffectiveMinLength() int {
	if !p.Strict {
		return FloorMinLength
	}
	return min(max(p.MinLength, FloorMinLength), MaxMinLength)
}

// Violation codes (stable; shown to users as field details).
const (
	ViolationTooShort = "too_short"
	ViolationTooLong  = "too_long"
	ViolationCommon   = "common"
	ViolationContext  = "context"
	ViolationRepeated = "repetitive"
)

// Violation is one reason a password is rejected.
type Violation struct {
	Code    string
	Message string
}

// contextWords are product words users are tempted to build passwords from.
var contextWords = []string{"dockyard", "docker"}

// Check returns every reason pw is unacceptable, or nil. context are
// account-specific strings (username, email local part, display name) the
// password must not be built from.
func (p Policy) Check(pw string, context ...string) []Violation {
	n := Normalize(pw)
	var out []Violation
	length := utf8.RuneCountInString(n)
	minLen := p.EffectiveMinLength()
	if length < minLen {
		out = append(out, Violation{ViolationTooShort, "use at least " + strconv.Itoa(minLen) + " characters; a passphrase of several words is easy to remember"})
	}
	if length > MaxLength {
		out = append(out, Violation{ViolationTooLong, "use at most " + strconv.Itoa(MaxLength) + " characters"})
	}
	lower := strings.ToLower(n)
	if IsCommon(lower) {
		out = append(out, Violation{ViolationCommon, "this password appears in lists of common or breached passwords"})
	}
	if containsContext(lower, context) {
		out = append(out, Violation{ViolationContext, "do not build the password from your username, name, email or the product name"})
	}
	if repetitive(lower) {
		out = append(out, Violation{ViolationRepeated, "avoid repeated or sequential characters"})
	}
	return out
}

func containsContext(lower string, context []string) bool {
	words := append(append([]string{}, contextWords...), context...)
	for _, w := range words {
		w = strings.ToLower(Normalize(strings.TrimSpace(w)))
		if at := strings.IndexByte(w, '@'); at > 0 {
			w = w[:at]
		}
		if utf8.RuneCountInString(w) < 4 {
			continue // too short to be meaningful
		}
		// Only reject when the context word is most of the password, so a
		// long passphrase that happens to mention a word stays allowed.
		if strings.Contains(lower, w) && 2*utf8.RuneCountInString(w) >= utf8.RuneCountInString(lower) {
			return true
		}
	}
	return false
}

// repetitive reports passwords made of one repeated character or a single
// ascending/descending run (aaaaaaaa, 12345678, abcdefgh, 87654321).
func repetitive(lower string) bool {
	r := []rune(lower)
	if len(r) < 2 {
		return false
	}
	same, up, down := true, true, true
	for i := 1; i < len(r); i++ {
		d := r[i] - r[i-1]
		same = same && d == 0
		up = up && d == 1
		down = down && d == -1
	}
	return same || up || down
}

// commonPasswords is the offline blocklist: the 100,000 most common
// passwords of the xato-net 10-million-password breach corpus (SecLists,
// MIT licensed; see THIRD_PARTY.md), reduced to the entries of at least
// FloorMinLength characters (shorter ones fail the length check anyway),
// lower-cased and de-duplicated. Matching is case-insensitive.
//
//go:embed common-passwords.txt.gz
var commonPasswords []byte

var (
	blocklistOnce sync.Once
	blocklist     map[string]struct{}
)

func loadBlocklist() {
	blocklist = make(map[string]struct{}, 40000)
	zr, err := gzip.NewReader(bytes.NewReader(commonPasswords))
	if err != nil {
		panic("password: embedded blocklist is corrupt: " + err.Error())
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			blocklist[line] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		panic("password: embedded blocklist is corrupt: " + err.Error())
	}
}

// IsCommon reports whether pw (any case) is on the blocklist.
func IsCommon(pw string) bool {
	blocklistOnce.Do(loadBlocklist)
	_, ok := blocklist[strings.ToLower(Normalize(pw))]
	return ok
}

// BlocklistSize is the number of blocked passwords (diagnostics, tests).
func BlocklistSize() int {
	blocklistOnce.Do(loadBlocklist)
	return len(blocklist)
}
