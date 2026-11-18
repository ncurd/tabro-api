import { describe, expect, it } from 'vitest'
import { buildModelPrice, createModelPriceForm, unitsForMode } from '../adminModelPricing'
import type { AdminModelPolicy } from '@/api/admin/models'

const model: AdminModelPolicy = { model: 'test', platforms: ['openai'], has_provider: true, enabled: true, source: 'automatic',
  pricing: null, default_pricing: { billing_mode: 'token', price_unit: 'million_tokens', input_price_per_million: 2.5, output_price_per_million: 10 }, channel_override: false }

describe('administrator model price form', () => {
  it('sends per-million token prices unchanged and preserves an explicit free price', () => {
    const form = createModelPriceForm(model)
    form.input_price_per_million = '0'
    form.cache_read_price_per_million = '0.0000000001'
    expect(buildModelPrice(form)).toEqual({ price: {
      billing_mode: 'token', price_unit: 'million_tokens', input_price_per_million: 0,
      output_price_per_million: 10, cache_read_price_per_million: 0.0000000001
    } })
  })

  it('supports numeric v-model values without losing zero', () => {
    const form = createModelPriceForm(model)
    form.input_price_per_million = 0
    expect(buildModelPrice(form).price?.input_price_per_million).toBe(0)
  })

  it.each(['', ' ', '-1', 'NaN', 'Infinity', '-Infinity', 'not-a-price'])('rejects missing or invalid required price %j', value => {
    const form = createModelPriceForm(model)
    form.output_price_per_million = value
    expect(buildModelPrice(form).error).toBeTruthy()
  })

  it('keeps missing optional cache prices absent and rejects invalid supplied ones', () => {
    const form = createModelPriceForm(model)
    expect(buildModelPrice(form).price).not.toHaveProperty('cache_write_price_per_million')
    form.cache_write_price_per_million = '-0.1'
    expect(buildModelPrice(form).error).toBe('invalidPrice')
  })

  it('uses ASR seconds and TTS characters, and never treats an empty unit price as free', () => {
    const asr = createModelPriceForm({ ...model, model: 'paraformer-realtime-v2', default_pricing: null })
    expect(asr.price_unit).toBe('second')
    expect(buildModelPrice(asr).error).toBe('requiredPrice')
    asr.unit_price = '0.0002'
    expect(buildModelPrice(asr)).toEqual({ price: { billing_mode: 'audio', price_unit: 'second', unit_price: 0.0002 } })
    const tts = createModelPriceForm({ ...model, model: 'qwen3-tts-flash-realtime', default_pricing: null })
    expect(tts.price_unit).toBe('character')
    tts.unit_price = 0
    expect(buildModelPrice(tts).price?.unit_price).toBe(0)
  })

  it('limits units to the billing mode', () => {
    expect(unitsForMode('duration')).toEqual(['second'])
    expect(unitsForMode('video')).toEqual(['second'])
    expect(unitsForMode('image')).toEqual(['image'])
    expect(unitsForMode('per_request')).toEqual(['request'])
    const form = createModelPriceForm(model)
    form.price_unit = 'second'
    expect(buildModelPrice(form).error).toBe('invalidUnit')
  })

  it('preserves separate per-second directions, explicit zero, and absent prices without token or unit fields', () => {
    const form = createModelPriceForm({ ...model, pricing: { billing_mode: 'duration', price_unit: 'second',
      input_audio_price_per_second: 0, output_video_price_per_second: 0.0000000001 } })
    expect(form.input_audio_price_per_second).toBe('0')
    expect(form.output_audio_price_per_second).toBe('')
    // Switching from another mode must not submit hidden prices from that mode.
    form.input_price_per_million = '10'
    form.unit_price = '5'
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'duration', price_unit: 'second',
      input_audio_price_per_second: 0, output_video_price_per_second: 0.0000000001 } })
    form.billing_mode = 'audio'
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'audio', price_unit: 'second', unit_price: 5 } })
  })

  it('requires at least one explicit duration price and accepts numeric zero as the only configured direction', () => {
    const form = createModelPriceForm(model)
    form.billing_mode = 'duration'; form.price_unit = 'second'
    expect(buildModelPrice(form).error).toBe('requiredDurationPrice')
    form.output_audio_price_per_second = 0
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'duration', price_unit: 'second', output_audio_price_per_second: 0 } })
    form.price_unit = 'character'
    expect(buildModelPrice(form).error).toBe('invalidUnit')
  })

  it.each(['input_audio_price_per_second', 'output_audio_price_per_second', 'output_video_price_per_second'] as const)(
    'rejects invalid supplied duration direction %s even when another direction is valid', field => {
      const form = createModelPriceForm(model)
      form.billing_mode = 'duration'; form.price_unit = 'second'
      form.input_audio_price_per_second = '0.001'
      for (const invalid of ['-0.01', 'NaN', 'Infinity', '-Infinity', 'invalid', Number.NaN, Number.POSITIVE_INFINITY]) {
        form[field] = invalid
        expect(buildModelPrice(form).error).toBe('invalidPrice')
      }
    })

  it('prefills video resolution prices case-insensitively, preserving zero and other configured resolutions', () => {
    const form = createModelPriceForm({ ...model, pricing: { billing_mode: 'video', price_unit: 'second', resolution_prices: [
      { resolution: '480p', unit_price: 0 }, { resolution: '1080P', unit_price: 0.3 }, { resolution: '4k', unit_price: 0.8 }
    ] } })
    expect(form.unit_price).toBe('')
    expect(form.resolution_prices).toEqual([
      { resolution: '480P', unit_price: '0' }, { resolution: '720P', unit_price: '' },
      { resolution: '1080P', unit_price: '0.3' }, { resolution: '4K', unit_price: '0.8' }
    ])
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'video', price_unit: 'second', resolution_prices: [
      { resolution: '480P', unit_price: 0 }, { resolution: '1080P', unit_price: 0.3 }, { resolution: '4K', unit_price: 0.8 }
    ] } })
  })

  it('requires an explicit video default or resolution price and never invents a default', () => {
    const form = createModelPriceForm({ ...model, default_pricing: null })
    form.billing_mode = 'video'; form.price_unit = 'second'
    expect(form.unit_price).toBe('')
    expect(form.resolution_prices.every(tier => tier.unit_price === '')).toBe(true)
    expect(buildModelPrice(form).error).toBe('requiredVideoPrice')
    form.resolution_prices[0].unit_price = 0
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'video', price_unit: 'second',
      resolution_prices: [{ resolution: '480P', unit_price: 0 }] } })
  })

  it('opens known unpriced video models in video mode without overriding an existing billing mode', () => {
    for (const name of ['wan3.0-video', 'wan3.0-video-prime', 'happyhorse-1.0-r2v', 'doubao-seedance-2-0-260128', 'doubao-seedance-2-5-260628']) {
      const form = createModelPriceForm({ ...model, model: name, default_pricing: null })
      expect(form.billing_mode).toBe('video')
      expect(form.price_unit).toBe('second')
      expect(form.unit_price).toBe('')
      expect(buildModelPrice(form).error).toBe('requiredVideoPrice')
    }
    expect(createModelPriceForm({ ...model, model: 'wan3.0-video', pricing: {
      billing_mode: 'per_request', price_unit: 'request', unit_price: 0.5
    } }).billing_mode).toBe('per_request')
  })

  it('keeps legacy video default prices and optionally combines them with resolution overrides', () => {
    const form = createModelPriceForm({ ...model, pricing: { billing_mode: 'video', price_unit: 'second', unit_price: 0 } })
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'video', price_unit: 'second', unit_price: 0 } })
    form.unit_price = '0.1'
    form.resolution_prices[1].unit_price = '0.25'
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'video', price_unit: 'second', unit_price: 0.1,
      resolution_prices: [{ resolution: '720P', unit_price: 0.25 }] } })
  })

  it.each(['-0.01', 'NaN', 'Infinity', '-Infinity', 'invalid', Number.NaN, Number.POSITIVE_INFINITY])(
    'rejects an invalid video resolution price %j even with a valid default', value => {
      const form = createModelPriceForm({ ...model, pricing: { billing_mode: 'video', price_unit: 'second', unit_price: 0.1 } })
      form.resolution_prices[0].unit_price = value
      expect(buildModelPrice(form).error).toBe('invalidPrice')
      form.resolution_prices[0].unit_price = '0.2'
      form.unit_price = value
      expect(buildModelPrice(form).error).toBe('invalidPrice')
    })

  it('omits hidden video resolution prices when switching to another billing mode', () => {
    const form = createModelPriceForm({ ...model, pricing: { billing_mode: 'video', price_unit: 'second', resolution_prices: [
      { resolution: '720P', unit_price: 0.2 }
    ] } })
    form.billing_mode = 'per_request'; form.price_unit = 'request'; form.unit_price = 0.5
    expect(buildModelPrice(form)).toEqual({ price: { billing_mode: 'per_request', price_unit: 'request', unit_price: 0.5 } })
  })
})
