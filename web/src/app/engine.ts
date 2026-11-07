import { fmtTime } from '../lib/time'
import { streamURL, type Clip } from '../lib/api'
import { itemAt, nextAfter } from '../lib/clips'
import { app, camClips, playbackAt, playOnFrom, toast, type AppState } from './state'

// Drives two <video> elements from the app state. One shows the clip under
// the playhead; the other loads the next clip a little before the current one
// ends, so playback rolls from clip to clip without waiting (plan: "double
// buffer"). Gaps between clips are skipped while playing.
//
// React only renders what the engine reports back (shown, slot, preview,
// buffering); all <video> work happens here, outside render.

const PRELOAD_S = 12 // start loading the next clip this long before the end

export class Engine {
  private v: [HTMLVideoElement, HTMLVideoElement]
  private clip: [Clip | null, Clip | null] = [null, null]
  private active: 0 | 1 = 0
  private seen: { seek: number; cam: number; day: unknown; t: number } = { seek: -1, cam: -1, day: null, t: -1 }
  private raf = 0
  private cleanup: (() => void)[] = []

  constructor(a: HTMLVideoElement, b: HTMLVideoElement) {
    this.v = [a, b]
    this.v.forEach((v, i) => {
      const on = (type: string, fn: () => void) => {
        v.addEventListener(type, fn)
        this.cleanup.push(() => v.removeEventListener(type, fn))
      }
      const mine = (fn: () => void) => () => {
        if (i === this.active) fn()
      }
      on('ended', mine(() => this.onEnded()))
      on('error', () => this.onError(i))
      on('waiting', mine(() => app.set({ buffering: true })))
      for (const ev of ['playing', 'canplay', 'seeked', 'loadeddata']) on(ev, mine(() => app.set({ buffering: false })))
    })
    this.cleanup.push(app.subscribe(() => this.sync()))
    this.sync()
    this.raf = requestAnimationFrame(this.tick)
  }

  destroy() {
    cancelAnimationFrame(this.raf)
    for (const fn of this.cleanup) fn()
    for (const v of this.v) {
      v.pause()
      v.removeAttribute('src')
      v.load()
    }
  }

  private get cur() {
    return this.v[this.active]
  }

  private sync() {
    const s = app.get()
    for (const v of this.v) {
      if (v.muted !== s.muted) v.muted = s.muted
      if (v.defaultPlaybackRate !== s.rate || v.playbackRate !== s.rate) {
        v.defaultPlaybackRate = s.rate
        v.playbackRate = s.rate
      }
    }
    const seen = this.seen
    const moved = s.seek !== seen.seek || s.cam !== seen.cam || s.day !== seen.day
    const scrubbed = s.scrubbing && s.t !== seen.t
    this.seen = { seek: s.seek, cam: s.cam, day: s.day, t: s.t }
    if (moved && !s.scrubbing) this.goTo(s)
    else if (scrubbed) this.scrub(s)
    this.playState()
  }

  private load(i: 0 | 1, c: Clip) {
    const v = this.v[i]
    this.clip[i] = c
    v.preload = 'auto'
    v.src = streamURL(c.id)
    if (i === this.active) app.set({ buffering: true })
  }

  private swap(i: 0 | 1) {
    if (i === this.active) return
    this.cur.pause()
    this.active = i
    app.set({ slot: i })
  }

  /** Show the clip under the playhead at the right position. */
  private goTo(s: AppState) {
    const c = itemAt(camClips(s), s.t)
    if (!c) {
      for (const v of this.v) v.pause()
      app.set({ shown: null, preview: null, buffering: false })
      if (s.playing) queueMicrotask(() => playOnFrom(app.get().t))
      return
    }
    const found = this.clip.findIndex((x) => x?.id === c.id)
    const i = found < 0 ? this.active : (found as 0 | 1)
    if (found < 0) this.load(i, c)
    this.swap(i)
    const off = (s.t - c.start) / 1000
    if (Math.abs(this.cur.currentTime - off) > 0.25) this.cur.currentTime = off
    app.set({ shown: c.id, preview: null })
  }

  /** While dragging: seek the loaded clip live, or show the thumbnail of one that isn't loaded. */
  private scrub(s: AppState) {
    const c = itemAt(camClips(s), s.t)
    if (!c) {
      app.set({ shown: null, preview: null })
      return
    }
    const i = this.clip.findIndex((x) => x?.id === c.id)
    if (i < 0) {
      app.set({ shown: c.id, preview: c.id })
      return
    }
    this.swap(i as 0 | 1)
    if (!this.cur.seeking) this.cur.currentTime = (s.t - c.start) / 1000
    app.set({ shown: c.id, preview: null })
  }

  private playState() {
    const s = app.get()
    const v = this.cur
    const want = s.playing && !s.scrubbing && s.shown !== null && s.preview === null && this.clip[this.active]?.id === s.shown
    if (want && v.paused && !v.ended) {
      v.play().catch((e: unknown) => {
        // Autoplay with sound is blocked until the user interacts: retry muted.
        if (e instanceof DOMException && e.name === 'NotAllowedError' && !v.muted) {
          app.set({ muted: true })
          v.muted = true
          v.play().catch(() => app.set({ playing: false }))
        }
      })
    } else if (!want && !v.paused) {
      v.pause()
    }
    const other = this.v[1 - this.active]
    if (!other.paused) other.pause()
  }

  private tick = () => {
    this.raf = requestAnimationFrame(this.tick)
    const s = app.get()
    const c = this.clip[this.active]
    const v = this.cur
    if (!s.playing || s.scrubbing || !c || s.shown !== c.id || v.paused || v.seeking) return
    const t = c.start + v.currentTime * 1000
    if (Math.abs(t - s.t) >= 1) playbackAt(t)
    const left = Number.isFinite(v.duration) ? v.duration - v.currentTime : (c.end - t) / 1000
    if (left < PRELOAD_S) {
      const n = nextAfter(camClips(s), c.start)
      const o = (1 - this.active) as 0 | 1
      if (n && this.clip[o]?.id !== n.id) {
        this.load(o, n)
        this.v[o].currentTime = 0
      }
    }
  }

  private onEnded() {
    const c = this.clip[this.active]
    if (c) queueMicrotask(() => playOnFrom(app.get().t, c))
  }

  private onError(i: number) {
    const c = this.clip[i]
    this.clip[i] = null
    if (i !== this.active || !c) return // a preload failed; it'll be retried when needed
    const s = app.get()
    app.set({ buffering: false })
    toast(`Couldn’t play the clip from ${fmtTime(c.start, s.tz)}`)
    if (s.playing) queueMicrotask(() => playOnFrom(app.get().t, c))
  }
}
