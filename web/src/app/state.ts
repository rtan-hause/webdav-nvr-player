import type { Camera, Clip } from '../lib/api'
import { itemAt } from '../lib/clips'
import { loadSetting } from '../lib/storage'
import { createStore } from '../lib/store'
import type { Span } from '../lib/time'

// App state for the player: one playhead (t) on one day for the selected
// camera. Built around a single clock so a synced multi-camera grid can be
// added later (plan decision 6).
//
// This module holds the state, where it starts, and selectors. The actions
// that change it live beside it: view (zoom and pan), playhead, days (loading
// and moving between days, boot), prefs, theater and url.

export type DayData = {
  date: string // YYYY-MM-DD in the camera time zone
  start: number
  end: number
  byCam: Map<number, Clip[]> // sorted by start
  spans: Map<number, Span[]> // clips merged where they touch
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

/** localStorage keys for the preferences remembered between visits. */
export const PREF_KEYS = { rate: 'nvr.playbackRate', newestFirst: 'nvr.newestFirst' }

const savedRate = Number(loadSetting(PREF_KEYS.rate))

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
  newestFirst: loadSetting(PREF_KEYS.newestFirst) !== 'false',
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
/** The clip the player is showing, if any. */
export const shownClip = (s: AppState) => (s.shown === null ? null : (camClips(s).find((c) => c.id === s.shown) ?? null))

let toastId = 0
export const toast = (msg: string) => app.set({ toast: { msg, id: ++toastId } })
