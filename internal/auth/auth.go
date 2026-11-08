// Package auth signs people in: a local "admin" password (the default), an
// OpenID Connect provider, or nothing (AUTH=none, for use behind a proxy that
// already signs people in). There is one kind of user and every signed-in
// user can do everything.
//
// Sessions are server-side: the cookie holds a random token and the database
// keeps only its SHA-256, so a copy of the database can't be used to sign in.
// Sessions slide: each use pushes the expiry out by the session length, which
// is a setting changed in the app.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rtan-hause/webdav-nvr-player/internal/config"
	"github.com/rtan-hause/webdav-nvr-player/internal/httpx"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
)

const (
	// AdminUser is the local account created on first start in password mode.
	AdminUser = "admin"
	// DefaultSessionDays is how long a session lasts without use, until changed in Settings.
	DefaultSessionDays = 30
	// MinPasswordLength applies to passwords set in the app.
	MinPasswordLength = 10

	cookieName         = "nvr_session"
	settingSessionDays = "session_days"
	touchEvery         = 10 * time.Minute // how often activity is written back (and the expiry moved)
)

// bcryptCost is the password hashing work factor (tests lower it).
var bcryptCost = 12

// Options configures a Manager.
type Options struct {
	Mode      config.AuthMode
	Store     *store.Store
	PublicURL string      // used for the OIDC callback and to decide on Secure cookies
	OIDC      config.OIDC // AUTH=oidc only
	Now       func() time.Time
	Client    *http.Client // for talking to the OIDC provider (tests swap it)
}

// Manager checks sessions and serves the sign-in endpoints.
type Manager struct {
	opts    Options
	now     func() time.Time
	limit   *limiter
	oidc    *oidcClient
	dummyPW []byte // compared against when a username doesn't exist, so timing doesn't tell
}

// New returns a Manager. For OIDC the provider is contacted on first sign-in,
// so the app starts even if the provider is briefly unreachable.
func New(opts Options) *Manager {
	m := &Manager{opts: opts, now: opts.Now, limit: newLimiter(10, 15*time.Minute)}
	if m.now == nil {
		m.now = time.Now
	}
	if opts.Client == nil {
		m.opts.Client = &http.Client{Timeout: 15 * time.Second}
	}
	m.dummyPW, _ = bcrypt.GenerateFromPassword([]byte("not a real password"), bcryptCost)
	if opts.Mode == config.AuthOIDC {
		m.oidc = newOIDC(opts.OIDC, opts.PublicURL+"/auth/oidc/callback", m.opts.Client)
	}
	return m
}

// Mode returns the sign-in mode.
func (m *Manager) Mode() config.AuthMode { return m.opts.Mode }

// Bootstrap creates the admin user on first start in password mode and
// returns its generated password (empty when nothing was created).
func (m *Manager) Bootstrap(ctx context.Context) (string, error) {
	if m.opts.Mode != config.AuthPassword {
		return "", nil
	}
	n, err := m.opts.Store.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	pw, hash, err := newPassword()
	if err != nil {
		return "", err
	}
	if err := m.opts.Store.CreateUser(ctx, AdminUser, hash, m.now().UnixMilli()); err != nil {
		return "", err
	}
	return pw, nil
}

// ResetPassword gives username (created if missing) a new generated password
// and signs out its sessions. Used by `nvr reset-password`.
func ResetPassword(ctx context.Context, st *store.Store, username string, now time.Time) (string, error) {
	pw, hash, err := newPassword()
	if err != nil {
		return "", err
	}
	err = st.SetPassword(ctx, username, hash)
	if errors.Is(err, store.ErrNotFound) {
		err = st.CreateUser(ctx, username, hash, now.UnixMilli())
	}
	if err != nil {
		return "", err
	}
	_, err = st.DeleteOtherSessions(ctx, username, "")
	return pw, err
}

// newPassword returns a readable random password (about 79 bits) and its hash.
func newPassword() (string, string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	pw := sb.String()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return pw, string(hash), err
}

// ---------- sessions ----------

type ctxKey struct{}

type identity struct {
	user      string
	tokenHash string
}

