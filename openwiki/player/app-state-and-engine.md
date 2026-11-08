---
type: subsystem
title: Player State and Playback Engine
description: The webdav-nvr-player frontend's architecture, covering the tiny external store, the AppState shape built around one playhead clock, day loading in the camera time zone, boot and refresh, URL sync, and the two-video double-buffer engine that rolls from clip to clip and skips gaps.
tags: [frontend, react, state, playback, video, timeline]
sources:
  - id: openwiki-source-2a66ee6d8055e80ef6e42df6
    resource: repo://web/src/App.tsx
  - id: openwiki-source-24e768187d07b3ab1b5c8255
    resource: repo://web/src/app/days.ts
  - id: openwiki-source-9c2567fe87a7eac5c1147511
    resource: repo://web/src/app/engine.ts
  - id: openwiki-source-197ea74907f1a481e4cc83b1
    resource: repo://web/src/app/player.test.ts
  - id: openwiki-source-5a4a43ff7cc5d923e25b5ab6
    resource: repo://web/src/app/playhead.ts
  - id: openwiki-source-9056b0e887a10ee685479252
    resource: repo://web/src/app/prefs.ts
  - id: openwiki-source-244f7dad4f356d1989362bda
    resource: repo://web/src/app/state.ts
  - id: openwiki-source-5d6837f11960cd7bc5f7278c
    resource: repo://web/src/app/url.ts
  - id: openwiki-source-060d4a8974b2c24d7f62ae7c
    resource: repo://web/src/app/view.ts
  - id: openwiki-source-1d02ef8eb8a670d60c809e75
    resource: repo://web/src/lib/storage.ts
  - id: openwiki-source-08ebfe3cbc4cf16cf01cc49c
    resource: repo://web/src/lib/store.ts
  - id: openwiki-source-b46c06953ae419539b33ae92
    resource: repo://web/src/lib/time.ts
