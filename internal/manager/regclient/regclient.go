// Package regclient is the manager's minimal OCI Distribution client for
// digest checks and registry connection tests (#19, #20). It only reads
// manifests; image pulls run on the agents through the Engine (#21).
//
//   - HEAD (then GET when the registry sends no digest) of
//     /v2/<repository>/manifests/<tag|digest> with the OCI and Docker
//     manifest media types; for an index / manifest list and a requested
//     platform the index is fetched and the platform's manifest digest
//     selected.
//   - Authentication follows the registry's WWW-Authenticate challenge:
//     Bearer (token service, scope repository:<repo>:pull, the credential
//     as basic auth to the realm) or Basic. Credentials are only sent over
//     HTTPS unless the connection explicitly allows plain HTTP, and never
//     to a plain-HTTP token realm.
//   - Failures carry a stable class: unauthorized (401), forbidden (403),
//     not_found (404 / unknown manifest), rate_limited (429),
//     registry_unavailable (5xx, network), platform_not_found,
//     invalid_response. There is no anonymous retry after a credential was
//     refused.
//   - Rate limits and outages: 429/5xx/network errors are retried a few
//     times with exponential backoff and full jitter, honoring Retry-After
//     up to a bound; a longer Retry-After (or the retry budget running out)
//     fails fast and puts the host and credential in a cooldown during
//     which their checks fail with rate_limited without contacting it (no
//     retry storm). Anonymous and authenticated limits differ, so an
//     anonymous 429 never holds back a connection's checks, nor the
//     reverse.
//   - Pull limits: every manifest and blob response is reported to the
//     rate-limit observer (SetRateLimitObserver) with the limit the
//     registry sent (RateLimit-Limit / RateLimit-Remaining with ";w="
//     windows, their X-RateLimit-* forms, RateLimit-Reset) and whether it
//     was a 429.
//   - Results are cached for a short TTL and concurrent identical checks
//     (registry, repository, tag, platform, credential) share one request.
//     The platform manifest an index digest selects is remembered (it never
//     changes), so a check of an unchanged multi-platform tag is one HEAD.
//   - Created reads a platform manifest's image config for its creation
//     time (display only; one manifest and one blob GET per digest, cached).
//
// Registry-specific notes: Docker Hub serves the API at
// registry-1.docker.io and counts GET manifest requests (not HEAD) against
// the pull allowance, so HEAD is always tried first; authentication raises
// the allowance per account tier but never removes the limits. GHCR and
// Quay behave like the reference distribution registry.
package regclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/imageref"
	"github.com/neurekadev/docker-manager/internal/logging"
)

// Error classes.
const (
	ClassUnauthorized     = "unauthorized"
	ClassForbidden        = "forbidden"
	ClassNotFound         = "not_found"
	ClassRateLimited      = "rate_limited"
	ClassUnavailable      = "registry_unavailable"
	ClassPlatformNotFound = "platform_not_found"
	ClassInvalidResponse  = "invalid_response"
)

// Manifest media types.
const (
	MediaOCIIndex      = "application/vnd.oci.image.index.v1+json"
	MediaOCIManifest   = "application/vnd.oci.image.manifest.v1+json"
	MediaDockerList    = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaDockerV2      = "application/vnd.docker.distribution.manifest.v2+json"
	acceptManifests    = MediaOCIIndex + ", " + MediaDockerList + ", " + MediaOCIManifest + ", " + MediaDockerV2
	maxManifestBytes   = 4 << 20
	maxConfigBytes     = 4 << 20
	maxCreatedEntries  = 4096
	maxPlatformEntries = 4096
	acceptImage        = MediaOCIManifest + ", " + MediaDockerV2
	maxErrorBodyBytes  = 16 << 10
	maxTokenBodyBytes  = 64 << 10
	maxMessageLen      = 200
	defaultTokenExpiry = 60 * time.Second
)

// Defaults.
const (
	DefaultMaxAttempts   = 3
	DefaultBaseBackoff   = time.Second
	DefaultMaxBackoff    = 30 * time.Second
	DefaultMaxRetryAfter = time.Minute
	DefaultCacheTTL      = time.Minute
	DefaultTimeout       = 30 * time.Second
)

// Error is a classified registry failure. Message never contains
// credentials or tokens.
type Error struct {
	Class string
	// Status is the HTTP status, if any.
	Status int
	// RetryAfter is the registry's retry guidance (rate limits).
	RetryAfter time.Duration
	Message    string
}

func (e *Error) Error() string { return "registry " + e.Class + ": " + e.Message }

