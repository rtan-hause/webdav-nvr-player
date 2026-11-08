import { fetchCameras, fetchClips, fetchDays, fetchStatus, HttpError, type Camera, type Clip, type IndexStatus, type Snapshot } from '../lib/api'
import { itemAt, mergeSpans } from '../lib/clips'
import { addDays, atLocalTime, dateOf, dayBounds, fmtDay, type Span } from '../lib/time'
import { clampT, seekTo } from './playhead'
import { app, camClips, cameraName, toast, type DayData } from './state'
import { positionInURL } from './url'
import { clampView } from './view'

// Loading days, moving between them, and starting the app.

function dayData(date: string, tz: string, clips: Clip[], snaps: Pick<Snapshot, 'camera_id' | 'ts'>[]): DayData {
  const { start, end } = dayBounds(date, tz)
  const byCam = new Map<number, Clip[]>()
  for (const c of clips) {
    let arr = byCam.get(c.camera_id)
    if (!arr) byCam.set(c.camera_id, (arr = []))
    arr.push(c)
  }
  const spans = new Map<number, Span[]>()
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
function landing(data: DayData, cam: number, sameTime: number, now: number): number {
  const t = Math.min(clampT(sameTime, data), now)
  const clips = data.byCam.get(cam) ?? []
  if (!clips.length || itemAt(clips, t)) return t
  let best = clips[0]
  for (const c of clips) if (Math.abs(c.start - t) < Math.abs(best.start - t)) best = c
  return best.start
}

/** The latest clip of the day from any camera. */
function newestClip(day: DayData): Clip | null {
  let latest: Clip | null = null
  for (const arr of day.byCam.values()) {
    const c = arr.at(-1)
    if (c && (!latest || c.start > latest.start)) latest = c
  }
  return latest
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
      const t = opts.at !== undefined ? clampT(opts.at, data) : landing(data, s.cam, s.day ? start + (s.t - s.day.start) : start, Date.now())
      const span = Math.min(s.span, end - start)
      return { day: data, dayLoading: false, t, seek: s.seek + 1, span, view0: clampView(t - span / 2, span, data) }
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
  const own = camClips(s).at(-1)
  if (own) return seekTo(own.start)
  const latest = newestClip(s.day)
  if (!latest) return toast(`No recordings on ${date === s.today ? 'today' : fmtDay(date)}`)
  toast(`No recordings from ${cameraName(s)}; showing ${cameraName(s, latest.camera_id)}`)
  app.set({ cam: latest.camera_id })
  seekTo(latest.start)
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
    ;[status, cameras] = await Promise.all([fetchStatus(), fetchCameras()])
  } catch (e) {
    if (e instanceof HttpError && e.status === 503) return { kind: 'setup', reason: 'No recordings server is set up yet.' }
    return { kind: 'setup', reason: `Couldn’t load the index: ${e instanceof Error ? e.message : String(e)}` }
  }
  if (!cameras.length || !status.index.latest_clip_ms) return { kind: 'setup', reason: 'No recordings have been indexed yet.' }

  const tz = status.time_zone
  const url = positionInURL()
  const t = url.t > 0 ? url.t : status.index.latest_clip_ms
  const today = dateOf(Date.now(), tz)
  app.set({ tz, cameras, today, cam: cameras.some((c) => c.id === url.cam) ? url.cam : cameras[0].id })
  await loadDay(dateOf(t, tz), { at: t })
  const s = app.get()
  if (!url.cam && s.day) {
    // Start on the camera with the newest clip, at that clip.
    const latest = newestClip(s.day)
    if (latest && !url.t) {
      app.set({ cam: latest.camera_id })
      seekTo(latest.start)
    } else if (latest && !camClips(s).length) {
      app.set({ cam: latest.camera_id })
    }
  }
  void refresh()
  return { kind: 'ready' }
}
