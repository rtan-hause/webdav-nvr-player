// Times are Unix milliseconds. Days and clock labels are in the cameras' time
// zone (from /api/status), not the browser's, so the timeline shows the same
// times as the file names and the camera's on-screen clock.

/** A time range in Unix ms, end exclusive. */
export type Span = { start: number; end: number }

const formatters = new Map<string, Intl.DateTimeFormat>()

function formatter(tz: string, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = tz + JSON.stringify(opts)
  let f = formatters.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', { timeZone: tz, ...opts })
    formatters.set(key, f)
  }
  return f
}

type Parts = { y: number; mo: number; d: number; h: number; mi: number; s: number }

function parts(ms: number, tz: string): Parts {
  const out: Record<string, number> = {}
  for (const p of formatter(tz, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).formatToParts(ms)) {
    if (p.type !== 'literal') out[p.type] = Number(p.value)
  }
  return { y: out.year, mo: out.month, d: out.day, h: out.hour, mi: out.minute, s: out.second }
}

/** Offset of tz from UTC at instant ms, in ms (local = utc + offset). */
export function tzOffset(ms: number, tz: string): number {
  const p = parts(ms, tz)
  return Date.UTC(p.y, p.mo - 1, p.d, p.h, p.mi, p.s) - Math.floor(ms / 1000) * 1000
}

/** The instant a local wall-clock time happens (first one, if it repeats). */
function fromLocal(localAsUTC: number, tz: string): number {
  let t = localAsUTC - tzOffset(localAsUTC, tz)
  t = localAsUTC - tzOffset(t, tz)
  return t
}

/**
 * A wall-clock time on a YYYY-MM-DD day, written as if it were UTC (the form
 * fromLocal takes). Day, hour, minute and second may overflow; Date.UTC carries them.
 */
function localAsUTC(date: string, dayOffset = 0, h = 0, mi = 0, s = 0): number {
  const [y, m, d] = date.split('-').map(Number)
  return Date.UTC(y, m - 1, d + dayOffset, h, mi, s)
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** YYYY-MM-DD of instant ms in tz. */
export function dateOf(ms: number, tz: string): string {
  const p = parts(ms, tz)
  return `${p.y}-${pad2(p.mo)}-${pad2(p.d)}`
}

export function addDays(date: string, n: number): string {
  return new Date(localAsUTC(date, n)).toISOString().slice(0, 10)
}

/** Start and end (exclusive) of a local day; 23 or 25 h long when DST changes. */
export function dayBounds(date: string, tz: string): Span {
  return { start: fromLocal(localAsUTC(date), tz), end: fromLocal(localAsUTC(date, 1), tz) }
}

/** The instant of a local wall-clock time ("17:42" or "17:42:05") on a YYYY-MM-DD day, or null if it isn't one. */
export function atLocalTime(date: string, hms: string, tz: string): number | null {
  const m = /^(\d{1,2}):(\d{2})(?::(\d{2}))?$/.exec(hms.trim())
  if (!m) return null
  const [h, mi, sec] = [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)]
  if (h > 23 || mi > 59 || sec > 59) return null
  return fromLocal(localAsUTC(date, 0, h, mi, sec), tz)
}

/** Instants inside [from, to] where local time is a multiple of step (for timeline ticks). */
export function localTicks(from: number, to: number, step: number, tz: string): number[] {
  const out: number[] = []
  const off = tzOffset(from, tz)
  for (let local = Math.ceil((from + off) / step) * step; out.length < 500; local += step) {
    const t = fromLocal(local, tz)
    if (t > to) break
    if (t >= from && (out.length === 0 || t > out[out.length - 1])) out.push(t)
  }
  return out
}

/** 17:42:05 */
export const fmtTime = (ms: number, tz: string) => formatter(tz, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(ms)

/** 17:42 */
export const fmtHM = (ms: number, tz: string) => formatter(tz, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(ms)

/** Fri, Nov 6 (for a YYYY-MM-DD date) */
export function fmtDay(date: string): string {
  return new Date(localAsUTC(date, 0, 12)).toLocaleDateString('en-US', { timeZone: 'UTC', weekday: 'short', month: 'short', day: 'numeric' })
}

/** 26 s, 4m 05s, 1h 02m */
export function fmtDur(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${pad2(s % 60)}s`
  return `${Math.floor(m / 60)}h ${pad2(m % 60)}m`
}

/** 0:09 (position inside a clip) */
export function fmtClock(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) sec = 0
  return `${Math.floor(sec / 60)}:${pad2(Math.floor(sec % 60))}`
}

/** 20.7 MB */
export function fmtSize(bytes: number): string {
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`
  return `${(bytes / 1e6).toFixed(1)} MB`
}