// ClassOf returns the class of err ("" for non-registry errors).
func ClassOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// Credential authenticates to a registry.
type Credential struct {
	Username string
	Secret   logging.Secret
}

// Request is one digest check.
type Request struct {
	Ref imageref.Ref
	// Platform ("os/arch[/variant]") selects a manifest from an index;
	// empty keeps the index digest.
	Platform string
	// Credential is nil for anonymous access.
	Credential *Credential
	// CredentialKey identifies the credential for caching (connection ID
	// and secret version); it must change when the credential changes.
	CredentialKey string
	// PlainHTTP uses http:// (self-hosted registries only).
	PlainHTTP bool
	// Fresh bypasses the result cache and request sharing (connection
	// tests).
	Fresh bool
}

// Result is the digest of a reference.
type Result struct {
	// Digest is the digest of the manifest the tag names (an index for
	// multi-platform images).
	Digest    string
	MediaType string
	// PlatformDigest is the requested platform's manifest digest (equal to
	// Digest for single-platform manifests).
	PlatformDigest string
	CheckedAt      time.Time
	// Cached reports that the result came from the cache or a shared
	// in-flight request.
	Cached bool
}

// Options configures a Client.
type Options struct {
	HTTP          *http.Client
	Clock         clock.Clock
	Logger        *slog.Logger
	MaxAttempts   int
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
	MaxRetryAfter time.Duration
	CacheTTL      time.Duration
	// Jitter returns a value in [0,1) (tests make it deterministic).
	Jitter func() float64
}

// Client performs digest checks. Safe for concurrent use.
type Client struct {
	opts Options

	mu       sync.Mutex
	cache    map[string]cached
	inflight map[string]*call
	tokens   map[string]cachedToken
	cooldown map[string]time.Time
	// created caches image creation times per manifest digest (immutable;
	// the zero time: the image records none).
	created map[string]time.Time
	// platforms maps an index (API host, repository, index digest,
	// platform) to the platform manifest digest it selects (immutable).
	platforms map[string]string
	// observer receives the rate-limit report of every registry response.
	observer func(context.Context, RateLimit)

	// onJoin, when set (tests), runs when a check joins an in-flight one.
	onJoin func()
}

type cached struct {
	res     Result
	expires time.Time
}

type cachedToken struct {
	token   string
	expires time.Time
}

type call struct {
	done chan struct{}
	res  Result
	err  error
}

// New returns a client.
func New(opts Options) *Client {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	// Never follow a redirect from HTTPS to plain HTTP: Go keeps the
	// Authorization header for same-host redirects.
	hc := *opts.HTTP
	next := hc.CheckRedirect
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing a redirect from HTTPS to plain HTTP")
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	opts.HTTP = &hc
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = DefaultBaseBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = DefaultMaxBackoff
	}
	if opts.MaxRetryAfter <= 0 {
		opts.MaxRetryAfter = DefaultMaxRetryAfter
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64 //nolint:gosec // backoff jitter, not security
	}
	return &Client{opts: opts, cache: map[string]cached{}, inflight: map[string]*call{}, tokens: map[string]cachedToken{},
		cooldown: map[string]time.Time{}, created: map[string]time.Time{}, platforms: map[string]string{}}
}

// SetRateLimitObserver sets the function every manifest and blob response
// is reported to (nil: none). It runs synchronously in the check, so it
// must be quick and must not call the client.
func (c *Client) SetRateLimitObserver(fn func(context.Context, RateLimit)) {
	c.mu.Lock()
	c.observer = fn
	c.mu.Unlock()
}

func (r Request) key() string {
	return strings.Join([]string{r.Ref.Host, r.Ref.Repository, r.Ref.Object(), r.Platform, r.CredentialKey,
		strconv.FormatBool(r.PlainHTTP)}, "\x00")
}

