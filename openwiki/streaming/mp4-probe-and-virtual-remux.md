---
type: subsystem
title: MP4 Probing and Virtual Remux
description: How webdav-nvr-player reads duration, codec and resolution from MP4 headers using small range requests, and how it serves fragmented camera uploads as a regular faststart MP4 by building a moov in memory and splicing it in front of the original bytes.
tags: [media, mp4, fragmented-mp4, remux, streaming, probe]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-a6772a3d2ce2f1717dba565f
    resource: repo://internal/httpapi/remux.go
  - id: openwiki-source-0efa0f19ffd5cf26634375fc
    resource: repo://internal/media/mp4probe.go
  - id: openwiki-source-03556898a2d8207df157e85c
    resource: repo://internal/media/remux_test.go
  - id: openwiki-source-e02f409e9136b20b77209986
    resource: repo://internal/media/remux.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# MP4 Probing and Virtual Remux

`internal/media` does two jobs. Neither one transcodes or downloads whole files.

1. **`Probe`**, called during ingest, learns a clip's duration, codec and resolution from its MP4 boxes.
2. **`Defragment`**, called on first playback, turns a *fragmented* MP4 into a **virtual regular MP4**: a new header kept in memory, followed by a byte range of the original file.

Both read through an `io.ReaderAt` backed by WebDAV range requests (see [WebDAV Source Client](webdav-source.md)). Nothing is written back to the server. The only data kept is a small header in memory.

## Why this exists

Cameras record HEVC (and some H.264) with AAC audio. Transcoding 4K would need a GPU or a lot of CPU and a second copy of the footage, so the player streams the **original bytes**.

Reolink and most NVRs write **fragmented MP4**: an initial `moov` with `mvex` and no samples, then `moof`+`mdat` pairs of about 2 s each. That `moov` claims a duration of about one second and has no fragment index. Firefox trusts that duration and stops after the first fragment, and seeking doesn't work. The player can't rewrite files on the server (read-only WebDAV), so it builds the regular layout on the fly.

## Reading boxes cheaply

`windowReader` caches an **8 KiB window**. Walking top-level box headers then costs one range request per window rather than one per header, and a typical `moof` plus the next box header fit in one read. Both functions:

- check that the first box is a known top-level type (`ftyp`, `moov`, `mdat`, `free` and so on), and otherwise return `ErrNotMP4`;
- support 64-bit box sizes (`size == 1`) and boxes that run to the end of the file (`size == 0`);
- refuse a `moov` over 64 MiB or a `moof` over 4 MiB;
- tolerate a **truncated tail**: once the header is parsed, a cut-off final `mdat` or `moof` stops the walk and keeps the complete fragments. This handles files still uploading or cut short.

## `Probe(r, size) → Info`

`Info` holds `Duration`, `Codec` (the sample-entry fourcc of the first video track, such as `hvc1` or `avc1`), `Width`, `Height`, `Fragmented`, `Fragments` and `MoovAtEnd`.

- **Regular file:** parse `moov` (`mvhd` for the movie duration, `trak`s for the codec and size) and return. A `moov` placed after `mdat` is fine: the walk skips over `mdat` without reading it.
- **Fragmented file** (`moov` contains `mvex`): the header duration is meaningless, so Probe sums the sample durations of the video track across **every `moof`**, reading only the small `moof` boxes. It uses `trun` per-sample durations, or the `tfhd`/`trex` defaults. That costs about one small range request per fragment and never touches the media data. If no fragment produced ticks, Probe falls back to `mehd`.

The errors it can return feed ingest's retry logic (see [Ingest Pipeline](../ingest/pipeline.md)):

- `ErrNotMP4`: a permanent failure, no retry;
- "no moov box", "no video track", "file has no duration";
- a bad box size, usually meaning an incomplete upload, which is retried with backoff.

`CodecName` maps fourccs to display names: `hvc1`/`hev1` → HEVC, `avc1`/`avc3` → H.264, `av01` → AV1, `vp09` → VP9. The API returns this name, and the browser uses it to check `canPlayType`.

## `Defragment(r, size) → *Remux`

```go
type Remux struct {
    Head      []byte // ftyp + rebuilt moov + mdat header
    DataStart int64  // first media byte in the source
    DataEnd   int64  // one past the last media byte
    Duration  time.Duration
}
// virtual file = Head ++ source[DataStart:DataEnd]
```

Steps:

