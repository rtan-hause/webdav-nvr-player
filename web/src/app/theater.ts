import { app } from './state'

// Theater mode (decision 18): the video full screen, with the browser's
// fullscreen when it allows it.

let root: HTMLElement | null = null
export const setRoot = (el: HTMLElement | null) => {
  root = el
}

export function enterTheater() {
  app.set({ layout: 'theater', drawer: false })
  if (document.fullscreenEnabled && root && !document.fullscreenElement) root.requestFullscreen().catch(() => {})
}

export function exitTheater() {
  app.set({ layout: 'split', drawer: false })
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
}

export const toggleTheater = () => (app.get().layout === 'theater' ? exitTheater() : enterTheater())
