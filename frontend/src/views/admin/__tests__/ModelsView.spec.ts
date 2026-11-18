import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import ModelsView from '../ModelsView.vue'
import Pagination from '@/components/common/Pagination.vue'
import messages from '@/i18n/locales/zh-CN'
import type { AdminModelPolicy, UpdateAdminModelRequest } from '@/api/admin/models'

const { list, update, resetPrice, useAuthStore } = vi.hoisted(() => ({ list: vi.fn(), update: vi.fn(), resetPrice: vi.fn(), useAuthStore: vi.fn() }))
vi.mock('@/api/admin/models', () => ({ adminModelsAPI: { list, update, resetPrice } }))
vi.mock('@/stores/auth', () => ({ useAuthStore }))
// Vitest uses the CSP runtime-only build without Vite's JIT compilation flag.
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string, params: Record<string, string> = {}) => {
  const value = key.split('.').reduce<unknown>((current, part) => current && typeof current === 'object' ? (current as Record<string, unknown>)[part] : undefined, messages)
  return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => params[name] ?? '') : key
} }) }))

let serverModels: AdminModelPolicy[]
let wrappers: VueWrapper[] = []
let auth: { isAdmin: boolean }
function wireCopy<T>(value: T): T { return JSON.parse(JSON.stringify(value)) }

function render() {
  const wrapper = mount(ModelsView, { attachTo: document.body,
    global: {
      stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true, RouterLink: { template: '<a><slot /></a>' }, Pagination: true,
        BaseDialog: { props: ['show', 'title'], template: '<section v-if="show" role="dialog"><h2>{{ title }}</h2><slot /><footer><slot name="footer" /></footer></section>' } }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}

function row(wrapper: VueWrapper, model = 'gpt-test') { return wrapper.get(`tr[data-model="${model}"]`) }

beforeEach(() => {
  vi.clearAllMocks()
  auth = reactive({ isAdmin: true })
  useAuthStore.mockReturnValue(auth)
  serverModels = [{ model: 'gpt-test', platforms: ['openai'], has_provider: true, enabled: true, source: 'automatic', pricing: null,
    default_pricing: { billing_mode: 'token', price_unit: 'million_tokens', input_price_per_million: 2.5, output_price_per_million: 10 }, channel_override: true },
  { model: 'paraformer-realtime-v2', platforms: ['dashscope'], has_provider: true, enabled: false, source: 'manual',
    pricing: { billing_mode: 'audio', price_unit: 'second', unit_price: 0.000001 }, default_pricing: null, channel_override: false },
  { model: 'gpt-unprovided', platforms: ['openai'], has_provider: false, enabled: false, source: 'unpriced',
    pricing: null, default_pricing: null, channel_override: false }]
  list.mockImplementation(async (params) => {
    const items = serverModels.filter(model => (!params.search || model.model.includes(params.search))
      && (params.enabled == null || model.enabled === params.enabled)
      && (params.has_provider == null || model.has_provider === params.has_provider))
    const offset = (params.page - 1) * params.page_size
    return { items: wireCopy(items.slice(offset, offset + params.page_size)), total: items.length, page: params.page, page_size: params.page_size }
  })
  update.mockImplementation(async (request: UpdateAdminModelRequest) => {
    const current = serverModels.find(model => model.model === request.model)!
    Object.assign(current, request, { source: request.pricing ? 'manual' : current.default_pricing ? 'automatic' : 'unpriced' })
    return wireCopy(current)
  })
  resetPrice.mockImplementation(async (model: string) => {
    const current = serverModels.find(item => item.model === model)!
    current.pricing = null
    current.source = current.default_pricing ? 'automatic' : 'unpriced'
    return wireCopy(current)
  })
})
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers = []; vi.useRealTimers() })

