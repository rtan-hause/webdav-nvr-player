import { useEffect, useRef, useState } from 'react'
import './app/app.css'
import {
  app,
  boot,
  exitTheater,
  jumpClip,
  refresh,
  selectCamera,
  setRoot,
  skip,
  stepRate,
  syncURL,
  toggleMute,
  togglePlay,
  toggleTheater,
  zoomStep,
  type Boot,
} from './app/state'
import { useStore } from './lib/store'
import { ClipList } from './components/ClipList'
import { Dock } from './components/Dock'
import { Header } from './components/Header'
import { Viewport } from './components/Viewport'
import { StatusPage } from './StatusPage'
import { Settings } from './Settings'
import { SignIn } from './SignIn'
import { getJSON, SIGNED_OUT, type AuthStatus } from './lib/api'

// Layout C "Split" (plan decision 14): clip list on the left, video and
// timeline on the right; stacked on a phone. Theater mode (decision 18) puts
// the video full screen with the header, dock and clip drawer over it.

const IDLE_MS = 2500

const useHash = () => {
  const [hash, setHash] = useState(location.hash)
  useEffect(() => {
    const on = () => setHash(location.hash)
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  return hash
}

export default function App() {
  const [auth, setAuth] = useState<AuthStatus | null>(null)
  const [offline, setOffline] = useState(false)

  useEffect(() => {
    let live = true
    getJSON<AuthStatus>('/api/auth')
      .then((a) => {
        if (live) setAuth(a)
      })
      .catch(() => {
        if (live) setOffline(true)
      })
    // Any API call answered "sign in" (session expired or signed out elsewhere) returns here.
    const signedOut = () => setAuth((a) => (a && a.mode !== 'none' ? { ...a, signed_in: false, user: undefined } : a))
    window.addEventListener(SIGNED_OUT, signedOut)
    return () => {
      live = false
      window.removeEventListener(SIGNED_OUT, signedOut)
    }
  }, [])

  if (offline) {
    return (
      <main className="grid h-full place-content-center justify-items-center gap-3 p-4 text-center">
        <p className="font-display text-2xl font-semibold">Can’t reach the NVR server</p>
        <p className="text-muted">Check that it’s running, then try again.</p>
        <button type="button" onClick={() => location.reload()} className="rounded-full border border-line bg-ink-2 px-4 py-1.5 font-medium hover:border-faint focus-visible:outline-2 focus-visible:outline-sodium">
          Try again
        </button>
      </main>
    )
  }
  if (!auth) return <p className="grid h-full place-items-center text-muted">Loading…</p>
  if (!auth.signed_in) return <SignIn auth={auth} onSignedIn={setAuth} />
  return (
    <SignedIn
      auth={auth}
      onSignOut={() => {
        app.set({ playing: false })
        history.replaceState(history.state, '', location.pathname + location.search)
        setAuth({ ...auth, signed_in: false, user: undefined })
      }}
    />
  )
}

function SignedIn({ auth, onSignOut }: { auth: AuthStatus; onSignOut: () => void }) {
  const [phase, setPhase] = useState<Boot | { kind: 'loading' }>({ kind: 'loading' })
  const hash = useHash()

  useEffect(() => {
    let live = true
    boot().then((b) => {
      if (live) setPhase(b)
    })
    return () => {
      live = false
    }
  }, [])

  if (hash === '#/settings') return <Settings auth={auth} onSignOut={onSignOut} />
  if (hash === '#/status' || phase.kind === 'setup') {
    return <StatusPage reason={phase.kind === 'setup' ? phase.reason : undefined} playerReady={phase.kind === 'ready'} />
  }
  if (phase.kind === 'loading') {
    return <p className="grid h-full place-items-center text-muted">Loading recordings…</p>
  }
  return <Player />
}

function Player() {
  const root = useRef<HTMLDivElement>(null)
  const layout = useStore(app, (s) => s.layout)
  const drawer = useStore(app, (s) => s.drawer)
  const quiet = useStore(app, (s) => s.idle && s.playing && !s.drawer && !s.scrubbing)
  const dayError = useStore(app, (s) => s.dayError)

  useEffect(() => {
    setRoot(root.current)
    const el = root.current!

    // Header and dock heights, so theater mode can place things around them.
    const ro = new ResizeObserver(() => {
      el.style.setProperty('--top-h', `${el.querySelector<HTMLElement>('.top')?.offsetHeight ?? 0}px`)
      el.style.setProperty('--dock-h', `${el.querySelector<HTMLElement>('.dock')?.offsetHeight ?? 0}px`)
    })
    for (const sel of ['.top', '.dock']) {
      const n = el.querySelector(sel)
      if (n) ro.observe(n)
    }

    // Idle: no pointer or key activity for a while fades the controls while playing.
    let idleTimer = 0
    const poke = () => {
      if (app.get().idle) app.set({ idle: false })
      clearTimeout(idleTimer)
      idleTimer = window.setTimeout(() => app.set({ idle: true }), IDLE_MS)
    }
    const activity = ['pointermove', 'pointerdown', 'keydown', 'wheel'] as const
    for (const ev of activity) window.addEventListener(ev, poke, { passive: true })

    // Leaving the browser's full screen (Esc) leaves theater mode too.
    const onFullscreen = () => {
      if (!document.fullscreenElement && app.get().layout === 'theater') exitTheater()
    }
    document.addEventListener('fullscreenchange', onFullscreen)

    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const target = e.target instanceof Element ? e.target : null
      if (target?.closest('input, textarea, select, [contenteditable="true"]') && e.key !== 'Escape') return
      if (target?.tagName === 'BUTTON' && (e.key === ' ' || e.key === 'Enter')) return
      const s = app.get()
      switch (e.key) {
        case ' ':
        case 'k':
          togglePlay()
          break
        case 'ArrowLeft':
          skip(e.shiftKey ? -60 : -5)
          break
        case 'ArrowRight':
          skip(e.shiftKey ? 60 : 5)
          break
        case '[':
          jumpClip(-1)
          break
        case ']':
          jumpClip(1)
          break
        case 'f':
        case 'F':
          toggleTheater()
          break
        case 'm':
          toggleMute()
          break
        case '<':
          stepRate(-1)
          break
        case '>':
          stepRate(1)
          break
        case '+':
        case '=':
          zoomStep(1)
          break
        case '-':
        case '_':
          zoomStep(-1)
          break
        case 'Escape':
          if (s.drawer) app.set({ drawer: false })
          else if (s.layout === 'theater') exitTheater()
          else return
          break
        default: {
          const n = /^[1-9]$/.test(e.key) ? s.cameras[Number(e.key) - 1] : undefined
          if (!n) return
          selectCamera(n.id)
        }
      }
      e.preventDefault()
    }
    window.addEventListener('keydown', onKey)

    const urlTimer = window.setInterval(syncURL, 2000)
    const refreshTimer = window.setInterval(() => void refresh(), 30_000)

    return () => {
      setRoot(null)
      ro.disconnect()
      clearTimeout(idleTimer)
      for (const ev of activity) window.removeEventListener(ev, poke)
      document.removeEventListener('fullscreenchange', onFullscreen)
      window.removeEventListener('keydown', onKey)
      clearInterval(urlTimer)
      clearInterval(refreshTimer)
    }
  }, [])

  return (
    <div ref={root} className="app" data-layout={layout} data-drawer={drawer || undefined} data-quiet={quiet || undefined}>
      <Header />
      <main className="player" aria-label="Video">
        <Viewport />
        {dayError && (
          <p role="alert" className="absolute inset-x-3 top-3 rounded-lg border border-rec/50 bg-ink-0/90 px-3 py-2 text-sm">
            Couldn’t load this day’s clips: {dayError}
          </p>
        )}
      </main>
      <section className="dock" aria-label="Timeline">
        <Dock />
      </section>
      <aside className="events" aria-label="Clips">
        <ClipList />
      </aside>
    </div>
  )
}
