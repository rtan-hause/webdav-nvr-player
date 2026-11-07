import { useEffect, useRef, useState, type PointerEvent } from 'react'
import { Engine } from '../app/engine'
import { app, camClips, cameraName, enterTheater, exitTheater, playOnFrom, seekTo } from '../app/state'
import { canPlay, thumbURL } from '../lib/api'
import { nextAfter } from '../lib/clips'
import { useStore } from '../lib/store'
import { fmtTime } from '../lib/time'
import { SkipFlash, type Skip } from '../player/SkipFlash'
import { ControlBar } from './ControlBar'
import { Toast } from './Toast'

// The video area for the selected camera. Gestures (plan decision 19):
//  - mouse: click plays/pauses; double-click the left/right side skips 5 s,
//    the middle enters theater mode
//  - touch: tap shows/hides the controls; double-tap a side skips
//  - after a skip, further taps on the same side add 5 s more

const DOUBLE_MS = 300
const CHAIN_MS = 700
const SLOP_PX = 40

type Zone = 'back' | 'middle' | 'forward'
type Tap = { t: number; x: number; y: number; zone: Zone }

function useSkipFlash() {
  const [skip, setSkip] = useState<Skip | null>(null)
  const chain = useRef<{ until: number; side: Skip['side']; total: number } | null>(null)
  const flash = (side: Skip['side'], seconds: number, now: number) => {
    const c = chain.current
    const total = (c && c.side === side && now < c.until ? c.total : 0) + seconds
    chain.current = { until: now + CHAIN_MS, side, total }
    setSkip({ side, seconds: total, id: now })
  }
  const chaining = (side: Skip['side'], now: number) => {
    const c = chain.current
    return c !== null && c.side === side && now < c.until
  }
  return { skip, flash, chaining }
}

export function Viewport() {
  const a = useRef<HTMLVideoElement>(null)
  const b = useRef<HTMLVideoElement>(null)
  const slot = useStore(app, (s) => s.slot)
  const preview = useStore(app, (s) => s.preview)
  const buffering = useStore(app, (s) => s.buffering)
  const shownId = useStore(app, (s) => s.shown)
  const playing = useStore(app, (s) => s.playing)
  const shown = useStore(app, (s) => (s.shown === null ? null : (camClips(s).find((c) => c.id === s.shown) ?? null)))
  const { skip, flash, chaining } = useSkipFlash()
  const [touchShown, setTouchShown] = useState(false)
  const last = useRef<Tap | null>(null)
  const single = useRef<number | undefined>(undefined)
  const box = useRef<HTMLDivElement>(null)
  const pic = useRef<HTMLDivElement>(null)
  const aspect = shown && shown.width > 0 && shown.height > 0 ? shown.width / shown.height : 16 / 9

  // Where the picture actually is inside the box (object-contain leaves bars
  // at the sides or top and bottom). The control bar, badges and tap zones
  // follow the picture, not the box.
  useEffect(() => {
    const el = box.current!
    const update = () => {
      const W = el.clientWidth
      const H = el.clientHeight
      let w = W
      let h = W / aspect
      if (h > H) {
        h = H
        w = H * aspect
      }
      el.style.setProperty('--pic-x', `${(W - w) / 2}px`)
      el.style.setProperty('--pic-y', `${(H - h) / 2}px`)
      el.style.setProperty('--pic-w', `${w}px`)
      el.style.setProperty('--pic-h', `${h}px`)
    }
    const ro = new ResizeObserver(update)
    ro.observe(el)
    update()
    return () => ro.disconnect()
  }, [aspect])

  useEffect(() => {
    const engine = new Engine(a.current!, b.current!)
    return () => {
      engine.destroy()
      clearTimeout(single.current)
    }
  }, [])

  const skipBy = (side: Skip['side'], now: number) => {
    // seekTo clamps to the day and the engine finds the clip; a gap shows the gap panel.
    seekTo(app.get().t + (side === 'forward' ? 5000 : -5000))
    flash(side, 5, now)
  }

  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    const mouse = e.pointerType === 'mouse'
    if (mouse && e.button !== 0) return
    clearTimeout(single.current)
    const r = (pic.current ?? e.currentTarget).getBoundingClientRect()
    const x = (e.clientX - r.left) / r.width
    const zone: Zone = x < 0.4 ? 'back' : x > 0.6 ? 'forward' : 'middle'
    const now = e.timeStamp
    if (zone !== 'middle' && chaining(zone, now)) {
      skipBy(zone, now)
      return
    }
    const prev = last.current
    if (prev && prev.zone === zone && now - prev.t < DOUBLE_MS && Math.hypot(e.clientX - prev.x, e.clientY - prev.y) < SLOP_PX) {
      last.current = null
      if (zone === 'middle') {
        if (app.get().layout === 'theater') exitTheater()
        else enterTheater()
      } else {
        skipBy(zone, now)
      }
      return
    }
    last.current = { t: now, x: e.clientX, y: e.clientY, zone }
    single.current = window.setTimeout(() => {
      if (mouse) app.set((s) => ({ playing: !s.playing }))
      else setTouchShown((v) => !v)
    }, DOUBLE_MS)
  }

  const video = 'absolute inset-0 size-full object-contain transition-opacity duration-100'
  return (
    <div ref={box} className="viewport group relative size-full overflow-hidden bg-black select-none">
      {/* Both stay mounted; only the active one shows, and neither during a gap. */}
      <video ref={a} playsInline muted className={`${video} ${slot === 0 && shownId !== null ? 'opacity-100' : 'opacity-0'}`} aria-hidden={slot !== 0} />
      <video ref={b} playsInline muted className={`${video} ${slot === 1 && shownId !== null ? 'opacity-100' : 'opacity-0'}`} aria-hidden={slot !== 1} />
      {preview !== null && <img src={thumbURL(preview)} alt="" className="absolute inset-0 size-full object-contain" />}
      {/* Gesture layer over the picture; the control bar sits above it. */}
      <div className="absolute inset-0 touch-manipulation" onPointerUp={onPointerUp} />
      {shownId === null && <GapPanel />}
      {buffering && shownId !== null && preview === null && playing && <Spinner />}
      <Toast />
      {/* Overlays that belong to the picture itself. */}
      <div ref={pic} className="picture pointer-events-none absolute" style={{ left: 'var(--pic-x, 0px)', top: 'var(--pic-y, 0px)', width: 'var(--pic-w, 100%)', height: 'var(--pic-h, 100%)' }}>
        {shown && <CodecBadge codec={shown.codec} width={shown.width} height={shown.height} />}
        <SkipFlash skip={skip} />
        <ControlBar onSkip={skipBy} touchShown={touchShown} />
      </div>
    </div>
  )
}

