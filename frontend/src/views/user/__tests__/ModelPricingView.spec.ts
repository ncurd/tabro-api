import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ModelPricingView from '../ModelPricingView.vue'

const { getAvailable } = vi.hoisted(() => ({ getAvailable: vi.fn() }))
vi.mock('@/api/modelPricing', () => ({ modelPricingAPI: { getAvailable } }))

function mountPricing() {
  return mount(ModelPricingView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } }
  })
}

describe('ModelPricingView media prices', () => {
  beforeEach(() => getAvailable.mockReset())

  it('shows per-second tiers, explicit free pricing, and missing prices without token columns', async () => {
    getAvailable.mockResolvedValue({ groups: [{
      id: 1, name: '视频', platform: 'dashscope', rate_multiplier: 1, effective_rate_multiplier: 2,
      models: [
        { id: 'wan3.0-video', pricing_available: true, billing_mode: 'video', price_unit: 'second', unit_price: 0.24, tiers: [{ label: '1080P', unit_price: 0.5 }] },
        { id: 'free-video', pricing_available: true, billing_mode: 'video', price_unit: 'second', unit_price: 0 },
        { id: 'missing-video', pricing_available: false, billing_mode: 'video', price_unit: 'second' },
        { id: 'tier-only', pricing_available: true, billing_mode: 'video', price_unit: 'second', tiers: [{ label: '720P', unit_price: 0.1 }] }
      ]
    }] })
    const wrapper = mountPricing()
    await flushPromises()
    expect(wrapper.text()).toContain('默认：0.24 ✦ / 秒')
    expect(wrapper.text()).toContain('1080P：0.5 ✦ / 秒')
    expect(wrapper.text()).toContain('默认：0 ✦ / 秒')
    expect(wrapper.text()).toContain('未配置价格 · 秒')
    expect(wrapper.text()).toContain('仅已配置档位可用')
    expect(wrapper.text()).not.toContain('1M tokens')
    expect(wrapper.findAll('th').map((header) => header.text())).toEqual(['模型', '计费单位与单价'])
  })

  it('keeps token prices and uses character/request units for audio models', async () => {
    getAvailable.mockResolvedValue({ groups: [{
      id: 2, name: '混合', platform: 'openai', rate_multiplier: 1, effective_rate_multiplier: 1,
      models: [
        { id: 'gpt-llm', pricing_available: true, billing_mode: 'token', price_unit: 'million_tokens', input_price_per_million: 2 },
        { id: 'qwen3-tts-flash', pricing_available: true, billing_mode: 'audio', price_unit: 'character', unit_price: 0.0000032 },
        { id: 'qwen-voice-enrollment', pricing_available: true, billing_mode: 'per_request', price_unit: 'request', unit_price: 1.2 }
      ]
    }] })
    const wrapper = mountPricing()
    await flushPromises()
    expect(wrapper.text()).toContain('2.0000 ✦')
    expect(wrapper.text()).toContain('0.0000032 ✦ / 字符')
    expect(wrapper.text()).toContain('1.2 ✦ / 次')
    expect(wrapper.findAll('th')).toHaveLength(9)
    const audioRow = wrapper.findAll('tbody tr').find((row) => row.text().includes('qwen3-tts-flash'))
    expect(audioRow?.findAll('td')).toHaveLength(2)
    expect(audioRow?.findAll('td')[1].attributes('colspan')).toBe('8')
  })
})
