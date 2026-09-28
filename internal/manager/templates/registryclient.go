package templates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// The client of other instances' public registries (template registry).
// A registry is a Docker Manager's origin (its DOCKER_MANAGER_PUBLIC_URL);
// the client reads <origin>/api/v1/template-registry and the icon and
// archive paths the index names. Safety: HTTPS only (plain HTTP only for
// loopback addresses, like the manager's own public URL), no redirects to
// another origin or from HTTPS to HTTP, every path must stay below the
// registry routes of the same origin, and every body is size-limited.
// Private network addresses are allowed: registries are added by the
// instance owner, and homelab managers usually talk over a LAN.

// Registry format and limits.
const (
	RegistryFormat     = "docker-manager.template-registry/v1"
	registryIndexPath  = "/api/v1/template-registry"
	registryRoutes     = "/api/v1/template-registry/templates/"
	maxRegistryIndex   = 4 << 20
	maxRegistryEntries = 1000
	maxRegistryVersion = 50
	indexTimeout       = 15 * time.Second
	archiveTimeout     = 2 * time.Minute
)

// RegistryIndex is a registry's decoded index.
type RegistryIndex struct {
	Format     string          `json:"format"`
	InstanceID string          `json:"instanceId"`
	Name       string          `json:"name"`
	URL        string          `json:"url"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	Templates  []RegistryEntry `json:"templates"`
}

// RegistryEntry is a template of a registry.
type RegistryEntry struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	Icon        *RegistryIcon     `json:"icon"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	Versions    []RegistryVersion `json:"versions"`
}

// RegistryIcon names a template's icon.
type RegistryIcon struct {
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
}

// RegistryVersion is a published version of a registry template.
type RegistryVersion struct {
	Number      int             `json:"number"`
	Label       string          `json:"label"`
	Notes       string          `json:"notes"`
	PublishedAt time.Time       `json:"publishedAt"`
	Archive     RegistryArchive `json:"archive"`
}

// RegistryArchive describes a version's archive.
type RegistryArchive struct {
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentSize int64  `json:"contentSize"`
	Entries     int    `json:"entries"`
	URL         string `json:"url"`
}

// RegistryError is a failed registry request, classified for users.
type RegistryError struct {
	// Class is unreachable, insecure, invalid or not_found.
	Class   string
	Message string
}

func (e *RegistryError) Error() string { return e.Message }

// RegistryClass returns the error class (the API maps it to error codes).
func (e *RegistryError) RegistryClass() string { return e.Class }

// Registry error classes.
const (
	RegistryUnreachable = "unreachable"
	RegistryInsecure    = "insecure"
	RegistryInvalid     = "invalid"
	RegistryNotFound    = "not_found"
)

func regErr(class, format string, args ...any) error {
	return &RegistryError{Class: class, Message: fmt.Sprintf(format, args...)}
}

// NormalizeRegistryURL returns a registry's origin from what a user pasted:
// the manager's address, its /registry page or the index URL. HTTPS only;
// plain HTTP only for loopback addresses.
func NormalizeRegistryURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", regErr(RegistryInvalid, "enter the other Docker Manager's address, for example https://docker.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", regErr(RegistryInsecure, "registries are reached over HTTPS (plain HTTP only on this machine)")
		}
	default:
		return "", regErr(RegistryInvalid, "the address must start with https://")
	}
	p := strings.TrimSuffix(u.Path, "/")
	switch p {
	case "", "/registry", registryIndexPath:
	default:
		return "", regErr(RegistryInvalid, "use the Docker Manager's address without a path (or its /registry page)")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// RegistryClient reads registries.
type RegistryClient struct {
	HTTP *http.Client
}

// NewRegistryClient returns a client (hc nil: a default HTTP client with
// the redirect rules above).
func NewRegistryClient(hc *http.Client) *RegistryClient {
	if hc == nil {
		hc = &http.Client{}
	}
	c := *hc
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		first := via[0].URL
		if req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
			return errors.New("the registry redirected to another address")
		}
		return nil
	}
	return &RegistryClient{HTTP: &c}
}

// resolve turns a path named by the index into a URL of the same origin
// below the registry routes.
func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", regErr(RegistryInvalid, "invalid registry address")
	}
	r, err := url.Parse(ref)
	if err != nil || r.IsAbs() || r.Host != "" || !strings.HasPrefix(r.Path, registryRoutes) || strings.Contains(r.Path, "..") {
		return "", regErr(RegistryInvalid, "the registry names a file outside its registry")
	}
	return b.ResolveReference(r).String(), nil
}

// get fetches url with a body limit; etag sends If-None-Match.
func (c *RegistryClient) get(ctx context.Context, u, etag string, limit int64, timeout time.Duration) ([]byte, string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, regErr(RegistryInvalid, "invalid registry address")
	}
	req.Header.Set("Accept", "application/json, */*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", false, regErr(RegistryUnreachable, "the registry could not be reached")
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, true, nil
	case res.StatusCode == http.StatusNotFound:
		return nil, "", false, regErr(RegistryNotFound, "the registry does not share this (404)")
	case res.StatusCode != http.StatusOK:
		return nil, "", false, regErr(RegistryUnreachable, "the registry answered HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "", false, regErr(RegistryUnreachable, "the registry's answer was cut off")
	}
	if int64(len(b)) > limit {
		return nil, "", false, regErr(RegistryInvalid, "the registry's answer is larger than allowed")
	}
	return b, res.Header.Get("ETag"), false, nil
}

