---
type: configuration-reference
title: Configuration
description: Every environment variable webdav-nvr-player reads, with defaults, validation rules and the components that use it, plus the one runtime setting (session lifetime) kept in the database.
tags: [configuration, environment, settings, oidc, webdav]
sources:
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-b12cba1b5936a1da8739d39c
    resource: repo://internal/auth/auth.go
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# Configuration

All startup configuration comes from environment variables, read once by `config.FromEnv` in `internal/config/config.go`. There is no config file. `make run` and `docker compose` load `.env` (copied from `.env.example`) into the environment. `FromEnv` takes a `getenv` function, so tests pass a map instead of touching the real environment (`internal/config/config_test.go`).

Any invalid value stops startup with an error that says what was wrong. Values are trimmed of surrounding whitespace, except passwords and client secrets (`WEBDAV_PASSWORD`, `OIDC_CLIENT_SECRET`), which may legitimately contain spaces.

## Server

| Variable | Default | Rules / effect |
|---|---|---|
| `PORT` | `8080` | Integer 1–65535. Listens on `:<PORT>`. |
| `DATA_DIR` | `./data` | SQLite database (`nvr.db`) and thumbnails. The container image and compose file pin it to `/data`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` (`slog.Level` text). `make dev` forces `debug`. |
| `DATABASE_URL` | empty (SQLite) | Must start with `postgres://` or `postgresql://`. See [Store and Database Schema](../data/store-and-schema.md). |

## Recordings source

| Variable | Default | Rules / effect |
|---|---|---|
| `WEBDAV_URL` | empty | `http`/`https` URL with a host. It **must not** contain user info: put credentials in the variables below. When empty, the server still starts (sign-in, status page) but no index or ingest runs, and clip endpoints answer 503. |
| `WEBDAV_USERNAME` | empty | |
| `WEBDAV_PASSWORD` / `WEBDAV_PASSWORD_FILE` | empty | Set one, not both. The file variant has trailing newlines stripped, which suits mounted secrets. |
| `WEBDAV_ROOT` | `/` | Folder inside the share where the date folders start. Normalized to `/x/y/` (always a leading and trailing slash). Every path the app stores is relative to it. |

## Cameras

| Variable | Default | Rules / effect |
|---|---|---|
| `CAMERA_PROFILE` | `reolink` | Lowercased, then looked up in the profile registry at startup. An unknown id fails startup and lists the supported ones. See [Camera Profiles](../ingest/camera-profiles.md). |
| `CAMERA_TZ` (falls back to `TZ`) | UTC | IANA name. It turns the camera's wall-clock file-name times into UTC instants and sets the day boundaries used by `/api/days` and recent scans. The binary embeds `time/tzdata`, so this works on hosts without zoneinfo. |

## Indexing

| Variable | Default | Rules / effect |
|---|---|---|
| `INGEST_SECRET` | empty (webhook off) | At least 16 characters. Required as `X-NVR-Secret` on `POST /api/ingest` and `/api/rescan`. See [Upload Webhook (SFTPGo)](../integrations/upload-webhook.md). |
| `INGEST_WORKERS` | 2 | 1–32 workers that probe clips and make thumbnails. |
| `SCAN_INTERVAL` | `5m` | Go duration, at least 1 minute. How often today's and yesterday's folders are rescanned. |
| `FULL_SCAN_INTERVAL` | `24h` | Go duration, at least 1 hour. How often the full crawl and deletion reconciliation run. A full crawl also runs at startup. |
| `FFMPEG_PATH` | `ffmpeg` | Used for frame-grab thumbnails. If it isn't found, startup logs a warning and carries on. |

How these drive scanning is described in [Ingest Pipeline](../ingest/pipeline.md).

## Sign-in

| Variable | Default | Rules / effect |
|---|---|---|
| `AUTH` | `password` | `password`, `oidc` or `none`. Anything else is an error. |
| `PUBLIC_URL` | empty | `http(s)://host[:port]` with no path (a trailing `/` is allowed and removed). Required for `oidc`, where it builds the callback `PUBLIC_URL/auth/oidc/callback`. It also makes cookies `Secure` on https. |
| `OIDC_ISSUER` | — | Required for `oidc`. Must be an http(s) URL. A trailing `/` is removed. |
| `OIDC_CLIENT_ID` | — | Required for `oidc`. |
| `OIDC_CLIENT_SECRET` / `OIDC_CLIENT_SECRET_FILE` | empty | Set one, not both. Leave both empty for a public client that uses PKCE only. |
| `OIDC_SCOPES` | `openid email profile` | Space separated. |
| `OIDC_PROVIDER_NAME` | `single sign-on` | Label on the sign-in button. |

The OIDC variables are only read when `AUTH=oidc`. See [Sign-in and Sessions](../auth/sign-in-and-sessions.md).

## Settings stored in the database

One setting is changed in the app, not in the environment: **session lifetime**, the number of days an unused device stays signed in. It is stored in the `settings` table under the key `session_days`, defaults to 30 (`auth.DefaultSessionDays`), and `PUT /api/settings` accepts 1–365.

## How configuration reaches components

`cmd/nvr/main.go` reads `Config` once and hands the relevant pieces to each constructor:

- `store.Open(DatabaseURL, DataDir)`
- `auth.New` with the mode, public URL and OIDC settings
- `webdav.New(URL, Root, Username, Password)`
- `profile.Lookup(CameraProfile)`
- `ingest.New` with `Workers`, `ScanEvery`, `FullScanEvery` and `CameraTZ`
- `httpapi.Index` with `CameraTZ` and `IngestSecret`

`WebDAV.Configured()` (whether `WEBDAV_URL` is set) decides whether the index and ingest are built at all. See [System Architecture](overview.md).

## Deployment notes

- `docker-compose.yml` loads `.env` if it exists (`required: false`), then pins `DATA_DIR=/data` and `PORT=8080` inside the container and mounts the named volume `nvr-data` at `/data`.
- The Helm chart maps the same variables from its values and keeps secrets in a Secret. See [Deployment](../operations/deployment.md).
