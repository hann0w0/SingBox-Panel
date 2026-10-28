import { describe, expect, it } from 'vitest'
import { LogReplay, mergeLogSnapshot } from './logReplay'

describe('log follower replay', () => {
  it('skips the overlapping tail across SSE batches but preserves new identical lines', () => {
    const replay = new LogReplay(['a', 'b', 'c'])
    expect(replay.consume(['b'])).toEqual([])
    expect(replay.consume(['c', 'new', 'c'])).toEqual(['new', 'c'])
  })

  it('matches the correct suffix when the historical log contains duplicate lines', () => {
    const replay = new LogReplay(['a', 'b', 'a', 'c'])
    expect(replay.consume(['a'])).toEqual([])
    expect(replay.consume(['c', 'new'])).toEqual(['new'])
  })

  it('does not discard events when the follower joins another tab or replay was rotated away', () => {
    expect(new LogReplay([]).consume(['new'])).toEqual(['new'])
    expect(new LogReplay(['old']).consume(['new', 'new'])).toEqual(['new', 'new'])
  })

  it('merges a refreshing snapshot with live lines without losing the gap', () => {
    expect(mergeLogSnapshot(['a', 'b', 'c'], ['c', 'new'])).toEqual(['a', 'b', 'c', 'new'])
    expect(mergeLogSnapshot(['a', 'b'], ['new'])).toEqual(['a', 'b', 'new'])
  })
})
