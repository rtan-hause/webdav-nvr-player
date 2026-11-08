import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Camera, Clip } from '../lib/api'
import { boot, goToTime, jumpToLatest, loadDay } from './days'
import { jumpClip, playbackAt, playOnFrom, seekTo, selectCamera } from './playhead'
import { stepRate } from './prefs'
import { app } from './state'
import { MIN_SPAN, setView, zoomAround, zoomStep } from './view'

// The player's actions, driven through the store as the UI drives them. The
// server is a stubbed fetch; the clock is fixed at NOW.

const M = 60_000
const H = 60 * M
const DAY = '2026-11-06'
const T0 = Date.UTC(2026, 10, 6) // DAY starts here (UTC cameras)
const NOW = Date.UTC(2026, 10, 7, 12) // the next day, noon
const CAMS: Camera[] = [
  { id: 1, name: 'Front', channel: '00' },
  { id: 2, name: 'Garage', channel: '00' },
]

let nextID = 1
function clip(cam: number, startMin: number, lenMin: number): Clip {
  const start = T0 + startMin * M
  return { id: nextID++, camera_id: cam, start, end: start + lenMin * M, codec: 'H.264', width: 640, height: 360, size: 1, file: 'x.mp4', thumb: false }
}

// Camera 1: 08:00-08:10, 08:10-08:20 (touching), 12:00-12:05. Camera 2: 20:00-20:01.
const front = [clip(1, 8 * 60, 10), clip(1, 8 * 60 + 10, 10), clip(1, 12 * 60, 5)]
const garage = [clip(2, 20 * 60, 1)]
let server: { clips: Clip[]; snapshots: { id: number; camera_id: number; ts: number }[]; days: string[] }

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

const initial = app.get()

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'], now: NOW })
  server = { clips: [...front, ...garage], snapshots: [{ id: 1, camera_id: 2, ts: T0 + 3 * H }], days: [DAY] }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url.startsWith('/api/clips')) return json({ clips: server.clips, snapshots: server.snapshots })
      if (url.startsWith('/api/days')) return json({ days: server.days })
      if (url === '/api/status') return json({ time_zone: 'UTC', index: { latest_clip_ms: garage[0].start } })
      if (url === '/api/cameras') return json(CAMS)
      return new Response('{}', { status: 404 })
    }),
  )
  vi.stubGlobal('location', { search: '', pathname: '/', hash: '' })
  app.set({ ...initial, tz: 'UTC', cameras: CAMS, today: '2026-11-07', days: [DAY], cam: 1 })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/** Load DAY with the playhead at `at` and the whole day in view. */
async function onDay(at = T0 + 8 * H) {
  await loadDay(DAY, { at })
  setView(T0, 24 * H)
}

