package store

import (
	"encoding/json"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// linkJSON is the stored form of a stack's or template's link (a JSON
// list in their links column, independent of domain field names).
type linkJSON struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// linksJSON encodes links ("[]" for none).
func linksJSON(links []domain.Link) string {
	out := make([]linkJSON, 0, len(links))
	for _, l := range links {
		out = append(out, linkJSON(l))
	}
	return mustJSON(out)
}

// linksOf decodes a links column (an empty list when unreadable).
func linksOf(raw string) []domain.Link {
	var in []linkJSON
	_ = json.Unmarshal([]byte(raw), &in)
	out := make([]domain.Link, 0, len(in))
	for _, l := range in {
		out = append(out, domain.Link(l))
	}
	return out
}
