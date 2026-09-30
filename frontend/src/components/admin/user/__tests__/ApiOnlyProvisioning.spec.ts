import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import UserCreateModal from '../UserCreateModal.vue'
import UserEditModal from '../UserEditModal.vue'
import UserApiKeysModal from '../UserApiKeysModal.vue'

const createUser = vi.fn()
const updateUser = vi.fn()
const getUserApiKeys = vi.fn()
const provisionIdentity = vi.fn()
const showSuccess = vi.fn()
const showError = vi.fn()

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      create: (...args: unknown[]) => createUser(...args),
      update: (...args: unknown[]) => updateUser(...args),
      getUserApiKeys: (...args: unknown[]) => getUserApiKeys(...args)
    },
    apiKeys: {
      provisionOIDCGatewayIdentity: (...args: unknown[]) => provisionIdentity(...args)
    },
    groups: { getAll: vi.fn().mockResolvedValue([]) },
    userAttributes: { updateUserAttributeValues: vi.fn() }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

const BaseDialog = defineComponent({
  template: '<div><slot /><slot name="footer" /></div>'
})

const stubs = { BaseDialog, Icon: true, UserAttributeForm: true, GroupBadge: true, GroupOptionItem: true }

const apiOnlyUser = {
  id: 42,
  email: 'api@example.test',
  username: 'api',
  notes: '',
  role: 'user',
  api_only: true,
  concurrency: 1,
  status: 'active'
} as any

describe('admin API-only user provisioning', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    createUser.mockResolvedValue(apiOnlyUser)
    updateUser.mockResolvedValue(apiOnlyUser)
    getUserApiKeys.mockResolvedValue({ items: [] })
    provisionIdentity.mockResolvedValue({ api_key: { id: 9, oidc_managed: true, key: '' } })
  })

  it('creates an API-only user without sending a password', async () => {
    const wrapper = mount(UserCreateModal, { props: { show: true }, global: { stubs } })
    await wrapper.find('input[type="email"]').setValue('api@example.test')
    await wrapper.find('input[type="checkbox"]').setValue(true)
    expect(wrapper.find('input[placeholder="admin.users.enterPassword"]').exists()).toBe(false)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(createUser).toHaveBeenCalledOnce()
    expect(createUser.mock.calls[0][0]).toMatchObject({ email: 'api@example.test', api_only: true })
    expect(createUser.mock.calls[0][0]).not.toHaveProperty('password')
  })

  it('requires a password when enabling console login for an API-only user', async () => {
    const wrapper = mount(UserEditModal, {
      props: { show: true, user: apiOnlyUser },
      global: { stubs }
    })
    await wrapper.find('input[type="checkbox"]').setValue(false)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(updateUser).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.users.passwordRequiredForConsole')

    await wrapper.find('input[placeholder="admin.users.passwordRequiredForConsole"]').setValue('test-password')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(42, expect.objectContaining({ api_only: false, password: 'test-password' }))
  })

  it('binds an Auth identity for a user without their gateway session', async () => {
    const wrapper = mount(UserApiKeysModal, {
      props: { show: true, user: apiOnlyUser },
      global: { stubs }
    })
    await flushPromises()
    await wrapper.find('#oidc-identity-issuer').setValue('https://auth.example.test')
    await wrapper.find('#oidc-identity-subject').setValue('user-123')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(provisionIdentity).toHaveBeenCalledWith(42, {
      issuer: 'https://auth.example.test',
      subject: 'user-123'
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.users.oidcIdentityProvisioned')
  })
})
