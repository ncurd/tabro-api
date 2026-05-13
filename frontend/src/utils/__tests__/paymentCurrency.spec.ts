import { describe, expect, it } from 'vitest'

import {
  formatPaymentAmount,
  getCurrencyCreditRate,
  getPaymentCurrencyFromType,
  isStripePaymentType,
  toCredits,
} from '../paymentCurrency'

describe('paymentCurrency helpers', () => {
  it('converts supported recharge currencies to credits', () => {
    expect(toCredits(50, 'CNY')).toBe(50)
    expect(toCredits(10, 'USD')).toBe(65)
    expect(toCredits(10, 'GBP')).toBe(90)
    expect(toCredits(10, 'EUR')).toBe(76)
  })

  it('exposes fixed credit rates for supported currencies', () => {
    expect(getCurrencyCreditRate('CNY')).toBe(1)
    expect(getCurrencyCreditRate('USD')).toBe(6.5)
    expect(getCurrencyCreditRate('GBP')).toBe(9)
    expect(getCurrencyCreditRate('EUR')).toBe(7.6)
  })

  it('formats payment amounts with the selected currency symbol', () => {
    expect(formatPaymentAmount(12.3, 'CNY')).toBe('¥12.30')
    expect(formatPaymentAmount(12.3, 'USD')).toBe('$12.30')
    expect(formatPaymentAmount(12.3, 'GBP')).toBe('£12.30')
    expect(formatPaymentAmount(12.3, 'EUR')).toBe('€12.30')
  })

  it('recognizes persisted stripe payment types with currency suffixes', () => {
    expect(isStripePaymentType('stripe')).toBe(true)
    expect(isStripePaymentType('stripe_usd')).toBe(true)
    expect(getPaymentCurrencyFromType('stripe_gbp')).toBe('GBP')
    expect(getPaymentCurrencyFromType('stripe_eur')).toBe('EUR')
    expect(getPaymentCurrencyFromType('alipay')).toBe('CNY')
  })
})
