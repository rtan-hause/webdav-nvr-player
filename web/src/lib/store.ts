import { useSyncExternalStore } from 'react'

// A tiny external store. Components read slices with useStore(store, selector);
// a selector must return a primitive or a stable reference so unrelated
// updates (like the playhead moving every frame) don't re-render everything.

export type Store<S> = {
  get(): S
  set(patch: Partial<S> | ((s: S) => Partial<S>)): void
  subscribe(fn: () => void): () => void
}

export function createStore<S extends object>(initial: S): Store<S> {
  let state = initial
  const subs = new Set<() => void>()
  return {
    get: () => state,
    set(patch) {
      const p = typeof patch === 'function' ? patch(state) : patch
      let changed = false
      for (const k in p) {
        if (!Object.is(p[k], state[k])) {
          changed = true
          break
        }
      }
      if (!changed) return
      state = { ...state, ...p }
      for (const fn of subs) fn()
    },
    subscribe(fn) {
      subs.add(fn)
      return () => subs.delete(fn)
    },
  }
}

export function useStore<S, T>(store: Store<S>, select: (s: S) => T): T {
  return useSyncExternalStore(store.subscribe, () => select(store.get()))
}