// Resolve returns the digest of req.Ref (and of req.Platform's manifest).
func (c *Client) Resolve(ctx context.Context, req Request) (Result, error) {
	if req.Ref.Host == "" || req.Ref.Repository == "" || req.Ref.Object() == "" {
		return Result{}, &Error{Class: ClassInvalidResponse, Message: "incomplete image reference"}
	}
	if req.Fresh {
		return c.resolve(ctx, req)
	}
	key := req.key()
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok && c.opts.Clock.Now().Before(hit.expires) {
		c.mu.Unlock()
		res := hit.res
		res.Cached = true
		return res, nil
	}
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		if c.onJoin != nil {
			c.onJoin()
		}
		select {
		case <-cl.done:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		res := cl.res
		res.Cached = true
		return res, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	cl.res, cl.err = c.resolve(ctx, req)
	c.mu.Lock()
	delete(c.inflight, key)
	if cl.err == nil {
		c.cache[key] = cached{res: cl.res, expires: c.opts.Clock.Now().Add(c.opts.CacheTTL)}
	}
	c.pruneLocked()
	c.mu.Unlock()
	close(cl.done)
	return cl.res, cl.err
}

// Forget drops cached results and tokens of a credential (after rotation
// or revocation; the key changes anyway, this frees memory early).
func (c *Client) Forget(credentialKeyPrefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if parts := strings.Split(k, "\x00"); len(parts) > 4 && strings.HasPrefix(parts[4], credentialKeyPrefix) {
			delete(c.cache, k)
		}
	}
	for k := range c.tokens {
		if parts := strings.Split(k, "\x00"); len(parts) > 2 && strings.HasPrefix(parts[2], credentialKeyPrefix) {
			delete(c.tokens, k)
		}
	}
	for k := range c.created {
		if parts := strings.Split(k, "\x00"); len(parts) > 3 && strings.HasPrefix(parts[3], credentialKeyPrefix) {
			delete(c.created, k)
		}
	}
}

// ErrNoCreated reports an image without a usable creation time (no image
// config, no or an unparsable "created", or the Unix epoch of reproducible
// builds). It depends only on the digest's content and is cached too.
var ErrNoCreated = errors.New("the image records no creation time")

// Created returns when the image of a platform manifest was created (the
// "created" field of its image config), for display only: it never
// decides an update. It costs one manifest GET (by digest; Docker Hub
// counts it against the pull allowance) and one blob GET, without retries;
// results are cached per digest (a digest's content never changes). A host
// in a rate-limit cooldown is not contacted, and a 429 starts one.
// req.Ref names the repository; its tag and digest are ignored.
func (c *Client) Created(ctx context.Context, req Request, digest string) (created time.Time, cachedHit bool, err error) {
	if req.Ref.Host == "" || req.Ref.Repository == "" || !validDigest(digest) {
		return time.Time{}, false, &Error{Class: ClassInvalidResponse, Message: "incomplete image reference"}
	}
	key := strings.Join([]string{req.Ref.Host, req.Ref.Repository, digest, req.CredentialKey, strconv.FormatBool(req.PlainHTTP)}, "\x00")
	c.mu.Lock()
	if t, ok := c.created[key]; ok {
		c.mu.Unlock()
		if t.IsZero() {
			return t, true, ErrNoCreated
		}
		return t, true, nil
	}
	c.mu.Unlock()
	cool := cooldownKey(req)
	if until, ok := c.cooling(cool); ok {
		return time.Time{}, false, &Error{Class: ClassRateLimited, RetryAfter: until, Message: "the registry asked Docker Manager to slow down"}
	}
	created, err = c.fetchCreated(ctx, req, digest)
	var re *Error
	if errors.As(err, &re) && re.Class == ClassRateLimited {
		d := re.RetryAfter
		if d <= 0 {
			d = c.opts.MaxBackoff
		}
		c.setCooldown(cool, d)
	}
	if err != nil && !errors.Is(err, ErrNoCreated) {
		return time.Time{}, false, err
	}
	c.mu.Lock()
	if len(c.created) >= maxCreatedEntries {
		clear(c.created)
	}
	c.created[key] = created
	c.mu.Unlock()
	return created, false, err
}

func (c *Client) fetchCreated(ctx context.Context, req Request, digest string) (time.Time, error) {
	base := c.baseURL(req) + "/v2/" + req.Ref.Repository
	body, err := c.getVerified(ctx, req, base+"/manifests/"+digest, acceptImage, digest, maxManifestBytes)
	if err != nil {
		return time.Time{}, err
	}
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &m) != nil || !validDigest(m.Config.Digest) {
		return time.Time{}, ErrNoCreated
	}
	body, err = c.getVerified(ctx, req, base+"/blobs/"+m.Config.Digest, "*/*", m.Config.Digest, maxConfigBytes)
	if err != nil {
		return time.Time{}, err
	}
	var cfg struct {
		Created string `json:"created"`
	}
	if json.Unmarshal(body, &cfg) != nil || cfg.Created == "" {
		return time.Time{}, ErrNoCreated
	}
	t, err := time.Parse(time.RFC3339Nano, cfg.Created)
	// Reproducible builds record the Unix epoch (SOURCE_DATE_EPOCH=0).
	if err != nil || t.Unix() <= 0 {
		return time.Time{}, ErrNoCreated
	}
	return t.UTC(), nil
}