function GapPanel() {
  const tz = useStore(app, (s) => s.tz)
  const t = useStore(app, (s) => Math.floor(s.t / 1000) * 1000)
  const name = useStore(app, (s) => cameraName(s))
  const count = useStore(app, (s) => camClips(s).length)
  const loading = useStore(app, (s) => s.dayLoading)
  const next = useStore(app, (s) => nextAfter(camClips(s), s.t))
  if (loading) return null
  return (
    <div className="pointer-events-none absolute inset-0 grid place-content-center justify-items-center gap-3 p-4 text-center text-muted">
      <div>
        <strong className="block font-display text-[22px] font-semibold tracking-wide text-text">{count ? `No recording at ${fmtTime(t, tz)}` : 'No recordings this day'}</strong>
        <span>{!count ? `${name} has nothing on this day.` : next ? `${name} wasn’t recording.` : `No more recordings from ${name} this day.`}</span>
      </div>
      {next && (
        <button
          type="button"
          onClick={() => playOnFrom(app.get().t)}
          className="pointer-events-auto rounded-full border border-sodium/55 bg-sodium/8 px-3.5 py-2 font-semibold text-sodium hover:bg-sodium/16 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium"
        >
          Jump to next clip · {fmtTime(next.start, tz)}
        </button>
      )}
    </div>
  )
}

function Spinner() {
  return (
    <div aria-label="Loading" role="status" className="pointer-events-none absolute inset-0 grid place-items-center">
      <span className="size-10 animate-spin rounded-full border-3 border-text/25 border-t-sodium motion-reduce:animate-none" />
    </div>
  )
}

function CodecBadge({ codec, width, height }: { codec: string; width: number; height: number }) {
  const ok = canPlay(codec)
  return (
    <div
      className={`codec-badge pointer-events-none absolute top-2.5 right-2.5 rounded-md bg-ink-0/75 px-2 py-1 font-mono text-[11px] font-medium tracking-wide ${ok ? 'text-text' : 'border border-warn/50 text-warn'}`}
      title={ok ? undefined : 'This browser can’t decode this video. Try Safari, or Chrome/Edge with hardware HEVC support.'}
    >
      {ok ? `${codec} · ${width}×${height}` : `${codec} · this browser can’t decode it`}
    </div>
  )
}
