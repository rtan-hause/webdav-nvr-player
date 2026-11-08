import type { Span } from '../../lib/time'

// Timeline canvas geometry, shared by the drawing, the input handling and the
// dock that sizes the canvas. All sizes are CSS pixels.
//
//   ┌────────┬───────────────────────────────┐  0
//   │        │ overview strip (whole day)    │  MINI
//   │ camera ├───────────────────────────────┤  top = MINI + 4
//   │ names  │ lane per camera, LGAP between │
//   │        ├───────────────────────────────┤  height - AXIS
//   │        │ time axis labels              │  height
//   └────────┴───────────────────────────────┘
//   0     gutter                           width

export const AXIS = 20 // time labels at the bottom
export const MINI = 24 // overview strip at the top
export const LGAP = 3 // between lanes
export const LANE_H = 24 // default lane height; the user can drag the timeline taller or shorter
export const MIN_LANE = 14
export const MAX_LANE = 200

/** Canvas height for this many camera lanes of laneH pixels each. */
export const timelineHeight = (cameras: number, laneH = LANE_H) => MINI + 4 + cameras * laneH + Math.max(0, cameras - 1) * LGAP + AXIS

/** Maps between time and x for the view [view0, view0 + span), drawn right of the camera-name gutter. */
export type Scale = {
  gutter: number
  plotWidth: number
  toX(t: number): number
  toT(x: number): number
}

export function timeScale(gutter: number, width: number, view0: number, span: number): Scale {
  const plotWidth = Math.max(1, width - gutter)
  return {
    gutter,
    plotWidth,
    toX: (t) => gutter + ((t - view0) * plotWidth) / span,
    toT: (x) => view0 + ((x - gutter) * span) / plotWidth,
  }
}

/** The overview strip's mapping: the whole day across the plot, inset 4 px each side. */
export function overviewScale(sc: Scale, day: Span) {
  const len = day.end - day.start
  return {
    toX: (t: number) => sc.gutter + 4 + ((t - day.start) / len) * (sc.plotWidth - 8),
    toT: (x: number) => day.start + ((x - sc.gutter - 4) / (sc.plotWidth - 8)) * len,
  }
}

/** Where the camera lanes sit on a canvas of this height. */
export type Lanes = {
  top: number
  laneHeight: number
  y(i: number): number
  /** The lane under y (with a pixel of slack), or -1. */
  at(y: number, count: number): number
}

export function lanes(height: number, cameras: number): Lanes {
  const n = Math.max(1, cameras)
  const top = MINI + 4
  const laneHeight = (height - AXIS - top - LGAP * (n - 1)) / n
  const y = (i: number) => top + i * (laneHeight + LGAP)
  return {
    top,
    laneHeight,
    y,
    at(py, count) {
      for (let i = 0; i < count; i++) if (py >= y(i) - 1 && py <= y(i) + laneHeight + 1) return i
      return -1
    },
  }
}

/** Which band of the canvas y is in. */
export const region = (y: number, height: number) => (y < MINI + 2 ? 'mini' : y > height - AXIS ? 'axis' : 'lane')
