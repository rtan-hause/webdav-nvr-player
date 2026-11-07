package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/rtan-hause/webdav-nvr-player/internal/config"
)

// OpenID Connect sign-in: authorization code flow with PKCE, state and nonce.
// The state, nonce and PKCE verifier ride in a short-lived signed cookie, so
// nothing is kept on the server between the redirect and the callback.

const flowCookie = "nvr_oidc"

type oidcClient struct {
	cfg      config.OIDC
	redirect string
	client   *http.Client
	key      []byte // signs the flow cookie; a restart just means signing in again

	mu       sync.Mutex
	provider *oidc.Provider
}

func newOIDC(cfg config.OIDC, redirect string, client *http.Client) *oidcClient {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &oidcClient{cfg: cfg, redirect: redirect, client: client, key: key}
}

// setup discovers the provider on first use and remembers it.
func (c *oidcClient) setup(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.client), c.cfg.Issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("couldn’t reach the sign-in provider at %s: %w", c.cfg.Issuer, err)
		}
		c.provider = p
	}
	return c.provider, &oauth2.Config{
		ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, RedirectURL: c.redirect,
		Endpoint: c.provider.Endpoint(), Scopes: c.cfg.Scopes,
	}, nil
}

type flow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"r"`
	Expires  int64  `json:"e"`
}

func (c *oidcClient) seal(f flow) string {
	b, _ := json.Marshal(f)
	payload := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *oidcClient) open(v string, now time.Time) (flow, error) {
	var f flow
	payload, sig, ok := strings.Cut(v, ".")
	if !ok {
		return f, errors.New("malformed")
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return f, errors.New("bad signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(b, &f) != nil {
		return f, errors.New("malformed")
	}
	if now.Unix() > f.Expires {
		return f, errors.New("expired")
	}
	return f, nil
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeNext keeps the post-sign-in redirect on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// failed sends the browser back to the app with a message for the sign-in page.
func failed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?auth_error="+url.QueryEscape(msg), http.StatusFound)
}

func (m *Manager) oidcLogin(w http.ResponseWriter, r *http.Request) {
	_, conf, err := m.oidc.setup(r.Context())
	if err != nil {
		slog.Error("OIDC discovery failed", "err", err)
		failed(w, r, "The sign-in provider isn’t reachable right now. Try again in a moment.")
		return
	}
	f := flow{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next")), Expires: m.now().Add(10 * time.Minute).Unix()}
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: m.oidc.seal(f), Path: "/auth/oidc/", MaxAge: 600,
		HttpOnly: true, Secure: m.secure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, conf.AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), http.StatusFound)
}

func (m *Manager) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/auth/oidc/", MaxAge: -1, HttpOnly: true, Secure: m.secure(r), SameSite: http.SameSiteLaxMode})
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		failed(w, r, "The sign-in provider said: "+msg)
		return
	}
	c, err := r.Cookie(flowCookie)
	if err != nil {
		failed(w, r, "That sign-in link has expired. Try again.")
		return
	}
	f, err := m.oidc.open(c.Value, m.now())
	if err != nil || q.Get("state") == "" || !hmac.Equal([]byte(q.Get("state")), []byte(f.State)) {
		failed(w, r, "That sign-in link has expired. Try again.")
		return
	}
	provider, conf, err := m.oidc.setup(r.Context())
	if err != nil {
		failed(w, r, "The sign-in provider isn’t reachable right now. Try again in a moment.")
		return
	}
	ctx := oidc.ClientContext(r.Context(), m.oidc.client)
	tok, err := conf.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(f.Verifier))
	if err != nil {
		slog.Warn("OIDC code exchange failed", "err", err)
		failed(w, r, "Sign-in didn’t complete. Try again.")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := provider.Verifier(&oidc.Config{ClientID: m.opts.OIDC.ClientID, Now: m.now}).Verify(ctx, raw)
	if err == nil && !hmac.Equal([]byte(idt.Nonce), []byte(f.Nonce)) {
		err = errors.New("nonce doesn’t match this sign-in")
	}
	if err != nil {
		slog.Warn("OIDC ID token rejected", "err", err)
		failed(w, r, "Sign-in didn’t complete. Try again.")
		return
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	_ = idt.Claims(&claims)
	user := claims.Email
	if user == "" {
		user = claims.PreferredUsername
	}
	if user == "" {
		user = idt.Subject
	}
	if err := m.startSession(w, r, user); err != nil {
		failed(w, r, "Sign-in didn’t complete. Try again.")
		return
	}
	http.Redirect(w, r, f.Next, http.StatusFound)
}
