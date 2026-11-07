// Types and fetchers for the Go API (internal/httpapi).

export type Camera = { id: number; name: string; channel: string }

export type Clip = {
  id: number
  camera_id: number
  start: number // Unix ms
  end: number
  codec: string // display name, e.g. "HEVC"
  width: number
  height: number
  size: number
  file: string
  thumb: boolean
  thumb_kind?: 'snapshot' | 'frame'
}

/** A camera snapshot not attached to any clip (shown as a dot on the timeline). */
export type Snapshot = { id: number; camera_id: number; ts: number }

export type SourceStatus = {
  configured: boolean
  url?: string
  username?: string
  profile?: string
  time_zone?: string
  ok: boolean
  error?: string
  folders?: string[]
  files: number
}

export type IndexStatus = {
  index: {
    clips: Record<string, number>
    thumbs: Record<string, number>
    snapshots: Record<string, number>
    paired_snapshots: number
    cameras: number
    latest_clip_ms: number
    earliest_clip_ms: number
  }
  ingest: {
    scanning?: string
    last_full_scan: string
    last_recent_scan: string
    last_scan_error?: string
    last_scan_added: number
    working: number
  }
  database: string
  time_zone: string
  webhook: boolean
}

export class HttpError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export type AuthStatus = {
  mode: 'password' | 'oidc' | 'none'
  signed_in: boolean
  user?: string
  provider?: string // OIDC button label
}

/** Fired when the server says the session is gone; the app shows the sign-in page. */
export const SIGNED_OUT = 'nvr:signed-out'

async function check(r: Response): Promise<Response> {
  if (r.status === 401) window.dispatchEvent(new Event(SIGNED_OUT))
  if (!r.ok) {
    let msg = `HTTP ${r.status}`
    try {
      const body = (await r.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // not JSON
    }
    throw new HttpError(r.status, msg)
  }
  return r
}

export async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const r = await check(await fetch(url, { signal }))
  return r.json() as Promise<T>
}

/** POST/PUT a JSON body; resolves to the parsed reply, or null for an empty one. */
export async function sendJSON<T = unknown>(method: 'POST' | 'PUT', url: string, body?: unknown): Promise<T | null> {
  const r = await check(await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) }))
  if (r.status === 204) return null
  return r.json() as Promise<T>
}

export const isAbort = (e: unknown) => e instanceof DOMException && e.name === 'AbortError'

export const streamURL = (id: number) => `/api/clips/${id}/stream`
export const thumbURL = (id: number) => `/api/clips/${id}/thumb`

export const fetchClips = (from: number, to: number, signal?: AbortSignal) =>
  getJSON<{ clips: Clip[]; snapshots: Snapshot[] }>(`/api/clips?from=${from}&to=${to}`, signal)

export const fetchDays = (from: string, to: string, signal?: AbortSignal) => getJSON<string[]>(`/api/days?from=${from}&to=${to}`, signal)

// Can this browser decode the codec? Checked once per codec.
const playable = new Map<string, boolean>()
export function canPlay(codec: string): boolean {
  let ok = playable.get(codec)
  if (ok === undefined) {
    const v = document.createElement('video')
    const types: Record<string, string[]> = {
      HEVC: ['video/mp4; codecs="hvc1.1.6.L153.B0"', 'video/mp4; codecs="hev1.1.6.L153.B0"'],
      'H.264': ['video/mp4; codecs="avc1.640033"'],
      AV1: ['video/mp4; codecs="av01.0.12M.08"'],
    }
    ok = (types[codec] ?? []).some((t) => v.canPlayType(t) !== '') || !(codec in types)
    playable.set(codec, ok)
  }
  return ok
}
