---
type: api-reference
title: HTTP API
description: Route map and behavior of the webdav-nvr-player HTTP server, covering clip queries, streaming, thumbnails, the secret-protected ingest webhook, the loopback-only raw route, SPA serving, and how sign-in wraps the mux.
tags: [http, api, routes, streaming, webhook, spa]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-b12cba1b5936a1da8739d39c
    resource: repo://internal/auth/auth.go
  - id: openwiki-source-7aaec0c52e398dfd7e502e0b
    resource: repo://internal/httpapi/api.go
  - id: openwiki-source-cce8135ab65abb0ef1ea8c90
    resource: repo://internal/httpapi/httpapi.go
  - id: openwiki-source-a6772a3d2ce2f1717dba565f
    resource: repo://internal/httpapi/remux.go
  - id: openwiki-source-7afa473e4d0747f8346cea12
    resource: repo://internal/signedurl/signedurl.go
  - id: openwiki-source-e7f75bc9b8df37775fc82ec6
    resource: repo://web/src/lib/api.ts
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# HTTP API

The whole HTTP surface is one `http.ServeMux` built by `httpapi.New` in `internal/httpapi/httpapi.go`. The binary serves the JSON API, the video streams and the embedded single-page web UI on the same port. The browser client for these endpoints is `web/src/lib/api.ts`.

## How the handler is assembled

`httpapi.New(Options)` registers routes in this order:

1. **Always on:** `GET /healthz` (plain `ok`), `GET /api/version`, and `GET /api/source`, which runs a live connection check against the WebDAV server with a 15 s timeout. With no WebDAV server configured, `/api/source` returns `configured: false`.
2. **Clip index routes** (`api.routes`), registered only when `Options.Index` is non-nil, meaning a WebDAV server is configured. See [System Architecture](../architecture/overview.md).
3. **Sign-in routes** (`auth.Manager.Routes`) when `Options.Auth` is set.
4. A catch-all `/api/` handler, so unknown API paths never fall through to the SPA. It answers **503** ("no recordings server configured; set WEBDAV_URL") when there is no index, and **404** otherwise.
5. `/` serves the SPA.

When auth is configured, the finished mux is wrapped in `auth.Manager.Protect`. Every `/api/` request then needs a session, except a small allow-list (`openAPI` in `internal/auth/auth.go`): version, auth status, login, logout, and the ingest and rescan webhooks, which check their own secret. Paths outside `/api/` are not guarded: the web UI, `/healthz`, the OIDC redirects and `/internal/raw`. In `AUTH=none` mode nothing is guarded. See [Sign-in and Sessions](../auth/sign-in-and-sessions.md).

## Clip index routes

| Route | Purpose |
|---|---|
| `GET /api/status` | Index statistics (`store.Stats`), ingest scan status, database dialect, camera time zone, and whether the webhook is enabled. Feeds the status page. |
| `GET /api/cameras` | All cameras (always a JSON array, never `null`). |
| `GET /api/clips?from=&to=[&camera=]` | Clips and *unpaired* snapshots overlapping `[from, to)`, in Unix milliseconds. The window must be positive and at most **8 days**. |
| `GET /api/clips/{id}` | One clip plus its path, processing status and error. |
| `GET /api/clips/{id}/thumb` | The clip's JPEG thumbnail from `DATA_DIR`. |
| `GET`/`HEAD /api/clips/{id}/stream` | The video, either passed through or virtually remuxed. |
| `GET /api/days?from=&to=` | Which calendar days (`YYYY-MM-DD` in the camera time zone) have footage. The range can be at most 62 days. |
| `POST /api/ingest` | Upload webhook. Indexes one file. |
| `POST /api/rescan` | Starts a full crawl in the background. |
| `GET`/`HEAD /internal/raw/{token}` | Loopback-only signed passthrough for ffmpeg. |

### Clip JSON

`ClipJSON` is what the UI sees: `id`, `camera_id`, `start`/`end` (Unix ms), `codec` as a display name (via `media.CodecName`, for example "HEVC"), `width`, `height`, `size`, `file` (base name only), `thumb` (whether a thumbnail exists) and `thumb_kind` (`snapshot` or `frame`). Snapshots in `/api/clips` are only the ones not paired with a clip; the timeline draws them as dots.

`loadClip` treats a clip with status `deleted` the same as a missing row, so it returns **404** "no such clip". A clip that disappeared from the server therefore stops being playable as soon as a scan marks it deleted.

### Days

`/api/days` walks each day from `from` to `to` in the camera's `time.Location` and asks `store.HasClipsBetween` for that day's local-midnight-to-midnight range. Day boundaries therefore follow `CAMERA_TZ` and handle DST-length days correctly. The reply also carries `time_zone`.

## Streaming

