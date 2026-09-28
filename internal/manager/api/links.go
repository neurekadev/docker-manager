package api

import "code.neureka.dev/docker-manager/docker-manager/internal/domain"

// WebLink is a web link of a stack or template (documentation, website,
// repository). The services check links with domain.NormalizeLinks (422
// on body.links[i].url or body.links[i].label). Never log or audit a URL:
// audit the number of links.
type WebLink struct {
	Label string `json:"label,omitempty" maxLength:"60" example:"Documentation" doc:"Optional; without one the URL's host is shown."`
	URL   string `json:"url" maxLength:"2048" example:"https://docs.example.com" doc:"An absolute http:// or https:// address without a user name or password, listed once."`
}

// webLinks converts links for a response (nil for none, so an empty list
// is left out).
func webLinks(in []domain.Link) []WebLink {
	if len(in) == 0 {
		return nil
	}
	out := make([]WebLink, 0, len(in))
	for _, l := range in {
		out = append(out, WebLink(l))
	}
	return out
}

// domainLinks converts submitted links (nil stays nil).
func domainLinks(in []WebLink) []domain.Link {
	if in == nil {
		return nil
	}
	out := make([]domain.Link, 0, len(in))
	for _, l := range in {
		out = append(out, domain.Link(l))
	}
	return out
}

// domainLinksPatch converts an optional replacement list (nil: unchanged).
func domainLinksPatch(in *[]WebLink) *[]domain.Link {
	if in == nil {
		return nil
	}
	out := domainLinks(*in)
	if out == nil {
		out = []domain.Link{}
	}
	return &out
}
