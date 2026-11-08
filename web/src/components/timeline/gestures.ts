import { endScrub, scrubTo, seekTo, selectCamera } from '../../app/playhead'
import { app } from '../../app/state'
import { setView, zoomAround } from '../../app/view'
import { lanes, overviewScale, region, type Scale } from './geometry'

// Pointer and wheel input on the timeline canvas:
//  - lanes: a tap moves the playhead there and picks that camera (so does a
//    tap on the name); drag the playhead to scrub; drag anywhere else to pan
//    (when zoomed all the way out there's nothing to pan, so a drag scrubs)
//  - overview strip (top) and time axis (bottom): drag to move along the day
//  - wheel zooms around the pointer; shift+wheel or a sideways swipe pans;
//    two fingers pinch-zoom and pan

type Gesture =
  | { kind: 'scrub'; x0: number; off: number; moved: boolean } // dragging the playhead
  | { kind: 'lane'; x0: number; v0: number; panning: boolean } // a tap, or a drag that pans
  | { kind: 'pan'; x0: number; v0: number }
  | { kind: 'mini' }
  | { kind: 'name' } // tap on a camera name
  | { kind: 'pinch'; d0: number; span0: number; anchorT: number }

/** Movement under this many pixels is still a tap. */
const TAP_SLOP = 4

/** How close to the playhead a press must be to grab it; fingers get more room. */
const grabRadius = (e: PointerEvent) => (e.pointerType === 'mouse' ? 8 : 18)

const canPan = () => {
  const s = app.get()
  return !!s.day && s.span < s.day.end - s.day.start - 1000
}

/**
 * Handles input on the canvas. scale() gives the current time ↔ x mapping
 * (the one last drawn). Returns a function that removes the listeners.
 */
