---
type: subsystem
title: Ingest Pipeline
description: How webdav-nvr-player keeps its index in step with the WebDAV server, covering the upload webhook, recent and full scans, the settle window, deletion reconciliation, and the dispatcher and worker pool that use database rows as the work queue, with retry backoff and scan status.
tags: [ingest, scanning, webhook, work-queue, dispatcher, retries]
sources:
  - id: openwiki-source-37047f321b6d15c60cf15860
    resource: repo://internal/ingest/ingest.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# Ingest Pipeline

`internal/ingest` keeps the index in [the store](../data/store-and-schema.md) in step with the files on the WebDAV server. It has two halves:

- **Finding files** inserts new clip and snapshot rows (status `pending`) and marks vanished files `deleted`.
- **Background work** runs a dispatcher that reads due rows and hands them to a worker pool. The workers probe clips, pair snapshots with clips, and make thumbnails.

There is no separate jobs table: each row's state columns *are* the queue.

`cmd/nvr` creates the `Ingester` with `ingest.New(Options)` and runs `Run(ctx)` in a goroutine after the HTTP server starts. See [System Architecture](../architecture/overview.md).

## Options and defaults

| Option | Default | Source |
|---|---|---|
| `Workers` | 2 | `INGEST_WORKERS` |
| `RecentEvery` | 5 min | `SCAN_INTERVAL` |
| `FullEvery` | 24 h | `FULL_SCAN_INTERVAL` |
| `Settle` | 60 s (negative disables) | scans skip files modified more recently than this |
| `MaxAttempts` | 5 | give up on a file after this many errors |
| `Location` | UTC | `CAMERA_TZ`, for turning file-name times into instants and for "today" |
| `FrameURL` | — | builds the loopback signed URL ffmpeg reads from |

`Now` and `Log` can be injected, which is how the tests control time.

## Three ways in

### 1. Webhook: `Notify(relPath)`

`POST /api/ingest` (see [HTTP API](../api/http-api.md) and [Upload Webhook (SFTPGo)](../integrations/upload-webhook.md)) calls `Notify`, which:

1. cleans the path (`path.Clean("/"+p)[1:]`, so `..` can't climb above the root);
2. classifies it with the profile, returning immediately with `Ignore` for files that aren't recordings;
3. **stats that single file** on WebDAV rather than listing its folder, which can hold thousands of files on a busy day;
4. inserts it, then pokes the dispatcher so work starts immediately.

The webhook does **not** apply the settle window, because the upload server only fires it once the upload has finished.

### 2. Recent scan: `ScanRecent`

Every `RecentEvery`, `ScanRecent` lists the folders `Profile.DateDirs` returns for **yesterday and today in the camera time zone**. A folder that doesn't exist yet (`webdav.ErrNotFound`) is skipped. Each folder goes through `scanDir(dir, reconcile=true)`, so files that disappeared from it are marked deleted.

### 3. Full crawl: `ScanAll`

The full crawl runs **at startup** and then every `FullEvery`, or on demand through `POST /api/rescan`. It:

1. snapshots every live path (`AllLivePaths`);
2. walks the tree from the root, depth-first, visiting sibling folders in **reverse name order** (date folders sort by name, so this is newest first) down to a maximum depth of 8, and indexes the files it finds;
3. marks every path that was live before the walk but not seen during it as deleted.

If the walk fails partway, step 3 doesn't run, so a WebDAV outage never mass-deletes the index.

### Settling and inserting

`scanDir` lists one folder and keeps only files whose `ModTime` is older than `now - Settle`, or unknown. Files still uploading are left for the next scan. `add` classifies each entry, resolves the camera id (cached in memory after `EnsureCamera`), converts the file-name time with `Media.In(Location)`, and batch-inserts snapshots before clips. Inserts are idempotent: an existing live path is a no-op, and a path previously marked `deleted` is revived as a fresh `pending` row.

### Deletion

`markDeleted` calls `Store.MarkDeleted` and removes the thumbnail files it returns from `DATA_DIR`. Deletions are expected, because retention on the server is not the player's job.

## Scan scheduling and status

- `scanMu` allows **one scan at a time**. `runScan` uses `TryLock`: a scheduled scan that finds another running is skipped, since the running scan covers it. `Rescan` checks the lock, then starts a full crawl in a goroutine with a context that ignores request cancellation. It returns `false` (HTTP 409) if a scan is already running.
- `Status()` reports the scan in progress (`full` or `recent`), the times of the last full and recent scans, the last error, how many files the last scan added, and `Working`, the number of jobs in flight. The status page shows this through `/api/status`.
- Every scan ends by poking the dispatcher.

## Dispatcher and workers

`Run` starts `Workers` goroutines reading from an unbuffered `jobs` channel, the scheduler goroutine, and the dispatcher loop in the calling goroutine.

`dispatchDue` reads up to 32 rows of each kind, in priority order:

1. **Snapshots to pair** (`SnapshotsToPair`): cheap, and they let waiting clips pair.
2. **Clips to probe** (`ClipsToProbe`): `pending` and due.
3. **Clips needing a thumbnail** (`ClipsNeedingThumb`).

Every job has a key (`s<id>` or `c<id>`). An `inflight` set prevents the same row being handed out twice while a worker still holds it. Because clip probe and thumbnail jobs share the `c<id>` key, they never run at the same time for one clip. When a worker finishes, it removes its key and pokes the dispatcher.

When nothing was dispatched, the dispatcher sleeps until the soonest of:

- a poke (new file, finished job, finished scan);
- `Store.NextWakeMS` + 50 ms (the next pairing deadline or retry time);
- 30 s at most.

On a database error it logs and keeps looping.

### Probing a clip

`probeClip` reads the MP4 headers through `Source.ReaderAt` (range requests) with a 60 s timeout. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md).

- **Success:** sets `end_ms = start_ms + duration`, plus codec, width and height, and status `ready`. It then tries to pair the clip with a snapshot under `pairMu`. The pairing deadline is the **upload time (`mod_ms`) + `Pairing.Wait`**, so a backfill of old files gets a frame immediately instead of waiting.
- **File gone** (`webdav.ErrFileNotFound`): the clip is marked deleted.
- **Other errors:** `SetClipFailed` increments `attempts` and schedules a retry with **exponential backoff**: 30 s × 2^attempts, so 30 s, 1 m, 2 m and so on, capped at 32 min. The failure becomes permanent (status `failed`) when the file is not an MP4 (`media.ErrNotMP4`) or after `MaxAttempts` tries.

### Pairing and thumbnails

`pairSnapshot` and `makeThumb` are covered in [Snapshot Pairing and Thumbnails](snapshot-pairing-and-thumbnails.md). Thumbnail failures use the same backoff and attempt limit, but only change `thumb_state`. The clip stays playable.

## Concurrency invariants

- `pairMu` serializes every pairing decision (`PairClip`, `PairSnapshot`), so two workers can never link the same snapshot to two clips.
- `scanMu` serializes scans.
- The `inflight` set stops the same row being worked on twice in this process. Nothing prevents another process from doing so, hence **one replica per database**.

## Tests

`internal/ingest/ingest_test.go` drives the ingester against a fake source and a real SQLite store:

- `TestFullScanIndexesPairsAndMakesThumbnails`: end-to-end crawl, probe, pairing and thumbnail creation.
- `TestWebhookAndLateSnapshot`: a clip arrives by webhook and its snapshot arrives later and upgrades the thumbnail.
- `TestRecentScanSkipsFilesStillUploading`: the settle window.
