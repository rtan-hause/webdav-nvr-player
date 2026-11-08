import type { Span } from '../lib/time'
import { app, type AppState } from './state'

// The timeline's view: which stretch of the day it shows (view0, span).

export const MIN_SPAN = 4 * 60_000
const ZOOMS = [6 * 3600_000, 3600_000, 15 * 60_000, 5 * 60_000]
const DAY_MS = 86_400_000

const daySpan = (s: AppState) => (s.day ? s.day.end - s.day.start : DAY_MS)

/** A view start that keeps a view of this span inside the day. */
export function clampView(view0: number, span: number, day: Span | null): number {
  if (!day) return view0
  return Math.min(day.end - span, Math.max(day.start, view0))
}

/** View change that keeps t on screen. While playing (paging) it pages forward. */
export function follow(s: AppState, t: number, paging: boolean): Partial<AppState> {
  if (s.viewLocked) return {}
  const a = s.view0
  const b = s.view0 + s.span
  if (paging && t > b - s.span * 0.02 && t <= b + s.span * 0.5) return { view0: clampView(t - s.span * 0.1, s.span, s.day) }
  if (t < a || t > b) return { view0: clampView(t - s.span / 2, s.span, s.day) }
  return {}
}

export function setView(view0: number, span?: number) {
  app.set((s) => {
    const sp = Math.min(daySpan(s), Math.max(MIN_SPAN, span ?? s.span))
    return { span: sp, view0: clampView(view0, sp, s.day) }
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
  const next = dir > 0 ? (levels.find((z) => z < s.span * 0.98) ?? MIN_SPAN) : ([...levels].reverse().find((z) => z > s.span * 1.02) ?? daySpan(s))
  const f = (s.t - s.view0) / s.span
  zoomAround(next, s.t, f >= 0 && f <= 1 ? f : 0.5)
}
