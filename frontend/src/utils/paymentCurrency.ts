export type PaymentCurrency = 'CNY' | 'USD' | 'GBP' | 'EUR'

export interface PaymentCurrencyOption {
  code: PaymentCurrency
  symbol: string
  creditRate: number
}

export const PAYMENT_CURRENCIES: PaymentCurrencyOption[] = [
  { code: 'CNY', symbol: '¥', creditRate: 1 },
  { code: 'USD', symbol: '$', creditRate: 6.5 },
  { code: 'GBP', symbol: '£', creditRate: 9 },
  { code: 'EUR', symbol: '€', creditRate: 7.6 },
]

const currencyMap = new Map(PAYMENT_CURRENCIES.map((item) => [item.code, item]))

export function normalizePaymentCurrency(value: string | null | undefined): PaymentCurrency {
  const normalized = String(value ?? '').trim().toUpperCase()
  return currencyMap.has(normalized as PaymentCurrency) ? normalized as PaymentCurrency : 'CNY'
}

export function getCurrencyCreditRate(currency: PaymentCurrency): number {
  return currencyMap.get(currency)?.creditRate ?? 1
}

export function getCurrencySymbol(currency: PaymentCurrency): string {
  return currencyMap.get(currency)?.symbol ?? '¥'
}

export function toCredits(amount: number | null | undefined, currency: PaymentCurrency): number {
  const value = Number(amount ?? 0)
  if (!Number.isFinite(value) || value <= 0) return 0
  return Math.round(value * getCurrencyCreditRate(currency) * 100) / 100
}

export function formatPaymentAmount(amount: number | null | undefined, currency: PaymentCurrency): string {
  const value = Number(amount ?? 0)
  return `${getCurrencySymbol(currency)}${(Number.isFinite(value) ? value : 0).toFixed(2)}`
}

export function isStripePaymentType(paymentType: string | null | undefined): boolean {
  const normalized = String(paymentType ?? '').trim().toLowerCase()
  return normalized === 'stripe' || normalized.startsWith('stripe_')
}

export function getBasePaymentType(paymentType: string | null | undefined): string {
  const normalized = String(paymentType ?? '').trim().toLowerCase()
  if (isStripePaymentType(normalized)) return 'stripe'
  return normalized
}

export function getPaymentCurrencyFromType(paymentType: string | null | undefined): PaymentCurrency {
  const normalized = String(paymentType ?? '').trim().toLowerCase()
  if (!normalized.startsWith('stripe_')) return 'CNY'
  return normalizePaymentCurrency(normalized.slice('stripe_'.length))
}
