package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Auth is the panel's authentication layer: a single shared password guards
// the UI via an HMAC-signed session cookie, and a bearer token guards /ingest.
//
// No password hashing: the config file already stores the password in
// plaintext, so bcrypt would protect nothing the attacker who can read the
// config doesn't already have. Constant-time compares still avoid timing
// oracles at the HTTP boundary.
//
// A nil *Auth means auth is disabled — every method is safe on a nil receiver
// and behaves as an open panel (Middleware passes through, MeHandler reports
// auth_enabled:false / authenticated:true).
type Auth struct {
	username     string
	password     string
	sessionKey   []byte // HMAC key for session cookies
	ingestToken  string
	secure       bool // set Secure flag on cookies (behind TLS)
	ephemeralKey bool // sessionKey was randomly derived (won't survive restart)

	limiter *rateLimiter
}

const (
	sessionCookieName = "looper_session"
	sessionTTL        = 7 * 24 * time.Hour
	cookieVersion     = "v1"
)

// NewAuth builds an Auth. An empty sessionSecret derives a random key at boot
// (sessions then do NOT survive a restart — set a stable secret in config for
// persistence). An empty ingestToken generates a random hex token, retrievable
// via IngestToken so the caller can print it at startup.
func NewAuth(username, password, sessionSecret, ingestToken string) *Auth {
	a := &Auth{
		username:    username,
		password:    password,
		ingestToken: ingestToken,
		limiter:     newRateLimiter(5, time.Minute),
	}
	if sessionSecret != "" {
		a.sessionKey = []byte(sessionSecret)
	} else {
		a.sessionKey = randomBytes(32)
		a.ephemeralKey = true
	}
	if a.ingestToken == "" {
		a.ingestToken = hex.EncodeToString(randomBytes(24))
	}
	return a
}

// WithSecureCookies marks session cookies Secure (send only over HTTPS). The
// caller decides based on its serving scheme.
func (a *Auth) WithSecureCookies(secure bool) *Auth {
	if a != nil {
		a.secure = secure
	}
	return a
}

// IngestToken returns the bearer token /ingest expects. The caller prints this
// at startup when it was auto-generated so agents/tracers can be configured.
func (a *Auth) IngestToken() string {
	if a == nil {
		return ""
	}
	return a.ingestToken
}

// EphemeralSessionKey reports whether the session key was randomly derived
// (i.e. no stable secret configured), so the caller can warn that sessions
// won't survive a restart.
func (a *Auth) EphemeralSessionKey() bool {
	return a != nil && a.ephemeralKey
}

// ── Session cookie ─────────────────────────────────────────────────────────

// signSession returns the cookie value "v1:<expiry-unix>:<hmac>".
func (a *Auth) signSession(expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	return cookieVersion + ":" + exp + ":" + a.mac(exp)
}

// mac computes hex(HMAC-SHA256(sessionKey, msg)).
func (a *Auth) mac(msg string) string {
	m := hmac.New(sha256.New, a.sessionKey)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// validSession reports whether value is a well-formed, unexpired, correctly
// signed session token. All comparisons are constant-time.
func (a *Auth) validSession(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != cookieVersion {
		return false
	}
	exp, mac := parts[1], parts[2]
	expected := a.mac(exp)
	if subtle.ConstantTimeCompare([]byte(mac), []byte(expected)) != 1 {
		return false
	}
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(ts, 0))
}

func (a *Auth) sessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ── Handlers ───────────────────────────────────────────────────────────────

// loginRequest is the POST /api/login body. Username is optional (single
// shared account); only the password is checked.
type loginRequest struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
}

// LoginHandler handles POST /api/login. 204 + session cookie on success, 401
// on bad password. Per-IP rate limited.
func (a *Auth) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		// Auth disabled: nothing to log into, but report success so the SPA's
		// optimistic login flow doesn't wedge.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !a.limiter.allow(clientIP(r)) {
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.password)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	expiry := time.Now().Add(sessionTTL)
	http.SetCookie(w, a.sessionCookie(a.signSession(expiry), expiry))
	w.WriteHeader(http.StatusNoContent)
}