export function attachGestures(canvas: HTMLCanvasElement, scale: () => Scale): () => void {
  const pointers = new Map<number, { x: number; y: number }>()
  let gesture: Gesture | null = null
  const regionAt = (y: number) => region(y, canvas.clientHeight)

  /** Overview strip: centre the view on the time under x. */
  const overviewPan = (x: number) => {
    const s = app.get()
    if (!s.day) return
    setView(overviewScale(scale(), s.day).toT(x) - s.span / 2)
  }

  const down = (e: PointerEvent) => {
    canvas.setPointerCapture(e.pointerId)
    pointers.set(e.pointerId, { x: e.offsetX, y: e.offsetY })
    const sc = scale()
    if (pointers.size === 2) {
      if (gesture?.kind === 'scrub' && gesture.moved) endScrub()
      const [a, b] = [...pointers.values()]
      gesture = { kind: 'pinch', d0: Math.hypot(a.x - b.x, a.y - b.y) || 1, span0: app.get().span, anchorT: sc.toT((a.x + b.x) / 2) }
      app.set({ viewLocked: true })
      return
    }
    if (pointers.size > 2) return
    const r = regionAt(e.offsetY)
    if (e.offsetX < sc.gutter) {
      gesture = { kind: 'name' }
    } else if (r === 'mini') {
      gesture = { kind: 'mini' }
      app.set({ viewLocked: true })
      overviewPan(e.offsetX)
    } else if (r === 'axis') {
      gesture = { kind: 'pan', x0: e.offsetX, v0: app.get().view0 }
      app.set({ viewLocked: true })
    } else {
      const px = sc.toX(app.get().t)
      if (Math.abs(e.offsetX - px) <= grabRadius(e)) gesture = { kind: 'scrub', x0: e.offsetX, off: px - e.offsetX, moved: false }
      else gesture = { kind: 'lane', x0: e.offsetX, v0: app.get().view0, panning: false }
    }
  }

  /** Hovering (no button down): a cursor that says what a press would do. */
  const hoverCursor = (e: PointerEvent, sc: Scale) => {
    const r = regionAt(e.offsetY)
    if (e.offsetX < sc.gutter) return 'pointer'
    if (r === 'lane' && Math.abs(e.offsetX - sc.toX(app.get().t)) <= grabRadius(e)) return 'ew-resize'
    if (canPan()) return 'grab'
    return r === 'lane' ? 'pointer' : 'default'
  }

  const move = (e: PointerEvent) => {
    const sc = scale()
    if (!pointers.has(e.pointerId)) {
      canvas.style.cursor = hoverCursor(e, sc)
      return
    }
    pointers.set(e.pointerId, { x: e.offsetX, y: e.offsetY })
    if (!gesture) return
    const panBy = (x0: number, v0: number) => setView(v0 - ((e.offsetX - x0) * app.get().span) / sc.plotWidth)
    if (gesture.kind === 'pinch' && pointers.size === 2) {
      const [a, b] = [...pointers.values()]
      const mid = (a.x + b.x) / 2
      const span = (gesture.span0 * gesture.d0) / (Math.hypot(a.x - b.x, a.y - b.y) || 1)
      zoomAround(span, gesture.anchorT, (mid - sc.gutter) / sc.plotWidth)
    } else if (gesture.kind === 'mini') {
      overviewPan(e.offsetX)
    } else if (gesture.kind === 'pan') {
      panBy(gesture.x0, gesture.v0)
    } else if (gesture.kind === 'lane') {
      if (!gesture.panning && Math.abs(e.offsetX - gesture.x0) > TAP_SLOP) {
        if (canPan()) {
          gesture.panning = true
          app.set({ viewLocked: true })
          canvas.style.cursor = 'grabbing'
        } else {
          gesture = { kind: 'scrub', x0: gesture.x0, off: 0, moved: false } // nothing to pan: scrub instead
        }
      }
      if (gesture.kind === 'lane' && gesture.panning) panBy(gesture.x0, gesture.v0)
    }
    if (gesture.kind === 'scrub') {
      if (!gesture.moved && Math.abs(e.offsetX - gesture.x0) <= TAP_SLOP) return
      gesture.moved = true
      scrubTo(scale().toT(Math.min(canvas.clientWidth, Math.max(sc.gutter, e.offsetX + gesture.off))))
    }
  }

  const up = (e: PointerEvent) => {
    if (!pointers.has(e.pointerId)) return
    pointers.delete(e.pointerId)
    if (gesture?.kind === 'scrub' || gesture?.kind === 'lane' || gesture?.kind === 'name') {
      const tap = gesture.kind === 'name' || (gesture.kind === 'scrub' ? !gesture.moved : !gesture.panning)
      if (tap && e.type === 'pointerup') {
        const sc = scale()
        if (gesture.kind !== 'name') seekTo(sc.toT(Math.min(canvas.clientWidth, Math.max(sc.gutter, e.offsetX))))
        const cams = app.get().cameras
        const i = lanes(canvas.clientHeight, cams.length).at(e.offsetY, cams.length)
        if (i >= 0) selectCamera(cams[i].id)
      }
      if (gesture.kind === 'scrub' && gesture.moved) endScrub()
      if (gesture.kind === 'lane' && gesture.panning) canvas.style.cursor = 'grab'
      gesture = null
    }
    if (gesture?.kind === 'pinch' && pointers.size < 2) gesture = null
    if (pointers.size === 0) {
      gesture = null
      app.set({ viewLocked: false })
    }
  }

  const wheel = (e: WheelEvent) => {
    e.preventDefault()
    const s = app.get()
    const sc = scale()
    if (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) setView(s.view0 + ((e.deltaX || e.deltaY) * s.span) / sc.plotWidth)
    else zoomAround(s.span * Math.exp(e.deltaY * 0.0016), sc.toT(Math.max(sc.gutter, e.offsetX)), Math.max(0, e.offsetX - sc.gutter) / sc.plotWidth)
  }

  canvas.addEventListener('pointerdown', down)
  canvas.addEventListener('pointermove', move)
  canvas.addEventListener('pointerup', up)
  canvas.addEventListener('pointercancel', up)
  canvas.addEventListener('wheel', wheel, { passive: false })
  return () => {
    canvas.removeEventListener('pointerdown', down)
    canvas.removeEventListener('pointermove', move)
    canvas.removeEventListener('pointerup', up)
    canvas.removeEventListener('pointercancel', up)
    canvas.removeEventListener('wheel', wheel)
  }
}
