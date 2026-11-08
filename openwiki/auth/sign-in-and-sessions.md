---
type: subsystem
title: Sign-in and Sessions
description: How webdav-nvr-player signs people in (local admin password, OpenID Connect, or none), how sessions are stored, extended and expired, what the API guard lets through, and the sign-in and settings screens.
tags: [auth, sessions, oidc, password, security, cookies]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-b12cba1b5936a1da8739d39c
    resource: repo://internal/auth/auth.go
  - id: openwiki-source-f3c901cd7f81495d7b85597f
    resource: repo://internal/auth/oidc.go
  - id: openwiki-source-2b3977b19eb8b9dbb40615b8
    resource: repo://internal/store/auth.go
  - id: openwiki-source-929ec0c93a55f4b9a8006978
    resource: repo://web/src/SignIn.tsx
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Sign-in and Sessions

Package `internal/auth` owns sign-in. `AUTH` selects exactly one mode at a time (see [Configuration](../architecture/configuration.md)):

- **`password`** (default): local users, in practice a single `admin`.
- **`oidc`**: any OpenID Connect provider (Zitadel, Authentik, Keycloak and others).
- **`none`**: no sign-in, for running behind a proxy that already signs people in. Startup logs a warning that anyone who can reach the server can watch the cameras.

There are no roles: every signed-in user can do everything. The user tables and the endpoints that use them are in `internal/store/auth.go` and the migrations `0002_auth.sql` (see [Store and Database Schema](../data/store-and-schema.md)).

## Guarding the API

`Manager.Protect` wraps the whole HTTP mux (see [HTTP API](../api/http-api.md)). A request passes without a session when any of these is true:

- the mode is `none`;
- the path does not start with `/api/` (the SPA, `/healthz`, `/auth/oidc/*` and the loopback-only `/internal/raw/*`); or
- it is in the `openAPI` allow-list: `GET`/`HEAD /api/version`, `GET /api/auth`, `POST /api/login`, `POST /api/logout`, and `POST /api/ingest` / `POST /api/rescan`, which check their own shared secret.

Every other request needs a valid session cookie, or it gets **401** `{"error":"sign in to continue"}`. The web client turns any 401 into an `nvr:signed-out` event and shows the sign-in page. The UI itself always loads, and asks `GET /api/auth` whether to show the sign-in page.

## Sessions

Sessions are kept on the server:

- `startSession` makes a 32-byte random token, base64url-encoded, and sends it as the `nvr_session` cookie (`HttpOnly`, `SameSite=Lax`, `Path=/`). The `sessions` table stores only the token's **SHA-256** (`token_hash`), so a copy of the database cannot be used to sign in.
- **Sliding expiry.** `current` looks the session up by hash with `expires_ms > now`. If the session was last recorded as seen at least 10 minutes ago (`touchEvery`), it updates `seen_ms`, pushes `expires_ms` out by the session length, and re-sends the cookie with the new expiry. Activity therefore costs at most one write per 10 minutes per session.
- **Session length** is the `session_days` setting (default 30, editable 1–365 under Settings). It is read again each time a session is created or extended.
- **Pruning.** `cmd/nvr` deletes expired sessions every hour (`Store.PruneSessions`).
- **Secure cookies.** Cookies are marked `Secure` when the request came over TLS, when `X-Forwarded-Proto: https` is set, or when `PUBLIC_URL` starts with `https://`.

## Password mode

- **Bootstrap.** On a start with no users, `Bootstrap` creates `admin` with a generated password, which `cmd/nvr` logs once as a warning. The password is 16 random characters from an alphabet without look-alike characters (`0/o`, `1/l/i`), in groups of 4 separated by dashes (about 79 bits). Passwords are hashed with bcrypt, cost 12 (tests lower it).
- **Login** (`POST /api/login`) needs a JSON body (`Content-Type: application/json`, which also blocks cross-site HTML form posts). The body is capped at 64 KiB. If the user doesn't exist, the password is compared against a dummy bcrypt hash anyway, so response timing doesn't reveal valid usernames.
- **Rate limiting.** Each client IP and username pair gets 10 failures per 15-minute window. Further attempts get **429** until the window passes, and a successful login clears the counter. Behind a proxy on a loopback or private address, the client IP is the *last* `X-Forwarded-For` entry, the one the proxy added.
- **Changing the password** (`POST /api/password`) requires the current password (also rate-limited) and a new one of at least 10 characters. It then signs out every *other* session of that user.
- **Lost password.** `nvr reset-password [user]` (or `make reset-password`) calls `auth.ResetPassword`. It sets a fresh generated password, creates the user if it doesn't exist, and deletes all of that user's sessions.

Outside password mode, `/api/login` and `/api/password` answer 404.

## OIDC mode

`internal/auth/oidc.go` implements the authorization code flow with **PKCE, state and nonce**, without storing anything on the server between the redirect and the callback:

1. `GET /auth/oidc/login?next=/...` runs provider discovery the first time it is needed (so the app starts even if the provider is briefly down). It then generates state, nonce and a PKCE verifier and seals them, along with a sanitized `next` and a 10-minute expiry, into an HMAC-signed `nvr_oidc` cookie scoped to `/auth/oidc/`. Finally it redirects to the provider.
2. `GET /auth/oidc/callback` clears the flow cookie, then:
   - checks the signature, expiry and `state`;
   - exchanges the code with the verifier;
   - verifies the ID token against `OIDC_CLIENT_ID`;
   - checks the nonce.
3. The username is the token's `email` claim, then `preferred_username`, then `sub`. A session starts the same way as in password mode, and the browser is redirected to `next`.

`safeNext` only allows same-site paths: a path that starts with `//` or `/\` becomes `/`. Every failure redirects to `/?auth_error=<message>`, which the sign-in page shows once and then removes from the address bar. The flow-cookie key is random per process, so a restart in the middle of sign-in just means signing in again.

## Other endpoints

| Route | Behavior |
|---|---|
| `GET /api/auth` | `{mode, signed_in, user, provider}`, with `Cache-Control: no-store`. In `none` mode it always reports `signed_in: true`. |
| `POST /api/logout` | Deletes this session's row and clears the cookie. Returns 204. |
| `GET`/`PUT /api/settings` | `{session_days}`. |
| `POST /api/sessions/sign-out-others` | Deletes every session except the caller's, for all users. Returns `{signed_out: n}`. |

## Frontend

- `web/src/SignIn.tsx` shows a username and password form (`POST /api/login`) or, in OIDC mode, a button that links to `/auth/oidc/login?next=...` labelled with `OIDC_PROVIDER_NAME`. It reads `auth_error` from the URL, and explains where to find the first admin password and how to reset it.
- `web/src/Settings.tsx` (`#/settings`) edits the session length, changes the password (password mode only), and offers sign-out and sign-out-everywhere-else.

## Tests

`internal/auth/auth_test.go` covers password sign-in, sliding and expiring sessions, settings and password change, the wrong-password limit, `AUTH=none`, password reset, `safeNext`, and a full OIDC sign-in against an in-process fake provider (discovery document, JWKS and token endpoint).
