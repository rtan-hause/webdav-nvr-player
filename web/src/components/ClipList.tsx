import { useEffect, useRef } from 'react'
import { app, camClips, seekTo, setPlaying, toggleSort } from '../app/state'
import { thumbURL } from '../lib/api'
import { useStore } from '../lib/store'
import { fmtDur, fmtSize, fmtTime } from '../lib/time'
import { useClipAtPlayhead } from '../app/hooks'

// Every clip of the selected camera and day (plan decision 15), newest first
// by default. The clip under the playhead is highlighted and scrolled into
// view, unless the pointer is over the list.

export function ClipList() {
  const clips = useStore(app, camClips)
  const newestFirst = useStore(app, (s) => s.newestFirst)
  const tz = useStore(app, (s) => s.tz)
  const active = useClipAtPlayhead()
  const list = useRef<HTMLOListElement>(null)
  const hovering = useRef(false)
  const ordered = newestFirst ? [...clips].reverse() : clips

  useEffect(() => {
    const el = list.current
    if (!el || active === null || hovering.current) return
    // Only scroll the list's own box (desktop column, theater drawer), never the page.
    if (el.scrollHeight <= el.clientHeight + 1) return
    const row = el.querySelector<HTMLElement>(`[data-id="${active}"]`)
    if (!row) return
    const smooth = !matchMedia('(prefers-reduced-motion: reduce)').matches
    el.scrollTo({ top: row.offsetTop - el.offsetTop - el.clientHeight / 2 + row.offsetHeight / 2, behavior: smooth ? 'smooth' : 'auto' })
  }, [active, newestFirst, clips])

  return (
    <>
      <div className="flex items-center gap-2.5 px-3.5 py-2.5">
        <h2 className="font-display text-sm font-semibold tracking-[0.12em] uppercase">Clips</h2>
        <span className="font-mono text-xs text-faint">{clips.length}</span>
        <button
          type="button"
          onClick={toggleSort}
          aria-label={`Sorted ${newestFirst ? 'newest' : 'oldest'} first. Switch to ${newestFirst ? 'oldest' : 'newest'} first.`}
          className="ml-auto rounded-full border border-line bg-ink-2 px-2.5 py-1 text-[13px] font-medium whitespace-nowrap hover:border-faint focus-visible:outline-2 focus-visible:outline-sodium"
        >
          {newestFirst ? 'Newest first' : 'Oldest first'} <span className="text-sodium">↕</span>
        </button>
      </div>
      <ol
        ref={list}
        className="clip-list grid min-h-0 flex-1 content-start gap-0.5 overflow-y-auto px-2 pb-2.5 [scrollbar-color:var(--color-ink-3)_transparent] [scrollbar-width:thin]"
        onPointerEnter={() => {
          hovering.current = true
        }}
        onPointerLeave={() => {
          hovering.current = false
        }}
      >
        {ordered.map((c) => {
          const on = c.id === active
          return (
            <li key={c.id} data-id={c.id}>
              <button
                type="button"
                aria-current={on || undefined}
                aria-label={`Clip at ${fmtTime(c.start, tz)}, ${fmtDur(c.end - c.start)}`}
                onClick={() => {
                  seekTo(c.start)
                  setPlaying(true)
                }}
                className={`group grid w-full grid-cols-[112px_minmax(0,1fr)] grid-rows-2 gap-x-3 rounded-lg p-1.5 text-left hover:bg-ink-2 focus-visible:outline-2 focus-visible:outline-sodium ${on ? 'bg-ink-2' : ''}`}
              >
                <span className={`row-span-2 block aspect-video overflow-hidden rounded-md bg-ink-2 outline-2 -outline-offset-2 ${on ? 'outline-sodium' : 'outline-transparent group-hover:outline-ink-3'}`}>
                  {c.thumb && <img src={thumbURL(c.id)} alt="" loading="lazy" decoding="async" className="block size-full object-cover" />}
                </span>
                <span className={`self-end font-mono text-[13px] font-medium tabular-nums ${on ? 'text-sodium' : ''}`}>{fmtTime(c.start, tz)}</span>
                <span className="self-start text-xs text-muted">
                  {fmtDur(c.end - c.start)} · {fmtSize(c.size)}
                </span>
              </button>
            </li>
          )
        })}
      </ol>
    </>
  )
}