// User returns the signed-in user for a request that went through Protect ("" with AUTH=none).
func User(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(identity)
	return id.user
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) sessionDays(ctx context.Context) int {
	v, ok, err := m.opts.Store.Setting(ctx, settingSessionDays)
	if err != nil || !ok {
		return DefaultSessionDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return DefaultSessionDays
	}
	return n
}

func (m *Manager) secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || strings.HasPrefix(m.opts.PublicURL, "https://")
}

func (m *Manager) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: m.secure(r), SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: m.secure(r), SameSite: http.SameSiteLaxMode})
}

// startSession signs user in on this browser.
func (m *Manager) startSession(w http.ResponseWriter, r *http.Request, user string) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := m.now()
	expires := now.Add(time.Duration(m.sessionDays(r.Context())) * 24 * time.Hour)
	err := m.opts.Store.CreateSession(r.Context(), store.Session{
		TokenHash: hashToken(token), Username: user, CreatedMS: now.UnixMilli(), SeenMS: now.UnixMilli(), ExpiresMS: expires.UnixMilli(),
	})
	if err != nil {
		return err
	}
	m.setCookie(w, r, token, expires)
	slog.Info("signed in", "user", user, "via", m.opts.Mode)
	return nil
}

// current returns the session for the request's cookie, moving its expiry
// forward (at most every touchEvery) while it's in use.
func (m *Manager) current(w http.ResponseWriter, r *http.Request) (identity, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return identity{}, false
	}
	h := hashToken(c.Value)
	now := m.now()
	s, err := m.opts.Store.SessionByHash(r.Context(), h, now.UnixMilli())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("session lookup failed", "err", err)
		}
		return identity{}, false
	}
	if now.Sub(time.UnixMilli(s.SeenMS)) >= touchEvery {
		expires := now.Add(time.Duration(m.sessionDays(r.Context())) * 24 * time.Hour)
		if err := m.opts.Store.TouchSession(r.Context(), h, now.UnixMilli(), expires.UnixMilli()); err == nil {
			m.setCookie(w, r, c.Value, expires)
		}
	}
	return identity{user: s.Username, tokenHash: h}, true
}

// openAPI lists API endpoints that work without signing in. Everything else
// under /api/ needs a session. Paths outside /api/ are the web UI (which
// shows the sign-in page itself), /healthz, the OIDC redirects, and
// /internal/raw (loopback only, signed links).
var openAPI = map[string]bool{
	"GET /api/version":  true,
	"GET /api/auth":     true,
	"POST /api/login":   true,
	"POST /api/logout":  true,
	"POST /api/ingest":  true, // its own shared secret
	"POST /api/rescan":  true, // same
	"HEAD /api/version": true,
}

