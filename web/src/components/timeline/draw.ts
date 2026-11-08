import type { AppState, DayData } from '../../app/state'
import { lastStartLE } from '../../lib/clips'
import { fmtHM, fmtTime, localTicks } from '../../lib/time'
import { AXIS, lanes, MINI, overviewScale, timeScale, type Lanes, type Scale } from './geometry'

// Drawing the timeline canvas, back to front: background, overview strip,
// time not yet recorded, ticks, clips and snapshots, playhead, camera names.

function palette() {
  const cs = getComputedStyle(document.documentElement)
  const v = (name: string, fallback: string) => cs.getPropertyValue(`--color-${name}`).trim() || fallback
  return {
    ink0: v('ink-0', '#0c1114'),
    ink1: v('ink-1', '#131a1e'),
    ink2: v('ink-2', '#1b2429'),
    line: v('line', '#2a373f'),
    muted: v('muted', '#8c9ba2'),
    sodium: v('sodium', '#f2a33a'),
    clip: v('clip', '#3e7898'),
    clipHi: v('clip-hi', '#8cc3df'),
    snap: v('snap', '#c9d6db'),
    mono: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
    sans: 'Barlow, system-ui, sans-serif',
  }
}
type Colors = ReturnType<typeof palette>

// The smallest round step that keeps labels at least 56 px apart.
const STEPS = [30_000, 60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000, 15 * 60_000, 30 * 60_000, 3600_000, 2 * 3600_000, 3 * 3600_000, 6 * 3600_000]
function tickStep(span: number, width: number): number {
  return STEPS.find((st) => (st * width) / span >= 56) ?? STEPS[STEPS.length - 1]
}

/** Camera names shorten to their first word on a narrow timeline. */
const cameraLabel = (name: string, canvasWidth: number) => (canvasWidth < 560 ? name.split(/\s+/)[0] : name)

/** Everything one frame's layers need. */
type Frame = {
  ctx: CanvasRenderingContext2D
  s: AppState
  day: DayData
  colors: Colors
  width: number
  height: number
  bottom: number // top of the time axis
  sc: Scale
  lanes: Lanes
  label: (name: string) => string
}

/**
 * Returns a function that draws the timeline for a state and returns the
 * width of the camera-name gutter it laid out (which input handling needs).
 */
export function createPainter(canvas: HTMLCanvasElement): (s: AppState) => number {
  const ctx = canvas.getContext('2d')!
  const colors = palette()
  let hatch: CanvasPattern | null = null
  let gutter = 0

  return (s) => {
    const r = canvas.getBoundingClientRect()
    const d = window.devicePixelRatio || 1
    const width = r.width
    const height = r.height
    if (canvas.width !== Math.round(width * d) || canvas.height !== Math.round(height * d)) {
      canvas.width = Math.max(1, Math.round(width * d))
      canvas.height = Math.max(1, Math.round(height * d))
    }
    ctx.setTransform(d, 0, 0, d, 0, 0)
    ctx.clearRect(0, 0, width, height)
    const theater = s.layout === 'theater'
    ctx.fillStyle = theater ? 'rgba(14,19,22,.55)' : colors.ink1
    ctx.fillRect(0, 0, width, height)
    const day = s.day
    if (!day || width < 10) return gutter
    hatch ??= hatchPattern(ctx, colors)

    const label = (name: string) => cameraLabel(name, canvas.clientWidth)
    ctx.font = `600 12px ${colors.sans}`
    const widest = Math.max(0, ...s.cameras.map((c) => ctx.measureText(label(c.name)).width))
    gutter = Math.round(Math.min(widest + 20, width * 0.32))
    const f: Frame = {
      ctx,
      s,
      day,
      colors,
      width,
      height,
      bottom: height - AXIS,
      sc: timeScale(gutter, width, s.view0, s.span),
      lanes: lanes(canvas.clientHeight, s.cameras.length),
      label,
    }

    drawOverview(f)
    // Everything time-based below stays right of the name column.
    ctx.save()
    ctx.beginPath()
    ctx.rect(gutter, 0, f.sc.plotWidth, height)
    ctx.clip()
    drawFuture(f, hatch)
    drawTicks(f)
    drawLanes(f)
    drawPlayhead(f)
    ctx.restore()
    drawNames(f)
    return gutter
  }
}

/** Diagonal lines for time that hasn't happened yet. */
function hatchPattern(ctx: CanvasRenderingContext2D, colors: Colors): CanvasPattern | null {
  const hc = document.createElement('canvas')
  hc.width = hc.height = 8
  const hx = hc.getContext('2d')!
  hx.strokeStyle = colors.line
  hx.beginPath()
  hx.moveTo(0, 8)
  hx.lineTo(8, 0)
  hx.stroke()
  return ctx.createPattern(hc, 'repeat')
}

