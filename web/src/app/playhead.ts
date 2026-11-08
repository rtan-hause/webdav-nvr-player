import type { Clip } from '../lib/api'
import { lastStartLE, nextAfter } from '../lib/clips'
import { fmtDur, type Span } from '../lib/time'
import { app, camClips, cameraName, toast } from './state'
import { follow } from './view'

// Moving the playhead within the loaded day, and switching camera.

/** t kept inside the day (whose end is exclusive). */
export function clampT(t: number, day: Span | null): number {
  if (!day) return t
  return Math.min(day.end - 1, Math.max(day.start, t))
}

/** Move the playhead (user action): the player seeks there. */
export function seekTo(t: number) {
  app.set((s) => {
    const tt = clampT(t, s.day)
    return { t: tt, seek: s.seek + 1, ...follow(s, tt, false) }
  })
}

/** Playhead update from the playing video. */
export function playbackAt(t: number) {
  app.set((s) => ({ t, ...follow(s, t, true) }))
}

/** Playhead update while dragging (no seek until the drag ends). */
export function scrubTo(t: number) {
  app.set((s) => ({ t: clampT(t, s.day), scrubbing: true }))
}

export function endScrub() {
  app.set((s) => ({ scrubbing: false, seek: s.seek + 1 }))
}

export const setPlaying = (playing: boolean) => app.set({ playing })
export const togglePlay = () => app.set((s) => ({ playing: !s.playing }))
export const skip = (seconds: number) => seekTo(app.get().t + seconds * 1000)

export function jumpClip(dir: 1 | -1) {
  const s = app.get()
  const arr = camClips(s)
  const i = lastStartLE(arr, s.t)
  if (dir > 0) {
    const n = arr[i + 1]
    if (n) seekTo(n.start)
    return
  }
  const c = arr[i]
  if (c && s.t - c.start > 2000) seekTo(c.start)
  else if (arr[i - 1]) seekTo(arr[i - 1].start)
}

/** From a gap or the end of a clip, go to the next clip, saying how long the gap was. */
export function playOnFrom(t: number, endedClip?: Clip) {
  const s = app.get()
  const next = nextAfter(camClips(s), endedClip ? endedClip.start : t)
  if (!next) {
    app.set({ playing: false })
    toast(`End of recordings from ${cameraName(s)} for this day`)
    return
  }
  const gap = next.start - (endedClip ? endedClip.end : t)
  if (gap > 2000) toast(`Skipped ${fmtDur(gap)} with no recording`)
  seekTo(next.start)
}

export function selectCamera(id: number) {
  if (app.get().cam === id) return
  app.set((s) => ({ cam: id, seek: s.seek + 1 }))
}
