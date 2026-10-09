import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import GroupSelector from '../GroupSelector.vue'
import type { AdminGroup, GroupPlatform } from '@/types'

const providers: GroupPlatform[] = ['anthropic', 'openai', 'gemini', 'antigravity', 'azure_speech', 'dashscope', 'volcengine_ark']
const groups = ['all', ...providers].map((platform, index) => ({
  id: index + 1,
  name: platform,
  platform,
  subscription_type: 'standard',
  is_exclusive: false,
  rate_multiplier: 1,
  status: 'active',
  account_count: 1
} as AdminGroup))

function render(platform?: GroupPlatform, mixedScheduling = false) {
  return mount(GroupSelector, {
    props: { modelValue: [], groups, platform, mixedScheduling },
    global: {
      plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: {
        admin: { users: { groups: () => 'Groups' }, groups: { rateAndAccounts: () => 'Rate and accounts' } },
        common: { selectedCount: () => 'Selected', noGroupsAvailable: () => 'No groups' }
      } } })],
      stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' } }
    }
  })
}

describe('GroupSelector shared provider pools', () => {
  it.each(providers)('offers the shared group and the matching provider for %s', (platform) => {
    const wrapper = render(platform)
    const values = wrapper.findAll<HTMLInputElement>('input[type="checkbox"]').map(input => Number(input.element.value))
    expect(values).toEqual([1, groups.find(group => group.platform === platform)!.id])
    wrapper.unmount()
  })

  it('retains supported Antigravity mixed scheduling destinations', () => {
    const wrapper = render('antigravity', true)
    expect(wrapper.findAll<HTMLInputElement>('input').map(input => Number(input.element.value))).toEqual([1, 2, 4, 5])
    wrapper.unmount()
  })

  it('binds the shared group only when explicitly selected', async () => {
    const wrapper = render('openai')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.find('input[value="1"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([[[1]]])
    wrapper.unmount()
  })
})
