// Package gitremote lists the refs of an HTTP(S) Git repository in pure Go
// (the equivalent of `git ls-remote`, #33): no git CLI, no go-git. It is
// used by the manager for Git credential connection tests and by the agent
// to resolve a build's ref to the exact commit before BuildKit fetches it.
//
// Both transports are supported: the smart protocol (GET
// <repo>/info/refs?service=git-upload-pack, pkt-line ref advertisement,
// protocol v0/v1; HEAD's target from the symref capability) and the dumb
// protocol (static info/refs plus <repo>/HEAD).
package gitremote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
)

// Error classes.
const (
	ClassInvalidURL      = "invalid_git_url"
	ClassUnauthorized    = "unauthorized"
	ClassForbidden       = "forbidden"
	ClassNotFound        = "not_found"
	ClassRateLimited     = "rate_limited"
	ClassUnavailable     = "git_unavailable"
	ClassInvalidResponse = "invalid_response"
	ClassRefNotFound     = "ref_not_found"
)

// Limits.
const (
	maxAdvertisement = 32 << 20
	maxHead          = 4 << 10
	// DefaultTimeout bounds one listing.
	DefaultTimeout = 60 * time.Second
)

// Error is a classified failure; Message never contains credentials.
type Error struct {
	Class   string
	Status  int
	Message string
}

func (e *Error) Error() string { return "git " + e.Class + ": " + e.Message }

// ClassOf returns the class of err ("" for other errors).
func ClassOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// Repo is a normalized HTTP(S) repository URL: lower-case host, no user
// info, query or fragment, no trailing slash.
type Repo struct{ u url.URL }

// ParseURL validates and normalizes a repository URL. Only http and https
// are supported (SSH Git access is not in v1) and embedded credentials are
// refused: private repositories use Git credentials.
func ParseURL(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2048 {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL is too long"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "not a valid URL"}
	}
	switch u.Scheme {
	case "https", "http":
	default:
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "only http(s) Git URLs are supported (SSH Git access is not in v1)"}
	}
	if u.User != nil {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL must not contain credentials; use a Git credential"}
	}
	if u.Host == "" || u.Hostname() == "" {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL has no host"}
	}
	if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL must not contain a query or fragment (give the ref and context path separately)"}
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	if p == "" {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL has no repository path"}
	}
	path, err := url.PathUnescape(p)
	if err != nil || strings.ContainsAny(path, " \\#?:@") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") ||
		strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return Repo{}, &Error{Class: ClassInvalidURL, Message: "the Git URL path is invalid"}
	}
	n := url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host), Path: path, RawPath: p}
	return Repo{u: n}, nil
}

// String is the normalized URL.
func (r Repo) String() string { return r.u.String() }

// Host is host[:port] (lower case).
func (r Repo) Host() string { return r.u.Host }

// Path is the repository path ("/org/repo.git").
func (r Repo) Path() string { return r.u.Path }

// Secure reports whether the URL is https.
func (r Repo) Secure() bool { return r.u.Scheme == "https" }

// IsZero reports whether r was not parsed.
func (r Repo) IsZero() bool { return r.u.Host == "" }

// Credential authenticates to a Git host (HTTP basic: username + token).
type Credential struct {
	Username string
	Token    logging.Secret
}

// Refs is a repository's ref advertisement.
type Refs struct {
	// Head is the ref HEAD points to ("refs/heads/main"), if known.
	Head string
	// Commits maps full ref names (and "HEAD") to commit IDs; annotated
	// tags map to the commit they point to.
	Commits map[string]string
	// Smart reports whether the server spoke the smart protocol.
	Smart bool
}

