import { describe, expect, it, vi } from 'vitest'

import {
  isPaymentResultSuccessful,
  navigatePendingPaymentPage,
  openPendingPaymentPage,
  resolvePaymentPageOpenStrategy,
  shouldTrackPaymentInline,
} from '../paymentFlow'

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

  it('keeps the current page unchanged for stripe new-tab flow', () => {
    expect(shouldTrackPaymentInline('new-tab')).toBe(false)
    expect(shouldTrackPaymentInline('popup')).toBe(true)
    expect(shouldTrackPaymentInline('same-tab')).toBe(true)
  })

  it('opens a loading placeholder in the pending stripe tab', () => {
    const write = vi.fn()
    const close = vi.fn()
    const pendingTab = {
      closed: false,
      opener: {} as Window,
      location: { href: '' },
      focus: vi.fn(),
      document: {
        write,
        close,
      },
    } as any
    const openWindow = vi.fn().mockReturnValue(pendingTab)

    const result = openPendingPaymentPage(openWindow, 'Redirecting to payment', 'Preparing Stripe checkout...')

    expect(result).toBe(pendingTab)
    expect(openWindow).toHaveBeenCalledWith('', '_blank')
    expect(write).toHaveBeenCalled()
    expect(close).toHaveBeenCalled()
    expect(pendingTab.opener).toBeNull()
  })

  it('reuses the already opened stripe tab instead of opening another one', () => {
    const pendingTab = {
      closed: false,
      location: { href: '' },
      focus: vi.fn(),
    } as any
    const openWindow = vi.fn()
    const url = 'https://checkout.stripe.com/c/pay/test_session'

    const result = navigatePendingPaymentPage(pendingTab, url, openWindow)

    expect(result).toBe(true)
    expect(pendingTab.location.href).toBe(url)
    expect(pendingTab.focus).toHaveBeenCalled()
    expect(openWindow).not.toHaveBeenCalled()
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
