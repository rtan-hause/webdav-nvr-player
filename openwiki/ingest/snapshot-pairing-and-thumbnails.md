---
type: subsystem
title: Snapshot Pairing and Thumbnails
description: How webdav-nvr-player matches camera JPEG snapshots to clips by time window, waits for late snapshots, falls back to an ffmpeg frame grab over a signed loopback URL, and stores and serves 480 px thumbnails.
tags: [thumbnails, pairing, snapshots, ffmpeg, ingest]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-16d7758f33081cf8e163d06b
    resource: repo://cmd/nvr/main.go
  - id: openwiki-source-37047f321b6d15c60cf15860
    resource: repo://internal/ingest/ingest.go
  - id: openwiki-source-ce476511a169993c1bb994cb
    resource: repo://internal/profile/profile.go
  - id: openwiki-source-fe6ed484415e36d1f640a8c3
    resource: repo://internal/store/media.go
  - id: openwiki-source-c50a8c71315e4a6d1b31e98b
    resource: repo://internal/thumbs/thumbs.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Snapshot Pairing and Thumbnails

Every clip in the list and on the timeline has a thumbnail. Thumbnails come from two places:

1. **The camera's own JPEG snapshot**, which most cameras upload next to each clip. This is preferred: it's free and taken by the camera.
2. **A frame grabbed from the video with ffmpeg**, used when no snapshot arrives in time or the snapshot is unusable.

The logic is split across three packages. `internal/store` makes the pairing decisions in transactions, `internal/ingest` decides when to run them, and `internal/thumbs` renders the image files. See [Ingest Pipeline](pipeline.md) for how jobs are scheduled.

## Why pairing is by time, not name

A snapshot's file name is often a second or more off from its clip's, so names can't be matched exactly. Instead, each profile's `Pairing()` defines a window (see [Camera Profiles](camera-profiles.md)). A snapshot of the **same camera** may pair with a clip when:

```
clip.start − Before  ≤  snapshot.ts  ≤  clip.end
```

The candidate **closest to the clip's start** wins, with ties broken by id. Each snapshot pairs with at most one clip, and each clip with at most one snapshot. For Reolink, `Before` is 2 s and `Wait` is 60 s.

Pairing can only happen once a clip is `ready`, because `end_ms` is known only after probing. Clips and snapshots can arrive in any order, so pairing is attempted from both sides.

- **Clip side: `Store.PairClip`.** It runs after a probe succeeds, and again when the clip's wait deadline passes. It looks for the closest unpaired `ready` snapshot in the window. If none is found, the clip goes to `waiting` with `pair_deadline_ms` set, or to `frame` if the deadline has already passed. A clip that is already `done` with a frame thumbnail is left alone.
- **Snapshot side: `Store.PairSnapshot`.** It runs for each `pending` snapshot, ahead of the other jobs. It marks the snapshot `ready`, then looks for the closest `ready` clip of the same camera that has no snapshot yet and whose window contains the snapshot.
- **Linking** sets `snapshots.clip_id` and `clips.snapshot_id`, moves the clip to `thumb_state = snapshot`, and resets its attempts. A clip that already had a **frame** thumbnail is upgraded this way when a snapshot turns up later.

Both pairing methods run inside a database transaction *and* under ingest's `pairMu`, so concurrent workers can't race for the same snapshot.

## The wait deadline

After a successful probe, ingest sets the deadline to **upload time (`mod_ms`) + `Pairing.Wait`**. It measures from the upload time, not from when the clip was indexed, so:

- a live clip waits up to `Wait` (60 s for Reolink) for its snapshot to arrive;
- a backfill of old files (found by the startup crawl) has deadlines long in the past and goes straight to a frame grab if no snapshot is already indexed.

`Store.NextWakeMS` includes the earliest pending deadline, so the dispatcher wakes up exactly when a clip stops waiting. `makeThumb` then makes **one last pairing attempt** for a `waiting` clip, in case the snapshot arrived in the meantime, before falling back to a frame.

## Thumbnail lifecycle

`makeThumb(clipID)` runs for clips in `snapshot` or `frame` state, or `waiting` past their deadline. Each attempt has a 90 s timeout.

| State | What happens |
|---|---|
| `snapshot` | Fetch the snapshot JPEG from WebDAV (`Fetch`, capped at 32 MiB) and resize it in Go with `thumbs.FromJPEG`. |
| `frame` | Run ffmpeg on `FrameURL(clip.path)`, seeking to 10% of the clip's duration, using `thumbs.FromVideo`. |

Failure handling:

- **Snapshot row missing, file gone from WebDAV, or the JPEG can't be decoded:** `UnpairForFrame` unlinks it and sets the clip to `frame`. A gone file is also marked deleted. The next round grabs a frame.
- **Network error while fetching the snapshot, or an ffmpeg failure:** `SetThumbFailed` increments attempts with the same exponential backoff as probing. After `MaxAttempts` (5) the clip's `thumb_state` becomes `failed`. The clip itself stays `ready` and playable.
- **Success:** `SetThumb` records `thumb_state=done`, `thumb_kind` (`snapshot` or `frame`) and `thumb_path`.

## Rendering (`internal/thumbs`)

- **Size and format:** JPEGs 480 px wide (`thumbs.Width`), height following the aspect ratio. A snapshot narrower than 480 px is never upscaled.
- **Snapshots:** decoded with Go's `image/jpeg`, scaled with `x/image/draw` BiLinear, and re-encoded at quality 78.
- **Frames:** `ffmpeg -ss <seek> -i <url> -frames:v 1 -vf scale=480:-2 -q:v 4 -f image2 -c:v mjpeg pipe:1`. Seeking before `-i` makes ffmpeg read only what it needs over HTTP range requests. If ffmpeg isn't installed, the error says so. Startup also warns about it.
- **Storage:** `DATA_DIR/thumbs/<id/1000>/<id>.jpg`. Folders hold at most 1000 files. Writes go to a temp file that is then renamed, so readers never see a partial image.
- **Path safety:** `Maker.Abs` refuses any stored path that doesn't resolve under `thumbs/`.
- **Cleanup:** when a clip is marked deleted, its thumbnail file is removed. A thumbnail made from a snapshot that is later deleted is kept, because the clip still needs it.

## Keeping credentials out of ffmpeg

ffmpeg's command line is visible in the process list, so it must never contain WebDAV credentials. `cmd/nvr` builds `FrameURL` as `http://127.0.0.1:<port>/internal/raw/<token>`. The token is a `signedurl` HMAC that names the path and expires after 10 minutes. The raw route only serves loopback clients and proxies the file, range requests included, from WebDAV using the app's own credentials. See [HTTP API](../api/http-api.md).

## Serving

`GET /api/clips/{id}/thumb` returns 404 until a thumbnail exists. Afterwards it serves the file with `Cache-Control: private, max-age=300`, a short cache because a frame thumbnail can be upgraded to a snapshot. The clip JSON reports `thumb` and `thumb_kind`, so the UI knows whether to request it. Snapshots that never paired show up in `/api/clips` as dots on the timeline.

## Tests

- `internal/store/store_test.go`: `TestPairing` (windows, closest wins, both directions, upgrade) and `TestThumbLifecycleAndDeletion`.
- `internal/ingest/ingest_test.go`: `TestWebhookAndLateSnapshot` (a frame first, then upgraded when the snapshot arrives) and the end-to-end full-scan test.
- `internal/thumbs/thumbs_test.go`: resizing and path safety.