// IsCommitID reports whether s is a full SHA-1 or SHA-256 object ID.
func IsCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// Resolve returns the commit a ref names and the full ref name: "" means
// HEAD; a full commit ID is returned as is (fullRef ""); "refs/..." must
// match exactly; other names are looked up like git does (refs/<name>,
// refs/tags/<name>, refs/heads/<name>).
func (r Refs) Resolve(ref string) (commit, fullRef string, err error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "" || ref == "HEAD":
		if r.Head != "" {
			if c, ok := r.Commits[r.Head]; ok {
				return c, r.Head, nil
			}
		}
		if c, ok := r.Commits["HEAD"]; ok {
			return c, "HEAD", nil
		}
		return "", "", &Error{Class: ClassRefNotFound, Message: "the repository has no default branch (HEAD); give a ref"}
	case IsCommitID(ref):
		return ref, "", nil
	case strings.HasPrefix(ref, "refs/"):
		if c, ok := r.Commits[ref]; ok {
			return c, ref, nil
		}
	default:
		for _, cand := range []string{"refs/" + ref, "refs/tags/" + ref, "refs/heads/" + ref} {
			if c, ok := r.Commits[cand]; ok {
				return c, cand, nil
			}
		}
	}
	return "", "", &Error{Class: ClassRefNotFound, Message: "the ref " + strconv.Quote(ref) + " does not exist in the repository"}
}

// Options configures LsRemote.
type Options struct {
	// HTTP is the client (default: a client with DefaultTimeout).
	HTTP *http.Client
	// Credential authenticates (nil: anonymous).
	Credential *Credential
	// AllowPlainHTTP permits sending the credential over plain HTTP
	// (self-hosted servers; the credential then travels unencrypted).
	AllowPlainHTTP bool
}

// LsRemote lists the repository's refs.
func LsRemote(ctx context.Context, repo Repo, o Options) (Refs, error) {
	if repo.IsZero() {
		return Refs{}, &Error{Class: ClassInvalidURL, Message: "no repository"}
	}
	if o.Credential != nil && !repo.Secure() && !o.AllowPlainHTTP {
		return Refs{}, &Error{Class: ClassInvalidURL, Message: "refusing to send a Git credential over plain HTTP; use https or allow plain HTTP for this credential"}
	}
	hc := client(o.HTTP)
	base := repo.String()
	resp, err := get(ctx, hc, base+"/info/refs?service=git-upload-pack", o)
	if err != nil {
		return Refs{}, err
	}
	defer drain(resp)
	body, err := readLimited(resp.Body, maxAdvertisement)
	if err != nil {
		return Refs{}, &Error{Class: ClassUnavailable, Message: "could not read the ref advertisement"}
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-git-upload-pack-advertisement") {
		refs, err := parseSmart(body)
		refs.Smart = true
		return refs, err
	}
	refs, err := parseDumb(body)
	if err != nil {
		return Refs{}, err
	}
	// The dumb protocol does not list HEAD; read it.
	hr, err := get(ctx, hc, base+"/HEAD", o)
	if err != nil {
		if ClassOf(err) == ClassNotFound {
			return refs, nil
		}
		return Refs{}, err
	}
	defer drain(hr)
	head, err := readLimited(hr.Body, maxHead)
	if err == nil {
		h := strings.TrimSpace(string(head))
		if target, ok := strings.CutPrefix(h, "ref: "); ok {
			refs.Head = strings.TrimSpace(target)
		} else if IsCommitID(h) {
			refs.Commits["HEAD"] = h
		}
	}
	return refs, nil
}

// client returns hc with redirects from HTTPS to plain HTTP refused (Go
// keeps the Authorization header on same-host redirects).
func client(hc *http.Client) *http.Client {
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	c := *hc
	next := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
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
	return &c
}

