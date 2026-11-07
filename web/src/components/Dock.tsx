import { app, camClips, cameraName, jumpClip, togglePlay, zoomStep } from '../app/state'
import { itemAt } from '../lib/clips'
import { useStore } from '../lib/store'
import { fmtTime } from '../lib/time'
import { Icon } from './Icon'
import { Timeline } from './Timeline'
import { timelineHeight } from './timelineGeom'

// Under the video: clip transport, the wall-clock time at the playhead, zoom,
// and the day timeline.

function zoomLabel(span: number) {
  return span >= 3600_000 ? `${Math.round(span / 360_000) / 10} h` : `${Math.round(span / 60_000)} min`
}

export function Dock() {
  const playing = useStore(app, (s) => s.playing)
  const tz = useStore(app, (s) => s.tz)
  const second = useStore(app, (s) => Math.floor(s.t / 1000) * 1000)
  const file = useStore(app, (s) => itemAt(camClips(s), s.t)?.file ?? null)
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
      <div className="tl-wrap border-t border-line" style={{ height: timelineHeight(cameras) }}>
        <Timeline />
      </div>
    </>
  )
}
