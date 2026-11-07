import { useEffect, useState } from 'react'
import { app } from '../app/state'
import { useStore } from '../lib/store'

// Short notices over the video ("Skipped 4m 12s with no recording").
export function Toast() {
  const toast = useStore(app, (s) => s.toast)
  const [hidden, setHidden] = useState<number | null>(null)
  useEffect(() => {
    if (!toast) return
    const t = window.setTimeout(() => setHidden(toast.id), 2400)
    return () => clearTimeout(t)
  }, [toast])
  const show = toast !== null && hidden !== toast.id
  return (
    <div
      role="status"
      aria-live="polite"
      className={`toast pointer-events-none absolute top-3 left-1/2 max-w-[90%] -translate-x-1/2 truncate rounded-full border border-line bg-ink-0/88 px-3 py-1.5 text-[13px] transition-opacity duration-200 motion-reduce:transition-none ${show ? 'opacity-100' : 'opacity-0'}`}
    >
      {toast?.msg}
    </div>
  )
}
