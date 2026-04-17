import { describe, expect, it } from 'vitest'

import { isPaymentResultSuccessful, resolvePaymentPageOpenStrategy } from '../paymentFlow'

describe('paymentFlow helpers', () => {
  it('opens stripe payments in a new tab', () => {
    expect(resolvePaymentPageOpenStrategy('stripe', false)).toBe('new-tab')
  })

  it('keeps non-stripe desktop payments in popup flow', () => {
    expect(resolvePaymentPageOpenStrategy('alipay', false)).toBe('popup')
  })

  it('redirects any mobile payment directly', () => {
    expect(resolvePaymentPageOpenStrategy('alipay', true)).toBe('same-tab')
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
