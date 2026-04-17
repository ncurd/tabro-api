import { describe, expect, it } from 'vitest'

import { isPaymentResultSuccessful, shouldOpenPaymentPageDirectly } from '../paymentFlow'

describe('paymentFlow helpers', () => {
  it('redirects stripe payments immediately even on desktop', () => {
    expect(shouldOpenPaymentPageDirectly('stripe', false)).toBe(true)
  })

  it('keeps non-stripe desktop payments in popup flow', () => {
    expect(shouldOpenPaymentPageDirectly('alipay', false)).toBe(false)
  })

  it('redirects any mobile payment directly', () => {
    expect(shouldOpenPaymentPageDirectly('alipay', true)).toBe(true)
  })

  it('prefers backend order status over success query flags', () => {
    expect(isPaymentResultSuccessful('FAILED', 'success', 'TRADE_SUCCESS')).toBe(false)
    expect(isPaymentResultSuccessful('COMPLETED', null, null)).toBe(true)
  })

  it('falls back to query flags only when backend order is unavailable', () => {
    expect(isPaymentResultSuccessful(null, 'success', null)).toBe(true)
    expect(isPaymentResultSuccessful(undefined, null, 'TRADE_SUCCESS')).toBe(true)
    expect(isPaymentResultSuccessful(undefined, null, null)).toBe(false)
  })
})