describe('loading a day', () => {
  it('groups clips by camera, merges touching clips and keeps loose snapshots', async () => {
    await onDay()
    const d = app.get().day!
    expect(d).toMatchObject({ date: DAY, start: T0, end: T0 + 24 * H })
    expect(d.byCam.get(1)).toEqual(front)
    expect(d.spans.get(1)).toEqual([
      { start: front[0].start, end: front[1].end },
      { start: front[2].start, end: front[2].end },
    ])
    expect(d.snaps.get(2)).toEqual([T0 + 3 * H])
  })

  it('puts the playhead at `at` (clamped to the day) and centres the view on it', async () => {
    app.set({ span: 2 * H })
    await loadDay(DAY, { at: T0 + 10 * H })
    expect(app.get()).toMatchObject({ t: T0 + 10 * H, span: 2 * H, view0: T0 + 9 * H })
    await loadDay(DAY, { at: T0 - 5 * H })
    expect(app.get()).toMatchObject({ t: T0, view0: T0 })
  })

  it('without `at`, keeps the time of day if the camera recorded then, else goes to its nearest clip', async () => {
    await loadDay(DAY, { at: T0 + 8 * H + 5 * M })
    server.clips = front.map((c) => ({ ...c, start: c.start + 24 * H, end: c.end + 24 * H }))
    await loadDay('2026-11-07')
    expect(app.get().t).toBe(T0 + 24 * H + 8 * H + 5 * M) // same time of day, inside a clip

    app.set({ t: T0 + 24 * H + 11 * H }) // 11:00: in a gap; 12:00 is nearer than 08:10
    server.clips = [...front, ...garage]
    await loadDay(DAY)
    expect(app.get().t).toBe(front[2].start)
  })

  it('never lands after now', async () => {
    server.clips = []
    await loadDay(DAY, { at: T0 + 23 * H })
    await loadDay('2026-11-07') // 23:00 tomorrow is after NOW
    expect(app.get().t).toBe(NOW)
  })

  it('a quiet refresh updates clips without moving anything', async () => {
    await onDay(T0 + 9 * H)
    const before = app.get()
    server.clips = [...front, ...garage, clip(1, 22 * 60, 1)]
    await loadDay(DAY, { quiet: true })
    const s = app.get()
    expect(s.day!.byCam.get(1)).toHaveLength(4)
    expect({ t: s.t, seek: s.seek, view0: s.view0, span: s.span }).toEqual({ t: before.t, seek: before.seek, view0: before.view0, span: before.span })
  })

  it('reports a failed load', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'boom' }), { status: 502 })))
    await loadDay(DAY)
    expect(app.get()).toMatchObject({ dayLoading: false, dayError: 'boom' })
  })
})

describe('the view (zoom and pan)', () => {
  it('setView keeps the span between MIN_SPAN and the day, and the view inside the day', async () => {
    await onDay()
    setView(T0 - H, M)
    expect(app.get()).toMatchObject({ view0: T0, span: MIN_SPAN })
    setView(T0 + 23 * H, 2 * H)
    expect(app.get()).toMatchObject({ view0: T0 + 22 * H, span: 2 * H })
    setView(T0, 48 * H)
    expect(app.get()).toMatchObject({ view0: T0, span: 24 * H })
  })

  it('zoomAround keeps the anchor time at the same fraction of the width', async () => {
    await onDay()
    zoomAround(H, T0 + 10 * H, 0.25)
    expect(app.get()).toMatchObject({ view0: T0 + 10 * H - 15 * M, span: H })
  })

  it('zoomStep walks 24 h, 6 h, 1 h, 15 min, 5 min, keeping the playhead in place', async () => {
    await onDay(T0 + 12 * H)
    const spans = []
    for (let i = 0; i < 5; i++) {
      zoomStep(1)
      spans.push(app.get().span)
    }
    expect(spans).toEqual([6 * H, H, 15 * M, 5 * M, MIN_SPAN])
    const s = app.get()
    expect((s.t - s.view0) / s.span).toBeCloseTo(0.5)
    zoomStep(-1)
    expect(app.get().span).toBe(5 * M)
    for (let i = 0; i < 5; i++) zoomStep(-1)
    expect(app.get()).toMatchObject({ span: 24 * H, view0: T0 })
  })
})

