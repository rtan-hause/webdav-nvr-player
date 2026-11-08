---
type: architecture
title: System Architecture
description: How the single webdav-nvr-player Go binary is put together, what each internal package does, how a camera upload becomes playable video in the browser, and how startup and shutdown work.
tags: [architecture, startup, components, data-flow, single-replica]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-16d7758f33081cf8e163d06b
    resource: repo://cmd/nvr/main.go
  - id: openwiki-source-e3e55ef2e47ca3fb3eb99e5a
    resource: repo://docs/design-decisions.md
  - id: openwiki-source-37047f321b6d15c60cf15860
    resource: repo://internal/ingest/ingest.go
  - id: openwiki-source-a0349a2ec956db579b76f398
    resource: repo://internal/webui/embed.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# System Architecture

webdav-nvr-player is one Go binary (`cmd/nvr`) with the React web UI embedded in it. Cameras upload clips and JPEG snapshots to an FTP/SFTP server (SFTPGo in the reference setup). The player reads those same files over **read-only WebDAV**, indexes them in its own database, and streams the original bytes to the browser without transcoding. Everything it derives (the index, thumbnails) lives in `DATA_DIR` or the database. Nothing is ever written next to the recordings.

## Components

| Package | Responsibility |
|---|---|
| `internal/config` | Parses environment variables into `Config`. See [Configuration](configuration.md). |
| `internal/store` | The SQLite or Postgres index (cameras, clips, snapshots), plus users, sessions and settings. See [Store and Database Schema](../data/store-and-schema.md). |
| `internal/auth` | Password, OIDC or no sign-in. Session cookies and the `Protect` middleware. See [Sign-in and Sessions](../auth/sign-in-and-sessions.md). |
| `internal/source/webdav` | Read-only WebDAV client: list, stat, fetch, range streaming, `ReaderAt`. See [WebDAV Source Client](../streaming/webdav-source.md). |
| `internal/profile` (+ `reolink`) | Vendor naming rules: classify a path, list the folders for a day, snapshot pairing policy. See [Camera Profiles](../ingest/camera-profiles.md). |
| `internal/ingest` | Scans, the webhook, and the dispatcher and worker pool that probe clips, pair snapshots and make thumbnails. See [Ingest Pipeline](../ingest/pipeline.md). |
| `internal/media` | Reads MP4 headers only (probe) and builds the virtual faststart remux of fragmented MP4. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md). |
| `internal/thumbs` | 480 px JPEG thumbnails from camera snapshots or ffmpeg frame grabs. See [Snapshot Pairing and Thumbnails](../ingest/snapshot-pairing-and-thumbnails.md). |
| `internal/signedurl` | Short-lived HMAC tokens so ffmpeg can read recordings through the app. |
| `internal/httpapi` | Routes: JSON API, streaming, webhooks, SPA. See [HTTP API](../api/http-api.md). |
| `internal/webui` | `go:embed all:dist` of the Vite build. `dist/README.md` is always present so the embed pattern matches before the UI is built. |
| `web/` | React SPA: timeline, clip list, two-video playback engine. See [Player State and Playback Engine](../player/app-state-and-engine.md). |

## From upload to playback

```
camera ──FTP/SFTP──▶ upload server (SFTPGo) ──post-upload hook──▶ POST /api/ingest
                            │                                         │
                         WebDAV (read-only)                     ingest.Notify / scans
                            │                                         │
                            ▼                                         ▼
     media.Probe (range reads) ◀── worker pool ◀── dispatcher ◀── store rows (work queue)
     snapshot pairing / thumbs ─────────────────────────────▶ DATA_DIR/thumbs
                            │
browser ◀── /api/clips, /api/clips/{id}/stream (proxy or virtual remux), /thumb
```

1. **Discovery.** A file enters the index through the webhook (`POST /api/ingest`), a recent scan (today's and yesterday's folders) or the full crawl. The profile classifies it and turns the camera-local time from the file name into UTC with `CAMERA_TZ`. A row is inserted with status `pending`.
2. **Processing.** The dispatcher picks due rows: snapshots to pair first, then clips to probe, then clips needing thumbnails. It hands them to `INGEST_WORKERS` goroutines. Probing reads MP4 boxes with small range requests to fill in the end time, codec and resolution.
3. **Thumbnails.** A clip waits up to the profile's `Pairing.Wait` for a snapshot. If none arrives, ffmpeg grabs a frame through the loopback `/internal/raw/{token}` route.
4. **Playback.** The browser asks `/api/clips` for a day window, and the timeline lays the clips out. `/api/clips/{id}/stream` either proxies the original file with range support, or (for fragmented MP4) serves a regular `moov` header built in memory with the original media bytes after it.

## Startup sequence (`cmd/nvr/main.go`)

`run()` does the following, in order:

1. Load `Config` and set up the `slog` text handler at `LOG_LEVEL`.
2. Create `DATA_DIR`. Run `checkIPv4PortFree`: on macOS another program can hold the port on IPv4 only, and then `localhost` in the browser silently reaches that program. Then listen on `PORT`.
3. Install a signal context for SIGINT and SIGTERM.
4. Open the store. It always opens, because sign-in needs it even with no WebDAV server.
5. Build `auth.Manager`. In `password` mode, `Bootstrap` creates the `admin` user on first start and logs the generated password once. In `none` mode it logs a warning. A goroutine prunes expired sessions every hour.
6. **Only if `WEBDAV_URL` is set:** look up the profile (an unknown id is fatal), build the WebDAV client, warn if ffmpeg is missing, and create a per-process `signedurl.Signer`, the `thumbs.Maker` and the `ingest.Ingester`. The ingester's `FrameURL` points at `127.0.0.1:<bound port>/internal/raw/<token>` with a 10-minute token. Fill in `httpapi.Index` and the `/api/source` checker.
7. Serve HTTP with `ReadHeaderTimeout: 10s`.
8. In a goroutine, run one source check (logging whether the connection works), then `ing.Run(ctx)`. A full crawl runs right away.

Without a WebDAV server, the process still serves sign-in, `/api/source` (`configured:false`) and the UI. Clip endpoints answer 503.

### Shutdown

On a signal, `srv.Shutdown` gets 15 s to drain HTTP, then `run` waits for the ingester goroutine to return. The ingester stops its scheduler, dispatcher and workers when the context is cancelled.

### `reset-password` subcommand

`nvr reset-password [user]` loads the same config, opens the same store, prints a new password for the user (default `admin`) and signs out all of that user's sessions. It refuses to run unless `AUTH=password`. It must run where the server's `DATA_DIR` or `DATABASE_URL` is reachable, for example with `docker exec`.

## Cross-cutting design constraints

- **One replica per database.** Clip and snapshot rows double as the work queue, and a single in-process dispatcher claims them. There is no locking between processes. The Helm chart therefore runs one replica with a `Recreate` strategy. See [Deployment](../operations/deployment.md).
- **One WebDAV source and one profile per deployment.** Configuration is environment-only.
- **No transcoding.** HEVC and H.264 pass through. The browser checks `canPlayType` and warns when it can't decode a codec.
- **Times are stored in UTC milliseconds.** The hour repeated when DST ends is not disambiguated, so clips from it overlap.
- **Credentials never appear in process arguments.** Probing uses Go range reads, and ffmpeg reads through the signed loopback route.

The reasoning behind these choices is recorded in `docs/design-decisions.md`.
