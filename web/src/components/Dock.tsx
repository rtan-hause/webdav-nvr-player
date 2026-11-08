import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { goToTime, jumpToLatest } from '../app/days'
import { jumpClip, togglePlay } from '../app/playhead'
import { app, cameraName, clipAtPlayhead } from '../app/state'
import { zoomStep } from '../app/view'
import { loadSetting, saveSetting } from '../lib/storage'
import { useStore } from '../lib/store'
import { dateOf, fmtTime } from '../lib/time'
import { Icon } from './Icon'
import { Timeline } from './Timeline'
import { LANE_H, MAX_LANE, MIN_LANE, timelineHeight } from './timelineGeom'

// Under the video: clip transport, the wall-clock time at the playhead, jumps
// to a date and time or to the newest recording, zoom, and the day timeline.

const pill =
  'rounded-full border border-line bg-ink-2 px-3 py-1.5 font-display text-[13px] font-semibold tracking-[0.08em] uppercase hover:border-sodium hover:text-sodium focus-visible:outline-2 focus-visible:outline-sodium aria-expanded:border-sodium aria-expanded:text-sodium'

function zoomLabel(span: number) {
  return span >= 3600_000 ? `${Math.round(span / 360_000) / 10} h` : `${Math.round(span / 60_000)} min`
}

export function Dock() {
  const playing = useStore(app, (s) => s.playing)
  const tz = useStore(app, (s) => s.tz)
  const second = useStore(app, (s) => Math.floor(s.t / 1000) * 1000)
  const file = useStore(app, (s) => clipAtPlayhead(s)?.file ?? null)
  const name = useStore(app, (s) => cameraName(s))
  const span = useStore(app, (s) => s.span)
  const theater = useStore(app, (s) => s.layout === 'theater')
  const drawer = useStore(app, (s) => s.drawer)
  const cameras = useStore(app, (s) => s.cameras.length)

  const icon = 'grid size-9 place-items-center rounded-lg text-text hover:bg-ink-2 focus-visible:outline-2 focus-visible:outline-sodium'
  return (
    <>
      <div className="transport flex flex-wrap items-center gap-x-4 gap-y-2 px-3.5 py-2.5">
        <div className="flex items-center gap-1">
          <button type="button" className={icon} aria-label="Previous clip ([)" title="Previous clip ([)" onClick={() => jumpClip(-1)}>
            <Icon.PrevClip />
          </button>
          <button
            type="button"
            aria-label={playing ? 'Pause (space)' : 'Play (space)'}
            title={playing ? 'Pause (space)' : 'Play (space)'}
            onClick={togglePlay}
            className="grid size-11 place-items-center rounded-full bg-sodium text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium"
          >
            {playing ? <Icon.Pause /> : <Icon.Play />}
          </button>
          <button type="button" className={icon} aria-label="Next clip (])" title="Next clip (])" onClick={() => jumpClip(1)}>
            <Icon.NextClip />
          </button>
        </div>
        <div className="clock flex min-w-0 flex-col leading-tight">
          <span className="font-mono text-[22px] font-medium tracking-tight tabular-nums">{fmtTime(second, tz)}</span>
          <span className="truncate text-[13px] text-muted">{file ?? `${name} · no recording`}</span>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <GoTo />
          <button type="button" title="Go to the newest recording (End)" onClick={() => void jumpToLatest()} className={pill}>
            Latest
          </button>
          <div className="inline-flex items-center rounded-full border border-line bg-ink-1">
            <button type="button" className="grid size-8 place-items-center rounded-full text-lg font-semibold hover:bg-ink-2 focus-visible:outline-2 focus-visible:outline-sodium" aria-label="Zoom out (−)" title="Zoom out (−)" onClick={() => zoomStep(-1)}>
              −
            </button>
            <span className="min-w-12 text-center font-mono text-xs text-muted" aria-live="polite">
              {zoomLabel(span)}
            </span>
            <button type="button" className="grid size-8 place-items-center rounded-full text-lg font-semibold hover:bg-ink-2 focus-visible:outline-2 focus-visible:outline-sodium" aria-label="Zoom in (+)" title="Zoom in (+)" onClick={() => zoomStep(1)}>
              +
            </button>
          </div>
          {theater && (
            <button
              type="button"
              aria-expanded={drawer}
              onClick={() => app.set((s) => ({ drawer: !s.drawer }))}
              className={`rounded-full border bg-ink-2 px-3 py-1.5 font-display text-[13px] font-semibold tracking-[0.08em] uppercase focus-visible:outline-2 focus-visible:outline-sodium ${drawer ? 'border-sodium text-sodium' : 'border-line'}`}
            >
              Clips
            </button>
          )}
        </div>
      </div>
      <ResizableTimeline cameras={cameras} />
    </>
  )
}

