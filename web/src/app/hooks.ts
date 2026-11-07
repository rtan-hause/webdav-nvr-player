import { itemAt } from '../lib/clips'
import { useStore } from '../lib/store'
import { app, camClips } from './state'

/** Id of the clip under the playhead (re-renders only when it changes). */
export const useClipAtPlayhead = () => useStore(app, (s) => itemAt(camClips(s), s.t)?.id ?? null)
