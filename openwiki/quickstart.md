---
type: quickstart
title: Quickstart
description: Entry point to the webdav-nvr-player wiki, covering what the app does, how to run and develop it locally, a map of the codebase, and which page to read for common tasks.
tags: [quickstart, overview, getting-started, navigation]
sources:
  - id: openwiki-source-2dbef707347fbcbab7d5e69d
    resource: repo://charts/webdav-nvr-player/templates/deployment.yaml
  - id: openwiki-source-16d7758f33081cf8e163d06b
    resource: repo://cmd/nvr/main.go
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-7aaec0c52e398dfd7e502e0b
    resource: repo://internal/httpapi/api.go
  - id: openwiki-source-ce476511a169993c1bb994cb
    resource: repo://internal/profile/profile.go
  - id: openwiki-source-fe6ed484415e36d1f640a8c3
    resource: repo://internal/store/media.go
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
  - id: openwiki-source-3a44815832a872f4778f822b
    resource: repo://SECURITY.md
generated: { by: "claude-code", at: "2026-11-08T06:01:44.565Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# Quickstart

**webdav-nvr-player** is a web player for security-camera recordings stored on a WebDAV server. Cameras upload clips and snapshots over FTP/SFTP (to SFTPGo, for example). The player reads them back over **read-only WebDAV**, indexes them in SQLite (or Postgres), and plays them on a scrubbable, zoomable day timeline with one lane per camera. It passes HEVC and H.264 through without transcoding, serves fragmented MP4 uploads as regular MP4 on the fly, takes thumbnails from the camera's snapshots (with an ffmpeg fallback), and indexes new uploads as soon as a webhook reports them. It is one Go binary with the React UI embedded.

## Run it

**Docker Compose**

```sh
cp .env.example .env    # set WEBDAV_URL, WEBDAV_USERNAME, WEBDAV_PASSWORD, CAMERA_TZ
docker compose up -d
docker compose logs nvr # first start prints the admin password
# open http://localhost:8080
```

**From source** (Go 1.27+, Node 26+, ffmpeg):

```sh
make run     # first time: creates .env from .env.example and stops so you can fill it in
make test    # Go tests (race) + frontend typecheck and Vitest
make lint    # go vet, gofmt, eslint
make dev     # Go server only; run `npm run dev` in web/ for Vite hot reload (proxies /api)
make reset-password   # lost admin password
make e2e     # build the image and run the Playwright tests against it (needs Docker)
```

With no `WEBDAV_URL`, the server still starts and shows the status page (`#/status`), which explains what's missing.

## Codebase map

```
cmd/nvr/                 entrypoint: wiring, startup, shutdown, `nvr reset-password`
internal/config          env vars → Config
internal/store           SQLite/Postgres index + users/sessions/settings; rows are the work queue
internal/source/webdav   read-only WebDAV client (PROPFIND, ranges, streaming)
internal/profile[/reolink]  vendor file-naming rules (the extension point)
internal/ingest          webhook, scans, dispatcher + workers (probe, pair, thumbnail)
internal/media           MP4 box reading, header probe, virtual faststart remux of fragmented MP4
internal/thumbs          480 px JPEG thumbnails (Go resize or ffmpeg frame)
internal/signedurl       HMAC tokens for ffmpeg's loopback reads
internal/auth            password / OIDC / none; sessions; API guard
internal/httpapi         routes: API, stream, webhook, SPA
internal/httpx           JSON and {"error"} response helpers
internal/webui           go:embed of the built UI
web/                     React + Vite + Tailwind player (Vitest unit tests)
e2e/                     Playwright tests against the Docker image
charts/webdav-nvr-player Helm chart (released separately)
openwiki/                this wiki
SECURITY.md              how to report a vulnerability privately
```

Start with [System Architecture](architecture/overview.md) for how these fit together.

## Find the right page

| I want to… | Read |
|---|---|
| Understand the overall design and data flow | [System Architecture](architecture/overview.md) |
| Look up an environment variable or default | [Configuration](architecture/configuration.md) |
| Support a new camera brand | [Camera Profiles](ingest/camera-profiles.md) |
| Debug why clips aren't appearing or are slow to appear | [Ingest Pipeline](ingest/pipeline.md), [Upload Webhook (SFTPGo)](integrations/upload-webhook.md) |
| Wire an upload server's webhook to the player | [Upload Webhook (SFTPGo)](integrations/upload-webhook.md) |
| Debug missing or wrong thumbnails | [Snapshot Pairing and Thumbnails](ingest/snapshot-pairing-and-thumbnails.md) |
| Change the schema or a query | [Store and Database Schema](data/store-and-schema.md) |
| Fix playback or seeking in a browser | [MP4 Probing and Virtual Remux](streaming/mp4-probe-and-virtual-remux.md), [Player State and Playback Engine](player/app-state-and-engine.md) |
| Troubleshoot WebDAV connection or listing issues | [WebDAV Source Client](streaming/webdav-source.md) |
| Add or change an API endpoint | [HTTP API](api/http-api.md) |
| Change sign-in, sessions or OIDC | [Sign-in and Sessions](auth/sign-in-and-sessions.md) |
| Change the timeline, controls, layout or shortcuts | [Timeline and Player UI](player/timeline-and-ui.md) |
| Deploy with Docker or Helm | [Deployment](operations/deployment.md) |
| Cut a release or understand CI | [CI and Releases](operations/release-and-ci.md) |
| Run or write tests | [Testing Guide](testing/testing-guide.md) |

## Things that surprise newcomers

- **Run one replica per database.** The work queue is the clip and snapshot rows, read by one in-process dispatcher. This applies even with Postgres.
- **Times are UTC in storage, but camera-local in file names and in the UI.** `CAMERA_TZ` must match the cameras' clock setting. The hour that repeats when DST ends overlaps on the timeline.
- **Paths are always relative to `WEBDAV_ROOT`**: in the database, in the webhook body, and in the client.
- **The stream endpoint may serve bytes that don't exist on the server.** For fragmented MP4 it splices an in-memory `moov` in front of the original media range.
- **ffmpeg never sees credentials.** It reads through `127.0.0.1/internal/raw/<signed token>`.
- **Files deleted by retention are expected.** Scans mark them deleted and remove their thumbnails.

The [System Architecture](architecture/overview.md) page lists these constraints with the code that enforces them.
