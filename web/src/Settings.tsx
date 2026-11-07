import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { getJSON, HttpError, isAbort, sendJSON, type AuthStatus } from './lib/api'

// Settings (#/settings): how long sessions last, the admin password, signing
// out, and a link to the server and index status page.

const SESSION_CHOICES = [1, 7, 14, 30, 90, 365]
const MIN_PASSWORD = 10 // matches the server (auth.MinPasswordLength)

const days = (n: number) => (n === 1 ? '1 day' : n === 365 ? '1 year' : `${n} days`)
const errorText = (e: unknown) => (e instanceof HttpError ? e.message : 'Couldn’t reach the server.')

export function Settings({ auth, onSignOut }: { auth: AuthStatus; onSignOut: () => void }) {
  const signIn = auth.mode !== 'none'
  return (
    <div className="flex min-h-full flex-col">
      <header className="flex items-center gap-2 border-b border-line bg-ink-1 px-4 py-3">
        <span aria-hidden className="size-2 rounded-full bg-rec shadow-[0_0_0_3px_rgba(229,72,77,.18)]" />
        <span className="font-display text-base font-semibold tracking-[0.12em] uppercase">NVR</span>
        <a href="#" className="ml-auto rounded-full bg-sodium px-4 py-1.5 text-sm font-semibold text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium">
          Back to the player
        </a>
      </header>
      <main className="mx-auto grid w-full max-w-2xl gap-6 px-4 py-10">
        <h1 className="font-display text-3xl font-semibold">Settings</h1>

        {signIn ? (
          <>
            <Account auth={auth} onSignOut={onSignOut} />
            <SessionLength />
            {auth.mode === 'password' && <Password user={auth.user ?? 'admin'} />}
          </>
        ) : (
          <Section title="Sign-in">
            <p className="text-muted">
              Sign-in is off (<code className="font-mono text-text">AUTH=none</code>), so anyone who can reach this server can watch the cameras. Use this only behind a proxy that signs people in.
            </p>
          </Section>
        )}

        <Section title="Server and index">
          <p className="text-muted">Whether the app can reach your WebDAV server, and what it has indexed.</p>
          <a href="#/status" className="justify-self-start rounded-full border border-line bg-ink-2 px-4 py-1.5 text-sm font-medium hover:border-faint focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium">
            Open status page
          </a>
        </Section>
      </main>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-3 rounded-xl border border-line bg-ink-1 p-4">
      <h2 className="font-display text-lg font-semibold tracking-wide uppercase">{title}</h2>
      {children}
    </section>
  )
}

function Note({ tone, children }: { tone: 'good' | 'bad'; children: ReactNode }) {
  return (
    <p role={tone === 'bad' ? 'alert' : 'status'} className={`text-sm ${tone === 'good' ? 'text-clip-hi' : 'text-rec'}`}>
      {children}
    </p>
  )
}

const button = 'rounded-full border border-line bg-ink-2 px-4 py-1.5 text-sm font-medium hover:border-faint focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium disabled:opacity-50'

function Account({ auth, onSignOut }: { auth: AuthStatus; onSignOut: () => void }) {
  const [note, setNote] = useState<{ tone: 'good' | 'bad'; text: string } | null>(null)
  const signOut = async () => {
    try {
      await sendJSON('POST', '/api/logout')
    } finally {
      onSignOut()
    }
  }
  const signOutOthers = async () => {
    try {
      const r = await sendJSON<{ signed_out: number }>('POST', '/api/sessions/sign-out-others')
      const n = r?.signed_out ?? 0
      setNote({ tone: 'good', text: n === 0 ? 'No other devices were signed in.' : `Signed out ${n} other device${n === 1 ? '' : 's'}.` })
    } catch (e) {
      setNote({ tone: 'bad', text: errorText(e) })
    }
  }
  return (
    <Section title="Account">
      <p className="text-muted">
        Signed in as <b className="text-text">{auth.user}</b>
        {auth.mode === 'oidc' && auth.provider ? ` through ${auth.provider}` : ''}.
      </p>
      <div className="flex flex-wrap gap-2">
        <button type="button" className={button} onClick={signOut}>
          Sign out
        </button>
        <button type="button" className={button} onClick={signOutOthers}>
          Sign out other devices
        </button>
      </div>
      {note && <Note tone={note.tone}>{note.text}</Note>}
    </Section>
  )
}

