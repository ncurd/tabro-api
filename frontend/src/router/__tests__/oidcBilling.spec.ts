import { describe, expect, it } from 'vitest'
import { isLocalBillingPath } from '../oidcBilling'

describe('OIDC billing route restrictions', () => {
  it('blocks local credits, subscriptions, coupons and checkout, including subpaths', () => {
    for (const path of ['/subscriptions', '/redeem', '/admin/subscriptions', '/admin/redeem', '/admin/promo-codes', '/payment/qrcode']) {
      expect(isLocalBillingPath(path)).toBe(true)
      expect(isLocalBillingPath(`${path}/`)).toBe(true)
      expect(isLocalBillingPath(`${path}/123`)).toBe(true)
    }
  })

  it('keeps usage, Auth purchase links and historical order pages accessible', () => {
    for (const path of ['/usage', '/admin/usage', '/keys', '/purchase', '/payment', '/orders', '/admin/orders', '/payment/result', '/admin/groups', '/redeem-custom']) {
      expect(isLocalBillingPath(path)).toBe(false)
    }
  })
})
