import { useEffect, useRef } from 'react'
import { app, endScrub, scrubTo, selectCamera, setView, zoomAround } from '../app/state'
import { lastStartLE } from '../lib/clips'
import { fmtHM, fmtTime, localTicks } from '../lib/time'
import { AXIS, LGAP, MINI } from './timelineGeom'

// The day timeline (plan decision 16): a fixed, zoomable day with a moving
// playhead, one lane per camera. Drawn on a canvas because a busy day has over
// a thousand clips.
//
//  - camera names sit in their own column on the left; the day starts after it
//  - lanes: press or drag to move the playhead; a tap also picks that camera
//    (so does a tap on the name)
//  - overview strip (top) and time axis (bottom): drag to move along the day
//  - wheel zooms around the pointer; shift+wheel or a sideways swipe pans;
//    two fingers pinch-zoom and pan


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

// The smallest round step that keeps labels at least 56 px apart.
const STEPS = [30_000, 60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000, 15 * 60_000, 30 * 60_000, 3600_000, 2 * 3600_000, 3 * 3600_000, 6 * 3600_000]
function tickStep(span: number, width: number): number {
  return STEPS.find((st) => (st * width) / span >= 56) ?? STEPS[STEPS.length - 1]
}

export function Timeline() {
  const ref = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = ref.current!
    const ctx = canvas.getContext('2d')!
    const C = palette()
    let dirty = true
    let hatch: CanvasPattern | null = null

    const geom = () => {
      const s = app.get()
      const n = Math.max(1, s.cameras.length)
      const top = MINI + 4
      const laneH = (canvas.clientHeight - AXIS - top - LGAP * (n - 1)) / n
      return { top, laneH, y: (i: number) => top + i * (laneH + LGAP) }
    }
    // Width of the camera-name column, measured on each draw.
    let gutter = 0
    const plotW = () => Math.max(1, canvas.clientWidth - gutter)
    const T = (x: number) => {
      const s = app.get()
      return s.view0 + ((x - gutter) * s.span) / plotW()
    }
    const narrowNames = () => canvas.clientWidth < 560
    const label = (name: string) => (narrowNames() ? name.split(/\s+/)[0] : name)

    function draw() {
      const s = app.get()
      const r = canvas.getBoundingClientRect()
      const d = window.devicePixelRatio || 1
      const W = r.width
      const H = r.height
      if (canvas.width !== Math.round(W * d) || canvas.height !== Math.round(H * d)) {
        canvas.width = Math.max(1, Math.round(W * d))
        canvas.height = Math.max(1, Math.round(H * d))
      }
      ctx.setTransform(d, 0, 0, d, 0, 0)
      ctx.clearRect(0, 0, W, H)
      const theater = s.layout === 'theater'
      ctx.fillStyle = theater ? 'rgba(14,19,22,.55)' : C.ink1
      ctx.fillRect(0, 0, W, H)
      const day = s.day
      if (!day || W < 10) return
      if (!hatch) {
        const hc = document.createElement('canvas')
        hc.width = hc.height = 8
        const hx = hc.getContext('2d')!
        hx.strokeStyle = C.line
        hx.beginPath()
        hx.moveTo(0, 8)
        hx.lineTo(8, 0)
        hx.stroke()
        hatch = ctx.createPattern(hc, 'repeat')
      }
      ctx.font = `600 12px ${C.sans}`
      const widest = Math.max(0, ...s.cameras.map((c) => ctx.measureText(label(c.name)).width))
      gutter = Math.round(Math.min(widest + 20, W * 0.32))
      const L = gutter
      const PW = W - L
      const X = (t: number) => L + ((t - s.view0) * PW) / s.span
      const { top, laneH, y: laneY } = geom()
      const dayLen = day.end - day.start
      const bottom = H - AXIS

      // Overview strip: the whole day, the selected camera's activity, the visible window.
      const mx = (t: number) => L + 4 + ((t - day.start) / dayLen) * (PW - 8)
      ctx.fillStyle = C.ink2
      ctx.beginPath()
      ctx.roundRect(L + 4, 3, PW - 8, 8, 4)
      ctx.fill()
      ctx.fillStyle = 'rgba(140,195,223,.5)'
      for (const sp of day.spans.get(s.cam) ?? []) ctx.fillRect(mx(sp.start), 4, Math.max(1, mx(sp.end) - mx(sp.start)), 6)
      if (s.span < dayLen - 1000) {
        ctx.strokeStyle = C.sodium
        ctx.lineWidth = 1.5
        ctx.beginPath()
        ctx.roundRect(mx(s.view0), 1.5, Math.max(4, mx(s.view0 + s.span) - mx(s.view0)), 11, 3)
        ctx.stroke()
      }
      ctx.fillStyle = C.sodium
      ctx.fillRect(mx(s.t) - 1, 1, 2, 12)

      // Everything time-based below stays right of the name column.
      ctx.save()
      ctx.beginPath()
      ctx.rect(L, 0, PW, H)
      ctx.clip()

      // Time that hasn't happened yet.
      const now = Date.now()
      if (now < day.end && X(now) < W) {
        const a = Math.max(L, X(now))
        ctx.fillStyle = hatch!
        ctx.fillRect(a, top, W - a, bottom - top)
        if (X(now) > L) {
          ctx.fillStyle = C.muted
          ctx.fillRect(X(now), top, 1, bottom - top)
          ctx.font = `500 10px ${C.mono}`
          ctx.textAlign = 'right'
          ctx.textBaseline = 'alphabetic'
          ctx.fillText('NOW', X(now) - 4, bottom - 4)
        }
      }

      // Ticks and labels, at round local times.
      const step = tickStep(s.span, PW)
      ctx.font = `500 11px ${C.mono}`
      ctx.textBaseline = 'alphabetic'
      for (const tt of localTicks(s.view0, s.view0 + s.span, step, s.tz)) {
        const x = Math.round(X(tt)) + 0.5
        ctx.fillStyle = 'rgba(140,155,162,.16)'
        ctx.fillRect(x, top, 1, bottom - top)
        ctx.fillStyle = C.muted
        ctx.textAlign = x < L + 24 ? 'left' : x > W - 24 ? 'right' : 'center'
        ctx.fillText(step < 60_000 ? fmtTime(tt, s.tz) : fmtHM(tt, s.tz), x, H - 6)
      }

      // One lane per camera.
      const merged = s.span >= 3 * 3600_000
      s.cameras.forEach((cam, i) => {
        const y = laneY(i)
        const sel = cam.id === s.cam
        const arr = (merged ? day.spans.get(cam.id) : day.byCam.get(cam.id)) ?? []
        ctx.fillStyle = sel ? C.clipHi : C.clip
        for (let k = Math.max(0, lastStartLE(arr, s.view0)); k < arr.length && arr[k].start <= s.view0 + s.span; k++) {
          const x1 = X(arr[k].start)
          const x2 = X(arr[k].end)
          if (x2 < L) continue
          ctx.fillRect(x1, y + 2, Math.max(1.5, x2 - x1 - (merged ? 0 : 1)), laneH - 4)
        }
        ctx.fillStyle = C.snap
        for (const ts of day.snaps.get(cam.id) ?? []) {
          const x = X(ts)
          if (x < L - 3 || x > W + 3) continue
          ctx.beginPath()
          ctx.arc(x, y + laneH / 2, 2, 0, 7)
          ctx.fill()
        }
      })

      // Playhead.
      const px = X(s.t)
      if (px >= L - 6 && px <= W + 6) {
        ctx.fillStyle = C.sodium
        ctx.fillRect(Math.round(px) - 1, top - 2, 2, bottom - top + 6)
        ctx.beginPath()
        ctx.moveTo(px - 6, top - 4)
        ctx.lineTo(px + 6, top - 4)
        ctx.lineTo(px, top + 3)
        ctx.fill()
      }
      ctx.restore()

      // Camera names, in their own column; the selected lane is tinted across the full width.
      s.cameras.forEach((cam, i) => {
        const y = laneY(i)
        const sel = cam.id === s.cam
        if (sel) {
          ctx.fillStyle = 'rgba(140,195,223,.07)'
          ctx.fillRect(0, y, W, laneH)
        }
        let text = label(cam.name)
        ctx.font = `${sel ? 600 : 500} 12px ${C.sans}`
        while (text.length > 1 && ctx.measureText(text).width > L - 16) text = text.slice(0, -2) + '…'
        ctx.fillStyle = sel ? C.clipHi : C.muted
        ctx.textAlign = 'left'
        ctx.textBaseline = 'middle'
        ctx.fillText(text, 8, y + laneH / 2 + 1)
        ctx.textBaseline = 'alphabetic'
      })
      ctx.fillStyle = C.line
      ctx.fillRect(L - 1, top - 2, 1, bottom - top + 2)
    }

    // ---------- input ----------
    type Gesture =
      | { kind: 'scrub'; x0: number; moved: boolean }
      | { kind: 'pan'; x0: number; v0: number }
      | { kind: 'mini' }
      | { kind: 'name' } // tap on a camera name
      | { kind: 'pinch'; d0: number; span0: number; anchorT: number }
    const ptrs = new Map<number, { x: number; y: number }>()
    let g: Gesture | null = null
    const region = (y: number) => (y < MINI + 2 ? 'mini' : y > canvas.clientHeight - AXIS ? 'axis' : 'lane')
    const miniPan = (x: number) => {
      const s = app.get()
      if (!s.day) return
      setView(s.day.start + ((x - gutter - 4) / (plotW() - 8)) * (s.day.end - s.day.start) - s.span / 2)
    }

    const down = (e: PointerEvent) => {
      canvas.setPointerCapture(e.pointerId)
      ptrs.set(e.pointerId, { x: e.offsetX, y: e.offsetY })
      if (ptrs.size === 2) {
        if (g?.kind === 'scrub') endScrub()
        const [a, b] = [...ptrs.values()]
        g = { kind: 'pinch', d0: Math.hypot(a.x - b.x, a.y - b.y) || 1, span0: app.get().span, anchorT: T((a.x + b.x) / 2) }
        app.set({ viewLocked: true })
        return
      }
      if (ptrs.size > 2) return
      const r = region(e.offsetY)
      if (e.offsetX < gutter) {
        g = { kind: 'name' }
      } else if (r === 'mini') {
        g = { kind: 'mini' }
        app.set({ viewLocked: true })
        miniPan(e.offsetX)
      } else if (r === 'axis') {
        g = { kind: 'pan', x0: e.offsetX, v0: app.get().view0 }
        app.set({ viewLocked: true })
      } else {
        g = { kind: 'scrub', x0: e.offsetX, moved: false }
        scrubTo(T(e.offsetX))
      }
    }

    const move = (e: PointerEvent) => {
      if (!ptrs.has(e.pointerId)) {
        const r = region(e.offsetY)
        canvas.style.cursor = r === 'lane' || e.offsetX < gutter ? 'pointer' : app.get().span < (app.get().day ? app.get().day!.end - app.get().day!.start : 0) ? 'grab' : 'default'
        return
      }
      ptrs.set(e.pointerId, { x: e.offsetX, y: e.offsetY })
      if (!g) return
      const W = canvas.clientWidth
      const PW = plotW()
      if (g.kind === 'pinch' && ptrs.size === 2) {
        const [a, b] = [...ptrs.values()]
        const mid = (a.x + b.x) / 2
        const span = (g.span0 * g.d0) / (Math.hypot(a.x - b.x, a.y - b.y) || 1)
        zoomAround(span, g.anchorT, (mid - gutter) / PW)
      } else if (g.kind === 'mini') {
        miniPan(e.offsetX)
      } else if (g.kind === 'pan') {
        setView(g.v0 - ((e.offsetX - g.x0) * app.get().span) / PW)
      } else if (g.kind === 'scrub') {
        if (Math.abs(e.offsetX - g.x0) > 4) g.moved = true
        scrubTo(T(Math.min(W, Math.max(gutter, e.offsetX))))
      }
    }

    const up = (e: PointerEvent) => {
      if (!ptrs.has(e.pointerId)) return
      ptrs.delete(e.pointerId)
      if (g?.kind === 'scrub' || g?.kind === 'name') {
        if ((g.kind === 'name' || !g.moved) && e.type === 'pointerup') {
          const { laneH, y } = geom()
          const cams = app.get().cameras
          for (let i = 0; i < cams.length; i++) {
            if (e.offsetY >= y(i) - 1 && e.offsetY <= y(i) + laneH + 1) {
              selectCamera(cams[i].id)
              break
            }
          }
        }
        if (g.kind === 'scrub') endScrub()
        g = null
      }
      if (g?.kind === 'pinch' && ptrs.size < 2) g = null
      if (ptrs.size === 0) {
        g = null
        app.set({ viewLocked: false })
      }
    }

    const wheel = (e: WheelEvent) => {
      e.preventDefault()
      const s = app.get()
      const PW = plotW()
      if (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) setView(s.view0 + ((e.deltaX || e.deltaY) * s.span) / PW)
      else zoomAround(s.span * Math.exp(e.deltaY * 0.0016), T(Math.max(gutter, e.offsetX)), Math.max(0, e.offsetX - gutter) / PW)
    }

    canvas.addEventListener('pointerdown', down)
    canvas.addEventListener('pointermove', move)
    canvas.addEventListener('pointerup', up)
    canvas.addEventListener('pointercancel', up)
    canvas.addEventListener('wheel', wheel, { passive: false })

    const unsub = app.subscribe(() => {
      dirty = true
    })
    const ro = new ResizeObserver(() => {
      dirty = true
    })
    ro.observe(canvas)
    void document.fonts?.ready.then(() => {
      dirty = true
    })
    const nowTimer = window.setInterval(() => {
      dirty = true // the NOW line creeps forward
    }, 15_000)
    let raf = 0
    const frame = () => {
      raf = requestAnimationFrame(frame)
      if (dirty) {
        dirty = false
        draw()
      }
    }
    raf = requestAnimationFrame(frame)

    return () => {
      cancelAnimationFrame(raf)
      clearInterval(nowTimer)
      unsub()
      ro.disconnect()
      canvas.removeEventListener('pointerdown', down)
      canvas.removeEventListener('pointermove', move)
      canvas.removeEventListener('pointerup', up)
      canvas.removeEventListener('pointercancel', up)
      canvas.removeEventListener('wheel', wheel)
    }
  }, [])

  return <canvas ref={ref} className="block size-full touch-none" aria-label="Timeline. Click or drag on a lane to move the playhead; tap a lane to switch camera. Scroll to zoom." />
}