describe('the playhead', () => {
  it('seekTo clamps to the day, asks the player to seek, and brings an off-screen playhead into view', async () => {
    await onDay()
    setView(T0, 2 * H)
    const seek = app.get().seek
    seekTo(T0 + H)
    expect(app.get()).toMatchObject({ t: T0 + H, seek: seek + 1, view0: T0 }) // on screen: view stays
    seekTo(T0 + 10 * H)
    expect(app.get()).toMatchObject({ view0: T0 + 9 * H }) // off screen: centred
    seekTo(T0 + 30 * H)
    expect(app.get().t).toBe(T0 + 24 * H - 1)
  })

  it('playback pages the view forward near its right edge, unless the user is holding the view', async () => {
    await onDay()
    setView(T0, 2 * H)
    playbackAt(T0 + 2 * H - M)
    expect(app.get().view0).toBe(T0 + 2 * H - M - 12 * M) // 10% of the span before t
    app.set({ viewLocked: true })
    playbackAt(T0 + 5 * H)
    expect(app.get().view0).toBe(T0 + 2 * H - 13 * M)
  })

  it('jumpClip goes to the next clip, back to the start of this one, or to the previous one', async () => {
    await onDay(front[1].start + 5 * M)
    jumpClip(1)
    expect(app.get().t).toBe(front[2].start)
    seekTo(front[1].start + 5 * M)
    jumpClip(-1)
    expect(app.get().t).toBe(front[1].start)
    jumpClip(-1) // within 2 s of the start: previous clip
    expect(app.get().t).toBe(front[0].start)
  })

  it('playOnFrom skips a gap with a toast, and stops at the last clip', async () => {
    await onDay()
    app.set({ playing: true })
    playOnFrom(front[1].end, front[1])
    expect(app.get().t).toBe(front[2].start)
    expect(app.get().toast?.msg).toBe('Skipped 3h 40m with no recording')
    playOnFrom(front[2].end, front[2])
    expect(app.get()).toMatchObject({ playing: false })
    expect(app.get().toast?.msg).toBe('End of recordings from Front for this day')
  })

  it('selectCamera switches camera and asks the player to seek', async () => {
    await onDay()
    const seek = app.get().seek
    selectCamera(2)
    expect(app.get()).toMatchObject({ cam: 2, seek: seek + 1 })
    selectCamera(2)
    expect(app.get().seek).toBe(seek + 1)
  })

  it('goToTime seeks on the loaded day, loads another day, and never goes past now', async () => {
    await onDay()
    goToTime(DAY, '12:01')
    expect(app.get().t).toBe(T0 + 12 * H + M)
    goToTime('2026-11-07', '23:00')
    await vi.waitFor(() => expect(app.get().day?.date).toBe('2026-11-07'))
    expect(app.get().t).toBe(NOW)
    goToTime('', '12:00')
    expect(app.get().toast?.msg).toBe('Pick a date and a time')
  })

  it('jumpToLatest goes to the latest clip, switching to the camera that recorded last if needed', async () => {
    await onDay()
    await jumpToLatest()
    expect(app.get()).toMatchObject({ cam: 1, t: front[2].start })

    server.clips = garage
    await loadDay(DAY, { quiet: true }) // jumpToLatest only re-fetches today
    await jumpToLatest()
    expect(app.get()).toMatchObject({ cam: 2, t: garage[0].start })
    expect(app.get().toast?.msg).toBe('No recordings from Front; showing Garage')
  })
})

describe('settings', () => {
  it('stepRate moves through the rates and stops at the ends', () => {
    app.set({ rate: 1 })
    stepRate(1)
    expect(app.get().rate).toBe(1.5)
    stepRate(1)
    stepRate(1)
    expect(app.get().rate).toBe(2)
    for (let i = 0; i < 5; i++) stepRate(-1)
    expect(app.get().rate).toBe(0.5)
  })
})

describe('boot', () => {
  it('starts on the camera with the newest clip, at that clip', async () => {
    app.set({ ...initial })
    expect(await boot()).toEqual({ kind: 'ready' })
    expect(app.get()).toMatchObject({ tz: 'UTC', cameras: CAMS, cam: 2, t: garage[0].start })
  })

  it('returns to the camera and time in the address bar', async () => {
    app.set({ ...initial })
    vi.stubGlobal('location', { search: `?cam=1&t=${front[0].start + M}`, pathname: '/', hash: '' })
    await boot()
    expect(app.get()).toMatchObject({ cam: 1, t: front[0].start + M })
  })

  it('falls back to the status page when nothing is indexed', async () => {
    app.set({ ...initial })
    server.clips = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => (url === '/api/cameras' ? json([]) : json({ time_zone: 'UTC', index: { latest_clip_ms: 0 } }))),
    )
    expect(await boot()).toEqual({ kind: 'setup', reason: 'No recordings have been indexed yet.' })
  })
})
