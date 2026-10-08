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

/** Format an Auth decimal without losing precision through JavaScript numbers. */
export function formatExactCredits(value: string | null | undefined, options: CreditFormatOptions = {}): string {
  if (typeof value !== 'string' || !/^-?\d+(?:\.\d+)?$/.test(value)) {
    return '—'
  }

  const negative = value.startsWith('-')
  const [integer, fraction = ''] = (negative ? value.slice(1) : value).split('.')
  const whole = integer.replace(/^0+(?=\d)/, '')
  const decimal = fraction.replace(/0+$/, '')
  if (options.fractionDigits !== undefined) {
    const digits = Math.min(20, Math.max(0, Math.trunc(options.fractionDigits)))
    const padded = fraction.padEnd(digits + 1, '0')
    let units = BigInt(whole + padded.slice(0, digits))
    const remainder = padded.slice(digits)
    // Match Auth's decimal F2 display: midpoint values round away from zero.
    if (remainder[0] >= '5') units++
    const rounded = units.toString().padStart(digits + 1, '0')
    const sign = negative && units !== 0n ? '-' : ''
    const amount = digits ? `${rounded.slice(0, -digits)}.${rounded.slice(-digits)}` : rounded
    return `${sign}${amount} ${CREDIT_UNIT}`
  }
  const sign = negative && (whole !== '0' || decimal !== '') ? '-' : ''
  return `${sign}${whole}${decimal ? `.${decimal}` : ''} ${CREDIT_UNIT}`
}
