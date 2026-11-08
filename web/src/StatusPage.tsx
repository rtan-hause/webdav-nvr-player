import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { fetchSource, fetchStatus, fetchVersion, isAbort, type IndexStatus, type SourceStatus } from './lib/api'

// Server and index status: whether the app can reach the WebDAV server and
// what it has indexed. Shown at #/status, and instead of the player when
// there's nothing to play yet (reason says why).

export function StatusPage({ reason, playerReady }: { reason?: string; playerReady: boolean }) {
  const [version, setVersion] = useState<string | null>(null)
  const [source, setSource] = useState<SourceStatus | null>(null)
  const [checking, setChecking] = useState(true)
  const [offline, setOffline] = useState(false)

  // `checking` starts true for the first load; the button sets it before re-checking.
  const check = useCallback((signal?: AbortSignal) => {
    Promise.all([fetchVersion(signal), fetchSource(signal)])
      .then(([v, s]) => {
        setVersion(v)
        setSource(s)
        setOffline(false)
      })
      .catch((e: unknown) => {
        if (!isAbort(e)) setOffline(true)
      })
      .finally(() => setChecking(false))
  }, [])

  useEffect(() => {
    const ctrl = new AbortController()
    check(ctrl.signal)
    return () => ctrl.abort()
  }, [check])

  return (
    <div className="flex min-h-full flex-col">
      <header className="flex items-center gap-2 border-b border-line bg-ink-1 px-4 py-3">
        <span aria-hidden className="size-2 rounded-full bg-rec shadow-[0_0_0_3px_rgba(229,72,77,.18)]" />
        <span className="font-display text-base font-semibold tracking-[0.12em] uppercase">NVR</span>
        {version && <span className="ml-auto font-mono text-xs text-faint">{version}</span>}
      </header>

      <main className="mx-auto grid w-full max-w-4xl gap-8 px-4 py-10">
        <div className="grid gap-1">
          <p className="font-display text-sm font-semibold tracking-[0.12em] text-sodium uppercase">Status</p>
          <h1 className="font-display text-3xl font-semibold text-balance">Recordings and index</h1>
          <p className="max-w-[62ch] text-muted">{reason ?? 'Whether the app can reach your WebDAV server, and what it has indexed.'}</p>
          {playerReady && (
            <a href="#" className="mt-2 justify-self-start rounded-full bg-sodium px-4 py-1.5 font-semibold text-sodium-ink hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium">
              Back to the player
            </a>
          )}
        </div>

        <section className="grid gap-3 rounded-xl border border-line bg-ink-1 p-4" aria-live="polite">
          <h2 className="font-display text-lg font-semibold tracking-wide uppercase">Recordings server</h2>
          {offline ? (
            <Status tone="bad" title="Server not responding">
              The page couldn’t reach the nvr server. Check that it’s still running.
            </Status>
          ) : !source ? (
            <p className="text-muted">Checking…</p>
          ) : !source.configured ? (
            <Status tone="bad" title="No WebDAV server set">
              Set <code className="font-mono">WEBDAV_URL</code>, <code className="font-mono">WEBDAV_USERNAME</code> and <code className="font-mono">WEBDAV_PASSWORD</code> (in <code className="font-mono">.env</code> for <code className="font-mono">make run</code>), then restart.
            </Status>
          ) : source.ok ? (
            <Status tone="good" title="Connected">
              Signed in as <b className="text-text">{source.username || '(no username)'}</b>. Found {source.folders?.length ?? 0} folder{source.folders?.length === 1 ? '' : 's'} and {source.files} file{source.files === 1 ? '' : 's'} at the root.
            </Status>
          ) : (
            <Status tone="bad" title="Couldn’t connect">
              {source.error}
            </Status>
          )}
          {source?.configured && (
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
              <dt className="text-muted">URL</dt>
              <dd className="font-mono break-all">{source.url}</dd>
              <dt className="text-muted">Camera profile</dt>
              <dd className="font-mono">{source.profile}</dd>
              <dt className="text-muted">Camera time zone</dt>
              <dd className="font-mono">{source.time_zone}</dd>
            </dl>
          )}
          <button
            type="button"
            onClick={() => {
              setChecking(true)
              check()
            }}
            disabled={checking}
            className="justify-self-start rounded-full border border-line bg-ink-2 px-4 py-1.5 text-sm font-medium hover:border-faint focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sodium disabled:opacity-50"
          >
            {checking ? 'Checking…' : 'Check again'}
          </button>
        </section>

        {source?.ok && <IndexSection />}
      </main>
    </div>
  )
}

