import { useEffect, useRef, useState, type ReactNode } from 'react'
import { app, camClips, endScrub, RATES, scrubTo, seekTo, setRate, toggleMute, togglePlay, toggleTheater } from '../app/state'
import { useStore } from '../lib/store'
import { fmtClock } from '../lib/time'
import type { Skip } from '../player/SkipFlash'
import { Icon } from './Icon'

// The bar over the bottom of the video (plan decision 19): scrub bar for the
// current clip, play/pause, back/forward 5 s, position in the clip, speed,
// mute, theater. It fades while playing when the pointer is elsewhere or idle.

const rateLabel = (r: number) => `${r}×`

export function ControlBar({ onSkip, touchShown }: { onSkip: (side: Skip['side'], now: number) => void; touchShown: boolean }) {
  const playing = useStore(app, (s) => s.playing)
  const idle = useStore(app, (s) => s.idle)
  const scrubbing = useStore(app, (s) => s.scrubbing)
  const rate = useStore(app, (s) => s.rate)
  const muted = useStore(app, (s) => s.muted)
  const theater = useStore(app, (s) => s.layout === 'theater')
  const clip = useStore(app, (s) => (s.shown === null ? null : (camClips(s).find((c) => c.id === s.shown) ?? null)))
  // Position in the clip, to a tenth of a second so the bar doesn't re-render every frame.
  const pos = useStore(app, (s) => (clip ? Math.round((s.t - clip.start) / 100) / 10 : 0))
  const [menu, setMenu] = useState(false)
  const rateBox = useRef<HTMLDivElement>(null)
  const dragging = useRef(false)

  useEffect(() => {
    if (!menu) return
    const close = (e: globalThis.PointerEvent) => {
      if (!rateBox.current?.contains(e.target as Node)) setMenu(false)
    }
    document.addEventListener('pointerdown', close)
    return () => document.removeEventListener('pointerdown', close)
  }, [menu])

  const dur = clip ? (clip.end - clip.start) / 1000 : 0
  const forced = !playing || scrubbing || menu
  const show = forced || (!idle && touchShown)
  // The bar lives in a pointer-events-none overlay, so it opts back in only while it can be seen or hovered.
  const visibility = show ? 'pointer-events-auto opacity-100' : idle ? 'pointer-events-none opacity-0' : 'pointer-events-auto opacity-0 pointer-coarse:pointer-events-none group-hover:opacity-100'

  const stopDrag = () => {
    if (!dragging.current) return
    dragging.current = false
    endScrub()
  }

  return (
    <div
      className={`control-bar @container absolute inset-x-0 bottom-0 grid gap-1 bg-linear-to-t from-ink-0/90 via-ink-0/50 to-transparent px-3 pt-10 pb-2 transition-opacity duration-200 motion-reduce:transition-none ${visibility}`}
    >
      <input
        type="range"
        aria-label="Position in clip"
        min={0}
        max={dur}
        step="any"
        disabled={!clip}
        value={Math.min(pos, dur)}
        onPointerDown={() => {
          dragging.current = true
        }}
        onPointerUp={stopDrag}
        onPointerCancel={stopDrag}
        onBlur={stopDrag}
        onChange={(e) => {
          if (!clip) return
          const t = clip.start + Number(e.target.value) * 1000
          if (dragging.current) scrubTo(t)
          else seekTo(t) // keyboard
        }}
        className="h-4 w-full cursor-pointer accent-sodium disabled:cursor-default disabled:opacity-40"
      />
      <div className="flex items-center gap-0.5 @md:gap-1">
        <BarButton label={playing ? 'Pause (space)' : 'Play (space)'} onClick={togglePlay}>
          {playing ? <Icon.Pause /> : <Icon.Play />}
        </BarButton>
        <BarButton label="Back 5 seconds (←)" onClick={(e) => onSkip('back', e.timeStamp)}>
          <Icon.Skip back seconds={5} />
        </BarButton>
        <BarButton label="Forward 5 seconds (→)" onClick={(e) => onSkip('forward', e.timeStamp)}>
          <Icon.Skip seconds={5} />
        </BarButton>
        <span className="ml-1 font-mono text-xs text-text tabular-nums">
          {fmtClock(pos)} <span className="text-muted">/ {fmtClock(dur)}</span>
        </span>
        <span className="flex-1" />
        <div ref={rateBox} className="relative">
          <button
            type="button"
            aria-label={`Playback speed ${rateLabel(rate)}`}
            title="Playback speed (< >)"
            aria-haspopup="menu"
            aria-expanded={menu}
            onClick={() => setMenu((o) => !o)}
            className={`h-8 min-w-11 rounded-full px-2 font-mono text-sm tabular-nums hover:bg-ink-3/70 focus-visible:outline-2 focus-visible:outline-sodium @md:h-9 ${rate === 1 ? 'text-text' : 'text-sodium'}`}
          >
            {rateLabel(rate)}
          </button>
          {menu && (
            <div role="menu" aria-label="Playback speed" className="absolute right-0 bottom-full mb-2 grid min-w-24 gap-0.5 rounded-lg border border-line bg-ink-1/95 p-1 shadow-lg backdrop-blur-sm">
              {RATES.map((r) => (
                <button
                  key={r}
                  type="button"
                  role="menuitemradio"
                  aria-checked={r === rate}
                  onClick={() => {
                    setRate(r)
                    setMenu(false)
                  }}
                  className={`flex items-center justify-between gap-3 rounded-md px-2.5 py-1.5 text-left font-mono text-sm tabular-nums hover:bg-ink-3 focus-visible:outline-2 focus-visible:outline-sodium ${r === rate ? 'text-sodium' : 'text-text'}`}
                >
                  {rateLabel(r)}
                  {r === rate && <span aria-hidden>✓</span>}
                </button>
              ))}
            </div>
          )}
        </div>
        <BarButton label={muted ? 'Unmute (m)' : 'Mute (m)'} onClick={toggleMute}>
          {muted ? <Icon.Muted /> : <Icon.Sound />}
        </BarButton>
        <BarButton label={theater ? 'Exit theater mode (Esc)' : 'Theater mode (f)'} onClick={toggleTheater}>
          {theater ? <Icon.Shrink /> : <Icon.Expand />}
        </BarButton>
      </div>
    </div>
  )
}

export function BarButton({ label, onClick, children }: { label: string; onClick: (e: React.MouseEvent) => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="grid size-8 place-items-center rounded-full text-text hover:bg-ink-3/70 focus-visible:outline-2 focus-visible:outline-sodium @md:size-9"
    >
      {children}
    </button>
  )
}
