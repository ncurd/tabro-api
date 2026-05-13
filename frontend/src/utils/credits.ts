export const CREDIT_UNIT = '✦'

interface CreditFormatOptions {
  fractionDigits?: number
  emptyValue?: string
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

export function formatCreditNumber(
  value: number | null | undefined,
  options: CreditFormatOptions = {}
): string {
  if (!isFiniteNumber(value)) {
    return options.emptyValue ?? '-'
  }

  return value.toFixed(options.fractionDigits ?? 2)
}

export function formatCredits(
  value: number | null | undefined,
  options: CreditFormatOptions = {}
): string {
  const formatted = formatCreditNumber(value, options)
  if (formatted === (options.emptyValue ?? '-')) {
    return formatted
  }
  return `${formatted} ${CREDIT_UNIT}`
}