describe('administrator model management', () => {
  it('does not load or expose administrative controls to non-admin users', async () => {
    auth.isAdmin = false
    const wrapper = render()
    await flushPromises()
    expect(list).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('仅管理员可以管理模型')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('shows exact model IDs, prices, source and the channel priority rule', async () => {
    const wrapper = render()
    await flushPromises()
    expect(row(wrapper).text()).toContain('输入 / 百万 Token: 2.5 USD')
    expect(row(wrapper).text()).toContain('存在渠道覆盖价')
    expect(row(wrapper, 'paraformer-realtime-v2').text()).toContain('0.000001 USD / 秒')
    expect(wrapper.text()).toContain('渠道价 → 手动基础价 → 自动价')
    expect(wrapper.text()).toContain('调价不重算历史账单')
  })

  it('defaults to models with providers even when catalog-only models have a platform', async () => {
    const wrapper = render()
    await flushPromises()
    expect((wrapper.get('select[aria-label="是否有提供方"]').element as HTMLSelectElement).value).toBe('true')
    expect(list.mock.calls[0]?.[0]).toMatchObject({ page: 1, page_size: 20, has_provider: true })
    expect(wrapper.findAll('tr[data-model]').map(model => model.attributes('data-model'))).toEqual(['gpt-test', 'paraformer-realtime-v2'])
  })

  it('can include all models or show only models without providers', async () => {
    const wrapper = render()
    await flushPromises()
    const filter = wrapper.get('select[aria-label="是否有提供方"]')
    await filter.setValue('')
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0].has_provider).toBeUndefined()
    expect(wrapper.findAll('tr[data-model]')).toHaveLength(3)
    await filter.setValue('false')
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0]).toMatchObject({ page: 1, has_provider: false })
    expect(wrapper.findAll('tr[data-model]').map(model => model.attributes('data-model'))).toEqual(['gpt-unprovided'])
  })

  it('resets pagination when provider filtering changes and combines it with status and search', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('select[aria-label="模型状态"]').setValue('false')
    await flushPromises()
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0]).toMatchObject({ page: 2, enabled: false, has_provider: true })
    await wrapper.get('select[aria-label="是否有提供方"]').setValue('false')
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0]).toMatchObject({ page: 1, page_size: 20, enabled: false, has_provider: false })
    await wrapper.get('input[type="search"]').setValue('gpt')
    await vi.advanceTimersByTimeAsync(250)
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0]).toMatchObject({ page: 1, search: 'gpt', enabled: false, has_provider: false })
    expect(wrapper.findAll('tr[data-model]').map(model => model.attributes('data-model'))).toEqual(['gpt-unprovided'])
  })

  it('keeps the old enabled state while saving and after a failed toggle', async () => {
    let reject: (error: Error) => void = () => {}
    update.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail }))
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('[role="switch"]').trigger('click')
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: false, pricing: null })
    expect(row(wrapper).get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(row(wrapper).get('[role="switch"]').attributes('disabled')).toBeDefined()
    reject(new Error('failed'))
    await flushPromises()
    expect(row(wrapper).get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[role="alert"]').text()).toContain('原有配置已保留')
  })

  it('persists a successful toggle and keeps its manual price', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper, 'paraformer-realtime-v2').get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'paraformer-realtime-v2', enabled: true,
      pricing: { billing_mode: 'audio', price_unit: 'second', unit_price: 0.000001 } })
    expect(row(wrapper, 'paraformer-realtime-v2').get('[role="switch"]').attributes('aria-checked')).toBe('true')
  })

  it('saves an explicit zero per-million price without double unit conversion', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="input_price_per_million"]').setValue('0')
    await wrapper.get('[data-field="output_price_per_million"]').setValue('1.5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: true,
      pricing: { billing_mode: 'token', price_unit: 'million_tokens', input_price_per_million: 0, output_price_per_million: 1.5 } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(row(wrapper).text()).toContain('输入 / 百万 Token: 0 USD')
    expect(row(wrapper).text()).toContain('手动价格')
  })

  it('keeps failed price edits open and leaves the saved list unchanged', async () => {
    update.mockRejectedValueOnce({ response: { data: { message: '价格版本冲突，请刷新' } } })
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="input_price_per_million"]').setValue('9')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('价格版本冲突，请刷新')
    expect(row(wrapper).text()).toContain('输入 / 百万 Token: 2.5 USD')
    expect((wrapper.get('[data-field="input_price_per_million"]').element as HTMLInputElement).value).toBe('9')
  })

  it('restores automatic pricing without enabling a disabled model and clearly shows missing fallback prices', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper, 'paraformer-realtime-v2').get('button[aria-label="恢复 paraformer-realtime-v2 的自动价"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('没有可用的自动价格')
    await wrapper.get('[role="dialog"] footer button.btn-primary').trigger('click')
    await flushPromises()
    expect(resetPrice).toHaveBeenCalledWith('paraformer-realtime-v2')
    expect(row(wrapper, 'paraformer-realtime-v2').get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(row(wrapper, 'paraformer-realtime-v2').text()).toContain('未配置价格')
  })

  it('sends status filtering to the backend before pagination', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('select[aria-label="模型状态"]').setValue('false')
    await flushPromises()
    expect(list.mock.calls.at(-1)?.[0]).toMatchObject({ page: 1, page_size: 20, enabled: false })
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('paraformer-realtime-v2')
    expect(wrapper.text()).not.toContain('gpt-test')
  })

  it('hides existing rows and dialogs immediately when admin access is lost', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    auth.isAdmin = false
    await flushPromises()
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('gpt-test')
  })

  it('saves separate duration directions, retains zero after reload, and never submits hidden token prices', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="billing_mode"]').setValue('duration')
    expect(wrapper.find('[data-field="unit_price"]').exists()).toBe(false)
    expect(wrapper.find('[data-field="input_price_per_million"]').exists()).toBe(false)
    expect(wrapper.get('[data-field="price_unit"]').findAll('option').map(option => option.attributes('value'))).toEqual(['second'])
    expect(wrapper.get('[role="dialog"]').text()).toContain('留空的方向表示未配置，不是免费')
    await wrapper.get('form').trigger('submit')
    expect(update).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('至少填写一个时长方向')
    await wrapper.get('[data-field="input_audio_price_per_second"]').setValue('0')
    await wrapper.get('[data-field="output_video_price_per_second"]').setValue('0.0000002')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: true, pricing: {
      billing_mode: 'duration', price_unit: 'second', input_audio_price_per_second: 0, output_video_price_per_second: 0.0000002
    } })
    expect(row(wrapper).text()).toContain('音频输入 / 秒: 0 USD')
    expect(row(wrapper).text()).toContain('音频输出 / 秒: 未配置价格')
    expect(row(wrapper).text()).toContain('视频输出 / 秒: 0.0000002 USD')
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    expect((wrapper.get('[data-field="input_audio_price_per_second"]').element as HTMLInputElement).value).toBe('0')
    expect((wrapper.get('[data-field="output_audio_price_per_second"]').element as HTMLInputElement).value).toBe('')
  })

  it('keeps duration prices and edited values after a failed save', async () => {
    serverModels[0].pricing = { billing_mode: 'duration', price_unit: 'second', input_audio_price_per_second: 0.01, output_audio_price_per_second: 0 }
    serverModels[0].source = 'manual'
    update.mockRejectedValueOnce({ response: { data: { message: '方向价格保存失败' } } })
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="output_audio_price_per_second"]').setValue('0.03')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('方向价格保存失败')
    expect((wrapper.get('[data-field="output_audio_price_per_second"]').element as HTMLInputElement).value).toBe('0.03')
    expect(row(wrapper).text()).toContain('音频输出 / 秒: 0 USD')
    expect(row(wrapper).text()).not.toContain('0.03 USD')
  })

  it('saves video prices by resolution with no default, retaining zero and omitting blank tiers after reload', async () => {
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="billing_mode"]').setValue('video')
    expect(wrapper.get('[data-field="price_unit"]').findAll('option').map(option => option.attributes('value'))).toEqual(['second'])
    expect(wrapper.get('[data-field="unit_price"]').attributes('required')).toBeUndefined()
    expect(wrapper.find('[data-field="input_price_per_million"]').exists()).toBe(false)
    expect(wrapper.get('[role="dialog"]').text()).toContain('没有默认价则不支持该分辨率')
    await wrapper.get('form').trigger('submit')
    expect(update).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('请至少填写默认单价或一个分辨率单价')
    await wrapper.get('[data-field="resolution_price_480P"]').setValue('0')
    await wrapper.get('[data-field="resolution_price_1080P"]').setValue('0.3')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: true, pricing: {
      billing_mode: 'video', price_unit: 'second', resolution_prices: [
        { resolution: '480P', unit_price: 0 }, { resolution: '1080P', unit_price: 0.3 }
      ]
    } })
    expect(row(wrapper).text()).toContain('480P: 0 USD / 秒')
    expect(row(wrapper).text()).toContain('1080P: 0.3 USD / 秒')
    expect(row(wrapper).text()).not.toContain('720P:')
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    expect((wrapper.get('[data-field="unit_price"]').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('[data-field="resolution_price_480P"]').element as HTMLInputElement).value).toBe('0')
    expect((wrapper.get('[data-field="resolution_price_720P"]').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('[data-field="resolution_price_1080P"]').element as HTMLInputElement).value).toBe('0.3')
  })

  it('keeps the existing video default price and saves a resolution override alongside it', async () => {
    serverModels[0].pricing = { billing_mode: 'video', price_unit: 'second', unit_price: 0.1 }
    serverModels[0].source = 'manual'
    const wrapper = render()
    await flushPromises()
    expect(row(wrapper).text()).toContain('默认: 0.1 USD / 秒')
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    expect((wrapper.get('[data-field="unit_price"]').element as HTMLInputElement).value).toBe('0.1')
    await wrapper.get('[data-field="resolution_price_720P"]').setValue('0.25')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: true, pricing: {
      billing_mode: 'video', price_unit: 'second', unit_price: 0.1, resolution_prices: [{ resolution: '720P', unit_price: 0.25 }]
    } })
    expect(row(wrapper).text()).toContain('默认: 0.1 USD / 秒')
    expect(row(wrapper).text()).toContain('720P: 0.25 USD / 秒')
  })

  it('never submits hidden video resolution prices when changing billing modes', async () => {
    serverModels[0].pricing = { billing_mode: 'video', price_unit: 'second', resolution_prices: [{ resolution: '480P', unit_price: 0 }] }
    serverModels[0].source = 'manual'
    const wrapper = render()
    await flushPromises()
    await row(wrapper).get('button[aria-label="调整 gpt-test 的价格"]').trigger('click')
    await wrapper.get('[data-field="billing_mode"]').setValue('audio')
    expect(wrapper.find('[data-field="resolution_price_480P"]').exists()).toBe(false)
    await wrapper.get('[data-field="unit_price"]').setValue('0.002')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'gpt-test', enabled: true,
      pricing: { billing_mode: 'audio', price_unit: 'second', unit_price: 0.002 } })
  })
})
