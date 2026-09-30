import { describe, expect, it } from 'vitest'
import { adminPaymentConfigurationDestination, isRetiredPurchasePath, paymentPurchaseDestination } from '../paymentMigration'
import type { PublicSettings } from '@/types'

const settings = { internal_only: false, payment_enabled: false,
  account_center_url: 'https://auth.example.com/account' } as PublicSettings

describe('retired gateway purchase navigation', () => {
  it('routes only purchase entry points to the Auth billing page', () => {
    for (const path of ['/payment', '/purchase', '/payment/', '/purchase/']) {
      expect(isRetiredPurchasePath(path)).toBe(true)
      expect(paymentPurchaseDestination(path, settings)).toBe(
        'https://auth.example.com/Identity/Account/Manage/BillingPayments')
    }
    for (const path of ['/orders', '/payment/result', '/payment/qrcode', '/purchase/old']) {
      expect(isRetiredPurchasePath(path)).toBe(false)
      expect(paymentPurchaseDestination(path, settings)).toBeNull()
    }
  })

  it('allows local HTTP for debugging and drops the configured base path', () => {
    expect(paymentPurchaseDestination('/purchase', { ...settings,
      account_center_url: 'http://localhost:5000/account/' })).toBe(
      'http://localhost:5000/Identity/Account/Manage/BillingPayments')
  })

  it('keeps the read-only local page when the destination is absent or unsafe', () => {
    expect(paymentPurchaseDestination('/payment', null)).toBeNull()
    for (const url of ['', '//evil.example', 'javascript:alert(1)', 'http://remote.example',
      'https://user:secret@auth.example.com/', 'https://auth.example.com/?next=evil',
      'https://auth.example.com/#fragment']) {
      expect(paymentPurchaseDestination('/purchase', { ...settings, account_center_url: url })).toBeNull()
    }
  })

  it('sends only new admin configuration entry points to Auth', () => {
    expect(adminPaymentConfigurationDestination('/admin/orders/plans', settings)).toBe(
      'https://auth.example.com/Identity/Account/Manage/AdminPaymentProducts')
    expect(adminPaymentConfigurationDestination('/admin/orders/plans/', settings)).toBe(
      'https://auth.example.com/Identity/Account/Manage/AdminPaymentProducts')
    expect(adminPaymentConfigurationDestination('/admin/settings', settings, 'payment')).toBe(
      'https://auth.example.com/Identity/Account/Manage/AdminPaymentSettings')
    for (const path of ['/admin/orders', '/admin/orders/dashboard', '/admin/settings']) {
      expect(adminPaymentConfigurationDestination(path, settings)).toBeNull()
    }
    expect(adminPaymentConfigurationDestination('/admin/orders/plans', null)).toBeNull()
    expect(adminPaymentConfigurationDestination('/admin/settings', { ...settings,
      account_center_url: 'javascript:alert(1)' }, 'payment')).toBeNull()
  })
})
