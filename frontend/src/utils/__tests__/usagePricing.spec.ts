import { describe, expect, it } from 'vitest'
import { formatUsageRate, isCentralUsage, isImagePricedUsage, isTokenPricedUsage, usageDisplayCost, usageDisplayMultiplier, usageDisplayUnitCost } from '../usagePricing'

describe('historical billing prices', () => {
  it('converts central line costs with the stored final multiplier including free requests', () => {
    const usage = { billing_source: 'central', billing_mode: 'token', rate_multiplier: 6 }
    expect(usageDisplayCost(0.25, usage)).toBe(1.5)
    expect(usageDisplayCost(0.25, { ...usage, rate_multiplier: 0 })).toBe(0)
    expect(usageDisplayCost(0.25, { ...usage, rate_multiplier: 0.4 })).toBe(0.1)
    expect(usageDisplayCost(0.25, { ...usage, rate_multiplier: NaN })).toBeNull()
    expect(usageDisplayCost(Number.MAX_VALUE, usage)).toBeNull()
  })

  it('keeps local history unchanged and recognizes old central logs without guessing their pricing mode', () => {
    expect(usageDisplayCost(0.25, { billing_source: 'local', billing_mode: 'token', rate_multiplier: 6 })).toBe(0.25)
    expect(isCentralUsage({ billing_mode: 'central' })).toBe(true)
    expect(isTokenPricedUsage({ billing_mode: 'central', input_tokens: 100 })).toBe(false)
    expect(isTokenPricedUsage({ billing_mode: 'per_request', input_tokens: 100 })).toBe(false)
    expect(isTokenPricedUsage({ billing_mode: 'token' })).toBe(true)
  })

  it('shows actual price per image rather than the entire multi-image request', () => {
    const usage = { billing_source: 'central', billing_mode: 'image', image_count: 4, rate_multiplier: 3 }
    expect(isImagePricedUsage(usage)).toBe(true)
    expect(usageDisplayUnitCost(0.8, usage)).toBeCloseTo(0.6)
    expect(usageDisplayUnitCost(0.8, { ...usage, billing_source: 'local' })).toBe(0.8)
  })

  it('aligns high precision and free charges with recorded amounts rather than the rounded stored rate', () => {
    const usage = { billing_source: 'central', billing_mode: 'token', rate_multiplier: 3.1416, total_cost: 1, actual_cost: 3.141592 }
    expect(usageDisplayMultiplier(usage)).toBe(3.141592)
    expect(usageDisplayCost(0.1, usage)).toBeCloseTo(0.3141592, 10)
    expect(formatUsageRate(usage)).toBe('3.141592x')
    expect(usageDisplayCost(0.1, { ...usage, actual_cost: 0 })).toBe(0)
    expect(formatUsageRate({ ...usage, actual_cost: 0 })).toBe('0.0x')
    expect(formatUsageRate({ billing_source: 'central', rate_multiplier: NaN })).toBe('—')
  })
})
