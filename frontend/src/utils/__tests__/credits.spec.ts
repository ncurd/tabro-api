import { describe, expect, it } from 'vitest'

import { formatCreditNumber, formatCredits, formatExactCredits } from '../credits'

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

  it('preserves decimal strings from Auth without rounding through a number', () => {
    expect(formatExactCredits('9007199254740993.123456789012345678')).toBe('9007199254740993.123456789012345678 ✦')
    expect(formatExactCredits('00012.340000')).toBe('12.34 ✦')
    expect(formatExactCredits('0.000000')).toBe('0 ✦')
    expect(formatExactCredits('-0.00')).toBe('0 ✦')
    expect(formatExactCredits('-2.0500')).toBe('-2.05 ✦')
  })

  it('keeps missing and malformed Auth balances unknown', () => {
    for (const value of [null, undefined, '', 'NaN', '1e3', '<b>20</b>']) {
      expect(formatExactCredits(value)).toBe('—')
    }
  })

  it('shows two decimal places with exact midpoint-away-from-zero rounding like Auth', () => {
    expect(formatExactCredits('9007199254740993.125', { fractionDigits: 2 })).toBe('9007199254740993.13 ✦')
    expect(formatExactCredits('1.005', { fractionDigits: 2 })).toBe('1.01 ✦')
    expect(formatExactCredits('1.025', { fractionDigits: 2 })).toBe('1.03 ✦')
    expect(formatExactCredits('-1.005', { fractionDigits: 2 })).toBe('-1.01 ✦')
    expect(formatExactCredits('1.135', { fractionDigits: 2 })).toBe('1.14 ✦')
    expect(formatExactCredits('9.995', { fractionDigits: 2 })).toBe('10.00 ✦')
    expect(formatExactCredits('1.125001', { fractionDigits: 2 })).toBe('1.13 ✦')
    expect(formatExactCredits('-0.001', { fractionDigits: 2 })).toBe('0.00 ✦')
  })
})
