import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UsageFilters from '../UsageFilters.vue'

vi.mock('@/api/admin', () => ({ adminAPI: {
  groups: { list: vi.fn().mockResolvedValue({ items: [] }) },
  dashboard: { getModelStats: vi.fn().mockResolvedValue({ models: [] }) }
} }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

describe('usage billing mode filter', () => {
  it('offers duration as its own backend filter value and emits the selected mode', async () => {
    const filters: Record<string, unknown> = {}
    const wrapper = mount(UsageFilters, {
      props: { modelValue: filters, exporting: false, startDate: '2026-11-01', endDate: '2026-11-15' },
      global: { stubs: { Select: {
        props: ['modelValue', 'options'], emits: ['update:modelValue', 'change'],
        template: `<select :value="modelValue ?? ''" @change="$emit('update:modelValue', $event.target.value); $emit('change')">
          <option v-for="option in options" :key="option.value ?? ''" :value="option.value ?? ''">{{ option.label }}</option>
        </select>`
      } } }
    })
    await flushPromises()
    const modeSelect = wrapper.findAll('select').find(select => select.find('option[value="duration"]').exists())!
    expect(modeSelect.get('option[value="duration"]').text()).toBe('admin.usage.billingModeDuration')
    await modeSelect.setValue('duration')
    expect(filters.billing_mode).toBe('duration')
    expect(wrapper.emitted('change')).toHaveLength(1)
    wrapper.unmount()
  })
})
