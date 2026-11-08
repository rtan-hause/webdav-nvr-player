import { saveSetting } from '../lib/storage'
import { app, PREF_KEYS, RATES } from './state'

// Playback preferences. Rate and clip order are remembered between visits.

export function setRate(rate: number) {
  app.set({ rate })
  saveSetting(PREF_KEYS.rate, String(rate))
}

export function stepRate(dir: 1 | -1) {
  const i = RATES.indexOf(app.get().rate)
  setRate(RATES[Math.min(Math.max((i < 0 ? 1 : i) + dir, 0), RATES.length - 1)])
}

export const toggleMute = () => app.set((s) => ({ muted: !s.muted }))

export function toggleSort() {
  const newestFirst = !app.get().newestFirst
  app.set({ newestFirst })
  saveSetting(PREF_KEYS.newestFirst, String(newestFirst))
}
