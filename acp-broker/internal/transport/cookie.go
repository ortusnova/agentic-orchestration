package transport

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// DefaultCookieTTL is the lifetime of a browser session cookie.
const DefaultCookieTTL = 12 * time.Hour

// cookieName names the browser session cookie.
const cookieName = "acp_broker_session"

// cookieSessions holds the browser sessions this process issued, in
// memory only, so they all end with the process. Session IDs are keyed by
// their SHA-256 digest, so a lookup's timing reveals nothing about them.
type cookieSessions struct {
	mu      sync.Mutex
	expires map[[sha256.Size]byte]time.Time
	now     func() time.Time
}

func newCookieSessions() *cookieSessions {
	return &cookieSessions{expires: map[[sha256.Size]byte]time.Time{}, now: time.Now}
}

func (c *cookieSessions) issue(ttl time.Duration) (id string, expires time.Time) {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf) // crypto/rand.Read never returns an error
	id = base64.RawURLEncoding.EncodeToString(buf)
	expires = c.now().Add(ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expires[sha256.Sum256([]byte(id))] = expires
	return id, expires
}

func (c *cookieSessions) valid(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	c.mu.Lock()
	defer c.mu.Unlock()
	expires, ok := c.expires[key]
	if !ok {
		return false
	}
	if !c.now().Before(expires) {
		delete(c.expires, key)
		return false
	}
	return true
}

func (c *cookieSessions) revoke(r *http.Request) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.expires, sha256.Sum256([]byte(cookie.Value)))
}

func (h *HTTP) secureCookie() bool {
	return strings.HasPrefix(strings.ToLower(h.cfg.PublicURL), "https://")
}

// login trades the viewer token, presented as a bearer header, for a
// session cookie a browser's EventSource sends on its own.
func (h *HTTP) login(w http.ResponseWriter, r *http.Request) {
	if !h.tokenMatches(bearer(r)) {
		writeJSON(w, http.StatusUnauthorized, broker.Event{Type: "error", Message: "unauthorized"})
		return
	}
	id, expires := h.sessions.issue(h.cfg.CookieTTL)
	// Secure follows the public URL: a plain-HTTP listener on loopback or
	// a private network still needs the cookie to work.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is set whenever the public URL is https
		Name:     cookieName,
		Value:    id,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(h.cfg.CookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secureCookie(),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// logout ends the browser session the request's cookie names and clears
// the cookie.
func (h *HTTP) logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.revoke(r)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // clears the cookie set by login
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookie(),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}
