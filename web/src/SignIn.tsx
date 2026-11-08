import { useState, type FormEvent } from 'react'
import { HttpError, login, type AuthStatus } from './lib/api'

// Sign-in page: the admin password (AUTH=password) or a button for the
// OpenID Connect provider (AUTH=oidc).

// An OIDC failure comes back as ?auth_error=...; read it once and tidy the address bar.
function takeAuthError(): string | null {
  const q = new URLSearchParams(location.search)
  const msg = q.get('auth_error')
  if (msg) {
    q.delete('auth_error')
    const rest = q.toString()
    history.replaceState(history.state, '', `${location.pathname}${rest ? `?${rest}` : ''}${location.hash}`)
  }
  return msg
}

export function SignIn({ auth, onSignedIn }: { auth: AuthStatus; onSignedIn: (a: AuthStatus) => void }) {
  const [error, setError] = useState<string | null>(takeAuthError)
  const [busy, setBusy] = useState(false)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const a = await login(username, password)
      if (a) onSignedIn(a)
    } catch (err) {
      setError(err instanceof HttpError ? err.message : 'Couldn’t reach the server. Check that it’s running.')
      setBusy(false)
    }
  }

  const next = encodeURIComponent(location.pathname + location.search)
  const field = 'w-full rounded-lg border border-line bg-ink-2 px-3 py-2 text-text placeholder:text-faint focus-visible:border-sodium focus-visible:outline-none'
  return (
    <main className="grid min-h-full place-items-center px-4 py-10">
      <div className="grid w-full max-w-sm gap-6 rounded-2xl border border-line bg-ink-1 p-6">
        <div className="flex items-center gap-2 font-display text-base font-semibold tracking-[0.12em] uppercase">
          <span aria-hidden className="size-2 rounded-full bg-rec shadow-[0_0_0_3px_rgba(229,72,77,.18)]" />
          NVR
        </div>
        <h1 className="font-display text-3xl font-semibold">Sign in</h1>

        {error && (
          <p role="alert" className="rounded-lg border border-rec/50 bg-rec/10 px-3 py-2 text-sm">
            {error}
          </p>
        )}

        {auth.mode === 'oidc' ? (
          <a
            href={`/auth/oidc/login?next=${next}`}
            className="rounded-full bg-sodium px-4 py-2.5 text-center font-semibold text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium"
          >
            Sign in with {auth.provider ?? 'single sign-on'}
          </a>
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <label className="grid gap-1.5 text-sm">
              <span className="text-muted">Username</span>
              <input id="signin-username" className={field} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" spellCheck={false} required />
            </label>
            <label className="grid gap-1.5 text-sm">
              <span className="text-muted">Password</span>
              <input id="signin-password" className={field} type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus required />
            </label>
            <button
              type="submit"
              disabled={busy}
              className="rounded-full bg-sodium px-4 py-2.5 font-semibold text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium disabled:opacity-60"
            >
              {busy ? 'Signing in…' : 'Sign in'}
            </button>
            <p className="text-sm text-muted">
              First time? The password for <b className="text-text">admin</b> is in the server log, on the line that starts “created the admin user”. Lost it? Run <code className="font-mono text-text">nvr reset-password</code> on the server.
            </p>
          </form>
        )}
      </div>
    </main>
  )
}
