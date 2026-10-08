import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SettingsView from '../SettingsView.vue'
import Toggle from '@/components/common/Toggle.vue'

const { localeRef, setLocaleMock, showErrorMock, showSuccessMock } = vi.hoisted(() => ({
  localeRef: { value: 'en' },
  setLocaleMock: vi.fn(async (code: string) => {
    localeRef.value = code
  }),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn()
}))

const settingsApi = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  testSmtpConnection: vi.fn(),
  sendTestEmail: vi.fn(),
  getAdminApiKey: vi.fn(),
  regenerateAdminApiKey: vi.fn(),
  deleteAdminApiKey: vi.fn(),
  getOverloadCooldownSettings: vi.fn(),
  updateOverloadCooldownSettings: vi.fn(),
  getStreamTimeoutSettings: vi.fn(),
  updateStreamTimeoutSettings: vi.fn(),
  getRectifierSettings: vi.fn(),
  updateRectifierSettings: vi.fn(),
  getBetaPolicySettings: vi.fn(),
  updateBetaPolicySettings: vi.fn(),
  getWebSearchEmulationConfig: vi.fn(),
  updateWebSearchEmulationConfig: vi.fn(),
  resetWebSearchUsage: vi.fn(),
  testWebSearchEmulation: vi.fn()
}))

const groupsGetAllMock = vi.hoisted(() => vi.fn())
const proxiesListMock = vi.hoisted(() => vi.fn())
const paymentGetProvidersMock = vi.hoisted(() => vi.fn())
const copyToClipboardMock = vi.hoisted(() => vi.fn())
const adminSettingsFetchMock = vi.hoisted(() => vi.fn())
const fetchPublicSettingsMock = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: localeRef
    })
  }
})

vi.mock('@/i18n', () => ({
  availableLocales: [
    { code: 'en', name: 'English', flag: '🇺🇸' },
    { code: 'zh-CN', name: '简体中文', flag: '🇨🇳' },
    { code: 'de', name: 'Deutsch', flag: '🇩🇪' }
  ],
  setLocale: setLocaleMock
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} })
}))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: settingsApi,
    groups: {
      getAll: groupsGetAllMock
    },
    proxies: {
      list: proxiesListMock
    },
    payment: {
      getProviders: paymentGetProvidersMock
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock,
    fetchPublicSettings: fetchPublicSettingsMock
  })
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({
    fetch: adminSettingsFetchMock
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: copyToClipboardMock
  })
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback
}))

const appLayoutStub = {
  template: '<div><slot /></div>'
}

const simpleStub = {
  template: '<div><slot /></div>'
}

