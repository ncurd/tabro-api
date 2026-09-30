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

  it('displays and refreshes backend token prices for GPT-6 Sol/Luna and Opus 5.5 without applying the group multiplier again', async () => {
    // Values are already adjusted by the backend and deliberately differ from list prices.
    const models = ['gpt-6-sol', 'gpt-6-luna', 'claude-opus-5-5'].map(id => ({
      id, pricing_available: true, billing_mode: 'token', price_unit: 'million_tokens',
      input_price_per_million: 12.34, output_price_per_million: 56.78,
      cache_write_price_per_million: 15.42, cache_read_price_per_million: 1.23,
      priority_input_price_per_million: 24.68, priority_output_price_per_million: 113.56
    }))
    const group = {
      id: 3, name: '新模型', platform: 'openai', rate_multiplier: 2, effective_rate_multiplier: 3,
      models
    }
    getAvailable.mockResolvedValueOnce({ groups: [group] }).mockResolvedValueOnce({
      groups: [{ ...group, models: models.map(model => ({ ...model, input_price_per_million: 8.76 })) }]
    })

    const wrapper = mountPricing()
    await flushPromises()
    expect(wrapper.text()).toContain('有效倍率 3.00x')
    expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    for (const model of models) {
      const row = wrapper.findAll('tbody tr').find(row => row.text().includes(model.id))!
      expect(row.findAll('td').slice(2).map(cell => cell.text())).toEqual([
        '12.3400 ✦', '56.7800 ✦', '15.4200 ✦', '1.2300 ✦', '24.6800 ✦', '113.5600 ✦', '暂无'
      ])
    }

    await wrapper.findAll('button').find(button => button.text() === '刷新')!.trigger('click')
    await flushPromises()
    expect(getAvailable).toHaveBeenCalledTimes(2)
    for (const row of wrapper.findAll('tbody tr')) {
      expect(row.findAll('td')[2].text()).toBe('8.7600 ✦')
    }
  })

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
