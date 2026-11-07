// Line icons for the player (24×24, currentColor).

const svg = {
  viewBox: '0 0 24 24',
  className: 'size-6',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.75,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
}

const small = { ...svg, className: 'size-[18px]' }

export const Icon = {
  Play: () => (
    <svg {...svg}>
      <path d="M7 4.5v15l12-7.5z" fill="currentColor" />
    </svg>
  ),
  Pause: () => (
    <svg {...svg}>
      <path d="M8 5v14M16 5v14" strokeWidth={3} />
    </svg>
  ),
  Skip: ({ back = false, seconds }: { back?: boolean; seconds: number }) => (
    <svg {...svg}>
      {back ? (
        <>
          <path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" />
          <path d="M3 3v5h5" />
        </>
      ) : (
        <>
          <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" />
          <path d="M21 3v5h-5" />
        </>
      )}
      <text x="12" y="15.6" textAnchor="middle" fontSize="8.5" fontWeight="600" fill="currentColor" stroke="none" fontFamily="IBM Plex Mono, ui-monospace, monospace">
        {seconds}
      </text>
    </svg>
  ),
  Sound: () => (
    <svg {...svg}>
      <path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" />
      <path d="M15.5 9a4 4 0 0 1 0 6M18 6.5a7.5 7.5 0 0 1 0 11" />
    </svg>
  ),
  Muted: () => (
    <svg {...svg}>
      <path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" />
      <path d="M16 9.5l5 5M21 9.5l-5 5" />
    </svg>
  ),
  Expand: () => (
    <svg {...svg}>
      <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
    </svg>
  ),
  Shrink: () => (
    <svg {...svg}>
      <path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" />
    </svg>
  ),
  PrevClip: () => (
    <svg {...small}>
      <path d="M6 5v14M19 5v14L9 12z" fill="currentColor" />
    </svg>
  ),
  NextClip: () => (
    <svg {...small}>
      <path d="M18 5v14M5 5v14l10-7z" fill="currentColor" />
    </svg>
  ),
  ChevronLeft: () => (
    <svg {...small}>
      <path d="M15 5l-7 7 7 7" strokeWidth={2} />
    </svg>
  ),
  ChevronRight: () => (
    <svg {...small}>
      <path d="M9 5l7 7-7 7" strokeWidth={2} />
    </svg>
  ),
  Calendar: () => (
    <svg {...small}>
      <rect x="4" y="5.5" width="16" height="14.5" rx="2" />
      <path d="M4 10h16M8.5 3.5v4M15.5 3.5v4" />
    </svg>
  ),
  Status: () => (
    <svg {...small}>
      <rect x="4" y="4" width="16" height="6" rx="1.5" />
      <rect x="4" y="14" width="16" height="6" rx="1.5" />
      <path d="M8 7h.01M8 17h.01" strokeWidth={2.5} />
    </svg>
  ),
}