function SessionLength() {
  const [value, setValue] = useState<number | null>(null)
  const [note, setNote] = useState<{ tone: 'good' | 'bad'; text: string } | null>(null)

  useEffect(() => {
    const ctrl = new AbortController()
    getJSON<{ session_days: number }>('/api/settings', ctrl.signal)
      .then((s) => setValue(s.session_days))
      .catch((e: unknown) => {
        if (!isAbort(e)) setNote({ tone: 'bad', text: errorText(e) })
      })
    return () => ctrl.abort()
  }, [])

  const save = async (n: number) => {
    setValue(n)
    setNote(null)
    try {
      await sendJSON('PUT', '/api/settings', { session_days: n })
      setNote({ tone: 'good', text: `Saved. A device you don’t use for ${days(n)} is signed out.` })
    } catch (e) {
      setNote({ tone: 'bad', text: errorText(e) })
    }
  }

  const choices = value !== null && !SESSION_CHOICES.includes(value) ? [...SESSION_CHOICES, value].sort((a, b) => a - b) : SESSION_CHOICES
  return (
    <Section title="Stay signed in">
      <p className="max-w-[60ch] text-muted">How long a device stays signed in without being used. Each visit starts the count again.</p>
      <label className="flex flex-wrap items-center gap-3 text-sm">
        <span className="text-muted">Sign out after</span>
        <select
          id="session-days"
          disabled={value === null}
          value={value ?? ''}
          onChange={(e) => save(Number(e.target.value))}
          className="rounded-lg border border-line bg-ink-2 px-3 py-1.5 text-text [color-scheme:dark] focus-visible:border-sodium focus-visible:outline-none"
        >
          {choices.map((n) => (
            <option key={n} value={n}>
              {days(n)} unused
            </option>
          ))}
        </select>
      </label>
      {note && <Note tone={note.tone}>{note.text}</Note>}
    </Section>
  )
}

function Password({ user }: { user: string }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<{ tone: 'good' | 'bad'; text: string } | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (next.length < MIN_PASSWORD) return setNote({ tone: 'bad', text: `Use at least ${MIN_PASSWORD} characters.` })
    if (next !== again) return setNote({ tone: 'bad', text: 'The new passwords don’t match.' })
    setBusy(true)
    setNote(null)
    try {
      await sendJSON('POST', '/api/password', { current, new: next })
      setCurrent('')
      setNext('')
      setAgain('')
      setNote({ tone: 'good', text: 'Password changed. Other devices were signed out.' })
    } catch (err) {
      setNote({ tone: 'bad', text: errorText(err) })
    } finally {
      setBusy(false)
    }
  }

  const field = 'w-full max-w-sm rounded-lg border border-line bg-ink-2 px-3 py-2 text-text focus-visible:border-sodium focus-visible:outline-none'
  return (
    <Section title="Password">
      <form onSubmit={submit} className="grid gap-3">
        {/* Lets password managers file the new password under the right account. */}
        <input type="text" name="username" autoComplete="username" value={user} readOnly hidden />
        <label className="grid gap-1.5 text-sm">
          <span className="text-muted">Current password</span>
          <input id="pw-current" className={field} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-muted">New password (at least {MIN_PASSWORD} characters)</span>
          <input id="pw-new" className={field} type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required />
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-muted">New password again</span>
          <input id="pw-again" className={field} type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required />
        </label>
        <button type="submit" disabled={busy} className={`${button} justify-self-start`}>
          {busy ? 'Changing…' : 'Change password'}
        </button>
        {note && <Note tone={note.tone}>{note.text}</Note>}
      </form>
    </Section>
  )
}
