import type { OrderStatus } from '@/types/payment'

const SUCCESS_STATUSES = new Set<OrderStatus>(['COMPLETED', 'PAID', 'RECHARGING'])

export function shouldOpenPaymentPageDirectly(paymentType: string, isMobile: boolean): boolean {
  return isMobile || paymentType === 'stripe'
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
