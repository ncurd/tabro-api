import { describe, expect, it } from 'vitest'
import { formIntervalsToAPI, validateIntervals, type IntervalFormEntry } from '../types'

const tier = (label: string, price: number | string | null): IntervalFormEntry => ({
  min_tokens: 0, max_tokens: null, tier_label: label,
  input_price: null, output_price: null, cache_write_price: null, cache_read_price: null,
  per_request_price: price, sort_order: 0
})

describe('video pricing form', () => {
  it('accepts independent resolution tiers and preserves per-second units', () => {
    const tiers = [tier('720P', '0.12'), tier('1080P', '0.24')]
    expect(validateIntervals(tiers, 'video')).toBeNull()
    expect(formIntervalsToAPI(tiers).map(item => item.per_request_price)).toEqual([0.12, 0.24])
  })
  it('distinguishes an explicit free tier from a missing price', () => {
    expect(validateIntervals([tier('720P', 0)], 'video')).toBeNull()
    expect(validateIntervals([tier('720P', null)], 'video')).not.toBeNull()
    expect(validateIntervals([tier('720P', '')], 'video')).not.toBeNull()
  })
  it('rejects ambiguous resolution labels and non-finite or negative prices', () => {
    expect(validateIntervals([tier('720P', 1), tier(' 720p ', 2)], 'video')).not.toBeNull()
    for (const price of [-1, Infinity, 'invalid']) {
      expect(validateIntervals([tier('1080P', price)], 'video')).not.toBeNull()
    }
  })
  it('still rejects overlapping token ranges', () => {
    expect(validateIntervals([tier('a', 1), tier('b', 2)], 'token')).not.toBeNull()
  })
})
