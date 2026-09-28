package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestLinkURLProblem(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", MaxLinkURL-len("https://example.com/"))
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"https://docs.example.com/guide?page=2#install", ""},
		{"http://192.168.1.10:8080/", ""},
		{"HTTPS://Example.com", ""},
		{"https://[::1]:8443/x", ""},
		{long, ""},
		{"", linkURLRequired},
		{long + "b", "at most 2048 characters"},
		{"javascript:alert(1)", linkURLScheme},
		{"JavaScript://example.com/%0Aalert(1)", linkURLScheme},
		{"data:text/html,<b>x</b>", linkURLScheme},
		{"file:///etc/passwd", linkURLScheme},
		{"ftp://example.com/", linkURLScheme},
		{"//example.com/path", linkURLScheme},
		{"example.com", linkURLScheme},
		{"https:example.com", linkURLScheme},
		{"https:///path", linkURLScheme},
		{"https://exa mple.com", linkURLScheme},
		{"https://example.com/\npath", linkURLScheme},
		{"https://user:secret@example.com/", linkURLCredentials},
		{"https://token@example.com/", linkURLCredentials},
	} {
		if got := LinkURLProblem(tc.url); got != tc.want {
			t.Errorf("LinkURLProblem(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestLinkLabelProblem(t *testing.T) {
	if got := LinkLabelProblem(""); got != "" {
		t.Errorf("empty label: %q", got)
	}
	if got := LinkLabelProblem(strings.Repeat("é", MaxLinkLabel)); got != "" {
		t.Errorf("60 characters: %q", got)
	}
	if got := LinkLabelProblem(strings.Repeat("é", MaxLinkLabel+1)); got != "at most 60 characters" {
		t.Errorf("61 characters: %q", got)
	}
	if got := LinkLabelProblem("Docs\nmore"); got != linkControl {
		t.Errorf("line break: %q", got)
	}
}

func inputField(t *testing.T, err error) string {
	t.Helper()
	var in *InputError
	if !errors.As(err, &in) {
		t.Fatalf("error %v is not an InputError", err)
	}
	return in.Field
}

func TestNormalizeLinks(t *testing.T) {
	got, err := NormalizeLinks([]Link{{Label: "  Docs ", URL: " https://docs.example.com "}, {URL: "https://github.com/acme/app"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Link{Label: "Docs", URL: "https://docs.example.com"}) || got[1].Label != "" {
		t.Fatalf("normalized %+v", got)
	}
	if got, err := NormalizeLinks(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil list: %v %v", got, err)
	}

	many := make([]Link, MaxLinks+1)
	for i := range many {
		many[i] = Link{URL: "https://example.com/" + strings.Repeat("x", i+1)}
	}
	if _, err := NormalizeLinks(many); inputField(t, err) != "links" {
		t.Errorf("11 links: %v", err)
	}
	if _, err := NormalizeLinks(many[:MaxLinks]); err != nil {
		t.Errorf("10 links: %v", err)
	}

	for name, tc := range map[string]struct {
		links []Link
		field string
	}{
		"scheme":      {[]Link{{URL: "https://ok.example"}, {URL: "javascript:alert(1)"}}, "links[1].url"},
		"credentials": {[]Link{{URL: "https://me:pw@example.com"}}, "links[0].url"},
		"missing url": {[]Link{{Label: "Docs"}}, "links[0].url"},
		"duplicate":   {[]Link{{URL: "https://example.com"}, {Label: "again", URL: " https://example.com "}}, "links[1].url"},
		"long label":  {[]Link{{Label: strings.Repeat("a", MaxLinkLabel+1), URL: "https://example.com"}}, "links[0].label"},
		"long url":    {[]Link{{URL: "https://example.com/" + strings.Repeat("a", MaxLinkURL)}}, "links[0].url"},
	} {
		if _, err := NormalizeLinks(tc.links); err == nil || inputField(t, err) != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}
}

func TestSanitizeLinksDropsInvalidOnes(t *testing.T) {
	in := []Link{
		{Label: "Docs", URL: "https://docs.example.com"},
		{Label: "Bad", URL: "javascript:alert(1)"},
		{URL: "https://user:pw@example.com"},
		{Label: "Again", URL: "https://docs.example.com"},
		{Label: strings.Repeat("a", MaxLinkLabel+1), URL: "https://long.example.com"},
		{Label: " Repo ", URL: " https://git.example.com/app "},
	}
	got := SanitizeLinks(in)
	if len(got) != 2 || got[0].URL != "https://docs.example.com" || got[1] != (Link{Label: "Repo", URL: "https://git.example.com/app"}) {
		t.Fatalf("sanitized %+v", got)
	}
	var many []Link
	for i := range MaxLinks + 5 {
		many = append(many, Link{URL: "https://example.com/" + strings.Repeat("x", i+1)})
	}
	if got := SanitizeLinks(many); len(got) != MaxLinks {
		t.Errorf("kept %d links, want %d", len(got), MaxLinks)
	}
	if got := SanitizeLinks(nil); got == nil || len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
}