// LogoutHandler handles POST /api/logout. Clears the session cookie.
func (a *Auth) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if a != nil {
		// Expire the cookie in the past to evict it client-side.
		http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0)))
	}
	w.WriteHeader(http.StatusNoContent)
}

// meResponse is the GET /api/me body.
type meResponse struct {
	AuthEnabled   bool   `json:"auth_enabled"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
}

// MeHandler handles GET /api/me. Safe on a nil receiver: auth disabled reports
// auth_enabled:false, authenticated:true (the panel is open).
func (a *Auth) MeHandler(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{AuthEnabled: true}
	if a == nil {
		resp.AuthEnabled = false
		resp.Authenticated = true
	} else if a.authed(r) {
		resp.Authenticated = true
		resp.Username = a.username
	}
	writeJSON(w, http.StatusOK, resp)
}

// authed reports whether the request carries a valid session cookie.
func (a *Auth) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return a.validSession(c.Value)
}

// ── Middleware ─────────────────────────────────────────────────────────────

// Middleware guards next. When auth is disabled (nil *Auth) it passes through.
// Otherwise:
//   - POST /api/login and GET /api/me are always allowed (login surface).
//   - POST /ingest requires "Authorization: Bearer <ingestToken>".
//   - everything else requires a valid session cookie.
//
// Failures return 401 JSON {"error":"unauthorized"} for /api/* paths, or a
// bare 401 otherwise. The server issues no redirects — the SPA client handles
// routing to the login view.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.allowedUnauthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/ingest" {
			if a.validBearer(r) {
				next.ServeHTTP(w, r)
				return
			}
			a.deny(w, r)
			return
		}
		if a.authed(r) {
			next.ServeHTTP(w, r)
			return
		}
		a.deny(w, r)
	})
}

// allowedUnauthenticated lists requests reachable without a session: the
// login POST, the /api/me probe the SPA uses to decide what to render, and
// every SPA shell/asset GET. The static bundle carries no run data — all of
// it lives behind /api/*, /ingest, and /api/events — and without the shell
// the browser could never render the login screen in the first place.
func (a *Auth) allowedUnauthenticated(r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/login":
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/api/logout":
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/me":
		return true
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		!strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/ingest":
		// SPA shell + hashed assets (client-side routes, /assets/*, /).
		return true
	default:
		return false
	}
}

// validBearer checks the ingest bearer token in constant time.
func (a *Auth) validBearer(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	tok := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(tok), []byte(a.ingestToken)) == 1
}

// deny writes the 401. JSON body for /api/* so the SPA parses it; bare 401
// otherwise.
func (a *Auth) deny(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// ── Rate limiter ─────────────────────────────────────────────────────────────

// rateLimiter is a naive fixed-window per-key counter. Sufficient for a
// supervision panel with a single shared credential: it caps brute-force
// throughput without the memory/complexity of a sliding window. State is
// GC'd lazily when a key's window has elapsed.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*windowCount
}

type windowCount struct {
	count int
	start time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string]*windowCount{}}
}

// allow records an attempt for key and reports whether it is within the limit.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	wc := rl.hits[key]
	if wc == nil || now.Sub(wc.start) >= rl.window {
		// Fresh window. GC keys whose window has elapsed while we hold the lock.
		rl.gc(now)
		rl.hits[key] = &windowCount{count: 1, start: now}
		return true
	}
	if wc.count >= rl.max {
		return false
	}
	wc.count++
	return true
}

// gc drops expired windows. Called under lock on access — no background timer.
func (rl *rateLimiter) gc(now time.Time) {
	for k, wc := range rl.hits {
		if now.Sub(wc.start) >= rl.window {
			delete(rl.hits, k)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is not recoverable and must not silently weaken
		// security by continuing with a zero key.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