// getVerified GETs a manifest or blob and checks its sha256 digest.
func (c *Client) getVerified(ctx context.Context, req Request, u, accept, digest string, limit int64) ([]byte, error) {
	resp, err := c.authorized(ctx, req, http.MethodGet, u, accept)
	if err != nil {
		return nil, err
	}
	body, err := readLimited(resp.Body, limit)
	_ = resp.Body.Close()
	if err != nil {
		return nil, &Error{Class: ClassUnavailable, Message: "could not read the registry's response"}
	}
	sum := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return nil, &Error{Class: ClassInvalidResponse, Message: "the content does not match its digest"}
	}
	return body, nil
}

func validDigest(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

func (c *Client) pruneLocked() {
	now := c.opts.Clock.Now()
	for k, v := range c.cache {
		if !now.Before(v.expires) {
			delete(c.cache, k)
		}
	}
	for k, v := range c.tokens {
		if !now.Before(v.expires) {
			delete(c.tokens, k)
		}
	}
	for k, v := range c.cooldown {
		if !now.Before(v) {
			delete(c.cooldown, k)
		}
	}
}

// resolve runs attempts with backoff.
func (c *Client) resolve(ctx context.Context, req Request) (Result, error) {
	apiHost := imageref.APIHost(req.Ref.Host)
	coolKey := cooldownKey(req)
	for attempt := 1; ; attempt++ {
		if until, ok := c.cooling(coolKey); ok {
			return Result{}, &Error{Class: ClassRateLimited, RetryAfter: until,
				Message: "the registry asked Docker Manager to slow down; checks resume after " + until.Round(time.Second).String()}
		}
		res, err := c.attempt(ctx, req)
		if err == nil {
			res.CheckedAt = c.opts.Clock.Now().UTC()
			return res, nil
		}
		var re *Error
		if !errors.As(err, &re) || (re.Class != ClassRateLimited && re.Class != ClassUnavailable) || ctx.Err() != nil {
			return Result{}, err
		}
		wait := c.backoff(attempt)
		if re.RetryAfter > 0 {
			wait = re.RetryAfter
		}
		if attempt >= c.opts.MaxAttempts || wait > c.opts.MaxRetryAfter {
			if re.Class == ClassRateLimited {
				cool := re.RetryAfter
				if cool <= 0 {
					cool = c.opts.MaxBackoff
				}
				c.setCooldown(coolKey, cool)
			}
			return Result{}, err
		}
		c.opts.Logger.Debug("registry check retrying", "registry", apiHost, "class", re.Class, "attempt", attempt, "wait", wait)
		select {
		case <-c.opts.Clock.After(wait):
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.opts.BaseBackoff << (attempt - 1)
	if d > c.opts.MaxBackoff || d <= 0 {
		d = c.opts.MaxBackoff
	}
	// Full jitter, at least a tenth of the step.
	return d/10 + time.Duration(c.opts.Jitter()*float64(d-d/10))
}

// cooldownKey is the rate-limit cooldown's key: the registry's API host and
// the credential, because anonymous limits (per IP address) and an
// account's limits are counted apart.
func cooldownKey(req Request) string {
	return imageref.APIHost(req.Ref.Host) + "\x00" + req.CredentialKey
}

func (c *Client) cooling(key string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.cooldown[key]
	if !ok {
		return 0, false
	}
	left := until.Sub(c.opts.Clock.Now())
	if left <= 0 {
		delete(c.cooldown, key)
		return 0, false
	}
	return left, true
}

func (c *Client) setCooldown(key string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	until := c.opts.Clock.Now().Add(d)
	if cur, ok := c.cooldown[key]; !ok || until.After(cur) {
		c.cooldown[key] = until
	}
}

// attempt performs one check (with at most one authentication round).
func (c *Client) attempt(ctx context.Context, req Request) (Result, error) {
	base := c.baseURL(req)
	manifestURL := base + "/v2/" + req.Ref.Repository + "/manifests/" + req.Ref.Object()
	resp, err := c.authorized(ctx, req, http.MethodHead, manifestURL, acceptManifests)
	if err != nil {
		return Result{}, err
	}
	_ = resp.Body.Close()
	res := Result{Digest: resp.Header.Get("Docker-Content-Digest"), MediaType: mediaType(resp.Header.Get("Content-Type"))}
	// An index is content-addressed: the platform manifest it selects never
	// changes, so a known index needs no GET (Docker Hub counts every
	// manifest GET as a pull, HEAD is free).
	if req.Platform != "" && isIndex(res.MediaType) && validDigest(res.Digest) &&
		(req.Ref.Digest == "" || req.Ref.Digest == res.Digest) {
		if d, ok := c.knownPlatform(req, res.Digest); ok {
			res.PlatformDigest = d
			return res, nil
		}
	}
	var body []byte
	if res.Digest == "" || res.MediaType == "" || (req.Platform != "" && isIndex(res.MediaType)) {
		resp, err := c.authorized(ctx, req, http.MethodGet, manifestURL, acceptManifests)
		if err != nil {
			return Result{}, err
		}
		body, err = readLimited(resp.Body, maxManifestBytes)
		_ = resp.Body.Close()
		if err != nil {
			return Result{}, &Error{Class: ClassUnavailable, Message: "could not read the manifest"}
		}
		sum := sha256.Sum256(body)
		computed := "sha256:" + hex.EncodeToString(sum[:])
		if d := resp.Header.Get("Docker-Content-Digest"); d != "" && d != computed && strings.HasPrefix(d, "sha256:") {
			return Result{}, &Error{Class: ClassInvalidResponse, Message: "the manifest does not match the digest the registry reported"}
		}
		if req.Ref.Digest != "" && req.Ref.Digest != computed && strings.HasPrefix(req.Ref.Digest, "sha256:") {
			return Result{}, &Error{Class: ClassInvalidResponse, Message: "the manifest does not match the requested digest"}
		}
		res.Digest = computed
		if mt := mediaType(resp.Header.Get("Content-Type")); mt != "" {
			res.MediaType = mt
		}
		if res.MediaType == "" {
			res.MediaType = sniffMediaType(body)
		}
	}
	res.PlatformDigest = res.Digest
	if req.Platform != "" && isIndex(res.MediaType) {
		d, err := selectPlatform(body, req.Platform)
		if err != nil {
			return Result{}, err
		}
		res.PlatformDigest = d
		c.rememberPlatform(req, res.Digest, d)
	}
	return res, nil
}

// platformKey keys the platform manifest an index selects: API host,
// repository, index digest (verified content) and platform.
func platformKey(req Request, index string) string {
	return strings.Join([]string{imageref.APIHost(req.Ref.Host), req.Ref.Repository, index, req.Platform}, "\x00")
}

func (c *Client) knownPlatform(req Request, index string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.platforms[platformKey(req, index)]
	return d, ok
}

func (c *Client) rememberPlatform(req Request, index, digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.platforms) >= maxPlatformEntries {
		clear(c.platforms)
	}
	c.platforms[platformKey(req, index)] = digest
}

func (c *Client) baseURL(req Request) string {
	scheme := "https"
	if req.PlainHTTP {
		scheme = "http"
	}
	return scheme + "://" + imageref.APIHost(req.Ref.Host)
}

// authorized sends a request, answering one authentication challenge.
func (c *Client) authorized(ctx context.Context, req Request, method, u, accept string) (*http.Response, error) {
	scope := "repository:" + req.Ref.Repository + ":pull"
	tokenKey := strings.Join([]string{imageref.APIHost(req.Ref.Host), scope, req.CredentialKey}, "\x00")
	authHeader := ""
	c.mu.Lock()
	if t, ok := c.tokens[tokenKey]; ok && c.opts.Clock.Now().Before(t.expires) {
		authHeader = "Bearer " + t.token
	}
	c.mu.Unlock()
	resp, err := c.send(ctx, method, u, accept, authHeader)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		c.observe(ctx, req, resp)
		return c.check(resp)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	drain(resp)
	if authHeader != "" {
		// A cached token expired early: drop it and answer the challenge.
		c.mu.Lock()
		delete(c.tokens, tokenKey)
		c.mu.Unlock()
	}
	scheme, params := parseChallenge(challenge)
	switch scheme {
	case "bearer":
		token, ttl, err := c.fetchToken(ctx, req, params, scope)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.tokens[tokenKey] = cachedToken{token: token, expires: c.opts.Clock.Now().Add(ttl)}
		c.mu.Unlock()
		authHeader = "Bearer " + token
	case "basic":
		if req.Credential == nil {
			return nil, &Error{Class: ClassUnauthorized, Status: http.StatusUnauthorized,
				Message: "the registry requires authentication and no registry connection matches this image"}
		}
		if !req.PlainHTTP && !strings.HasPrefix(u, "https://") {
			return nil, &Error{Class: ClassInvalidResponse, Message: "refusing to send credentials over plain HTTP"}
		}
		authHeader = "Basic " + basicAuth(req.Credential)
	default:
		return nil, &Error{Class: ClassUnauthorized, Status: http.StatusUnauthorized, Message: "the registry refused the request (401)"}
	}
	resp, err = c.send(ctx, method, u, accept, authHeader)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		drain(resp)
		msg := "the registry rejected the credential (401): check the username and token, and whether the token expired or was revoked"
		if req.Credential == nil {
			msg = "the registry requires authentication (401) and no registry connection matches this image"
		}
		return nil, &Error{Class: ClassUnauthorized, Status: http.StatusUnauthorized, Message: msg}
	}
	c.observe(ctx, req, resp)
	return c.check(resp)
}

func (c *Client) send(ctx context.Context, method, u, accept, auth string) (*http.Response, error) {
	hr, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, &Error{Class: ClassInvalidResponse, Message: "invalid registry URL"}
	}
	hr.Header.Set("Accept", accept)
	hr.Header.Set("User-Agent", "Docker Manager")
	if auth != "" {
		hr.Header.Set("Authorization", auth)
	}
	resp, err := c.opts.HTTP.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Class: ClassUnavailable, Message: "the registry is unreachable: " + netMessage(err)}
	}
	return resp, nil
}