func get(ctx context.Context, hc *http.Client, u string, o Options) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Class: ClassInvalidURL, Message: "invalid Git URL"}
	}
	req.Header.Set("User-Agent", "git/2.45.0 (Docker Manager)")
	req.Header.Set("Accept", "*/*")
	if o.Credential != nil {
		req.SetBasicAuth(o.Credential.Username, string(o.Credential.Token))
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Class: ClassUnavailable, Message: "the Git server is unreachable: " + netMessage(err)}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	drain(resp)
	e := &Error{Status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		e.Class, e.Message = ClassUnauthorized, "the Git server requires authentication (401)"
		if o.Credential != nil {
			e.Message = "the Git server rejected the credential (401): check the username and token, and whether it expired or was revoked"
		}
	case resp.StatusCode == http.StatusForbidden:
		e.Class, e.Message = ClassForbidden, "the Git server denied access (403)"
	case resp.StatusCode == http.StatusNotFound:
		e.Class, e.Message = ClassNotFound, "the repository does not exist (404), or the credential cannot see it"
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Class, e.Message = ClassRateLimited, "the Git server's rate limit was reached (429)"
	case resp.StatusCode >= 500:
		e.Class, e.Message = ClassUnavailable, fmt.Sprintf("the Git server is unavailable (%d)", resp.StatusCode)
	default:
		e.Class, e.Message = ClassInvalidResponse, fmt.Sprintf("unexpected Git server response (%d)", resp.StatusCode)
	}
	return nil, e
}

// parseSmart parses a smart-protocol (v0/v1) ref advertisement.
func parseSmart(b []byte) (Refs, error) {
	refs := Refs{Commits: map[string]string{}}
	bad := func(msg string) (Refs, error) {
		return Refs{}, &Error{Class: ClassInvalidResponse, Message: "invalid ref advertisement: " + msg}
	}
	first := true
	sawService := false
	for len(b) > 0 {
		if len(b) < 4 {
			return bad("truncated packet")
		}
		n, err := strconv.ParseUint(string(b[:4]), 16, 16)
		if err != nil {
			return bad("bad packet length")
		}
		if n == 0 { // flush
			b = b[4:]
			continue
		}
		if n < 4 || int(n) > len(b) {
			return bad("bad packet length")
		}
		line := string(b[4:n])
		b = b[n:]
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "# service=") {
			sawService = true
			continue
		}
		if strings.HasPrefix(line, "ERR ") {
			return Refs{}, &Error{Class: ClassUnavailable, Message: "the Git server reported an error"}
		}
		if strings.HasPrefix(line, "version ") {
			return bad("protocol v2 is not requested and not supported here")
		}
		refPart, caps, _ := strings.Cut(line, "\x00")
		if first {
			first = false
			for _, c := range strings.Fields(caps) {
				if v, ok := strings.CutPrefix(c, "symref=HEAD:"); ok {
					refs.Head = v
				}
			}
		}
		sha, name, ok := strings.Cut(refPart, " ")
		if !ok || !IsCommitID(sha) {
			return bad("bad ref line")
		}
		if name == "capabilities^{}" {
			continue // empty repository
		}
		if base, peeled := strings.CutSuffix(name, "^{}"); peeled {
			refs.Commits[base] = sha
			continue
		}
		if _, seen := refs.Commits[name]; !seen {
			refs.Commits[name] = sha
		}
	}
	if !sawService {
		return bad("missing service line")
	}
	return refs, nil
}

// parseDumb parses a dumb-protocol info/refs file ("<sha>\t<ref>").
func parseDumb(b []byte) (Refs, error) {
	refs := Refs{Commits: map[string]string{}}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if !ok || !IsCommitID(sha) || name == "" {
			return Refs{}, &Error{Class: ClassInvalidResponse, Message: "the server did not answer like a Git repository"}
		}
		if base, peeled := strings.CutSuffix(name, "^{}"); peeled {
			refs.Commits[base] = sha
			continue
		}
		if _, seen := refs.Commits[name]; !seen {
			refs.Commits[name] = sha
		}
	}
	if err := sc.Err(); err != nil {
		return Refs{}, &Error{Class: ClassInvalidResponse, Message: "the ref list is malformed"}
	}
	return refs, nil
}

func netMessage(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