/** Overview strip: the whole day, the selected camera's activity, the visible window. */
function drawOverview({ ctx, s, day, colors, sc }: Frame) {
  const ov = overviewScale(sc, day)
  // Bar from y=4 to MINI-4; the window outline and playhead tick stand a little proud of it.
  const barH = MINI - 8
  ctx.fillStyle = colors.ink2
  ctx.beginPath()
  ctx.roundRect(sc.gutter + 4, 4, sc.plotWidth - 8, barH, 4)
  ctx.fill()
  ctx.fillStyle = 'rgba(140,195,223,.5)'
  for (const sp of day.spans.get(s.cam) ?? []) ctx.fillRect(ov.toX(sp.start), 6, Math.max(1, ov.toX(sp.end) - ov.toX(sp.start)), barH - 4)
  if (s.span < day.end - day.start - 1000) {
    ctx.strokeStyle = colors.sodium
    ctx.lineWidth = 1.5
    ctx.beginPath()
    ctx.roundRect(ov.toX(s.view0), 2.5, Math.max(6, ov.toX(s.view0 + s.span) - ov.toX(s.view0)), barH + 3, 4)
    ctx.stroke()
  }
  ctx.fillStyle = colors.sodium
  ctx.fillRect(ov.toX(s.t) - 1, 2, 2, barH + 4)
}

/** Time that hasn't happened yet: hatched, with a NOW line. */
function drawFuture({ ctx, day, colors, width, bottom, sc, lanes }: Frame, hatch: CanvasPattern | null) {
  const now = Date.now()
  const x = sc.toX(now)
  if (now >= day.end || x >= width) return
  const a = Math.max(sc.gutter, x)
  ctx.fillStyle = hatch!
  ctx.fillRect(a, lanes.top, width - a, bottom - lanes.top)
  if (x > sc.gutter) {
    ctx.fillStyle = colors.muted
    ctx.fillRect(x, lanes.top, 1, bottom - lanes.top)
    ctx.font = `500 10px ${colors.mono}`
    ctx.textAlign = 'right'
    ctx.textBaseline = 'alphabetic'
    ctx.fillText('NOW', x - 4, bottom - 4)
  }
}

/** Grid lines and labels at round local times. */
function drawTicks({ ctx, s, colors, width, height, bottom, sc, lanes }: Frame) {
  const step = tickStep(s.span, sc.plotWidth)
  ctx.font = `500 11px ${colors.mono}`
  ctx.textBaseline = 'alphabetic'
  for (const tt of localTicks(s.view0, s.view0 + s.span, step, s.tz)) {
    const x = Math.round(sc.toX(tt)) + 0.5
    ctx.fillStyle = 'rgba(140,155,162,.16)'
    ctx.fillRect(x, lanes.top, 1, bottom - lanes.top)
    ctx.fillStyle = colors.muted
    ctx.textAlign = x < sc.gutter + 24 ? 'left' : x > width - 24 ? 'right' : 'center'
    ctx.fillText(step < 60_000 ? fmtTime(tt, s.tz) : fmtHM(tt, s.tz), x, height - 6)
  }
}

/** One lane per camera: its clips (merged when zoomed out) and loose snapshots. */
function drawLanes({ ctx, s, day, colors, width, sc, lanes }: Frame) {
  const merged = s.span >= 3 * 3600_000
  s.cameras.forEach((cam, i) => {
    const y = lanes.y(i)
    const sel = cam.id === s.cam
    const arr = (merged ? day.spans.get(cam.id) : day.byCam.get(cam.id)) ?? []
    ctx.fillStyle = sel ? colors.clipHi : colors.clip
    for (let k = Math.max(0, lastStartLE(arr, s.view0)); k < arr.length && arr[k].start <= s.view0 + s.span; k++) {
      const x1 = sc.toX(arr[k].start)
      const x2 = sc.toX(arr[k].end)
      if (x2 < sc.gutter) continue
      ctx.fillRect(x1, y + 2, Math.max(1.5, x2 - x1 - (merged ? 0 : 1)), lanes.laneHeight - 4)
    }
    ctx.fillStyle = colors.snap
    for (const ts of day.snaps.get(cam.id) ?? []) {
      const x = sc.toX(ts)
      if (x < sc.gutter - 3 || x > width + 3) continue
      ctx.beginPath()
      ctx.arc(x, y + lanes.laneHeight / 2, 2, 0, 7)
      ctx.fill()
    }
  })
}

function drawPlayhead({ ctx, s, colors, width, bottom, sc, lanes }: Frame) {
  const px = sc.toX(s.t)
  if (px < sc.gutter - 6 || px > width + 6) return
  ctx.fillStyle = colors.sodium
  ctx.fillRect(Math.round(px) - 1, lanes.top - 2, 2, bottom - lanes.top + 6)
  ctx.beginPath()
  ctx.moveTo(px - 6, lanes.top - 4)
  ctx.lineTo(px + 6, lanes.top - 4)
  ctx.lineTo(px, lanes.top + 3)
  ctx.fill()
}

/** Camera names, in their own column; the selected lane is tinted across the full width. */
function drawNames({ ctx, s, colors, width, bottom, sc, lanes, label }: Frame) {
  s.cameras.forEach((cam, i) => {
    const y = lanes.y(i)
    const sel = cam.id === s.cam
    if (sel) {
      ctx.fillStyle = 'rgba(140,195,223,.07)'
      ctx.fillRect(0, y, width, lanes.laneHeight)
    }
    let text = label(cam.name)
    ctx.font = `${sel ? 600 : 500} 12px ${colors.sans}`
    while (text.length > 1 && ctx.measureText(text).width > sc.gutter - 16) text = text.slice(0, -2) + '…'
    ctx.fillStyle = sel ? colors.clipHi : colors.muted
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    ctx.fillText(text, 8, y + lanes.laneHeight / 2 + 1)
    ctx.textBaseline = 'alphabetic'
  })
  ctx.fillStyle = colors.line
  ctx.fillRect(sc.gutter - 1, lanes.top - 2, 1, bottom - lanes.top + 2)
}
