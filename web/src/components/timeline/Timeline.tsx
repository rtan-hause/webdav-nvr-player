import { useEffect, useRef } from 'react'
import { app } from '../../app/state'
import { createPainter } from './draw'
import { attachGestures } from './gestures'
import { timeScale } from './geometry'

// The day timeline (plan decision 16): a fixed, zoomable day with a moving
// playhead, one lane per camera, camera names in a column on the left. Drawn
// on a canvas because a busy day has over a thousand clips. Drawing is in
// draw.ts and input in gestures.ts; both use the time ↔ x Scale from geometry.ts.

export function Timeline() {
  const ref = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = ref.current!
    const draw = createPainter(canvas)
    let gutter = 0 // the camera-name column's width, as last drawn
    const scale = () => {
      const s = app.get()
      return timeScale(gutter, canvas.clientWidth, s.view0, s.span)
    }
    const detach = attachGestures(canvas, scale)

    // Redraw on the next frame after anything changes.
    let dirty = true
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
        gutter = draw(app.get())
      }
    }
    raf = requestAnimationFrame(frame)

    return () => {
      cancelAnimationFrame(raf)
      clearInterval(nowTimer)
      unsub()
      ro.disconnect()
      detach()
    }
  }, [])

  return <canvas ref={ref} className="block size-full touch-none" aria-label="Timeline. Click a lane to move the playhead there and switch to that camera; drag the playhead to scrub; drag elsewhere to pan. Scroll to zoom." />
}
