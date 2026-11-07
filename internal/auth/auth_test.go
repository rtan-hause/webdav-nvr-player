package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/rtan-hause/webdav-nvr-player/internal/config"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	m      *Manager
	st     *store.Store
	clk    *clock
	srv    *httptest.Server
	client *http.Client // keeps cookies, doesn't follow redirects
}

// newRig serves the auth routes plus /api/clips (needs a session), /api/ingest
// (open) and / (the web UI) behind Protect.
func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := &clock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	opts.Store, opts.Now = st, clk.now
	r := &rig{st: st, clk: clk}
	mux := http.NewServeMux()
	r.srv = httptest.NewServer(nil)
	t.Cleanup(r.srv.Close)
	if opts.PublicURL == "" {
		opts.PublicURL = r.srv.URL
	}
	r.m = New(opts)
	r.m.Routes(mux)
	mux.HandleFunc("GET /api/clips", func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "clips for "+User(req.Context())) })
	mux.HandleFunc("POST /api/ingest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html>app</html>") })
	r.srv.Config.Handler = r.m.Protect(mux)
	jar, _ := cookiejar.New(nil)
	r.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r
}

func (r *rig) do(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Hashing at full strength takes seconds under the race detector.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestPasswordSignIn(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthPassword})
	pw, err := r.m.Bootstrap(context.Background())
	if err != nil || len(pw) != 19 {
		t.Fatalf("Bootstrap = %q, %v", pw, err)
	}
	if again, _ := r.m.Bootstrap(context.Background()); again != "" {
		t.Fatal("Bootstrap must only create the admin once")
	}

	// Open without a session: version-free paths, the web UI, the webhook, the auth status.
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("clips without a session: %d", code)
	}
	if code, body := r.do(t, "GET", "/", nil); code != 200 || !strings.Contains(body, "app") {
		t.Fatalf("web UI: %d", code)
	}
	if code, _ := r.do(t, "POST", "/api/ingest", nil); code != 202 {
		t.Fatalf("webhook must stay reachable (it has its own secret): %d", code)
	}
	if code, body := r.do(t, "GET", "/api/auth", nil); code != 200 || !strings.Contains(body, `"signed_in":false`) || !strings.Contains(body, `"mode":"password"`) {
		t.Fatalf("auth status: %d %s", code, body)
	}

	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "nope"}); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "ghost", "password": pw}); code != 401 {
		t.Fatalf("unknown user: %d", code)
	}
	if code, body := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw}); code != 200 {
		t.Fatalf("right password: %d %s", code, body)
	}
	if code, body := r.do(t, "GET", "/api/clips", nil); code != 200 || body != "clips for admin" {
		t.Fatalf("clips with a session: %d %q", code, body)
	}
	if _, body := r.do(t, "GET", "/api/auth", nil); !strings.Contains(body, `"user":"admin"`) {
		t.Fatalf("auth status after sign-in: %s", body)
	}

	if code, _ := r.do(t, "POST", "/api/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("clips after sign-out: %d", code)
	}
}

func TestSessionsSlideAndExpire(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthPassword})
	pw, _ := r.m.Bootstrap(context.Background())
	r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw})

	r.clk.add(29 * 24 * time.Hour)
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 200 {
		t.Fatalf("day 29: %d", code)
	}
	// That use pushed the expiry 30 days past day 29.
	r.clk.add(20 * 24 * time.Hour)
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 200 {
		t.Fatalf("20 days after last use: %d", code)
	}
	r.clk.add(31 * 24 * time.Hour)
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("31 days idle must sign out: %d", code)
	}

	// A shorter session length from Settings applies to new and touched sessions.
	r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw})
	if code, body := r.do(t, "PUT", "/api/settings", map[string]int{"session_days": 2}); code != 200 || !strings.Contains(body, `"session_days":2`) {
		t.Fatalf("set session length: %d %s", code, body)
	}
	r.clk.add(time.Hour)
	r.do(t, "GET", "/api/clips", nil) // touch: expiry is now 2 days out
	r.clk.add(3 * 24 * time.Hour)
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("session should have expired after 2 idle days: %d", code)
	}
}

func TestSettingsAndPassword(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthPassword})
	pw, _ := r.m.Bootstrap(context.Background())
	r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw})

	if _, body := r.do(t, "GET", "/api/settings", nil); !strings.Contains(body, `"session_days":30`) {
		t.Fatalf("default settings: %s", body)
	}
	for _, d := range []int{0, 366} {
		if code, _ := r.do(t, "PUT", "/api/settings", map[string]int{"session_days": d}); code != 400 {
			t.Fatalf("session_days %d: %d", d, code)
		}
	}
	// A plain form post (what another site could make a browser send) is refused.
	req, _ := http.NewRequest("PUT", r.srv.URL+"/api/settings", strings.NewReader("session_days=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, _ := r.client.Do(req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", resp.StatusCode)
	}

	// A second browser, to check it gets signed out by the password change.
	other := &rig{srv: r.srv, client: &http.Client{CheckRedirect: r.client.CheckRedirect}}
	other.client.Jar, _ = cookiejar.New(nil)
	other.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw})

	if code, _ := r.do(t, "POST", "/api/password", map[string]string{"current": "wrong", "new": "a long new password"}); code != 400 {
		t.Fatalf("wrong current password: %d", code)
	}
	if code, _ := r.do(t, "POST", "/api/password", map[string]string{"current": pw, "new": "short"}); code != 400 {
		t.Fatalf("short password: %d", code)
	}
	if code, body := r.do(t, "POST", "/api/password", map[string]string{"current": pw, "new": "a long new password"}); code != 204 {
		t.Fatalf("change password: %d %s", code, body)
	}
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 200 {
		t.Fatalf("this browser must stay signed in: %d", code)
	}
	if code, _ := other.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("other browsers must be signed out: %d", code)
	}
	r.do(t, "POST", "/api/logout", nil)
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "a long new password"}); code != 200 {
		t.Fatalf("sign in with the new password: %d", code)
	}

	// Sign out everywhere else.
	other.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "a long new password"})
	if code, body := r.do(t, "POST", "/api/sessions/sign-out-others", nil); code != 200 || !strings.Contains(body, `"signed_out":1`) {
		t.Fatalf("sign out others: %d %s", code, body)
	}
	if code, _ := other.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("other browser after sign-out-others: %d", code)
	}
}

