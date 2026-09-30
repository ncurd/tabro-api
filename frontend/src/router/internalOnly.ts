import type { PublicSettings } from '@/types'

/** Deployment policy is also enforced by the API, including existing sessions. */
export function accountCenterDestination(path: string, settings: PublicSettings | null, isAdmin = false): string | null {
  if (!settings?.internal_only || !settings.account_center_url) return null
  if (path === '/profile' && isAdmin) return null
  const prefixes = [
    '/home', '/dashboard', '/keys', '/usage', '/model-pricing', '/subscriptions',
    '/redeem', '/payment', '/purchase', '/orders', '/profile', '/register', '/email-verify',
    '/forgot-password', '/reset-password', '/custom', '/key-usage',
    '/auth/linuxdo/callback', '/auth/callback', '/admin/redeem', '/admin/promo-codes',
    '/admin/subscriptions', '/admin/orders',
  ]
  const isUserPage = path === '/' || prefixes.some(prefix => path === prefix || path.startsWith(`${prefix}/`))
  if (!isUserPage) return null
  try {
    const target = new URL(settings.account_center_url)
    const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(target.hostname)
    if ((target.protocol !== 'https:' && !(target.protocol === 'http:' && loopback)) || target.username || target.password) return null
    return target.href
  } catch {
    return null
  }
}
