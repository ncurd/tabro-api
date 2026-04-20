import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SettingsView from '../SettingsView.vue'

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

  it('uses the Chinese payment docs link for zh-CN locale', async () => {
    localeRef.value = 'zh-CN'

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
          ProxySelector: simpleStub,
          ImageUpload: simpleStub,
          BackupSettings: simpleStub
        }
      }
    })

    await flushPromises()

    expect(wrapper.get('[data-testid="payment-config-guide-link"]').attributes('href')).toContain('PAYMENT_CN.md')
    expect(wrapper.get('[data-testid="payment-provider-guide-link"]').attributes('href')).toContain('PAYMENT_CN.md')
  })
})