// check returns a 2xx response, or closes it and classifies the failure.
func (c *Client) check(resp *http.Response) (*http.Response, error) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	return nil, c.statusError(resp)
}

// statusError classifies a non-2xx response and closes its body.
func (c *Client) statusError(resp *http.Response) *Error {
	defer drain(resp)
	e := &Error{Status: resp.StatusCode}
	detail := registryMessage(resp)
	switch {
	case resp.StatusCode == http.StatusForbidden:
		e.Class, e.Message = ClassForbidden, "the registry denied access to this repository (403)"
	case resp.StatusCode == http.StatusNotFound:
		e.Class, e.Message = ClassNotFound, "the image or tag does not exist (404), or the credential cannot see it"
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Class, e.Message = ClassRateLimited, "the registry's rate limit was reached (429); authentication raises some limits but never removes them"
		e.RetryAfter = retryAfter(resp.Header, c.opts.Clock.Now())
	case resp.StatusCode >= 500:
		e.Class, e.Message = ClassUnavailable, fmt.Sprintf("the registry is unavailable (%d)", resp.StatusCode)
		e.RetryAfter = retryAfter(resp.Header, c.opts.Clock.Now())
	default:
		e.Class, e.Message = ClassInvalidResponse, fmt.Sprintf("unexpected registry response (%d)", resp.StatusCode)
	}
	if detail != "" {
		e.Message += ": " + detail
	}
	return e
}