func TestWrongPasswordLimit(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthPassword})
	pw, _ := r.m.Bootstrap(context.Background())
	for i := 0; i < 10; i++ {
		r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "guess"})
	}
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw}); code != 429 {
		t.Fatalf("after 10 wrong passwords even the right one waits: %d", code)
	}
	r.clk.add(16 * time.Minute)
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw}); code != 200 {
		t.Fatalf("after the wait: %d", code)
	}
}

func TestNoAuth(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthNone})
	if pw, _ := r.m.Bootstrap(context.Background()); pw != "" {
		t.Fatal("AUTH=none must not create a user")
	}
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 200 {
		t.Fatalf("AUTH=none: %d", code)
	}
	if _, body := r.do(t, "GET", "/api/auth", nil); !strings.Contains(body, `"signed_in":true`) {
		t.Fatalf("auth status: %s", body)
	}
}

func TestResetPassword(t *testing.T) {
	r := newRig(t, Options{Mode: config.AuthPassword})
	pw, _ := r.m.Bootstrap(context.Background())
	r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": pw})
	npw, err := ResetPassword(context.Background(), r.st, AdminUser, r.clk.now())
	if err != nil || npw == pw {
		t.Fatalf("ResetPassword = %q, %v", npw, err)
	}
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("reset must sign out existing sessions: %d", code)
	}
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": npw}); code != 200 {
		t.Fatalf("new password: %d", code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/": "/", "/?cam=1&t=2": "/?cam=1&t=2", "//evil.example": "/", "https://evil.example": "/", "/\\evil": "/", "": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- OIDC against a fake provider ----------

type fakeIDP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	challenge string // code_challenge from the authorize redirect
	nonce     string
	badNonce  bool
}

func newFakeIDP(t *testing.T, clientID string) *fakeIDP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIDP{key: key}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		nonce := p.nonce
		if p.badNonce {
			nonce = "something else"
		}
		now := time.Now()
		claims, _ := json.Marshal(map[string]any{
			"iss": p.srv.URL, "aud": clientID, "sub": "user-123", "email": "richard@example.com",
			"nonce": nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		})
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
		jws, _ := signer.Sign(claims)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idt})
	})
	return p
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIDP(t, "nvr")
	r := newRig(t, Options{Mode: config.AuthOIDC, OIDC: config.OIDC{Issuer: idp.srv.URL, ClientID: "nvr", ClientSecret: "s", Scopes: []string{"openid", "email"}, Name: "Zitadel"}})
	// The rig's clock is fixed in 2026; the fake provider signs with real time.
	r.m.now = time.Now

	if _, body := r.do(t, "GET", "/api/auth", nil); !strings.Contains(body, `"provider":"Zitadel"`) || !strings.Contains(body, `"signed_in":false`) {
		t.Fatalf("auth status: %s", body)
	}
	if code, _ := r.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "x"}); code != 404 {
		t.Fatalf("password sign-in must be off with OIDC: %d", code)
	}

	signIn := func(next string) (callback *url.URL) {
		req, _ := http.NewRequest("GET", r.srv.URL+"/auth/oidc/login?next="+url.QueryEscape(next), nil)
		resp, err := r.client.Do(req)
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("login redirect: %v %v", resp.StatusCode, err)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		q := loc.Query()
		if !strings.HasPrefix(loc.String(), idp.srv.URL+"/authorize") || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatalf("authorize URL: %s", loc)
		}
		idp.challenge, idp.nonce = q.Get("code_challenge"), q.Get("nonce")
		cb, _ := url.Parse(r.srv.URL + "/auth/oidc/callback?code=good-code&state=" + url.QueryEscape(q.Get("state")))
		return cb
	}
	follow := func(u *url.URL) (int, string) {
		resp, err := r.client.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}

	// Tampered state.
	cb := signIn("/")
	bad := *cb
	bad.RawQuery = "code=good-code&state=forged"
	if _, loc := follow(&bad); !strings.HasPrefix(loc, "/?auth_error=") {
		t.Fatalf("forged state: %q", loc)
	}
	// Token with the wrong nonce.
	idp.badNonce = true
	if _, loc := follow(signIn("/")); !strings.HasPrefix(loc, "/?auth_error=") {
		t.Fatalf("wrong nonce: %q", loc)
	}
	idp.badNonce = false
	if code, _ := r.do(t, "GET", "/api/clips", nil); code != 401 {
		t.Fatalf("failed sign-ins must not create a session: %d", code)
	}

	// The real thing, returning to where the user was.
	if code, loc := follow(signIn("/?cam=1&t=5")); code != 302 || loc != "/?cam=1&t=5" {
		t.Fatalf("callback: %d %q", code, loc)
	}
	if code, body := r.do(t, "GET", "/api/clips", nil); code != 200 || body != "clips for richard@example.com" {
		t.Fatalf("after OIDC sign-in: %d %q", code, body)
	}
	if code, _ := r.do(t, "POST", "/api/password", map[string]string{"current": "a", "new": "bbbbbbbbbbbb"}); code != 404 {
		t.Fatalf("password change with OIDC: %d", code)
	}
}