function IndexSection() {
  const [status, setStatus] = useState<IndexStatus | null>(null)

  // Refreshes every 5 s while the page is open.
  useEffect(() => {
    const ctrl = new AbortController()
    const load = () =>
      fetchStatus(ctrl.signal)
        .then(setStatus)
        .catch((e: unknown) => {
          if (!isAbort(e)) console.error(e)
        })
    load()
    const t = setInterval(load, 5000)
    return () => {
      ctrl.abort()
      clearInterval(t)
    }
  }, [])

  if (!status) return null
  const { index, ingest } = status
  const when = (iso: string) => (iso.startsWith('0001') ? 'not yet' : new Date(iso).toLocaleTimeString('en-US', { hour12: false }))

  return (
    <section className="grid gap-4 rounded-xl border border-line bg-ink-1 p-4">
      <h2 className="font-display text-lg font-semibold tracking-wide uppercase">Index</h2>
      <div className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
        <Stat label="Cameras" value={index.cameras} />
        <Stat label="Clips ready" value={index.clips.ready ?? 0} note={[index.clips.pending && `${index.clips.pending} pending`, index.clips.failed && `${index.clips.failed} failed`].filter(Boolean).join(', ')} />
        <Stat label="Thumbnails" value={index.thumbs.done ?? 0} note={index.thumbs.waiting ? `${index.thumbs.waiting} waiting for a snapshot` : ''} />
        <Stat label="Snapshots paired" value={index.paired_snapshots} note={`of ${index.snapshots.ready ?? 0}`} />
      </div>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
        <dt className="text-muted">Scanning</dt>
        <dd>
          {ingest.scanning ? `${ingest.scanning} scan running…` : 'idle'}
          {ingest.working ? `, ${ingest.working} file${ingest.working === 1 ? '' : 's'} in progress` : ''}
        </dd>
        <dt className="text-muted">Last full scan</dt>
        <dd className="font-mono">{when(ingest.last_full_scan)}</dd>
        <dt className="text-muted">Last recent scan</dt>
        <dd className="font-mono">{when(ingest.last_recent_scan)}</dd>
        <dt className="text-muted">Webhook</dt>
        <dd>
          {status.webhook ? (
            'on'
          ) : (
            <>
              off (set <code className="font-mono">INGEST_SECRET</code>)
            </>
          )}
        </dd>
        <dt className="text-muted">Database</dt>
        <dd className="font-mono">{status.database}</dd>
        {ingest.last_scan_error && (
          <>
            <dt className="text-rec">Scan error</dt>
            <dd className="break-words">{ingest.last_scan_error}</dd>
          </>
        )}
      </dl>
    </section>
  )
}

function Stat({ label, value, note }: { label: string; value: number; note?: string | number | false }) {
  return (
    <div>
      <p className="text-sm text-muted">{label}</p>
      <p className="font-mono text-2xl tabular-nums">{value.toLocaleString()}</p>
      {note ? <p className="text-xs text-faint">{note}</p> : null}
    </div>
  )
}

function Status({ tone, title, children }: { tone: 'good' | 'bad'; title: string; children: ReactNode }) {
  return (
    <div className="flex gap-3">
      <span aria-hidden className={`mt-1.5 size-2.5 shrink-0 rounded-full ${tone === 'good' ? 'bg-clip-hi' : 'bg-rec'}`} />
      <div>
        <p className="font-semibold">{title}</p>
        <p className="text-muted">{children}</p>
      </div>
    </div>
  )
}
