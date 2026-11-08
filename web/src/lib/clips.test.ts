import { describe, expect, it } from 'vitest'
import { itemAt, lastStartLE, mergeSpans, nextAfter } from './clips'

const arr = [
  { start: 10, end: 20 },
  { start: 20, end: 30 },
  { start: 50, end: 60 },
]

describe('clip lookups', () => {
  it('lastStartLE finds the last item starting at or before t', () => {
    expect(lastStartLE(arr, 5)).toBe(-1)
    expect(lastStartLE(arr, 10)).toBe(0)
    expect(lastStartLE(arr, 25)).toBe(1)
    expect(lastStartLE(arr, 99)).toBe(2)
    expect(lastStartLE([], 10)).toBe(-1)
  })

  it('itemAt finds the item covering t, ends exclusive', () => {
    expect(itemAt(arr, 15)).toBe(arr[0])
    expect(itemAt(arr, 20)).toBe(arr[1])
    expect(itemAt(arr, 30)).toBeNull() // in the gap
    expect(itemAt(arr, 5)).toBeNull()
  })

  it('nextAfter finds the first item starting after t', () => {
    expect(nextAfter(arr, 5)).toBe(arr[0])
    expect(nextAfter(arr, 25)).toBe(arr[2])
    expect(nextAfter(arr, 50)).toBeNull()
  })

  it('mergeSpans joins items closer than maxGap', () => {
    expect(mergeSpans(arr, 0)).toEqual([
      { start: 10, end: 30 },
      { start: 50, end: 60 },
    ])
    expect(mergeSpans(arr, 20)).toEqual([{ start: 10, end: 60 }])
    expect(mergeSpans(arr, 0)[0]).not.toBe(arr[0]) // doesn't modify the input
  })
})