`stream` (in `internal/httpapi/remux.go`) first asks `remuxFor` whether the clip is a fragmented MP4:

- **Not fragmented, or can't be remuxed:** `proxy` forwards the request to WebDAV as it is, including the `Range` headers. It copies back `Content-Length`, `Content-Range`, `Accept-Ranges`, `ETag` and `Last-Modified`, and sets `Content-Type: video/mp4` and `Cache-Control: private, max-age=3600` on 200/206 replies. An upstream 404 becomes "this recording is no longer on the server". Other upstream failures become **502**.
- **Fragmented:** the handler serves a virtual regular MP4: an in-memory `moov` header followed by the original media bytes, fetched with one range request per seek. `http.ServeContent` handles `Range`, `If-Range`, conditional requests and `HEAD`. The ETag is `"rx1-<fnv64(path,size,etag)>"`.

Remux headers are cached in memory (up to 2048 entries, oldest evicted first, keyed by path, size and ETag). Each one is built once even when requests arrive at the same time, and builds run detached from the request with a 30 s timeout, so a browser dropping its first request doesn't waste the work. Failed builds are not cached. The mechanics are in [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md).

### Thumbnails

`thumb` returns **404** "no thumbnail yet" until ingest has stored a `thumb_path`. Otherwise it serves the file with `Cache-Control: private, max-age=300`. The cache time is short because a frame-grab thumbnail can later be replaced by the camera's snapshot. See [Snapshot Pairing and Thumbnails](../ingest/snapshot-pairing-and-thumbnails.md).

## Loopback raw route and signed URLs

ffmpeg needs a URL to read a recording from when it grabs a fallback thumbnail frame. To keep WebDAV credentials out of process arguments, `cmd/nvr` gives ingest a `FrameURL` of the form `http://127.0.0.1:<port>/internal/raw/<token>`, where the token comes from `signedurl.Signer`.

- **Token format:** base64url(8-byte big-endian expiry ∥ path) + `.` + base64url(first 16 bytes of HMAC-SHA256). The key is 32 random bytes made fresh for each process, so tokens don't survive a restart. `cmd/nvr` signs them for 10 minutes.
- **Checks:** `raw` rejects any client whose remote address is not a loopback IP (**403** "local use only"), then rejects bad or expired tokens (**403**), then streams the file through `proxy`.

## Webhook endpoints

`POST /api/ingest` and `POST /api/rescan` both go through `authorized`:

- If `INGEST_SECRET` is empty, both return **403**: the webhook is off.
- The `X-NVR-Secret` header is compared in constant time. A missing or wrong header gets **401**.

`ingest` reads at most 64 KiB of JSON `{"path": "..."}` (relative to `WEBDAV_ROOT`) and calls `Ingester.Notify`:

| Outcome | Response |
|---|---|
| File not on the server (`webdav.ErrFileNotFound`) | 404 |
| Other source error | 502 |
| Profile classifies it as `Ignore` | 204 |
| Clip or snapshot indexed | 202 `{"kind": "clip"}` or `{"kind": "snapshot"}` |

`rescan` returns **409** if a scan is already running, otherwise **202** `{"status":"started"}`. Wiring an upload server to these endpoints is covered in [Upload Webhook (SFTPGo)](../integrations/upload-webhook.md), and the scans themselves in [Ingest Pipeline](../ingest/pipeline.md).

## SPA serving

`spa(ui)` serves the embedded build (`internal/webui`):

- Only `GET` and `HEAD` are allowed. Other methods get **405** with `Allow: GET, HEAD`.
- An existing non-directory file is served directly. Files under `assets/` get `Cache-Control: public, max-age=31536000, immutable` because Vite fingerprints their names.
- Any other path returns `index.html` with `Cache-Control: no-cache`, so client-side routes work and a new deploy is picked up right away.
- If the binary was built without the UI, it returns **503** and says to run `make web` and rebuild.

## Client side

`web/src/lib/api.ts` mirrors the JSON types (`Clip`, `Snapshot`, `SourceStatus`, `IndexStatus`, `AuthStatus`). Its `check` helper turns any **401** into a window-level `nvr:signed-out` event, which makes the app show the sign-in page, and turns `{error}` bodies into an `HttpError`. `canPlay(codec)` probes `HTMLVideoElement.canPlayType` once per codec, so the player can warn about undecodable HEVC instead of showing a black frame. See [Player State and Playback Engine](../player/app-state-and-engine.md).

## Tests

- `internal/httpapi/httpapi_test.go`: health, version, source status, the 503 without a server, SPA fallback and asset caching, the missing-UI message, and the SPA rejecting writes.
- `internal/httpapi/api_test.go`: clip and camera queries, thumbnails, range streaming, the raw route's loopback and signature checks, days, the webhook with and without a secret, status, fragmented-clip remux streaming, and sign-in guarding the API.