// Protect requires a session for the API.
func (m *Manager) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.opts.Mode == config.AuthNone || !strings.HasPrefix(r.URL.Path, "/api/") || openAPI[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := m.current(w, r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// ---------- endpoints ----------

// Routes adds the sign-in and settings endpoints.
func (m *Manager) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth", m.status)
	mux.HandleFunc("POST /api/login", m.login)
	mux.HandleFunc("POST /api/logout", m.logout)
	mux.HandleFunc("GET /api/settings", m.getSettings)
	mux.HandleFunc("PUT /api/settings", m.putSettings)
	mux.HandleFunc("POST /api/password", m.changePassword)
	mux.HandleFunc("POST /api/sessions/sign-out-others", m.signOutOthers)
	if m.oidc != nil {
		mux.HandleFunc("GET /auth/oidc/login", m.oidcLogin)
		mux.HandleFunc("GET /auth/oidc/callback", m.oidcCallback)
	}
}

type statusJSON struct {
	Mode     config.AuthMode `json:"mode"`
	SignedIn bool            `json:"signed_in"`
	User     string          `json:"user,omitempty"`
	Provider string          `json:"provider,omitempty"` // OIDC button label
}

func (m *Manager) status(w http.ResponseWriter, r *http.Request) {
	out := statusJSON{Mode: m.opts.Mode}
	switch m.opts.Mode {
	case config.AuthNone:
		out.SignedIn = true
	default:
		if id, ok := m.current(w, r); ok {
			out.SignedIn, out.User = true, id.user
		}
		if m.oidc != nil {
			out.Provider = m.opts.OIDC.Name
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	if m.opts.Mode != config.AuthPassword {
		httpx.Error(w, http.StatusNotFound, "password sign-in is off")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	user := strings.TrimSpace(body.Username)
	key := clientIP(r) + "|" + strings.ToLower(user)
	now := m.now()
	if !m.limit.allowed(key, now) {
		httpx.Error(w, http.StatusTooManyRequests, "Too many wrong passwords. Wait 15 minutes and try again.")
		return
	}
	u, err := m.opts.Store.UserByName(r.Context(), user)
	hash := []byte(u.PasswordHash)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		hash = m.dummyPW
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(body.Password)) != nil || err != nil {
		m.limit.fail(key, now)
		slog.Warn("failed sign-in", "user", user, "ip", clientIP(r))
		httpx.Error(w, http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	m.limit.reset(key)
	if err := m.startSession(w, r, u.Username); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, statusJSON{Mode: m.opts.Mode, SignedIn: true, User: u.Username})
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if err := m.opts.Store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			slog.Warn("sign-out failed", "err", err)
		}
	}
	m.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

type settingsJSON struct {
	SessionDays int `json:"session_days"`
}

func (m *Manager) getSettings(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, settingsJSON{SessionDays: m.sessionDays(r.Context())})
}

func (m *Manager) putSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsJSON
	if !readJSON(w, r, &body) {
		return
	}
	if body.SessionDays < 1 || body.SessionDays > 365 {
		httpx.Error(w, http.StatusBadRequest, "Session length must be 1 to 365 days.")
		return
	}
	if err := m.opts.Store.SetSetting(r.Context(), settingSessionDays, strconv.Itoa(body.SessionDays)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (m *Manager) changePassword(w http.ResponseWriter, r *http.Request) {
	if m.opts.Mode != config.AuthPassword {
		httpx.Error(w, http.StatusNotFound, "passwords are managed by your sign-in provider")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, _ := r.Context().Value(ctxKey{}).(identity)
	u, err := m.opts.Store.UserByName(r.Context(), id.user)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	key := clientIP(r) + "|" + strings.ToLower(u.Username)
	if !m.limit.allowed(key, m.now()) {
		httpx.Error(w, http.StatusTooManyRequests, "Too many wrong passwords. Wait 15 minutes and try again.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Current)) != nil {
		m.limit.fail(key, m.now())
		httpx.Error(w, http.StatusBadRequest, "Your current password isn’t right.")
		return
	}
	if len([]rune(body.New)) < MinPasswordLength {
		httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("Use at least %d characters.", MinPasswordLength))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.New), bcryptCost)
	if err == nil {
		err = m.opts.Store.SetPassword(r.Context(), u.Username, string(hash))
	}
	if err == nil {
		_, err = m.opts.Store.DeleteOtherSessions(r.Context(), u.Username, id.tokenHash)
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("password changed; other sessions signed out", "user", u.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) signOutOthers(w http.ResponseWriter, r *http.Request) {
	id, _ := r.Context().Value(ctxKey{}).(identity)
	n, err := m.opts.Store.DeleteOtherSessions(r.Context(), "", id.tokenHash)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"signed_out": n})
}

// ---------- helpers ----------

// readJSON decodes a small JSON body. Requiring application/json also keeps
// other sites from posting plain HTML forms here.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpx.Error(w, http.StatusUnsupportedMediaType, "send JSON")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		httpx.Error(w, http.StatusBadRequest, "couldn’t read the request")
		return false
	}
	return true
}

// clientIP is the caller's address. Behind a reverse proxy on a private
// network it's the last X-Forwarded-For entry, the one the proxy added.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	return host
}

// limiter allows max failures per key in a window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*bucket
}

type bucket struct {
	n     int
	until time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, m: map[string]*bucket{}}
}

func (l *limiter) allowed(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	return b == nil || now.After(b.until) || b.n < l.max
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) > 10_000 {
		for k, b := range l.m {
			if now.After(b.until) {
				delete(l.m, k)
			}
		}
	}
	b := l.m[key]
	if b == nil || now.After(b.until) {
		b = &bucket{until: now.Add(l.window)}
		l.m[key] = b
	}
	b.n++
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}
