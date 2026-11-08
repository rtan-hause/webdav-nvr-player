---
type: subsystem
title: WebDAV Source Client
description: The minimal read-only WebDAV client webdav-nvr-player uses to reach recordings, covering PROPFIND listing and stat, whole-file fetch for snapshots, the range-request ReaderAt used for probing and remuxing, pass-through streaming, error types, and how credentials are handled.
tags: [webdav, source, http, range-requests, propfind]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-37047f321b6d15c60cf15860
    resource: repo://internal/ingest/ingest.go
  - id: openwiki-source-b736e026c56ebf14ecd78120
    resource: repo://internal/source/webdav/integration_test.go
  - id: openwiki-source-f8675571380b944c920917c9
    resource: repo://internal/source/webdav/webdav.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# WebDAV Source Client

`internal/source/webdav` is the player's only connection to the recordings. It is a small, **read-only** client: it lists folders, stats files, and reads files, and it never writes. The WebDAV account therefore only needs list and download permissions. The reference server is SFTPGo's WebDAV endpoint.

`cmd/nvr` creates one `*webdav.Client` from `WEBDAV_URL`, `WEBDAV_ROOT`, `WEBDAV_USERNAME` and `WEBDAV_PASSWORD` (see [Configuration](../architecture/configuration.md)). It is shared by ingest, which uses `List`, `Stat`, `ReaderAt` and `Fetch`, and by the HTTP API, which uses `Stream` and `ReaderAt` (see [Ingest Pipeline](../ingest/pipeline.md) and [HTTP API](../api/http-api.md)). Each consumer depends only on a small interface (`ingest.Source`, `httpapi.Streamer`), so tests substitute fakes.

## Paths and URLs

- `New(rawURL, root, user, pass, hc)` joins the share URL path and `root` into a base URL that always ends in `/`. **Every path given to the client is relative to that root**, `/`-separated, with no leading slash. These are the same paths stored in the index and sent to the webhook.
- `fileURL` appends the relative path. `url.URL.String()` then escapes spaces and reserved characters segment by segment, which matters because camera names contain spaces ("Front of House").
- `URL()` returns the base URL, which never contains credentials. `WEBDAV_URL` itself is rejected at startup if it embeds user info.
- Credentials are sent as HTTP Basic auth on every request. They are never written into URLs, logs or process arguments.
- The default `http.Client` has a 30 s timeout.

## Operations

| Method | Request | Behavior |
|---|---|---|
| `List(ctx, dir)` | `PROPFIND Depth: 1` | Direct children of `dir` (`""` for the root), folders first, then by name. The folder itself is skipped. |
| `Stat(ctx, relPath)` | `PROPFIND Depth: 0` | One file's size, mtime and ETag. Used by the webhook so it doesn't have to list a whole day's folder. |
| `Fetch(ctx, relPath, max)` | `GET` | Downloads a whole small file (snapshot JPEGs, capped at 32 MiB by the caller). Larger files are refused. |
| `ReaderAt(ctx, relPath)` | `GET` + `Range` per call | An `io.ReaderAt` for MP4 probing and remux building (see [MP4 Probing and Virtual Remux](mp4-probe-and-virtual-remux.md)). |
| `Stream(ctx, method, relPath, hdr)` | `GET` / `HEAD` | Pass-through for playback. Forwards `Range`, `If-Range`, `If-None-Match` and `If-Modified-Since`. |

### PROPFIND parsing

The request asks for `resourcetype`, `getcontentlength`, `getlastmodified` and `getetag`. The response must be **207 Multi-Status**. Any other status produces an error that asks "is this a WebDAV URL?" and quotes the start of the body. The XML is capped at 64 MiB. For each `<response>`:

- the `href` is resolved against the listed URL, decoded, and made relative to the root (entries outside the root are dropped);
- properties are merged from every propstat with a `200` status, because servers split found and missing properties into separate propstats;
- an entry is a directory if it has `<collection/>` or its href ends in `/`;
- quotes are removed from ETags, and `Last-Modified` is parsed with `http.ParseTime`.

Ingest's settle window and deletion reconciliation depend on the size, mtime and ETag from these listings.

### Range reads

`rangeReader.ReadAt(p, off)` sends `Range: bytes=off-(off+len(p)-1)`:

- **206**: reads the body fully. A short body is reported as `io.EOF` (the end of the file).
- **416**: `io.EOF`.
- **200**: an error. The server ignored the range, and **range support is required**, otherwise every probe would download whole clips.
- Anything else is mapped through `statusErr`.

`media`'s window reader turns many small header reads into a few 8 KiB range requests.

### Streaming

`Stream` copies the client but sets **no timeout**, because a video stream can outlast 30 s. The request context bounds it instead. Server error statuses (404, 416 and so on) come back as responses rather than Go errors, so the API can pass them through. Only transport failures are errors.

## Errors

| Error | Meaning | Used by |
|---|---|---|
| `ErrUnauthorized` | 401/403. Wrong credentials or permissions. | Shown on the status page and in scan errors. |
| `ErrNotFound` | 404 on a **folder** PROPFIND. | `ScanRecent` skips day folders the camera hasn't created yet. |
| `ErrFileNotFound` | 404 on a **file** (Stat, Fetch, ReaderAt, or GET). | Ingest marks the row deleted. The webhook answers 404. The stream answers "no longer on the server". |

## Connection check

`cmd/nvr`'s `sourceCheck` lists the root (15 s timeout) for `/api/source` and the startup log. It reports up to 50 top-level folder names and the number of files directly in the root. The [status page](../player/timeline-and-ui.md) uses it to confirm the URL, credentials and root are right.

## Tests

- `webdav_test.go`: listing parse (folders first, decoding, merging propstats), listing with a root folder, and error mapping (401/403/404/non-207).
- `range_test.go`: `ReaderAt` (including a missing file), `Stat`, `Fetch` limits, and `Stream` forwarding `Range`.
- `integration_test.go`: `TestLiveServer` runs **only** when `WEBDAV_TEST_URL` (plus `_USERNAME`, `_PASSWORD`, `_ROOT`) is set. It walks the first folder at each level of a real server and checks names, sizes and mtimes. It is read-only.