// fetchToken gets a bearer token from the challenge's realm.
func (c *Client) fetchToken(ctx context.Context, req Request, params map[string]string, scope string) (string, time.Duration, error) {
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || (realm.Scheme != "https" && realm.Scheme != "http") {
		return "", 0, &Error{Class: ClassInvalidResponse, Message: "the registry sent an invalid token realm"}
	}
	if realm.Scheme == "http" && !req.PlainHTTP {
		return "", 0, &Error{Class: ClassInvalidResponse, Message: "the registry's token service is plain HTTP; refusing to use it"}
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", 0, &Error{Class: ClassInvalidResponse, Message: "invalid token realm"}
	}
	hr.Header.Set("User-Agent", "Docker Manager")
	if req.Credential != nil {
		hr.Header.Set("Authorization", "Basic "+basicAuth(req.Credential))
	}
	resp, err := c.opts.HTTP.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, &Error{Class: ClassUnavailable, Message: "the registry's token service is unreachable: " + netMessage(err)}
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		msg := "the registry's token service rejected the credential (401): check the username and token, and whether it expired or was revoked"
		if req.Credential == nil {
			msg = "the registry requires authentication (401) and no registry connection matches this image"
		}
		return "", 0, &Error{Class: ClassUnauthorized, Status: resp.StatusCode, Message: msg}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		if resp.StatusCode == http.StatusTooManyRequests {
			c.observe(ctx, req, resp)
		}
		return "", 0, c.statusError(resp)
	}
	b, err := readLimited(resp.Body, maxTokenBodyBytes)
	if err != nil {
		return "", 0, &Error{Class: ClassUnavailable, Message: "could not read the token response"}
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(b, &tr) != nil {
		return "", 0, &Error{Class: ClassInvalidResponse, Message: "the token service sent an invalid response"}
	}
	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}
	if token == "" {
		return "", 0, &Error{Class: ClassInvalidResponse, Message: "the token service sent no token"}
	}
	ttl := defaultTokenExpiry
	if tr.ExpiresIn > 0 {
		ttl = time.Duration(tr.ExpiresIn) * time.Second
	}
	// Refresh a little early; never cache longer than five minutes.
	ttl = min(ttl*9/10, 5*time.Minute)
	return token, ttl, nil
}

