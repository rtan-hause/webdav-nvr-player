import { useRef } from 'react'
import { addDays, fmtDay } from '../lib/time'
import { app, goToDay, selectCamera } from '../app/state'
import { useStore } from '../lib/store'
import { Icon } from './Icon'

// Brand, camera switcher (with the day's clip count) and the day navigator.

export function Header() {
  const cameras = useStore(app, (s) => s.cameras)
  const cam = useStore(app, (s) => s.cam)
  const day = useStore(app, (s) => s.day)

  return (
    <header className="top flex flex-wrap items-center gap-x-3.5 gap-y-2.5 border-b border-line px-3.5 py-3">
      <div className="flex items-center gap-2 font-display text-base font-semibold tracking-[0.12em] whitespace-nowrap uppercase">
        <span aria-hidden className="size-2 rounded-full bg-rec shadow-[0_0_0_3px_rgba(229,72,77,.18)]" />
        NVR
      </div>
      <div className="cams flex min-w-0 flex-1 gap-1.5 overflow-x-auto [scrollbar-width:none]" role="group" aria-label="Camera">
        {cameras.map((c, i) => {
          const on = c.id === cam
          const n = day?.byCam.get(c.id)?.length ?? 0
          return (
            <button
              key={c.id}
              type="button"
              aria-pressed={on}
              onClick={() => selectCamera(c.id)}
              className={`inline-flex flex-none items-center gap-2 rounded-full border px-3 py-1.5 text-sm font-medium whitespace-nowrap focus-visible:outline-2 focus-visible:outline-sodium ${on ? 'border-clip-hi bg-clip-hi/10 text-text' : 'border-line bg-ink-2 text-muted hover:text-text'}`}
            >
              {i < 9 && <kbd className="cam-key font-mono text-[11px] text-faint">{i + 1}</kbd>}
              {c.name}
              <span className={`font-mono text-xs ${on ? 'text-clip-hi' : 'text-faint'}`}>{n}</span>
            </button>
          )
        })}
      </div>
      <DayNav />
      <a href="#/status" className="grid size-9 place-items-center rounded-lg text-muted hover:bg-ink-2 hover:text-text focus-visible:outline-2 focus-visible:outline-sodium" aria-label="Server and index status" title="Server and index status">
        <Icon.Status />
      </a>
    </header>
  )
}

function DayNav() {
  const picker = useRef<HTMLInputElement>(null)
  const date = useStore(app, (s) => s.day?.date ?? '')
  const today = useStore(app, (s) => s.today)
  const days = useStore(app, (s) => s.days)
  const loading = useStore(app, (s) => s.dayLoading)
  if (!date) return null

  // The arrows step between days with recordings (the last two months); until
  // that list loads they step one day. The date picker reaches anything older.
  const known = days.length > 0
  const prev = known ? days.filter((d) => d < date).at(-1) : addDays(date, -1)
  const next = known ? days.find((d) => d > date) : date < today ? addDays(date, 1) : undefined

  const icon = 'grid size-9 place-items-center rounded-lg text-text hover:bg-ink-2 focus-visible:outline-2 focus-visible:outline-sodium disabled:cursor-default disabled:opacity-30 disabled:hover:bg-transparent'
  return (
    <div className="ml-auto flex items-center gap-0.5">
      <button type="button" className={icon} aria-label="Previous day with recordings" disabled={!prev} onClick={() => prev && goToDay(prev)}>
        <Icon.ChevronLeft />
      </button>
      <span className={`px-1.5 font-semibold whitespace-nowrap ${loading ? 'text-muted' : ''}`} aria-live="polite">
        {date === today ? 'Today' : fmtDay(date)}
      </span>
      <button type="button" className={icon} aria-label="Next day with recordings" disabled={!next || date >= today} onClick={() => next && goToDay(next)}>
        <Icon.ChevronRight />
      </button>
      {/* A calendar button opens the browser's date picker; the input itself stays out of sight under it. */}
      <span className="relative grid">
        <input
          ref={picker}
          type="date"
          tabIndex={-1}
          aria-hidden
          value={date}
          max={today}
          onChange={(e) => {
            if (/^\d{4}-\d{2}-\d{2}$/.test(e.target.value)) goToDay(e.target.value)
          }}
          className="pointer-events-none absolute inset-0 opacity-0 [color-scheme:dark]"
        />
        <button
          type="button"
          className={icon}
          aria-label="Pick a day"
          title="Pick a day"
          onClick={() => {
            const el = picker.current
            if (!el) return
            try {
              el.showPicker()
            } catch {
              el.focus()
            }
          }}
        >
          <Icon.Calendar />
        </button>
      </span>
    </div>
  )
}