describe('admin SettingsView', () => {
  beforeEach(() => {
    localeRef.value = 'en'
    setLocaleMock.mockClear()
    showErrorMock.mockClear()
    showSuccessMock.mockClear()
    fetchPublicSettingsMock.mockReset()
    fetchPublicSettingsMock.mockResolvedValue(null)
    adminSettingsFetchMock.mockReset()
    adminSettingsFetchMock.mockResolvedValue(undefined)
    copyToClipboardMock.mockReset()
    copyToClipboardMock.mockResolvedValue(undefined)

    settingsApi.getSettings.mockReset()
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false,
      default_subscriptions: [],
      registration_email_suffix_whitelist: [],
      payment_enabled: true,
      table_page_size_options: [10, 20, 50, 100],
      smtp_security: 'tls',
      smtp_use_tls: true
    })
    settingsApi.updateSettings.mockReset()
    settingsApi.updateWebSearchEmulationConfig.mockReset()
    settingsApi.updateWebSearchEmulationConfig.mockResolvedValue(undefined)
    settingsApi.getAdminApiKey.mockReset()
    settingsApi.getAdminApiKey.mockResolvedValue({
      exists: false,
      masked_key: ''
    })
    settingsApi.getOverloadCooldownSettings.mockReset()
    settingsApi.getOverloadCooldownSettings.mockResolvedValue({
      enabled: true,
      cooldown_minutes: 10
    })
    settingsApi.getStreamTimeoutSettings.mockReset()
    settingsApi.getStreamTimeoutSettings.mockResolvedValue({
      enabled: true,
      action: 'temp_unsched',
      temp_unsched_minutes: 5,
      threshold_count: 3,
      threshold_window_minutes: 10
    })
    settingsApi.getRectifierSettings.mockReset()
    settingsApi.getRectifierSettings.mockResolvedValue({
      enabled: true,
      thinking_signature_enabled: true,
      thinking_budget_enabled: true,
      apikey_signature_enabled: false,
      apikey_signature_patterns: []
    })
    settingsApi.getBetaPolicySettings.mockReset()
    settingsApi.getBetaPolicySettings.mockResolvedValue({
      rules: []
    })
    settingsApi.getWebSearchEmulationConfig.mockReset()
    settingsApi.getWebSearchEmulationConfig.mockResolvedValue({
      enabled: false,
      providers: []
    })

    groupsGetAllMock.mockReset()
    groupsGetAllMock.mockResolvedValue([])
    proxiesListMock.mockReset()
    proxiesListMock.mockResolvedValue({ items: [] })
    paymentGetProvidersMock.mockReset()
    paymentGetProvidersMock.mockResolvedValue({ data: [] })
  })

  it('shows the interface language selector and switches locale immediately', async () => {
    const wrapper = mount(SettingsView, {
      global: {
        stubs: {
          AppLayout: appLayoutStub,
          Icon: true,
          Select: simpleStub,
          ConfirmDialog: simpleStub,
          PaymentProviderList: simpleStub,
          PaymentProviderDialog: simpleStub,
          GroupBadge: simpleStub,
          GroupOptionItem: simpleStub,
          Toggle: true,
          RouterLink: simpleStub,
          ProxySelector: simpleStub,
          ImageUpload: simpleStub,
          BackupSettings: simpleStub
        }
      }
    })

    await flushPromises()

    const select = wrapper.get('[data-testid="interface-language-select"]')
    await select.setValue('zh-CN')

    expect(setLocaleMock).toHaveBeenCalledWith('zh-CN')
  })

  it('disables OIDC-only mode until OIDC is configured and enabled', async () => {
    const wrapper = mount(SettingsView, {
      global: {
        stubs: {
          AppLayout: appLayoutStub,
          Icon: true,
          Select: simpleStub,
          ConfirmDialog: simpleStub,
          PaymentProviderList: simpleStub,
          PaymentProviderDialog: simpleStub,
          GroupBadge: simpleStub,
          GroupOptionItem: simpleStub,
          Toggle,
          RouterLink: simpleStub,
          ProxySelector: simpleStub,
          ImageUpload: simpleStub,
          BackupSettings: simpleStub
        }
      }
    })

    await flushPromises()

    expect(wrapper.get('[data-testid="oidc-only-toggle"]').attributes('disabled')).toBeDefined()
  })

  it('only allows OIDC-only mode after enabled OIDC settings have been saved', async () => {
    const savedSettings = {
      backend_mode_enabled: false,
      default_subscriptions: [],
      registration_email_suffix_whitelist: [],
      payment_enabled: true,
      table_page_size_options: [10, 20, 50, 100],
      smtp_security: 'tls',
      smtp_use_tls: true,
      oidc_connect_enabled: true,
      oidc_only_enabled: false,
      oidc_connect_client_id: 'client-1',
      oidc_connect_client_secret_configured: true,
      oidc_connect_issuer_url: 'https://identity.example.test',
      oidc_connect_redirect_url: 'https://app.example.test/api/v1/auth/oauth/oidc/callback',
      oidc_connect_frontend_redirect_url: '/auth/oidc/callback',
      oidc_connect_token_auth_method: 'client_secret_post'
    }
    settingsApi.getSettings.mockResolvedValue(savedSettings)
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...savedSettings, ...payload }))

    const wrapper = mount(SettingsView, {
      global: {
        stubs: {
          AppLayout: appLayoutStub,
          Icon: true,
          Select: simpleStub,
          ConfirmDialog: simpleStub,
          PaymentProviderList: simpleStub,
          PaymentProviderDialog: simpleStub,
          GroupBadge: simpleStub,
          GroupOptionItem: simpleStub,
          Toggle,
          ProxySelector: simpleStub,
          ImageUpload: simpleStub,
          BackupSettings: simpleStub,
          RouterLink: simpleStub
        }
      }
    })
    await flushPromises()

    const toggle = wrapper.get('[data-testid="oidc-only-toggle"]')
    expect(toggle.attributes('disabled')).toBeUndefined()
    await toggle.trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(settingsApi.updateSettings).toHaveBeenCalledWith(expect.objectContaining({ oidc_only_enabled: true }))

    settingsApi.updateSettings.mockRejectedValueOnce({
      status: 403,
      code: 403,
      reason: 'OIDC_ADMIN_SESSION_REQUIRED',
      message: 'Sign in through OIDC'
    })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(showErrorMock).toHaveBeenCalledWith('admin.settings.oidc.onlyAdminSessionRequired')
  })

  const mountOIDCBillingSettings = () => mount(SettingsView, {
    global: {
      stubs: {
        AppLayout: appLayoutStub, Icon: true, Select: simpleStub,
        ConfirmDialog: simpleStub, PaymentProviderList: simpleStub,
        PaymentProviderDialog: simpleStub, GroupBadge: simpleStub,
        GroupOptionItem: simpleStub, Toggle, ProxySelector: simpleStub,
        ImageUpload: simpleStub, BackupSettings: simpleStub, RouterLink: simpleStub
      }
    }
  })

  it.each([
    { only: false, supported: false, disabled: true },
    { only: false, supported: true, disabled: true },
    { only: true, supported: false, disabled: true },
    { only: true, supported: true, disabled: false }
  ])('gates OIDC billing on saved OIDC-only mode and capability: %j', async ({ only, supported, disabled }) => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false,
      oidc_connect_enabled: true,
      oidc_only_enabled: only,
      oidc_billing_supported: supported
    })
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    expect(wrapper.get('[data-testid="oidc-billing-toggle"]').attributes('disabled') !== undefined).toBe(disabled)
    wrapper.unmount()
  })

  it('saves billing multiplier and the daily cutoff without submitting capability', async () => {
    const settings = {
      backend_mode_enabled: false,
      oidc_connect_enabled: true,
      oidc_only_enabled: true,
      oidc_billing_supported: true,
      oidc_billing_enabled: false
    }
    settingsApi.getSettings.mockResolvedValue(settings)
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...settings, ...payload }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('[data-testid="oidc-billing-toggle"]').trigger('click')
    await wrapper.get('#oidc-billing-multiplier').setValue('2.5')
    await wrapper.get('#oidc-billing-time').setValue('01:30')
    await wrapper.get('#oidc-billing-timezone').setValue('Asia/Shanghai')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const payload = settingsApi.updateSettings.mock.calls[0][0]
    expect(payload).toMatchObject({
      oidc_billing_enabled: true,
      oidc_billing_rate_multiplier: 2.5,
      oidc_billing_settlement_time: '01:30',
      oidc_billing_settlement_timezone: 'Asia/Shanghai'
    })
    expect(payload).not.toHaveProperty('oidc_billing_supported')
    expect(wrapper.find('input[placeholder="0.00"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each([
    ['#oidc-billing-multiplier', '0', 'billingMultiplierInvalid'],
    ['#oidc-billing-timezone', 'Invalid/Timezone', 'billingTimezoneInvalid'],
    ['#oidc-billing-time', '', 'billingTimeInvalid']
  ])('rejects invalid billing input %s', async (selector, value, error) => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false,
      oidc_connect_enabled: true, oidc_only_enabled: true,
      oidc_billing_supported: true, oidc_billing_enabled: true
    })
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get(selector).setValue(value)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith(`admin.settings.oidc.${error}`)
    wrapper.unmount()
  })

})