func basicAuth(c *Credential) string {
	return base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + string(c.Secret)))
}

// parseChallenge parses a WWW-Authenticate header (one challenge).
func parseChallenge(h string) (string, map[string]string) {
	h = strings.TrimSpace(h)
	scheme, rest, _ := strings.Cut(h, " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var val string
		if strings.HasPrefix(after, `"`) {
			end := 1
			for end < len(after) && after[end] != '"' {
				if after[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(after) {
				val, rest = strings.ReplaceAll(after[1:], `\"`, `"`), ""
			} else {
				val, rest = strings.ReplaceAll(after[1:end], `\"`, `"`), after[end+1:]
			}
		} else {
			val, rest, _ = strings.Cut(after, ",")
			val = strings.TrimSpace(val)
		}
		params[key] = val
	}
	return strings.ToLower(scheme), params
}

// retryAfter parses Retry-After (seconds or HTTP date) and the common
// provider reset headers.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s >= 0 {
			return time.Duration(s) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := t.Sub(now); d > 0 {
				return d
			}
		}
	}
	return resetAfter(h, now)
}

// resetAfter parses RateLimit-Reset / X-RateLimit-Reset: seconds until the
// window resets, or an absolute Unix time (0: none or already past).
func resetAfter(h http.Header, now time.Time) time.Duration {
	for _, k := range []string{"RateLimit-Reset", "X-RateLimit-Reset"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			if s, err := strconv.ParseInt(v, 10, 64); err == nil && s > 0 {
				// Seconds until reset, or an absolute Unix time.
				if s > 1_000_000_000 {
					if d := time.Unix(s, 0).Sub(now); d > 0 {
						return d
					}
					continue
				}
				return time.Duration(s) * time.Second
			}
		}
	}
	return 0
}

// RateLimit is what one registry response said about the pull limit of the
// credential it was sent with. Every manifest and blob response is
// reported, also one without limit headers (Limit 0): it still tells when
// the registry last answered.
type RateLimit struct {
	// Host is the reference's registry host (docker.io for Docker Hub).
	Host string
	// CredentialKey is the request's ("" for anonymous access).
	CredentialKey string
	// Limit is the number of pulls per Window (0: the registry sent none);
	// Remaining is what is left of it (-1: not sent).
	Limit     int64
	Remaining int64
	// Window is the limit's window (";w=<seconds>"; 0: not sent).
	Window time.Duration
	// ResetAt is when the window resets (zero: not sent).
	ResetAt time.Time
	// Limited reports a 429 answer; RetryAfter is its guidance (0: none).
	Limited    bool
	RetryAfter time.Duration
	At         time.Time
}

// Reported tells whether the registry sent a limit.
func (r RateLimit) Reported() bool { return r.Limit > 0 }

// observe reports a registry response to the rate-limit observer.
func (c *Client) observe(ctx context.Context, req Request, resp *http.Response) {
	c.mu.Lock()
	fn := c.observer
	c.mu.Unlock()
	if fn == nil {
		return
	}
	fn(ctx, rateLimitOf(req, resp.StatusCode, resp.Header, c.opts.Clock.Now().UTC()))
}

// rateLimitOf reads the rate-limit headers of a response: RateLimit-Limit
// and RateLimit-Remaining ("100" or "100;w=21600"), their X-RateLimit-*
// forms, RateLimit-Reset, and Retry-After on 429. Values that do not parse
// are ignored.
func rateLimitOf(req Request, status int, h http.Header, now time.Time) RateLimit {
	rl := RateLimit{Host: req.Ref.Host, CredentialKey: req.CredentialKey, Remaining: -1, At: now}
	if n, w, ok := limitHeader(h, "RateLimit-Limit", "X-RateLimit-Limit"); ok && n > 0 {
		rl.Limit, rl.Window = n, w
		if n, w, ok := limitHeader(h, "RateLimit-Remaining", "X-RateLimit-Remaining"); ok {
			rl.Remaining = min(n, rl.Limit)
			if rl.Window == 0 {
				rl.Window = w
			}
		}
	}
	if d := resetAfter(h, now); d > 0 {
		rl.ResetAt = now.Add(d)
	}
	if status == http.StatusTooManyRequests {
		rl.Limited, rl.RetryAfter = true, retryAfter(h, now)
		if rl.Reported() {
			rl.Remaining = 0
		}
	}
	return rl
}

