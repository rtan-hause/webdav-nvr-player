// Lookups over one camera's clips, sorted by start.

type Span = { start: number; end: number }

/** Index of the last item starting at or before t, or -1. */
export function lastStartLE<T extends Span>(arr: readonly T[], t: number): number {
  let lo = 0
  let hi = arr.length - 1
  let ans = -1
  while (lo <= hi) {
    const m = (lo + hi) >> 1
    if (arr[m].start <= t) {
      ans = m
      lo = m + 1
    } else {
      hi = m - 1
    }
  }
  return ans
}

/** The item covering t, if any. */
export function itemAt<T extends Span>(arr: readonly T[], t: number): T | null {
  const i = lastStartLE(arr, t)
  return i >= 0 && t < arr[i].end ? arr[i] : null
}

/** The first item starting after t. */
export function nextAfter<T extends Span>(arr: readonly T[], t: number): T | null {
  return arr[lastStartLE(arr, t) + 1] ?? null
}

/** Clips joined where they touch (gaps under maxGap ms), for drawing a zoomed-out day. */
export function mergeSpans(arr: readonly Span[], maxGap = 2000): Span[] {
  const out: Span[] = []
  for (const c of arr) {
    const last = out[out.length - 1]
    if (last && c.start - last.end <= maxGap) last.end = Math.max(last.end, c.end)
    else out.push({ start: c.start, end: c.end })
  }
  return out
}
