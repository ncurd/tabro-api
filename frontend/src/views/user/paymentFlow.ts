import type { OrderStatus } from '@/types/payment'

const SUCCESS_STATUSES = new Set<OrderStatus>(['COMPLETED', 'PAID', 'RECHARGING'])

export type PaymentPageOpenStrategy = 'same-tab' | 'new-tab' | 'popup'

export function resolvePaymentPageOpenStrategy(paymentType: string, isMobile: boolean): PaymentPageOpenStrategy {
  if (paymentType === 'stripe') return 'new-tab'
  if (isMobile) return 'same-tab'
  return 'popup'
}

export function isPaymentResultSuccessful(
  orderStatus?: OrderStatus | null,
  queryStatus?: string | null,
  tradeStatus?: string | null,
): boolean {
  if (orderStatus) {
    return SUCCESS_STATUSES.has(orderStatus)
  }
  return queryStatus === 'success' || tradeStatus === 'TRADE_SUCCESS'
}
