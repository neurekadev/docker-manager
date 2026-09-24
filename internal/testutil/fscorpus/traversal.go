// Package fscorpus generates filesystem security corpora for the file
// manager, archive extraction and restore paths (#15, #24, #29):
//
//   - TraversalCases: hostile path strings (dot-dot, percent-encoded,
//     double-encoded, overlong UTF-8, Unicode look-alikes, absolute, UNC,
//     backslash, NUL, mixed).
//   - Archives: zip-slip and tar-slip archives (relative, absolute,
//     backslash, symlink and hardlink escapes, device nodes) and
//     decompression bombs (ratio, lying size, nested, many entries).
//   - BuildEscapeTree: a directory tree with symlink and hardlink escapes.
//   - Swapper / RaceWhile: a TOCTOU helper that races swapping a directory
//     for a symlink to outside the root.
//
// The static parts are committed under test/corpora/fs (regenerate with
// `go test ./internal/testutil/fscorpus -run TestCorpusFiles -update`) so
// non-Go consumers can use them; see test/corpora/README.md.
package fscorpus

// PathCase is one hostile path string.
type PathCase struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Path     string `json:"path"`
}

// Traversal categories.
const (
	CatDotDot        = "dotdot"
	CatEncoded       = "encoded"
	CatDoubleEncoded = "double-encoded"
	CatOverlongUTF8  = "overlong-utf8"
	CatUnicode       = "unicode"
	CatAbsolute      = "absolute"
	CatBackslash     = "backslash"
	CatNUL           = "nul"
	CatMixed         = "mixed"
)

// TraversalCases returns the traversal corpus. A contained file API must,
// for every case, either reject the path or resolve it inside the root; it
// must never touch anything outside. Percent-encoded forms matter wherever
// a path passes through URL decoding (API path parameters, query strings,
// archive names produced by other tools).
func TraversalCases() []PathCase {
	return []PathCase{
		{"parent", CatDotDot, ".."},
		{"parent-slash", CatDotDot, "../"},
		{"parent-file", CatDotDot, "../secret.txt"},
		{"deep-parent", CatDotDot, "../../../../../../../../etc/passwd"},
		{"inner-parent", CatDotDot, "a/b/../../../secret.txt"},
		{"dot-parent", CatDotDot, "./../secret.txt"},
		{"trailing-parent", CatDotDot, "dir/.."},
		{"parent-after-dot-dirs", CatDotDot, "./././../secret.txt"},

		{"pct-dotdot", CatEncoded, "%2e%2e/secret.txt"},
		{"pct-dotdot-upper", CatEncoded, "%2E%2E%2Fsecret.txt"},
		{"pct-slash-only", CatEncoded, "..%2fsecret.txt"},
		{"pct-backslash", CatEncoded, "..%5csecret.txt"},
		{"pct-mixed-dots", CatEncoded, ".%2e/secret.txt"},
		{"pct-nul", CatEncoded, "safe.txt%00../../secret.txt"},

		{"double-dotdot", CatDoubleEncoded, "%252e%252e%252fsecret.txt"},
		{"double-slash", CatDoubleEncoded, "..%252fsecret.txt"},

		{"overlong-slash-c0af", CatOverlongUTF8, "..%c0%afsecret.txt"},
		{"overlong-backslash-c19c", CatOverlongUTF8, "..%c1%9csecret.txt"},
		{"overlong-dot-c0ae", CatOverlongUTF8, "%c0%ae%c0%ae/secret.txt"},
		{"overlong-raw-bytes", CatOverlongUTF8, "..\xc0\xafsecret.txt"},

		{"fullwidth-dots", CatUnicode, "\uff0e\uff0e/secret.txt"},
		{"fullwidth-solidus", CatUnicode, "..\uff0fsecret.txt"},
		{"two-dot-leader", CatUnicode, "\u2025/secret.txt"},
		{"division-slash", CatUnicode, "..\u2215secret.txt"},
		{"fraction-slash", CatUnicode, "..\u2044secret.txt"},
		{"one-dot-leader-pair", CatUnicode, "\u2024\u2024/secret.txt"},
		{"rtl-override", CatUnicode, "\u202etxt.terces/.."},
		{"combining-dot", CatUnicode, ".\u0307./secret.txt"},

		{"abs-unix", CatAbsolute, "/etc/passwd"},
		{"abs-double-slash", CatAbsolute, "//etc/passwd"},
		{"abs-root", CatAbsolute, "/"},
		{"abs-docker-sock", CatAbsolute, "/var/run/docker.sock"},
		{"abs-windows-drive", CatAbsolute, "C:\\Windows\\win.ini"},
		{"abs-windows-drive-slash", CatAbsolute, "C:/Windows/win.ini"},
		{"abs-unc", CatAbsolute, "\\\\server\\share\\secret.txt"},
		{"abs-device-namespace", CatAbsolute, "\\\\?\\C:\\secret.txt"},
		{"home-tilde", CatAbsolute, "~/.ssh/id_ed25519"},
		{"file-url", CatAbsolute, "file:///etc/passwd"},

		{"backslash-parent", CatBackslash, "..\\secret.txt"},
		{"backslash-deep", CatBackslash, "..\\..\\..\\etc\\passwd"},
		{"backslash-inner", CatBackslash, "a\\..\\..\\secret.txt"},
		{"backslash-mixed", CatBackslash, "..\\/..\\/secret.txt"},

		{"nul-truncation", CatNUL, "safe.txt\x00../../secret.txt"},
		{"nul-in-dotdot", CatNUL, ".\x00./secret.txt"},
		{"nul-extension", CatNUL, "secret.txt\x00.png"},

		{"four-dots", CatMixed, "....//secret.txt"},
		{"three-dots", CatMixed, ".../secret.txt"},
		{"semicolon", CatMixed, "..;/secret.txt"},
		{"trailing-space", CatMixed, ".. /secret.txt"},
		{"trailing-dot-windows", CatMixed, "..\\.\\secret.txt"},
		{"repeated-slashes", CatMixed, "a//..//..//secret.txt"},
		{"dot-segment-after-name", CatMixed, "dir/./../../secret.txt"},
		{"newline", CatMixed, "a\n/../../secret.txt"},
		{"very-long", CatMixed, longTraversal()},
	}
}

func longTraversal() string {
	b := make([]byte, 0, 4096)
	for len(b) < 4000 {
		b = append(b, "../"...)
	}
	return string(append(b, "secret.txt"...))
}
