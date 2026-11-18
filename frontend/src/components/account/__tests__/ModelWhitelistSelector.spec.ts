import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'
import aliyunTextModels from '@/data/aliyunTextModels.json'

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showInfo: vi.fn() }) }))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn() }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key })
}))

describe('ModelWhitelistSelector provider suggestions', () => {
  it('follows the base URL without overwriting selected aliases and fills related Aliyun models explicitly', async () => {
    const baseUrl = 'https://dashscope.aliyuncs.com/compatible-mode/v1'
    const wrapper = mount(ModelWhitelistSelector, {
      props: { modelValue: ['my-custom-alias'], platform: 'openai', accountType: 'apikey', baseUrl },
      global: { stubs: { ModelIcon: true, Icon: true } }
    })
    await wrapper.get('div.cursor-pointer').trigger('click')
    const choices = () => wrapper.findAll('button').map(button => button.text())
    expect(choices()).toContain(aliyunTextModels[0])
    expect(choices()).not.toContain('gpt-6.2-astra')
    await wrapper.setProps({ baseUrl: 'https://api.openai.com' })
    expect(choices()).toContain('gpt-6.2-astra')
    expect(wrapper.text()).toContain('my-custom-alias')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.setProps({ baseUrl })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.fillRelatedModels')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['my-custom-alias', ...aliyunTextModels])
    wrapper.unmount()
  })
})
