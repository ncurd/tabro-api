import type { PublicSettings } from '@/types'

const authBillingPath = '/Identity/Account/Manage/BillingPayments'
const authProductsPath = '/Identity/Account/Manage/AdminPaymentProducts'
const authSettingsPath = '/Identity/Account/Manage/AdminPaymentSettings'

export function isRetiredPurchasePath(path: string): boolean {
  const normalized = path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path
  return normalized === '/payment' || normalized === '/purchase'
}

/** Navigate only new-purchase entry points; historical orders and result pages stay local. */
export function paymentPurchaseDestination(path: string, settings: PublicSettings | null): string | null {
  if (!isRetiredPurchasePath(path)) return null
  return accountCenterPage(settings, authBillingPath)
}

export function adminPaymentConfigurationDestination(path: string, settings: PublicSettings | null,
  tab?: unknown): string | null {
  const normalized = path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path
  if (normalized === '/admin/orders/plans') return accountCenterPage(settings, authProductsPath)
  if (normalized === '/admin/settings' && tab === 'payment') return accountCenterPage(settings, authSettingsPath)
  return null
}

function accountCenterPage(settings: PublicSettings | null, pagePath: string): string | null {
  if (!settings?.account_center_url) return null
  try {
    const base = new URL(settings.account_center_url)
    const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname)
    if ((base.protocol !== 'https:' && !(base.protocol === 'http:' && loopback)) ||
      base.username || base.password || base.search || base.hash) return null
    return new URL(pagePath, base).href
  } catch {
    return null
  }
}
