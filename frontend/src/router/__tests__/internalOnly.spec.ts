import { describe, expect, it } from 'vitest'
import { accountCenterDestination } from '../internalOnly'
import type { PublicSettings } from '@/types'

const settings = { internal_only: true, account_center_url: 'https://auth.example/account' } as PublicSettings

describe('internal-only deployment navigation', () => {
  it('redirects user money and registration routes without copying query secrets', () => {
    for (const path of ['/dashboard', '/register', '/payment', '/payment/result', '/purchase', '/keys', '/redeem', '/subscriptions', '/profile', '/admin/redeem']) {
      expect(accountCenterDestination(path, settings)).toBe('https://auth.example/account')
    }
  })
  it('retains operational routes and operator security profile', () => {
    for (const path of ['/login', '/admin/accounts', '/admin/channels', '/admin/settings', '/auth/oidc/callback', '/docs']) {
      expect(accountCenterDestination(path, settings)).toBeNull()
    }
    expect(accountCenterDestination('/profile', settings, true)).toBeNull()
    expect(accountCenterDestination('/dashboard', { ...settings, internal_only: false })).toBeNull()
    expect(accountCenterDestination('/dashboard', null)).toBeNull()
  })
  it('does not navigate to malformed or script URLs', () => {
    for (const url of ['javascript:alert(1)', '//evil.example', 'https://user:secret@auth.example/', 'http://remote.example']) {
      expect(accountCenterDestination('/payment', { ...settings, account_center_url: url })).toBeNull()
    }
  })
})