// FetchIndex reads a registry's index (notModified: etag still current).
func (c *RegistryClient) FetchIndex(ctx context.Context, base, etag string) (RegistryIndex, string, bool, error) {
	var idx RegistryIndex
	b, newTag, notModified, err := c.get(ctx, base+registryIndexPath, etag, maxRegistryIndex, indexTimeout)
	if err != nil {
		var re *RegistryError
		if errors.As(err, &re) && re.Class == RegistryNotFound {
			return idx, "", false, regErr(RegistryNotFound, "no template registry at this address: it is not a Docker Manager, or it does not share templates")
		}
		return idx, "", false, err
	}
	if notModified {
		return idx, newTag, true, nil
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return idx, "", false, regErr(RegistryInvalid, "the address does not serve a template registry")
	}
	if err := idx.validate(); err != nil {
		return idx, "", false, err
	}
	return idx, newTag, false, nil
}

// validate checks an index's shape and bounds (it comes from another
// instance: never trust it).
func (idx *RegistryIndex) validate() error {
	switch {
	case idx.Format != RegistryFormat:
		return regErr(RegistryInvalid, "the registry uses an unknown format (%s); update Docker Manager", bounded(idx.Format, 64))
	case idx.InstanceID == "" || len(idx.InstanceID) > 64 || !printable(idx.InstanceID):
		return regErr(RegistryInvalid, "the registry does not identify its instance")
	case len(idx.Templates) > maxRegistryEntries:
		return regErr(RegistryInvalid, "the registry lists more than %d templates", maxRegistryEntries)
	}
	idx.Name = bounded(strings.TrimSpace(idx.Name), domain.MaxTemplateName)
	if idx.Name == "" {
		idx.Name = "Docker Manager"
	}
	seen := map[string]bool{}
	out := idx.Templates[:0]
	for _, e := range idx.Templates {
		if e.ID == "" || len(e.ID) > 64 || !printable(e.ID) || seen[e.ID] || len(e.Versions) == 0 {
			continue
		}
		seen[e.ID] = true
		e.Name = bounded(strings.TrimSpace(e.Name), domain.MaxTemplateName)
		if e.Name == "" {
			continue
		}
		e.Description = bounded(e.Description, domain.MaxTemplateDescription)
		tags, err := domain.NormalizeTemplateTags(e.Tags)
		if err != nil {
			tags = nil
		}
		e.Tags = tags
		if e.Icon != nil && (!hexSHA(e.Icon.SHA256) || e.Icon.Size <= 0 || e.Icon.Size > domain.MaxTemplateIcon) {
			e.Icon = nil
		}
		vs := e.Versions[:0]
		for _, v := range e.Versions {
			if v.Number < 1 || !domain.ValidTemplateLabel(v.Label) || !hexSHA(v.Archive.SHA256) || v.Archive.Size <= 0 {
				continue
			}
			v.Notes = bounded(v.Notes, domain.MaxTemplateVersionNote)
			vs = append(vs, v)
			if len(vs) == maxRegistryVersion {
				break
			}
		}
		if len(vs) == 0 {
			continue
		}
		e.Versions = vs
		out = append(out, e)
	}
	idx.Templates = out
	return nil
}

// FetchIcon reads and checks a template's icon.
func (c *RegistryClient) FetchIcon(ctx context.Context, base string, icon RegistryIcon) (domain.TemplateIcon, []byte, error) {
	u, err := resolve(base, icon.URL)
	if err != nil {
		return domain.TemplateIcon{}, nil, err
	}
	b, _, _, err := c.get(ctx, u, "", domain.MaxTemplateIcon, indexTimeout)
	if err != nil {
		return domain.TemplateIcon{}, nil, err
	}
	got, err := DetectIcon(b)
	if err != nil {
		return domain.TemplateIcon{}, nil, regErr(RegistryInvalid, "the registry's icon is not a supported image")
	}
	return got, b, nil
}

// FetchArchive downloads a version's archive and checks its size and
// digest against the index (limit: the largest accepted size).
func (c *RegistryClient) FetchArchive(ctx context.Context, base string, a RegistryArchive, limit int64) ([]byte, error) {
	if a.Size > limit {
		return nil, regErr(RegistryInvalid, "the template is larger than this instance accepts (DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB)")
	}
	u, err := resolve(base, a.URL)
	if err != nil {
		return nil, err
	}
	b, _, _, err := c.get(ctx, u, "", a.Size, archiveTimeout)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
		return nil, regErr(RegistryInvalid, "the downloaded template does not match the registry's digest; refresh the registry and try again")
	}
	return b, nil
}

func bounded(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func printable(s string) bool {
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func hexSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
