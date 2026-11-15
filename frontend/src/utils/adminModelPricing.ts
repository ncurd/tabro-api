import type { AdminModelPolicy, AdminModelPrice, ModelBillingMode, ModelPriceUnit } from '@/api/admin/models'

export interface ModelPriceForm {
  billing_mode: ModelBillingMode
  price_unit: ModelPriceUnit
  input_price_per_million: string | number
  output_price_per_million: string | number
  cache_write_price_per_million: string | number
  cache_read_price_per_million: string | number
  input_audio_price_per_second: string | number
  output_audio_price_per_second: string | number
  output_video_price_per_second: string | number
  unit_price: string | number
}

export const durationPriceFields = ['input_audio_price_per_second', 'output_audio_price_per_second', 'output_video_price_per_second'] as const

export function unitsForMode(mode: ModelBillingMode): ModelPriceUnit[] {
  switch (mode) {
    case 'token': return ['million_tokens']
    case 'duration': return ['second']
    case 'audio': return ['second', 'character']
    case 'video': return ['second']
    case 'image': return ['image']
    default: return ['request']
  }
}

export function createModelPriceForm(model: AdminModelPolicy): ModelPriceForm {
  const price = model.pricing ?? model.default_pricing
  const mode = price?.billing_mode ?? (/tts|cosyvoice/i.test(model.model) ? 'audio'
    : /asr|paraformer/i.test(model.model) ? 'audio' : 'token')
  const unit = price?.price_unit ?? (mode === 'audio' && /tts|cosyvoice/i.test(model.model) ? 'character' : unitsForMode(mode)[0])
  return {
    billing_mode: mode, price_unit: unit,
    input_price_per_million: numberText(price?.input_price_per_million),
    output_price_per_million: numberText(price?.output_price_per_million),
    cache_write_price_per_million: numberText(price?.cache_write_price_per_million),
    cache_read_price_per_million: numberText(price?.cache_read_price_per_million),
    input_audio_price_per_second: numberText(price?.input_audio_price_per_second),
    output_audio_price_per_second: numberText(price?.output_audio_price_per_second),
    output_video_price_per_second: numberText(price?.output_video_price_per_second),
    unit_price: numberText(price?.unit_price)
  }
}

function numberText(value: number | undefined): string { return value == null ? '' : String(value) }

export type PriceValidationError = 'requiredPrice' | 'requiredDurationPrice' | 'invalidPrice' | 'invalidUnit'

export function buildModelPrice(form: ModelPriceForm): { price: AdminModelPrice; error?: never } | { error: PriceValidationError; price?: never } {
  if (!unitsForMode(form.billing_mode).includes(form.price_unit)) return { error: 'invalidUnit' }
  const price: AdminModelPrice = { billing_mode: form.billing_mode, price_unit: form.price_unit }
  const fields = form.billing_mode === 'token'
    ? ['input_price_per_million', 'output_price_per_million', 'cache_write_price_per_million', 'cache_read_price_per_million'] as const
    : form.billing_mode === 'duration' ? durationPriceFields
    : ['unit_price'] as const
  for (const field of fields) {
    const text = String(form[field] ?? '').trim()
    const optional = form.billing_mode === 'duration' || field === 'cache_write_price_per_million' || field === 'cache_read_price_per_million'
    if (!text) {
      if (optional) continue
      return { error: 'requiredPrice' }
    }
    const amount = Number(text)
    if (!Number.isFinite(amount) || amount < 0) return { error: 'invalidPrice' }
    // The API uses prices per million tokens too; never apply a second unit conversion.
    price[field] = amount
  }
  if (form.billing_mode === 'duration' && !durationPriceFields.some(field => price[field] != null)) return { error: 'requiredDurationPrice' }
  return { price }
}

export function effectiveModelPrice(model: AdminModelPolicy): AdminModelPrice | null {
  return model.pricing ?? model.default_pricing
}
