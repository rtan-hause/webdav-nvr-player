---
type: extension-point
title: Camera Profiles
description: The profile.Profile interface that teaches webdav-nvr-player a camera vendor's file names, folder layout and snapshot timing, covering the Reolink reference profile, how profiles are registered and selected, and how to add a new vendor.
tags: [profiles, reolink, extension, ingest, naming, time-zone]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-16d7758f33081cf8e163d06b
    resource: repo://cmd/nvr/main.go
  - id: openwiki-source-ce476511a169993c1bb994cb
    resource: repo://internal/profile/profile.go
  - id: openwiki-source-2a1595c941673620c4de160c
    resource: repo://internal/profile/reolink/reolink_test.go
  - id: openwiki-source-498def6c2ef06e5e7dab7b0e
    resource: repo://internal/profile/reolink/reolink.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Camera Profiles

A **profile** holds everything vendor-specific about how a camera names and lays out its uploads. All other ingest code is vendor-neutral and only calls the four methods of `profile.Profile` in `internal/profile/profile.go`. One profile is active per deployment, chosen by `CAMERA_PROFILE` (default `reolink`).

## The interface

```go
type Profile interface {
    ID() string   // config value, e.g. "reolink"
    Name() string // display name, e.g. "Reolink (FTP upload)"
    Classify(relPath string) Media
    DateDirs(year int, month time.Month, day int) []string
    Pairing() Pairing
}
```

### `Classify(relPath) Media`

`Classify` decides what a file is from its path alone. The path is relative to `WEBDAV_ROOT` and `/`-separated. It returns a `Media`:

- `Kind`: `Clip`, `Snapshot` or `Ignore` (the zero value). Upload test files, temp files, sidecars and unparseable names must return `Ignore`. Ignored files are never inserted, and the webhook answers them with 204.
- `Camera`: display name. Together with `Channel` it identifies a row in `cameras`, created on first sight.
- `Channel`: the vendor's channel id, or `""`.
- `Local`: the camera's **wall-clock** time from the file name. Only the date and clock fields matter. The caller places that time in the camera zone with `Media.In(loc)`, using `CAMERA_TZ`. The resulting instant is stored as UTC milliseconds (`start_ms` for clips, `ts_ms` for snapshots).

Classification must be pure and cheap, because it runs on every entry of every directory listing and on every webhook call.

### `DateDirs(y, m, d) []string`

`DateDirs` returns the folders (relative to the root) that hold files for one camera-local calendar day. The recent scan calls it for today and yesterday in `CAMERA_TZ`, so it only lists a couple of folders instead of crawling the whole tree. A folder that doesn't exist yet is skipped. The daily full crawl does not use `DateDirs`: it walks everything. See [Ingest Pipeline](pipeline.md).

### `Pairing() Pairing`

`Pairing` sets the snapshot-to-clip pairing policy:

- `Before`: a snapshot of the same camera pairs with a clip if it was taken between `Before` ahead of the clip's start and the clip's end. The snapshot closest to the start wins, and each snapshot pairs with at most one clip.
- `Wait`: how long after a clip's upload to wait for its snapshot before grabbing a video frame with ffmpeg instead.

See [Snapshot Pairing and Thumbnails](snapshot-pairing-and-thumbnails.md).

## Registry

Vendor packages call `profile.Register(p)` from `init()`. Registering the same id twice panics, because that is a programming error. `cmd/nvr/main.go` blank-imports each vendor package so its `init` runs. At startup, `profile.Lookup(cfg.CameraProfile)` resolves the id. An unknown id fails startup with the sorted list of supported ids (`profile.IDs`).

## Reolink reference profile

`internal/profile/reolink` handles Reolink cameras uploading over FTP/SFTP:

- **Name pattern:** `<Camera>_<CH>_<YYYYMMDDHHMMSS>.<ext>`, matched against the base name with `^(.+)_(\d{2})_(\d{14})\.([A-Za-z0-9]+)$`. The camera group is greedy, so camera names may contain underscores (`Side_Gate_00_...`). The channel must be exactly two digits.
- **Extensions:** `mp4` → Clip; `jpg`/`jpeg` → Snapshot (case-insensitive). Anything else, including the `.txt` file Reolink writes when FTP settings are tested and `.mp4.tmp` partial uploads, → Ignore.
- **Invalid timestamps** (for example month 13) and blank camera names → Ignore.
- **Folders are not consulted.** Both the dated `YYYY/MM/DD/` layout and older `Camera/YYYY/MM/DD/` layouts classify correctly. `DateDirs` returns `YYYY/MM/DD` only, so with the older layout, recent scans won't find files and the webhook plus the full crawl have to cover them.
- **Pairing:** `Before = 2s`, `Wait = 60s`. The code comment records the measurement behind this: across 323k clips, 61% of snapshots are stamped 0–1 s after the clip's name and a few percent up to 4 s after. Anything inside the clip is a valid thumbnail. Snapshots normally arrive before the (larger) clip.
- Reolink records in roughly 28 s segments, and writes them as **fragmented MP4**. That is why the stream endpoint remuxes them on the fly. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md).

## Adding a vendor

1. Create `internal/profile/<vendor>/` implementing `profile.Profile`, and call `profile.Register(Profile{})` in `init()`.
2. Add a blank import in `cmd/nvr/main.go`.
3. Write table tests from **real file names**, following `internal/profile/reolink/reolink_test.go`. Cover clips, snapshots, ignored names (upload tests, temp files, bad dates), time-zone conversion with `Media.In`, `DateDirs`, and registry lookup.
4. Mention the new id in `.env.example` (`CAMERA_PROFILE`) and in the Helm values if needed (see [Configuration](../architecture/configuration.md)).

Design limits to keep in mind: one profile and one time zone per deployment, and the hour that repeats when DST ends is not disambiguated. Clips from it overlap on the timeline.
