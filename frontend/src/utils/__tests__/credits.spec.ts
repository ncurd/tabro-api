import { describe, expect, it } from 'vitest'

import { formatCreditNumber, formatCredits } from '../credits'

describe('credits formatting', () => {
  it('formats finite numeric values with the credit unit', () => {
    expect(formatCredits(12.3)).toBe('12.30 ✦')
    expect(formatCredits(0, { fractionDigits: 6 })).toBe('0.000000 ✦')
  })

  it('can return only the number part for compact layouts', () => {
    expect(formatCreditNumber(1.23456, { fractionDigits: 4 })).toBe('1.2346')
  })

  it('uses the configured empty value for missing or invalid values', () => {
    expect(formatCredits(undefined)).toBe('-')
    expect(formatCredits(Number.NaN, { emptyValue: '暂无' })).toBe('暂无')
  })
})
