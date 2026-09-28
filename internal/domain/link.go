package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Links of stacks and templates: web pages such as the documentation,
// the website or the repository, shown on their pages. Links are not
// secrets, but a URL may carry a query string: never log or audit a URL
// (audit the number of links only).

// Link bounds.
const (
	MaxLinks     = 10
	MaxLinkURL   = 2048
	MaxLinkLabel = 60
)

// Link is one web link. Label is optional (the web shows the URL's host
// without one).
type Link struct {
	Label string
	URL   string
}

// Link problems (the messages of the InputError NormalizeLinks returns; the
// web shows the same rules inline).
const (
	linkURLRequired    = "enter the link's address"
	linkURLScheme      = "must be a web address starting with http:// or https://"
	linkURLCredentials = "must not contain a user name or password"
	linkURLDuplicate   = "is listed twice"
	linkControl        = "must not contain line breaks or control characters"
)

// NormalizeLinks trims the labels and URLs of submitted links and checks
// them: at most MaxLinks, each URL an absolute http(s) address without
// credentials of at most MaxLinkURL characters and listed once, each label
// at most MaxLinkLabel characters. The error is an *InputError naming the
// field ("links", "links[1].url", "links[0].label"). A nil list becomes an
// empty one.
func NormalizeLinks(in []Link) ([]Link, error) {
	if len(in) > MaxLinks {
		return nil, &InputError{Field: "links", Message: fmt.Sprintf("at most %d links", MaxLinks)}
	}
	out := make([]Link, 0, len(in))
	seen := map[string]bool{}
	for i, l := range in {
		l = Link{Label: strings.TrimSpace(l.Label), URL: strings.TrimSpace(l.URL)}
		field := fmt.Sprintf("links[%d]", i)
		if msg := LinkURLProblem(l.URL); msg != "" {
			return nil, &InputError{Field: field + ".url", Message: msg}
		}
		if seen[l.URL] {
			return nil, &InputError{Field: field + ".url", Message: linkURLDuplicate}
		}
		seen[l.URL] = true
		if msg := LinkLabelProblem(l.Label); msg != "" {
			return nil, &InputError{Field: field + ".label", Message: msg}
		}
		out = append(out, l)
	}
	return out, nil
}

// SanitizeLinks keeps the valid links of untrusted data (another
// instance's template registry): invalid links and repeated URLs are
// dropped, not refused, and at most MaxLinks are kept.
func SanitizeLinks(in []Link) []Link {
	out := []Link{}
	seen := map[string]bool{}
	for _, l := range in {
		l = Link{Label: strings.TrimSpace(l.Label), URL: strings.TrimSpace(l.URL)}
		if LinkURLProblem(l.URL) != "" || LinkLabelProblem(l.Label) != "" || seen[l.URL] {
			continue
		}
		seen[l.URL] = true
		out = append(out, l)
		if len(out) == MaxLinks {
			break
		}
	}
	return out
}

// LinkURLProblem says what is wrong with a (trimmed) link URL ("" when it
// is valid).
func LinkURLProblem(raw string) string {
	switch {
	case raw == "":
		return linkURLRequired
	case utf8.RuneCountInString(raw) > MaxLinkURL:
		return fmt.Sprintf("at most %d characters", MaxLinkURL)
	case !utf8.ValidString(raw) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return linkURLScheme
	}
	// The parser lowers the scheme; "https:example.com" parses as opaque and
	// "https:/path" has no host: both are refused.
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Hostname() == "" {
		return linkURLScheme
	}
	if u.User != nil {
		return linkURLCredentials
	}
	return ""
}

// LinkLabelProblem says what is wrong with a (trimmed) link label ("" when
// it is valid; empty labels are).
func LinkLabelProblem(label string) string {
	switch {
	case !utf8.ValidString(label) || strings.IndexFunc(label, unicode.IsControl) >= 0:
		return linkControl
	case utf8.RuneCountInString(label) > MaxLinkLabel:
		return fmt.Sprintf("at most %d characters", MaxLinkLabel)
	}
	return ""
}