// "Go to" opens a small panel above the button with a date and a time,
// filled in with the playhead's; Go (or Enter) jumps there. Escape or a click
// outside closes it.
function GoTo() {
  const [open, setOpen] = useState(false)
  const wrap = useRef<HTMLDivElement>(null)
  const tz = useStore(app, (s) => s.tz)
  const today = useStore(app, (s) => s.today)

  useEffect(() => {
    if (!open) return
    const away = (e: globalThis.PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open])

  const t = app.get().t
  const field = 'rounded-md border border-line bg-ink-1 px-2 py-1.5 font-mono text-sm [color-scheme:dark] focus:border-sodium focus:outline-none'
  return (
    <div ref={wrap} className="relative">
      <button type="button" aria-expanded={open} aria-haspopup="dialog" title="Go to a date and time" onClick={() => setOpen((o) => !o)} className={pill}>
        Go to
      </button>
      {open && (
        <form
          role="dialog"
          aria-label="Go to a date and time"
          className="absolute right-0 bottom-full z-20 mb-2 flex w-max max-w-[calc(100vw-2rem)] flex-wrap items-center gap-2 rounded-xl border border-line bg-ink-2 p-2.5 shadow-lg shadow-black/40"
          onSubmit={(e) => {
            e.preventDefault()
            const f = new FormData(e.currentTarget)
            goToTime(String(f.get('date')), String(f.get('time')))
            setOpen(false)
          }}
          onKeyDown={(e) => {
            if (e.key !== 'Escape') return
            e.preventDefault()
            e.stopPropagation() // not the player's Escape (leave theater)
            setOpen(false)
          }}
        >
          <input name="date" type="date" aria-label="Date" required max={today} defaultValue={dateOf(t, tz)} className={field} />
          <input name="time" type="time" aria-label="Time" required step={1} defaultValue={fmtTime(t, tz)} autoFocus className={field} />
          <button type="submit" className="rounded-md bg-sodium px-3 py-1.5 text-sm font-semibold text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium">
            Go
          </button>
        </form>
      )}
    </div>
  )
}

// The timeline's height is set by dragging the handle along its top edge (or
// with the arrow keys when the handle has focus); double-click resets it. The
// lane height is what's remembered, so it still fits when cameras are added.

const LANE_KEY = 'nvr.laneHeight'

function savedLane(): number {
  const v = Number(loadSetting(LANE_KEY))
  return v >= MIN_LANE && v <= MAX_LANE ? v : LANE_H
}

function ResizableTimeline({ cameras }: { cameras: number }) {
  const n = Math.max(1, cameras)
  const [lane, setLane] = useState(savedLane)
  const latest = useRef(lane)
  const drag = useRef<{ y: number; lane: number } | null>(null)

  // Never more than 60% of the window, so the video keeps most of the screen.
  const maxLane = () => Math.max(MIN_LANE, Math.min(MAX_LANE, (window.innerHeight * 0.6 - timelineHeight(n, 0)) / n))
  const clamp = (v: number) => Math.round(Math.min(maxLane(), Math.max(MIN_LANE, v)))
  const update = (v: number, save: boolean) => {
    latest.current = v
    setLane(v)
    if (save) saveSetting(LANE_KEY, String(v))
  }
  const finish = () => {
    if (!drag.current) return
    drag.current = null
    update(latest.current, true)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const step = e.shiftKey ? 16 : 4
    if (e.key === 'ArrowUp') update(clamp(lane + step), true)
    else if (e.key === 'ArrowDown') update(clamp(lane - step), true)
    else if (e.key === 'Home') update(LANE_H, true)
    else return
    e.preventDefault()
    e.stopPropagation() // not the player's own arrow-key skipping
  }

  const shown = Math.min(lane, maxLane())
  return (
    <div className="tl-wrap relative border-t border-line" style={{ height: timelineHeight(n, shown) }}>
      <div
        role="separator"
        aria-orientation="horizontal"
        aria-label="Timeline height. Drag, or use the up and down arrow keys; double-click to reset."
        title="Drag to resize the timeline (double-click to reset)"
        aria-valuemin={MIN_LANE}
        aria-valuemax={MAX_LANE}
        aria-valuenow={Math.round(shown)}
        tabIndex={0}
        onPointerDown={(e: PointerEvent<HTMLDivElement>) => {
          e.currentTarget.setPointerCapture(e.pointerId)
          drag.current = { y: e.clientY, lane: shown }
        }}
        onPointerMove={(e) => {
          if (drag.current) update(clamp(drag.current.lane - (e.clientY - drag.current.y) / n), false)
        }}
        onPointerUp={finish}
        onPointerCancel={finish}
        onDoubleClick={() => update(LANE_H, true)}
        onKeyDown={onKeyDown}
        className="group/resize absolute inset-x-0 -top-2.5 z-10 h-3.5 cursor-row-resize touch-none focus-visible:outline-none"
      >
        <span
          aria-hidden
          className="mx-auto mt-2 block h-[3px] w-12 rounded-full bg-faint opacity-0 transition-opacity group-hover/resize:opacity-100 group-focus-visible/resize:bg-sodium group-focus-visible/resize:opacity-100 pointer-coarse:opacity-60"
        />
      </div>
      <Timeline />
    </div>
  )
}
