/** Local financial operations replaced by Auth when OIDC billing is enabled. */
export function isLocalBillingPath(path: string): boolean {
  return ['/subscriptions', '/redeem', '/admin/subscriptions', '/admin/redeem', '/admin/promo-codes', '/payment/qrcode']
    .some((prefix) => path === prefix || path.startsWith(`${prefix}/`))
}