1. Walk the file. Parse the fragmented `moov` (`parseRemuxMoov`), keeping `mvhd`, each track's headers, and other children such as `udta`/`meta` as whole boxes. A file **without `mvex` returns `ErrNotFragmented`**, which tells the caller to serve it as is.
2. For each `moof` (`readMoof`), append every sample's size, duration, flags (sync or not) and composition offset, and the source offset of each `trun` run (a "chunk"). A sample that runs past the end of the file marks the file as cut, and the walk stops there.
3. Drop tracks with no samples. At least one video track must remain.
4. `DataStart`/`DataEnd` span every chunk of every track. The fragment headers left inside that range become unused padding inside the new `mdat`.
5. Rebuild each `trak` with full sample tables (`stts`, `ctts`, `stss`, `stsz`, `stsc`, `stco`/`co64`). Chunk offsets are **virtual**: `len(Head) + (sourceOffset − DataStart)`.
6. Patch the durations in `mvhd`, `tkhd` and `mdhd`. `fixEdits` stretches the last edit-list entry to the track's real length, because fragmented writers usually put 0 there, which only means "everything" while the file is fragmented.
7. Build the head in order: a fresh `ftyp` (`isom`), the `moov`, then the `mdat` header. Offsets switch to `co64` and the `mdat` header to a 64-bit size when the result would exceed 4 GiB. The `moov` is built twice: once to measure its size (`stco` entries have a fixed width, so the size doesn't depend on `base`), and again with the real `base`.

The output is the same as `ffmpeg -c copy -movflags +faststart`: `moov` comes right after `ftyp`, and `remux_test.go` checks it **packet for packet** against ffmpeg's own faststart remux using `framemd5`. The head is a few KB per clip.

## Serving it (`internal/httpapi/remux.go`)

`GET`/`HEAD /api/clips/{id}/stream` works as follows (also covered in [HTTP API](../api/http-api.md)):

1. `remuxFor(clip)` looks the clip up in the **remux cache**. The key is `path + size + etag`, so a re-uploaded file gets a new entry.
   - The first request builds the result. Concurrent requests for the same clip wait on a `done` channel instead of building it again.
   - The build runs with `context.WithoutCancel` and a **30 s timeout**. Browsers often drop their first request to re-request a range, and that shouldn't waste the work.
   - The cache holds **2048 entries**, oldest evicted first. Failed builds are removed so the next request retries.
   - An `errReader` wrapper records network errors from the source, so a WebDAV failure isn't mistaken for "can't remux, pass it through". A not-found error becomes a 404.
2. **No remux** (`ErrNotFragmented`, or a parse error, which is logged): `proxy` passes the request through to WebDAV with the client's `Range` header.
3. **Remux:** `http.ServeContent` serves a `remuxFile` (`io.ReadSeeker`) with `ETag: "rx1-<fnv64>"` and `Last-Modified` taken from the file's mtime. That gives correct `Range`, `If-Range`, conditional GET and `HEAD` handling for free.
   - Reads below `len(Head)` come from memory.
   - Reads beyond it open **one streaming range request** `bytes=DataStart+(off−len(Head))-(DataEnd−1)` and keep reading from it while the reads stay sequential. A seek to another offset closes it and opens a new one.
   - An upstream answer that isn't 206, or a `Content-Range` that doesn't start at the requested offset, is an error. A body that ends early returns `io.ErrUnexpectedEOF`.

## Invariants and limits

- Original media bytes are never changed or copied to disk. The only state is the cached `Head`.
- The remux depends on the source file not changing. The cache key includes size and ETag, so a changed file produces a new head.
- A file that keeps growing (still uploading) is remuxed with the complete fragments only. Ingest's settle window and the upload webhook normally prevent such files from being indexed (see [Ingest Pipeline](../ingest/pipeline.md)).
- Regular (non-fragmented) MP4s, including ones with `moov` at the end, are passed through unchanged.

## Tests

`internal/media/mp4probe_test.go` and `remux_test.go` use ffmpeg (they skip if it, or the encoder they need, is missing) to encode H.264 and HEVC clips with the `moov` at the end, faststart, and fragmented layouts, plus a synthetic Reolink layout. They check:

- probe results;
- rejection of non-MP4 input;
- truncated uploads;
- that `Defragment` matches ffmpeg's faststart remux packet for packet;
- that regular files return `ErrNotFragmented`.

`internal/httpapi/api_test.go` `TestStreamRemuxesFragmentedClip` covers the HTTP path end to end.
