import { formatCreditNumber, formatCredits } from './credits'
import { BILLING_MODE_CENTRAL, BILLING_MODE_IMAGE, BILLING_MODE_TOKEN } from './billingMode'
import { formatMultiplier } from './formatters'

export const TOKENS_PER_MILLION = 1_000_000

interface UsagePricingRecord {
  billing_source?: string
  billing_mode?: string | null
  rate_multiplier?: number | null
  total_cost?: number | null
  actual_cost?: number | null
  image_count?: number
  input_tokens?: number
  output_tokens?: number
  cache_read_tokens?: number
  cache_creation_tokens?: number
}

export function isCentralUsage(usage: UsagePricingRecord | null): boolean {
  return usage?.billing_source === 'central' || usage?.billing_mode === BILLING_MODE_CENTRAL
}

export function formatUsageRate(usage: UsagePricingRecord | null): string {
  const multiplier = usageDisplayMultiplier(usage)
  if (multiplier == null) return '—'
  const formatted = multiplier >= 0.01
    ? new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 10, useGrouping: false }).format(multiplier)
    : formatMultiplier(multiplier)
  return `${formatted}x`
}

/** Older stored rates have four decimal places; recorded charge/base preserves the billed conversion. */
export function usageDisplayMultiplier(usage: UsagePricingRecord | null): number | null {
  if (isCentralUsage(usage) && isFiniteNumber(usage?.total_cost) && usage.total_cost > 0 &&
      isFiniteNumber(usage.actual_cost) && usage.actual_cost >= 0) {
    const ratio = usage.actual_cost / usage.total_cost
    if (Number.isFinite(ratio)) return Number(ratio.toPrecision(15))
  }
  const multiplier = usage?.rate_multiplier ?? (isCentralUsage(usage) ? null : 1)
  return isFiniteNumber(multiplier) && multiplier >= 0 ? multiplier : null
}

/** Central billing stores base line costs and the final multiplier frozen for this request. */
export function usageDisplayCost(cost: number | null | undefined, usage: UsagePricingRecord | null): number | null {
  if (!isFiniteNumber(cost)) return null
  if (!isCentralUsage(usage)) return cost
  const multiplier = usageDisplayMultiplier(usage)
  if (multiplier == null) return null
  const result = cost * multiplier
  return Number.isFinite(result) ? result : null
}

export function usageDisplayUnitCost(cost: number | null | undefined, usage: UsagePricingRecord | null): number | null {
  const displayedCost = usageDisplayCost(cost, usage)
  if (displayedCost == null) return null
  return isCentralUsage(usage) && isImagePricedUsage(usage)
    ? displayedCost / usage!.image_count!
    : displayedCost
}

export function isImagePricedUsage(usage: UsagePricingRecord | null): boolean {
  return !!usage && (usage.image_count ?? 0) > 0 &&
    usage.billing_mode === BILLING_MODE_IMAGE
}

export function isTokenPricedUsage(usage: UsagePricingRecord | null): boolean {
  if (!usage?.billing_mode || usage.billing_mode === BILLING_MODE_TOKEN) return true
  return false
}

interface TokenPriceFormatOptions {
  fractionDigits?: number
  withCurrencySymbol?: boolean
  emptyValue?: string
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

export function calculateTokenUnitPrice(
  cost: number | null | undefined,
  tokens: number | null | undefined
): number | null {
  if (!isFiniteNumber(cost) || !isFiniteNumber(tokens) || tokens <= 0) {
    return null
  }

  return cost / tokens
}

export function calculateTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined
): number | null {
  const unitPrice = calculateTokenUnitPrice(cost, tokens)
  if (unitPrice == null) {
    return null
  }

  return unitPrice * TOKENS_PER_MILLION
}

export function formatTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined,
  options: TokenPriceFormatOptions = {}
): string {
  const pricePerMillion = calculateTokenPricePerMillion(cost, tokens)
  if (pricePerMillion == null) {
    return options.emptyValue ?? '-'
  }

  const fractionDigits = options.fractionDigits ?? 4
  return options.withCurrencySymbol == false
    ? formatCreditNumber(pricePerMillion, { fractionDigits })
    : formatCredits(pricePerMillion, { fractionDigits })
}
