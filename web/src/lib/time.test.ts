import { describe, expect, it } from 'vitest'
import { addDays, atLocalTime, dateOf, dayBounds, fmtClock, fmtDay, fmtDur, fmtHM, fmtSize, fmtTime, localTicks, tzOffset } from './time'

const LA = 'America/Los_Angeles'
const H = 3600_000

describe('dates in a time zone', () => {
  it('dateOf uses the camera time zone, not UTC', () => {
    const t = Date.UTC(2026, 10, 7, 5, 55) // 21:55 on the 6th in LA
    expect(dateOf(t, 'UTC')).toBe('2026-11-07')
    expect(dateOf(t, LA)).toBe('2026-11-06')
  })

  it('addDays crosses months and years', () => {
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01')
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28')
    expect(addDays('2026-11-06', -61)).toBe('2026-09-06')
  })

  it('dayBounds is 24 h, or 23/25 h when daylight saving changes', () => {
    const len = (d: string) => {
      const b = dayBounds(d, LA)
      return (b.end - b.start) / H
    }
    expect(len('2026-11-06')).toBe(24)
    expect(len('2026-03-08')).toBe(23)
    expect(len('2026-11-01')).toBe(25)
    expect(dayBounds('2026-11-06', LA).start).toBe(Date.UTC(2026, 10, 6, 8)) // PST is UTC-8
    expect(dayBounds('2026-11-06', 'UTC')).toEqual({ start: Date.UTC(2026, 10, 6), end: Date.UTC(2026, 10, 7) })
  })

  it('tzOffset is local minus UTC', () => {
    expect(tzOffset(Date.UTC(2026, 6, 1), LA)).toBe(-7 * H)
    expect(tzOffset(Date.UTC(2026, 0, 1), LA)).toBe(-8 * H)
  })
})

describe('atLocalTime', () => {
  it('reads h:mm and h:mm:ss on a local day', () => {
    expect(atLocalTime('2026-11-06', '17:42', 'UTC')).toBe(Date.UTC(2026, 10, 6, 17, 42))
    expect(atLocalTime('2026-11-06', ' 7:05:09 ', LA)).toBe(Date.UTC(2026, 10, 6, 15, 5, 9))
  })

  it('rejects times that are not on a clock', () => {
    for (const bad of ['24:00', '12:60', '12:00:60', '1200', '', 'noon']) expect(atLocalTime('2026-11-06', bad, 'UTC')).toBeNull()
  })
})

describe('localTicks', () => {
  it('puts ticks at round local times inside [from, to]', () => {
    const { start, end } = dayBounds('2026-11-06', LA)
    const ticks = localTicks(start, end - 1, 6 * H, LA)
    expect(ticks.map((t) => fmtHM(t, LA))).toEqual(['00:00', '06:00', '12:00', '18:00'])
  })

  it('skips the hour that does not exist when clocks go forward', () => {
    const { start, end } = dayBounds('2026-03-08', LA)
    const ticks = localTicks(start, end - 1, H, LA)
    expect(ticks).toHaveLength(23)
    expect(new Set(ticks.map((t) => fmtHM(t, LA))).has('02:00')).toBe(false)
  })
})

describe('formatting', () => {
  it('formats clock times in the camera time zone', () => {
    const t = Date.UTC(2026, 10, 7, 5, 55, 21)
    expect(fmtTime(t, LA)).toBe('21:55:21')
    expect(fmtHM(t, 'UTC')).toBe('05:55')
  })

  it('formats days, durations, clip positions and sizes', () => {
    expect(fmtDay('2026-11-06')).toBe('Fri, Nov 6')
    expect(fmtDur(26_000)).toBe('26 s')
    expect(fmtDur(245_000)).toBe('4m 05s')
    expect(fmtDur(3_720_000)).toBe('1h 02m')
    expect(fmtClock(9.7)).toBe('0:09')
    expect(fmtClock(Number.NaN)).toBe('0:00')
    expect(fmtSize(20_700_000)).toBe('20.7 MB')
    expect(fmtSize(1_250_000_000)).toBe('1.3 GB')
  })
})
