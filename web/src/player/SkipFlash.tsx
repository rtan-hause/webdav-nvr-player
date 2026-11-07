export type Skip = { side: 'back' | 'forward'; seconds: number; id: number }

// Brief "« 5 s" / "5 s »" badge on the side that was double-tapped.
export function SkipFlash({ skip }: { skip: Skip | null }) {
  if (!skip) return null
  const back = skip.side === 'back'
  return (
    <div aria-hidden className={`pointer-events-none absolute inset-y-0 flex w-2/5 items-center ${back ? 'left-0 justify-start' : 'right-0 justify-end'}`}>
      <span
        key={skip.id}
        className="skip-flash mx-[4.5rem] rounded-full bg-ink-0/70 px-3.5 py-2 font-mono text-sm font-medium text-text tabular-nums"
      >
        {back ? `« ${skip.seconds} s` : `${skip.seconds} s »`}
      </span>
    </div>
  )
}