generated: { by: "claude-code", at: "2026-11-08T06:01:44.565Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# Player State and Playback Engine

The web UI (`web/`, React + Vite + Tailwind) is built around **one store** and **one playhead clock**. The timeline and controls *write* the playhead. A non-React `Engine` class *follows* it with two `<video>` elements and reports what's on screen back to the store. React only renders the state, and never drives video from inside render.

## The store (`web/src/lib/store.ts`)

`createStore(initial)` returns `{get, set, subscribe}`:

- `set(patch)` takes an object or a function `(s) => patch`. It is a no-op when every patched key is `Object.is`-equal to the current value. Otherwise it shallow-merges the patch and notifies all subscribers.
- `useStore(store, selector)` wraps `useSyncExternalStore`. A selector must return a primitive or a stable reference. That way the playhead moving every animation frame only re-renders the components that read `t`.

`web/src/app/hooks.ts` builds on it. For example, `useClipAtPlayhead` re-renders only when the *clip id* under the playhead changes.

## `AppState` and its action modules (`web/src/app/`)

`state.ts` holds only the state, its initial values, and selectors (`camClips`, `cameraName`, `clipAtPlayhead`, `shownClip`) plus `toast`. The actions that change the state live in modules beside it, one per job:

| Module | Owns |
|---|---|
| `view.ts` | The timeline window: `clampView`, `follow`, `setView`, `zoomAround`, `zoomStep` |
| `playhead.ts` | Moving `t` inside the loaded day and switching camera: `seekTo`, `playbackAt`, `scrubTo`/`endScrub`, `jumpClip`, `skip`, `playOnFrom`, `selectCamera` |
| `days.ts` | Loading and moving between days, and startup: `loadDay`, `goToDay`, `goToTime`, `jumpToLatest`, `refresh`, `boot` |
| `prefs.ts` | Playback rate, mute and clip order |
| `theater.ts` | Theater layout and the browser's fullscreen |
| `url.ts` | Reading and writing `?cam=&t=` |

The single exported store is `app`. Its state falls into these groups:

| Group | Fields |
|---|---|
| Context | `tz` (camera time zone from `/api/status`), `cameras`, `days` (days with footage), `today` |
| Day data | `day: DayData \| null`, `dayLoading`, `dayError` |
| Clock | `cam` (selected camera), `t` (playhead, Unix ms), `seek` (counter bumped when the *user* moves `t`), `playing`, `scrubbing`, `rate`, `muted` |
| Timeline view | `view0`, `span` (visible window), `viewLocked` (set while panning or pinching) |
| Layout | `newestFirst`, `layout` (`split` \| `theater`), `drawer`, `idle`, `toast` |
| Written by the engine | `shown` (clip on screen or `null`), `slot` (which `<video>` is visible), `preview` (clip whose thumbnail stands in while scrubbing), `buffering` |

`DayData` holds one camera-local day: `start`/`end` (23 or 25 h long on DST days), `byCam` (clips per camera, sorted by start), `spans` (clips merged where they are less than 2 s apart, for drawing a zoomed-out day) and `snaps` (times of unpaired snapshots).

Only one camera plays at a time. Keeping a single clock is deliberate: a synchronized multi-camera grid would be several viewports reading the same `t`.

### Moving the playhead (`playhead.ts`, `view.ts`)

There are three different writers, and the difference between them matters:

- `seekTo(t)`: a user action. It clamps `t` to the day, **bumps `seek`** so the engine repositions the video, and may recenter the view.
- `playbackAt(t)`: called by the engine while playing. It does *not* bump `seek`. While playing, the view **pages forward**: when `t` nears the right edge, the window jumps so `t` sits at 10% of the width.
- `scrubTo(t)` / `endScrub()`: dragging on the timeline. `t` moves live with `scrubbing: true`, and `endScrub` bumps `seek` once the drag ends.

`follow` never moves the view while `viewLocked` is set, so a user who is panning keeps control. Zooming goes through `setView`/`zoomAround` and is clamped to `[MIN_SPAN = 4 min, day length]`. `zoomStep` walks the levels 24 h → 6 h → 1 h → 15 min → 5 min, keeping the playhead's position on screen.

Other actions: `jumpClip(±1)` (previous or next clip; "previous" first goes back to the current clip's start if more than 2 s into it), `skip(seconds)`, `jumpToLatest()` (newest clip, switching to the camera that recorded most recently if the selected one has nothing that day), `goToTime(date, hms)` (never past now), `selectCamera(id)`, and `playOnFrom(t, endedClip?)`. `playOnFrom` jumps to the next clip and shows "Skipped N with no recording" for gaps over 2 s, or "End of recordings" at the end of the day.

### Persisted preferences (`prefs.ts`)

Playback rate (`nvr.playbackRate`, one of 0.5, 1, 1.5, 2) and list sort order (`nvr.newestFirst`) go in `localStorage` through `web/src/lib/storage.ts`. `loadSetting` and `saveSetting` wrap every access in `try`/`catch`, so private windows or blocked site data just don't remember them, and an unknown saved rate falls back to 1. Audio starts **muted**.

## Loading data (`days.ts`)

### Boot

`boot()` runs once after sign-in:

1. Fetches `/api/status` and `/api/cameras` together. A 503 (no WebDAV server), no cameras, or no indexed clips returns `{kind:'setup'}`, and the app shows the status page instead of the player.
2. Reads `?t=&cam=` from the URL. Without them, it starts at the index's latest clip.
3. Loads that day, then (unless the URL picked a camera) switches to the camera with the newest clip on it and seeks to that clip.
4. Starts `refresh()`.

### Days

`loadDay(date, {at?, quiet?})` computes the local-midnight bounds with `dayBounds(date, tz)`, then fetches `/api/clips?from=&to=` for all cameras. See [HTTP API](../api/http-api.md).

- A normal load aborts any earlier day request. It lands the playhead at `at` if given. Otherwise it keeps the same time of day if the camera recorded then, or moves to the camera's nearest clip, never later than now. It also bumps `seek`.
- A `quiet` load refreshes the current day in place. It never cancels or overrides a day the user explicitly picked.

`refresh()` runs every 30 s. It updates `today`, reloads the list of days with footage (last 62 days via `/api/days`), and quietly reloads today if that's what is shown and the user isn't scrubbing.

All day boundaries and labels use the **camera** time zone, not the browser's, through `Intl.DateTimeFormat` in `web/src/lib/time.ts`. The timeline therefore matches file names and the camera's on-screen clock.

### URL sync (`url.ts`)

`positionInURL()` reads `?cam=&t=` at boot. `syncURL()` runs every 2 s and writes `?cam=&t=` (rounded to the second) with `history.replaceState`, so a reload or a shared link returns to the same moment.

## The playback engine (`web/src/app/engine.ts`)

`new Engine(videoA, videoB)` is created by the viewport. It subscribes to `app`, runs a `requestAnimationFrame` loop, and owns all `<video>` side effects.

### Two-video double buffer

- **`sync()`**, on every store change: applies `muted` and `rate` to both videos. If `seek`, `cam` or `day` changed (and the user isn't scrubbing), it calls `goTo`. While scrubbing it calls `scrub`. It then calls `playState`.
- **`goTo`** finds the clip under `t` with a binary search (`itemAt`):
  - If there is no clip there (a gap), it pauses and, if playing, continues with `playOnFrom`.
  - If either slot already holds that clip, it **swaps** to it with no reload. Otherwise it loads the clip into the active slot.
  - It sets `currentTime` to the offset when that is more than 0.25 s off, and reports `shown`.
- **`scrub`**: if the clip under the cursor is already loaded in a slot, it seeks it live. If not, it sets `preview` to that clip, so the viewport shows its **thumbnail** instead of starting a network load for every clip the cursor passes over.
- **`tick`**, every animation frame while playing: converts `currentTime` back to wall-clock time and calls `playbackAt`. When less than **12 s** (`PRELOAD_S`) of the current clip is left, it loads the next clip into the *other* slot at time 0.
- **`onEnded`** calls `playOnFrom(t, endedClip)`. The next clip is usually already in the other slot, so `goTo` just swaps and playback continues without waiting.
- **`playState`** plays only when `playing`, not scrubbing, a clip is shown, no thumbnail preview is active, and the active slot holds the shown clip. Otherwise it pauses. The inactive video is always paused. If autoplay with sound is blocked (`NotAllowedError`), it mutes and retries.
- **Errors:** a failed preload is ignored and retried when needed. A failed active clip shows a toast ("Couldn't play the clip from HH:MM:SS") and moves on to the next clip.
- **`buffering`** follows the active video's `waiting` event (on) and its `playing`, `canplay`, `seeked` and `loadeddata` events (off).

Videos load `/api/clips/{id}/stream`. Fragmented Reolink MP4s arrive as a virtually remuxed regular MP4, so Firefox can seek past the first fragment. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md). `canPlay(codec)` in `lib/api.ts` lets the UI warn when a browser can't decode HEVC, instead of showing a black frame.

## App shell (`web/src/App.tsx`)

`App` fetches `/api/auth`, then shows one of: "Can't reach the NVR server" (when the network fails), `SignIn`, or `SignedIn`. The `nvr:signed-out` event takes it back to sign-in. `SignedIn` routes by hash: `#/settings` → Settings, `#/status` (or a `setup` boot result) → the status page, otherwise `Player`. `Player` installs the global keyboard shortcuts, idle detection, the fullscreen exit handler, and the `syncURL` (2 s) and `refresh` (30 s) intervals. The components are described in [Timeline and Player UI](timeline-and-ui.md).

## Tests

`web/src/app/player.test.ts` (Vitest) pins down the store's behavior: seeking, playback paging and `viewLocked`, zoom limits and steps, `jumpClip`, `playOnFrom` gaps and end of day, `loadDay` landing rules, and `boot`'s fallbacks. `web/src/lib/time.test.ts` covers camera-time-zone day bounds across DST, and `web/src/lib/clips.test.ts` the clip search and span merging. See [Testing Guide](../testing/testing-guide.md).
