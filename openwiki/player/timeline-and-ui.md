---
type: subsystem
title: Timeline and Player UI
description: The React components of the webdav-nvr-player UI, covering the canvas day timeline with lanes, zoom, pan, scrub and pinch, the viewport and its gestures, the custom control bar, the dock, the clip list, split and theater layouts, keyboard shortcuts, the status page, and the dark colour tokens.
tags: [frontend, ui, timeline, canvas, keyboard, layout, theming]
sources:
  - id: openwiki-source-2a66ee6d8055e80ef6e42df6
    resource: repo://web/src/App.tsx
  - id: openwiki-source-2f7729b14b86f21c8c1c7b30
    resource: repo://web/src/components/ClipList.tsx
  - id: openwiki-source-6c9b5ed1bdc83b6b24a3c1bf
    resource: repo://web/src/components/ControlBar.tsx
  - id: openwiki-source-9e9ecdfcb3b8fa6146f1e171
    resource: repo://web/src/components/timeline/draw.ts
  - id: openwiki-source-30b08c1765420c0c05d60851
    resource: repo://web/src/components/timeline/geometry.ts
  - id: openwiki-source-ac995e67156f747bd194fb08
    resource: repo://web/src/components/timeline/gestures.ts
  - id: openwiki-source-64e72a25f56f8ddb41b91c62
    resource: repo://web/src/components/timeline/Timeline.tsx
  - id: openwiki-source-de879d9d6c1d598298d6f11e
    resource: repo://web/src/components/Viewport.tsx
  - id: openwiki-source-2d994c2a65621110d714e307
    resource: repo://web/src/index.css
  - id: openwiki-source-a546101974e7cf9f4300a697
    resource: repo://web/src/StatusPage.tsx
