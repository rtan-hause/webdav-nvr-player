import { useStore } from '../lib/store'
import { app, clipAtPlayhead } from './state'

/** Id of the clip under the playhead (re-renders only when it changes). */
export const useClipAtPlayhead = () => useStore(app, (s) => clipAtPlayhead(s)?.id ?? null)
