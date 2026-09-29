package managermove

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
)

// The waiting manager authenticates its requests to the old manager
// without ever sending the move code:
//
//	Authorization: DMM <move id>:<unix time>:<nonce>:<mac>
//
// mac is base64url(HMAC-SHA256(K, "DMM1\n" method "\n" path "\n" time
// "\n" nonce "\n" move id)) with K = HKDF-SHA256(secret, info
// AuthKeyInfo). The old manager accepts a time within AuthWindow of its
// clock and each nonce once (replay cache, in memory).

// Authentication parameters.
const (
	// AuthScheme is the Authorization scheme of move requests.
	AuthScheme = authsep.MoveAuthScheme
	// AuthKeyInfo is the HKDF info of the authentication key.
	AuthKeyInfo = "docker-manager-move/auth/v1"
	// AuthWindow is how far a request's time may be from the old
	// manager's clock.
	AuthWindow = 5 * time.Minute
	// maxNonceLen bounds a nonce.
	maxNonceLen = 64
	// maxReplayEntries bounds the replay cache (a waiting manager sends a
	// request every 10 s; the window keeps a few hundred).
	maxReplayEntries = 10000
)

// MoveAuth is a move request's authentication as the API hands it to the
// service: the Authorization header and the request's method and path.
type MoveAuth struct {
	Header string
	Method string
	Path   string
}

// codeSecretBytes returns the raw secret of a move code.
func codeSecretBytes(code string) (id string, secret []byte, ok bool) {
	id, s, ok := authsep.ParseMoveCode(code)
	if !ok {
		return "", nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", nil, false
	}
	return id, raw, true
}

func authKey(secret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, nil, AuthKeyInfo, sha256.Size)
}

func authMAC(key []byte, method, path, ts, nonce, moveID string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("DMM1\n" + method + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + moveID))
	return h.Sum(nil)
}

// SignRequest returns the Authorization header of a move request (method
// and URL path) signed with code at now. The code never leaves this
// function.
func SignRequest(code, method, path string, now time.Time) (string, error) {
	id, secret, ok := codeSecretBytes(code)
	if !ok {
		return "", domain.ErrMoveCodeInvalid
	}
	key, err := authKey(secret)
	if err != nil {
		return "", err
	}
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nb)
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := base64.RawURLEncoding.EncodeToString(authMAC(key, method, path, ts, nonce, id))
	return AuthScheme + " " + id + ":" + ts + ":" + nonce + ":" + mac, nil
}

// parsedAuth is a parsed Authorization header of a move request.
type parsedAuth struct {
	moveID, ts, nonce string
	unix              int64
	mac               []byte
}

// parseAuth parses "DMM id:time:nonce:mac" (domain.ErrMoveCodeInvalid
// when malformed).
func parseAuth(header string) (parsedAuth, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, AuthScheme) || len(rest) > 512 {
		return parsedAuth{}, domain.ErrMoveCodeInvalid
	}
	parts := strings.Split(strings.TrimSpace(rest), ":")
	if len(parts) != 4 || parts[0] == "" || parts[2] == "" || len(parts[2]) > maxNonceLen {
		return parsedAuth{}, domain.ErrMoveCodeInvalid
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return parsedAuth{}, domain.ErrMoveCodeInvalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(mac) != sha256.Size {
		return parsedAuth{}, domain.ErrMoveCodeInvalid
	}
	return parsedAuth{moveID: parts[0], ts: parts[1], nonce: parts[2], unix: unix, mac: mac}, nil
}

// verifyAuthMAC checks a parsed request against code's secret, the
// window around now and the replay cache. The MAC is checked first: a
// time or replay verdict is only given to a holder of the code.
func verifyAuthMAC(p parsedAuth, code string, a MoveAuth, now time.Time, replay *replayCache) error {
	id, secret, ok := codeSecretBytes(code)
	if !ok || id != p.moveID {
		return domain.ErrMoveCodeInvalid
	}
	key, err := authKey(secret)
	if err != nil {
		return err
	}
	if !hmac.Equal(authMAC(key, a.Method, a.Path, p.ts, p.nonce, p.moveID), p.mac) {
		return domain.ErrMoveCodeInvalid
	}
	t := time.Unix(p.unix, 0)
	if d := now.Sub(t); d > AuthWindow || d < -AuthWindow {
		return domain.ErrMoveClockSkew
	}
	if !replay.use(p.moveID+":"+p.nonce, t.Add(AuthWindow), now) {
		return domain.ErrMoveCodeInvalid
	}
	return nil
}

// replayCache remembers the nonces seen within the window.
type replayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newReplayCache() *replayCache { return &replayCache{seen: map[string]time.Time{}} }

// use records key until expires; false when it was seen already (or the
// cache is full of live entries).
func (c *replayCache) use(key string, expires, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.seen[key]; dup {
		return false
	}
	if len(c.seen) >= maxReplayEntries {
		for k, exp := range c.seen {
			if !now.Before(exp) {
				delete(c.seen, k)
			}
		}
		if len(c.seen) >= maxReplayEntries {
			return false
		}
	}
	c.seen[key] = expires
	return true
}
