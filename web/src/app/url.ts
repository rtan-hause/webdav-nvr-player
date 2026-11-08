import { app } from './state'

// ?cam=&t= in the address bar, so a reload or shared link returns to the same moment.

/** The camera and time in the address bar; 0 (or NaN) where missing. */
export function positionInURL(): { cam: number; t: number } {
  const q = new URLSearchParams(location.search)
  return { cam: Number(q.get('cam')), t: Number(q.get('t')) }
}

/** Keep ?cam=&t= up to date with the playhead. */
export function syncURL() {
  const s = app.get()
  if (!s.day) return
  const q = new URLSearchParams(location.search)
  const t = String(Math.floor(s.t / 1000) * 1000)
  if (q.get('t') === t && q.get('cam') === String(s.cam)) return
  q.set('cam', String(s.cam))
  q.set('t', t)
  history.replaceState(history.state, '', `${location.pathname}?${q}${location.hash}`)
}