// maxLimitWindow bounds a reported window (a year).
const maxLimitWindow = 366 * 24 * time.Hour

// limitHeader parses the first of keys that is set: a count, optionally
// with a window (";w=<seconds>"). A list ("100, 5000;w=3600") counts its
// first entry.
func limitHeader(h http.Header, keys ...string) (int64, time.Duration, bool) {
	for _, k := range keys {
		v := strings.TrimSpace(h.Get(k))
		if v == "" {
			continue
		}
		v, _, _ = strings.Cut(v, ",")
		count, params, _ := strings.Cut(v, ";")
		n, err := strconv.ParseInt(strings.TrimSpace(count), 10, 64)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		var window time.Duration
		for p := range strings.SplitSeq(params, ";") {
			key, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "w") {
				continue
			}
			if s, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil && s > 0 &&
				time.Duration(s)*time.Second <= maxLimitWindow {
				window = time.Duration(s) * time.Second
			}
		}
		return n, window, true
	}
	return 0, 0, false
}

func registryMessage(resp *http.Response) string {
	b, err := readLimited(resp.Body, maxErrorBodyBytes)
	if err != nil || len(b) == 0 {
		return ""
	}
	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &body) != nil || len(body.Errors) == 0 {
		return ""
	}
	e := body.Errors[0]
	msg := strings.TrimSpace(e.Code + " " + e.Message)
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > maxMessageLen {
		msg = msg[:maxMessageLen]
	}
	return msg
}

func netMessage(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if len(msg) > maxMessageLen {
		msg = msg[:maxMessageLen]
	}
	return msg
}

func readLimited(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, errors.New("response too large")
	}
	return b, nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	_ = resp.Body.Close()
}

func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")
	return strings.TrimSpace(strings.ToLower(mt))
}

func isIndex(mt string) bool { return mt == MediaOCIIndex || mt == MediaDockerList }

func sniffMediaType(body []byte) string {
	var m struct {
		MediaType string          `json:"mediaType"`
		Manifests json.RawMessage `json:"manifests"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if m.MediaType != "" {
		return m.MediaType
	}
	if len(m.Manifests) > 0 {
		return MediaOCIIndex
	}
	return MediaOCIManifest
}

// Platform is os/arch[/variant].
type Platform struct {
	OS, Arch, Variant string
}

// ParsePlatform parses "linux/amd64", "linux/arm64/v8", "linux/arm/v7".
func ParsePlatform(s string) (Platform, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return Platform{}, fmt.Errorf("platform %q must be os/arch[/variant]", s)
	}
	p := Platform{OS: parts[0], Arch: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p.normalize(), nil
}

func (p Platform) normalize() Platform {
	switch p.Arch {
	case "x86_64", "x86-64":
		p.Arch = "amd64"
	case "aarch64":
		p.Arch = "arm64"
	}
	if p.Arch == "arm64" && p.Variant == "v8" {
		p.Variant = ""
	}
	if p.Arch == "amd64" && p.Variant == "v1" {
		p.Variant = ""
	}
	return p
}

func (p Platform) String() string {
	if p.Variant != "" {
		return p.OS + "/" + p.Arch + "/" + p.Variant
	}
	return p.OS + "/" + p.Arch
}

// selectPlatform picks the platform's manifest from an index.
func selectPlatform(index []byte, platform string) (string, error) {
	want, err := ParsePlatform(platform)
	if err != nil {
		return "", &Error{Class: ClassPlatformNotFound, Message: err.Error()}
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(index, &idx); err != nil {
		return "", &Error{Class: ClassInvalidResponse, Message: "the image index is not valid JSON"}
	}
	// Exact variant first, then the same os/arch without a variant (an
	// arm/v7 host runs an index entry that declares no variant).
	fallback := ""
	for _, m := range idx.Manifests {
		if m.Platform == nil || m.Digest == "" {
			continue
		}
		got := Platform{OS: strings.ToLower(m.Platform.OS), Arch: strings.ToLower(m.Platform.Architecture),
			Variant: strings.ToLower(m.Platform.Variant)}.normalize()
		if got.OS != want.OS || got.Arch != want.Arch {
			continue
		}
		if got.Variant == want.Variant {
			return m.Digest, nil
		}
		if got.Variant == "" && fallback == "" {
			fallback = m.Digest
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", &Error{Class: ClassPlatformNotFound, Message: "the image has no manifest for " + want.String()}
}
