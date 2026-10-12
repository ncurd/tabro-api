import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AuthAccessKeysPanel from '../AuthAccessKeysPanel.vue'
import { authAccessKeysUrl } from '@/utils/authAccessKeys'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Auth access key management', () => {
  it('links to the central identity page and explains the disabled direct default', () => {
    const wrapper = mount(AuthAccessKeysPanel, { props: { accountCenterUrl: 'https://auth.example' } })
    expect(wrapper.get('a').attributes('href')).toBe('https://auth.example/Identity/Account/Manage/AccessKeys')
    expect(wrapper.text()).toContain('keys.authKeysDirectDisabled')
    expect(wrapper.text()).toContain('keys.authKeysDescription')
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('does not offer a creation action when the account center is missing or invalid', () => {
    const wrapper = mount(AuthAccessKeysPanel)
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.text()).toContain('keys.authKeysMissingCenter')
    expect(authAccessKeysUrl('javascript:alert(1)')).toBeNull()
    expect(authAccessKeysUrl('/relative')).toBeNull()
  })

  it('preserves an Auth path base without duplicating an existing account-center path', () => {
    expect(authAccessKeysUrl('https://auth.example/auth/Identity/Account/Manage?returnUrl=old#section'))
      .toBe('https://auth.example/auth/Identity/Account/Manage/AccessKeys')
    expect(authAccessKeysUrl('https://auth.example/auth/')).toBe('https://auth.example/auth/Identity/Account/Manage/AccessKeys')
  })
})
