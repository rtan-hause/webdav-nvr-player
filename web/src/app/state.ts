import { fetchClips, fetchDays, getJSON, HttpError, type Camera, type Clip, type IndexStatus } from '../lib/api'
import { itemAt, lastStartLE, mergeSpans, nextAfter } from '../lib/clips'
import { loadSetting, saveSetting } from '../lib/storage'
import { createStore } from '../lib/store'
import { addDays, atLocalTime, dateOf, dayBounds, fmtDay, fmtDur } from '../lib/time'

// App state for the player: one playhead (t) on one day for the selected
// camera. Built around a single clock so a synced multi-camera grid can be
// added later (plan decision 6).

export type DayData = {
  date: string // YYYY-MM-DD in the camera time zone
  start: number
  end: number
  byCam: Map<number, Clip[]> // sorted by start
  spans: Map<number, { start: number; end: number }[]> // clips merged where they touch
  snaps: Map<number, number[]> // unpaired snapshot times
}

export type AppState = {
  tz: string
  cameras: Camera[]
  days: string[] // recent days with footage
  today: string
  day: DayData | null
  dayLoading: boolean
  dayError: string | null

  cam: number // selected camera id
  t: number // playhead, Unix ms
  seek: number // bumped when t is set by the user rather than by playback
  playing: boolean
  scrubbing: boolean // dragging on the timeline or the clip's scrub bar
  viewLocked: boolean // panning or pinching the timeline: don't move the view
  rate: number
  muted: boolean

  view0: number // timeline view start, Unix ms
  span: number // timeline view length, ms

  newestFirst: boolean
  layout: 'split' | 'theater'
  drawer: boolean // clip list drawer in theater mode
  idle: boolean // no pointer or key activity for a while
  toast: { msg: string; id: number } | null

  // Written by the playback engine.
  shown: number | null // clip on screen; null when there's no recording at t
  slot: 0 | 1 // which of the two <video> elements is visible
  preview: number | null // clip whose thumbnail stands in while scrubbing
  buffering: boolean
}

export const RATES = [0.5, 1, 1.5, 2]
const RATE_KEY = 'nvr.playbackRate'
const SORT_KEY = 'nvr.newestFirst'
export const MIN_SPAN = 4 * 60_000
const ZOOMS = [6 * 3600_000, 3600_000, 15 * 60_000, 5 * 60_000]

const savedRate = Number(loadSetting(RATE_KEY))

export const app = createStore<AppState>({
  tz: 'UTC',
  cameras: [],
  days: [],
  today: '',
  day: null,
  dayLoading: false,
  dayError: null,
  cam: 0,
  t: 0,
  seek: 0,
  playing: false,
  scrubbing: false,
  viewLocked: false,
  rate: RATES.includes(savedRate) ? savedRate : 1,
  muted: true,
  view0: 0,
  span: 86_400_000,
  newestFirst: loadSetting(SORT_KEY) !== 'false',
  layout: 'split',
  drawer: false,
  idle: false,
  toast: null,
  shown: null,
  slot: 0,
  preview: null,
  buffering: false,
})

const NO_CLIPS: Clip[] = []
export const camClips = (s: AppState): Clip[] => s.day?.byCam.get(s.cam) ?? NO_CLIPS
export const cameraName = (s: AppState, id = s.cam) => s.cameras.find((c) => c.id === id)?.name ?? `Camera ${id}`
export const clipAtPlayhead = (s: AppState) => itemAt(camClips(s), s.t)

// ---------- view (timeline zoom and pan) ----------

const daySpan = (s: AppState) => (s.day ? s.day.end - s.day.start : 86_400_000)

function clampView(s: AppState, view0: number, span = s.span): number {
  if (!s.day) return view0
  return Math.min(s.day.end - span, Math.max(s.day.start, view0))
}

/** View change that keeps t on screen. While playing it pages forward. */
function follow(s: AppState, t: number, paging: boolean): Partial<AppState> {
  if (s.viewLocked) return {}
  const a = s.view0
  const b = s.view0 + s.span
  if (paging && t > b - s.span * 0.02 && t <= b + s.span * 0.5) return { view0: clampView(s, t - s.span * 0.1) }
  if (t < a || t > b) return { view0: clampView(s, t - s.span / 2) }
  return {}
}

export function setView(view0: number, span?: number) {
  app.set((s) => {
    const sp = Math.min(daySpan(s), Math.max(MIN_SPAN, span ?? s.span))
    return { span: sp, view0: clampView(s, view0, sp) }
  })
}

/** Zoom to span, keeping anchorT at fraction frac of the width. */
export function zoomAround(span: number, anchorT: number, frac: number) {
  const s = app.get()
  const sp = Math.min(daySpan(s), Math.max(MIN_SPAN, span))
  setView(anchorT - frac * sp, sp)
}

/** + / − buttons: step through 24 h, 6 h, 1 h, 15 min, 5 min around the playhead. */
export function zoomStep(dir: 1 | -1) {
  const s = app.get()
  const levels = [daySpan(s), ...ZOOMS]
  const next = dir > 0 ? levels.find((z) => z < s.span * 0.98) ?? MIN_SPAN : [...levels].reverse().find((z) => z > s.span * 1.02) ?? daySpan(s)
  const f = (s.t - s.view0) / s.span
  zoomAround(next, s.t, f >= 0 && f <= 1 ? f : 0.5)
}