generated: { by: "claude-code", at: "2026-11-08T06:01:44.565Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# Timeline and Player UI

The player UI is a handful of components that read slices of the `app` store and call its actions. Playback itself is handled by the engine described in [Player State and Playback Engine](app-state-and-engine.md).

```
App ─ SignedIn ─ Player (.app[data-layout=split|theater])
                  ├─ Header      brand, camera switcher, day navigator
                  ├─ Viewport    two <video>s + Engine, thumbnail preview, gestures, ControlBar, Toast
                  ├─ Dock        transport, wall-clock time, go-to date/time, newest, zoom, Timeline
                  └─ ClipList    every clip of the selected camera and day
```

## Layouts

`Player` sets `data-layout`, `data-drawer` and `data-quiet` on its root. The CSS in `web/src/app/app.css` arranges the regions from those attributes:

- **Split** (the default): on desktop, the clip list sits on the left with the video and dock on the right. On a phone, the list stacks under the timeline. Touch drag and pinch-zoom get the same care as mouse input.
- **Theater** (the expand button, `F`, or double-clicking the middle of the video): the video gets the whole window, and the browser goes full screen if it can. The clip list becomes a drawer. Leaving the browser's full screen (Esc) also leaves theater mode. A `ResizeObserver` publishes `--top-h` and `--dock-h` so theater mode can place overlays around the header and dock.
- **Quiet**: after a period with no pointer or key activity while playing (and no drawer open or scrubbing), `idle` turns on and the controls fade.

## Timeline (`components/timeline/`)

The timeline is drawn on a **canvas**, because a busy day has over a thousand clips. It is split into four files:

| File | Job |
|---|---|
| `Timeline.tsx` | The React component: owns the canvas, schedules redraws, and wires up drawing and input. |
| `draw.ts` | `createPainter(canvas)`: draws one frame back to front (background, overview strip, time not yet recorded, ticks, clips and snapshots, playhead, camera names) and returns the name-gutter width it laid out. |
| `gestures.ts` | `attachGestures(canvas, scale)`: pointer and wheel input, turned into store actions. Returns a function that removes the listeners. |
| `geometry.ts` | Layout constants and the time ↔ x `Scale` (`timeScale`, `overviewScale`, `lanes`, `region`), shared by drawing, input and the dock. |

Input always uses the scale from the *last drawn* frame, because the gutter width is only known after drawing. `Timeline.tsx` redraws on the next animation frame after any store change, resize, or tick of the "NOW" line (a `dirty` flag). Device pixel ratio is handled.

Layout constants are in `geometry.ts`; the dock uses `timelineHeight` and the lane limits from it to size the canvas:

| Element | Description |
|---|---|
| Overview strip (`MINI` = 24 px, top) | The whole day: the selected camera's activity, the visible window outlined in sodium orange, and a playhead tick. |
| Lanes | One per camera, 24 px by default (the user can drag the timeline taller, 14–200 px per lane), with `LGAP` 3 px between lanes. |
| Axis (`AXIS` = 20 px, bottom) | Time labels at round local times. The step is the smallest of 30 s … 6 h that keeps labels at least 56 px apart. |
| Name gutter | Camera names in a left column, measured on each draw and capped at 32% of the width. Names are shortened to the first word below 560 px. |

What gets drawn:

- **Clips.** Zoomed out to 3 h or more, the lanes draw the merged `spans`. Zoomed in, they draw each clip from `byCam`, using a binary search so only visible clips are drawn. The selected camera's lane uses `--color-clip-hi`.
- **Unpaired snapshots** are drawn as dots.
- **Time after now** is hatched, with a labelled "NOW" line.
- Colours come from the CSS custom properties `--color-*` at runtime, with hard-coded fallbacks.

Interaction (pointer events plus wheel):

- **Tap a lane**: moves the playhead to that time and selects that camera. **Tapping a camera name** only selects the camera.
- **Drag the playhead**: scrub (`scrubTo` while dragging, `endScrub` on release). The grab radius is 8 px for a mouse and 18 px for touch.
- **Drag anywhere else on a lane**: pan. When zoomed all the way out there's nothing to pan, so the drag scrubs instead.
- **Drag on the overview strip or the axis**: move the view along the day.
- **Wheel**: zooms around the pointer (`zoomAround`). Shift+wheel or a sideways swipe pans.
- **Two fingers**: pinch-zoom around their midpoint, and pan.

`viewLocked` is set while panning or pinching, so playback doesn't page the view out from under the user.

## Viewport (`components/Viewport.tsx`)

The viewport owns the two `<video>` elements and creates the `Engine`. It renders:

- the video in the active `slot`;
- a full-size thumbnail `<img>` while `preview` is set (scrubbing over a clip that isn't loaded);
- a spinner while `buffering`;
- a warning when `canPlay(codec)` says the browser can't decode the clip's codec (typically HEVC), instead of a black frame;
- a message when the playhead is in a gap.

Gestures:

- **Mouse:** click plays or pauses. Double-click the left or right third to skip 5 s, or the middle to enter theater mode.
- **Touch:** a tap shows or hides the controls. Double-tap a side to skip. Further taps on the same side within 700 ms add 5 s each, shown by the `SkipFlash` badge ("« 10 s").
- Constants: double-tap window 300 ms, tap slop 40 px.

## Control bar (`components/ControlBar.tsx`)

The player draws its own controls, because the browser's controls can't take extra buttons and Firefox goes full screen on any double-click. The bar has:

- a scrub bar for the current clip;
- play/pause and back/forward 5 s;
- the position within the clip;
- a speed menu (`RATES` 0.5×, 1×, 1.5×, 2×, remembered between visits);
- mute (audio starts muted);
- a theater toggle.

It fades while playing when the pointer is elsewhere or the user is idle.

## Dock (`components/Dock.tsx`)

The dock sits under the video. It has:

- clip transport (previous/next clip, play);
- the wall-clock time at the playhead in the camera time zone, and the current file name;
- **go to** a date and time (`goToTime`);
- **newest** (`jumpToLatest`);
- zoom −/+ with a label ("6 h", "15 min");
- a drag handle that resizes the timeline height.

## Clip list (`components/ClipList.tsx`)

Every clip of the selected camera and day is listed. There is no event grouping. Each row shows a thumbnail (`/api/clips/{id}/thumb`), time, duration and size. The list is sorted newest first by default, and the order can be toggled and is remembered. The clip under the playhead is highlighted and scrolled into view, unless the pointer is over the list. Clicking a row seeks to that clip and plays it.

## Header (`components/Header.tsx`)

The header holds the brand, the camera switcher (with each camera's clip count for the day), and the day navigator. The day navigator steps through days and picks from the days with footage (`days`). It can't go past today.

## Keyboard shortcuts (`App.tsx`)

Shortcuts are ignored when a modifier key is held, when typing in a form field (except Esc), and for Space or Enter on a focused button.

| Key | Action |
|---|---|
| Space, `k` | play / pause |
| ← / → | −5 s / +5 s (with Shift: ±60 s) |
| `[` / `]` | previous / next clip |
| End | newest recording |
| `f` | theater mode |
| `m` | mute |
| `<` / `>` | slower / faster |
| `+` `=` / `-` `_` | zoom in / out |
| `1`–`9` | select camera N |
| Esc | close drawer, then leave theater |

## Status page (`StatusPage.tsx`)

The status page appears at `#/status`, and replaces the player when boot finds nothing to play (no WebDAV server, no cameras, nothing indexed yet). It shows:

- the version;
- the live WebDAV check from `/api/source`: URL, user, profile, time zone, top-level folders, or the error;
- the index and ingest status from `/api/status`: counts by status and thumbnail state, last scans, scan errors, and whether the webhook is enabled.

A button re-runs the connection check. Settings live at `#/settings` (see [Sign-in and Sessions](../auth/sign-in-and-sessions.md)).

## Theming (`web/src/index.css`)

The UI is dark by design. Tailwind v4 `@theme` tokens define the palette (`ink-0…3`, `line`, `text`, `muted`, `faint`, `sodium` accent, `clip`/`clip-hi`, `snap`, `rec`, `warn`) and the fonts (Barlow Condensed for display, Barlow, IBM Plex Mono). **Components use these tokens only, never literal colours**, so the palette can change in one place. The canvas timeline reads the same tokens at runtime.

## Developing the UI

Run `make dev` (the Go server on :8080) and `npm run dev` in `web/` (Vite with hot reload, which proxies `/api` to :8080). `npm run typecheck` and `npm run lint` run as part of `make test` and `make lint`. See [Quickstart](../quickstart.md).