// ---------- playhead ----------

function clampT(s: AppState, t: number): number {
  if (!s.day) return t
  return Math.min(s.day.end - 1, Math.max(s.day.start, t))
}

/** Move the playhead (user action): the player seeks there. */
export function seekTo(t: number) {
  app.set((s) => {
    const tt = clampT(s, t)
    return { t: tt, seek: s.seek + 1, ...follow(s, tt, false) }
  })
}

/** Playhead update from the playing video. */
export function playbackAt(t: number) {
  app.set((s) => ({ t, ...follow(s, t, true) }))
}

/** Playhead update while dragging (no seek until the drag ends). */
export function scrubTo(t: number) {
  app.set((s) => ({ t: clampT(s, t), scrubbing: true }))
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

/** Go to a wall-clock time ("17:42:05") on a day (YYYY-MM-DD), loading that day if needed; never past now. */
export function goToTime(date: string, hms: string) {
  const s = app.get()
  const t = /^\d{4}-\d{2}-\d{2}$/.test(date) ? atLocalTime(date, hms, s.tz) : null
  if (t === null) return toast('Pick a date and a time')
  const tt = Math.min(t, Date.now())
  const day = dateOf(tt, s.tz)
  if (day === s.day?.date) seekTo(tt)
  else void loadDay(day, { at: tt })
}

/**
 * Go to the newest recording: the start of the selected camera's latest clip
 * on the newest day with footage. If that camera has nothing that day, switch
 * to the camera that recorded most recently.
 */
export async function jumpToLatest() {
  const s0 = app.get()
  const date = [s0.days.at(-1), s0.day?.date].filter((d): d is string => !!d).sort().at(-1)
  if (!date) return
  if (date !== s0.day?.date) await loadDay(date)
  else if (date === s0.today) await loadDay(date, { quiet: true }) // pick up clips from the last few seconds
  const s = app.get()
  if (s.day?.date !== date) return // another day was picked meanwhile, or loading failed
  let latest = camClips(s).at(-1)
  if (!latest) {
    for (const arr of s.day.byCam.values()) {
      const c = arr.at(-1)
      if (c && (!latest || c.start > latest.start)) latest = c
    }
    if (!latest) return toast(`No recordings on ${date === s.today ? 'today' : fmtDay(date)}`)
    toast(`No recordings from ${cameraName(s)}; showing ${cameraName(s, latest.camera_id)}`)
    app.set({ cam: latest.camera_id })
  }
  seekTo(latest.start)
}

export function selectCamera(id: number) {
  if (app.get().cam === id) return
  app.set((s) => ({ cam: id, seek: s.seek + 1 }))
}

// ---------- settings ----------

export function setRate(rate: number) {
  app.set({ rate })
  saveSetting(RATE_KEY, String(rate))
}

export function stepRate(dir: 1 | -1) {
  const i = RATES.indexOf(app.get().rate)
  setRate(RATES[Math.min(Math.max((i < 0 ? 1 : i) + dir, 0), RATES.length - 1)])
}

export const toggleMute = () => app.set((s) => ({ muted: !s.muted }))

export function toggleSort() {
  const newestFirst = !app.get().newestFirst
  app.set({ newestFirst })
  saveSetting(SORT_KEY, String(newestFirst))
}

let toastId = 0
export const toast = (msg: string) => app.set({ toast: { msg, id: ++toastId } })

// ---------- theater mode (decision 18) ----------

let root: HTMLElement | null = null
export const setRoot = (el: HTMLElement | null) => {
  root = el
}

export function enterTheater() {
  app.set({ layout: 'theater', drawer: false })
  if (document.fullscreenEnabled && root && !document.fullscreenElement) root.requestFullscreen().catch(() => {})
}

export function exitTheater() {
  app.set({ layout: 'split', drawer: false })
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
}

export const toggleTheater = () => (app.get().layout === 'theater' ? exitTheater() : enterTheater())

// ---------- loading ----------

function dayData(date: string, tz: string, clips: Clip[], snaps: { camera_id: number; ts: number }[]): DayData {
  const { start, end } = dayBounds(date, tz)
  const byCam = new Map<number, Clip[]>()
  for (const c of clips) {
    let arr = byCam.get(c.camera_id)
    if (!arr) byCam.set(c.camera_id, (arr = []))
    arr.push(c)
  }
  const spans = new Map<number, { start: number; end: number }[]>()
  for (const [id, arr] of byCam) spans.set(id, mergeSpans(arr))
  const sn = new Map<number, number[]>()
  for (const x of snaps) {
    let arr = sn.get(x.camera_id)
    if (!arr) sn.set(x.camera_id, (arr = []))
    arr.push(x.ts)
  }
  return { date, start, end, byCam, spans, snaps: sn }
}

/**
 * Where the playhead lands on a newly picked day: the same time of day if
 * that camera recorded then, otherwise its nearest clip, never later than now.
 */
function landing(data: DayData, cam: number, sameTime: number): number {
  const t = Math.min(data.end - 1, Date.now(), Math.max(data.start, sameTime))
  const clips = data.byCam.get(cam) ?? []
  if (!clips.length || itemAt(clips, t)) return t
  let best = clips[0]
  for (const c of clips) if (Math.abs(c.start - t) < Math.abs(best.start - t)) best = c
  return best.start
}

let dayCtrl: AbortController | null = null

/**
 * Load a day. With `at`, the playhead goes there; otherwise it keeps its time
 * of day. `quiet` refreshes the current day without moving anything.
 */
export async function loadDay(date: string, opts: { at?: number; quiet?: boolean } = {}) {
  const s0 = app.get()
  const { start, end } = dayBounds(date, s0.tz)
  if (opts.quiet) {
    // A background refresh never cancels, or overrides, a day the user asked for.
    try {
      const r = await fetchClips(start, end)
      const s = app.get()
      if (s.day?.date === date && !s.dayLoading) app.set({ day: dayData(date, s.tz, r.clips, r.snapshots) })
    } catch {
      // try again next time
    }
    return
  }
  dayCtrl?.abort()
  const ctrl = (dayCtrl = new AbortController())
  app.set({ dayLoading: true, dayError: null })
  try {
    const r = await fetchClips(start, end, ctrl.signal)
    const data = dayData(date, s0.tz, r.clips, r.snapshots)
    app.set((s) => {
      const tt = opts.at !== undefined ? Math.min(end - 1, Math.max(start, opts.at)) : landing(data, s.cam, s.day ? start + (s.t - s.day.start) : start)
      const span = Math.min(s.span, end - start)
      const view0 = Math.min(end - span, Math.max(start, tt - span / 2))
      return { day: data, dayLoading: false, t: tt, seek: s.seek + 1, span, view0: span >= end - start ? start : view0 }
    })
  } catch (e) {
    if (ctrl.signal.aborted) return
    app.set({ dayLoading: false, dayError: e instanceof Error ? e.message : String(e) })
  }
}

export function goToDay(date: string) {
  const s = app.get()
  if (date > s.today || date === s.day?.date) return
  void loadDay(date)
}

/** Keep today's clips and the list of days fresh while the page is open. */
export async function refresh() {
  const s = app.get()
  const today = dateOf(Date.now(), s.tz)
  if (today !== s.today) app.set({ today })
  try {
    app.set({ days: await fetchDays(addDays(today, -61), today) })
  } catch {
    // keep the old list
  }
  if (s.day?.date === today && !s.scrubbing && !s.dayLoading) await loadDay(today, { quiet: true })
}

export type Boot = { kind: 'ready' } | { kind: 'setup'; reason: string }

/** Load cameras and the starting day. Falls back to the status page when there's nothing to play. */
export async function boot(): Promise<Boot> {
  let status: IndexStatus
  let cameras: Camera[]
  try {
    ;[status, cameras] = await Promise.all([getJSON<IndexStatus>('/api/status'), getJSON<Camera[]>('/api/cameras')])
  } catch (e) {
    if (e instanceof HttpError && e.status === 503) return { kind: 'setup', reason: 'No recordings server is set up yet.' }
    return { kind: 'setup', reason: `Couldn’t load the index: ${e instanceof Error ? e.message : String(e)}` }
  }
  if (!cameras.length || !status.index.latest_clip_ms) return { kind: 'setup', reason: 'No recordings have been indexed yet.' }

  const tz = status.time_zone
  const q = new URLSearchParams(location.search)
  const urlT = Number(q.get('t'))
  const urlCam = Number(q.get('cam'))
  const t = urlT > 0 ? urlT : status.index.latest_clip_ms
  const today = dateOf(Date.now(), tz)
  app.set({ tz, cameras, today, cam: cameras.some((c) => c.id === urlCam) ? urlCam : cameras[0].id })
  await loadDay(dateOf(t, tz), { at: t })
  const s = app.get()
  if (!urlCam && s.day) {
    // Start on the camera with the newest clip, at that clip.
    let latest: Clip | null = null
    for (const arr of s.day.byCam.values()) {
      const c = arr[arr.length - 1]
      if (c && (!latest || c.start > latest.start)) latest = c
    }
    if (latest && !urlT) {
      app.set({ cam: latest.camera_id })
      seekTo(latest.start)
    } else if (latest && !camClips(s).length) {
      app.set({ cam: latest.camera_id })
    }
  }
  void refresh()
  return { kind: 'ready' }
}

/** Keep ?cam=&t= in the address bar so a reload or shared link returns to the same moment. */
export function syncURL() {
  const s = app.get()
  if (!s.day) return
  const q = new URLSearchParams(location.search)
  const t = String(Math.floor(s.t / 1000) * 1000)
  if (q.get('t') === t && q.get('cam') === String(s.cam)) return
  q.set('cam', String(s.cam))
  q.set('t', t)
  history.replaceState(history.state, '', `${location.pathname}?${q}${location.hash}`)
}
