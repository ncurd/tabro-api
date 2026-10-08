import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SettingsView from '../SettingsView.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { OIDCBillingConnection } from '@/api/admin/settings'

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
  getOIDCBillingConnection: vi.fn(),
  updateOIDCBillingConnection: vi.fn(),
  setupOIDCBillingConnection: vi.fn(),
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
    settingsApi.getOIDCBillingConnection.mockReset()
    settingsApi.getOIDCBillingConnection.mockRejectedValue(new Error('Connection endpoint unavailable'))
    settingsApi.updateOIDCBillingConnection.mockReset()
    settingsApi.setupOIDCBillingConnection.mockReset()
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

  const connectionSettings = (overrides: Partial<OIDCBillingConnection> = {}): OIDCBillingConnection => ({
    billing_center: {
      enabled: true, base_url: 'https://auth.example.test', token_url: 'https://auth.example.test/connect/token',
      producer_client_id: 'api-billing', client_secret: '', timeout_seconds: 15, insecure_local: false
    },
    resource_server: {
      enabled: true, auto_provision: false, issuer_url: 'https://auth.example.test', discovery_url: '', jwks_url: '',
      audience: 'tabro-llm', required_scopes: 'llm.invoke', allowed_client_ids: 'tabro-agent',
      allowed_signing_algs: 'RS256,ES256,PS256', clock_skew_seconds: 120, jwks_cache_ttl_seconds: 300,
      tenant_claim: 'tenant_id', require_tenant: false,
      token_exchange: { require_actor: true, actor_claim: 'act', allowed_actor_client_ids: 'tabro-agent', max_delegation_depth: 4 }
    },
    client_secret_configured: true, source: 'config', oidc_billing_supported: false,
    ...overrides
  })

  it('keeps editable custom configuration in advanced settings and preserves a configured secret when saving', async () => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false, oidc_connect_enabled: true, oidc_only_enabled: true, oidc_billing_supported: false
    })
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    settingsApi.updateOIDCBillingConnection.mockImplementation(async (payload) => ({
      ...payload, client_secret_configured: true, source: 'database', oidc_billing_supported: true
    }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    expect((wrapper.get('#billing-base-url').element as HTMLInputElement).value).toBe('https://auth.example.test')
    expect((wrapper.get('#billing-client-secret').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('[data-testid="oidc-billing-toggle"]').attributes('disabled')).toBeDefined()
    const advanced = wrapper.get('[data-testid="oidc-billing-connection-advanced"]')
    expect(advanced.attributes('open')).toBeUndefined()
    expect(advanced.element.contains(wrapper.get('#billing-producer-client-id').element)).toBe(true)
    const advancedElement = advanced.element as HTMLDetailsElement
    advancedElement.open = true

    await wrapper.get('#billing-producer-client-id').setValue('updated-billing-client')
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    await flushPromises()

    const payload = settingsApi.updateOIDCBillingConnection.mock.calls[0][0]
    expect(payload.billing_center).toMatchObject({ producer_client_id: 'updated-billing-client', client_secret: '' })
    expect(payload.resource_server.token_exchange.allowed_actor_client_ids).toBe('tabro-agent')
    expect(payload).not.toHaveProperty('client_secret_configured')
    expect(payload).not.toHaveProperty('oidc_billing_supported')
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="oidc-billing-toggle"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('only shows the saved Auth URL, billing secret and setup action by default', async () => {
    settingsApi.getSettings.mockResolvedValue({ backend_mode_enabled: false, oidc_connect_enabled: true, oidc_connect_issuer_url: 'https://auth.example.test' })
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    const connection = wrapper.get('[data-testid="oidc-billing-connection"]')
    const advanced = connection.get('[data-testid="oidc-billing-connection-advanced"]')
    const basicInputs = connection.findAll('input').filter(input => !advanced.element.contains(input.element))
    expect(basicInputs.map(input => input.attributes('id'))).toEqual(['billing-auth-url', 'billing-client-secret'])
    expect(advanced.attributes('open')).toBeUndefined()
    expect(advanced.element.contains(connection.get('[data-testid="oidc-billing-connector-toggle"]').element)).toBe(true)
    expect(advanced.element.contains(connection.get('[data-testid="oidc-billing-resource-toggle"]').element)).toBe(true)
    expect((connection.get('#billing-auth-url').element as HTMLInputElement).readOnly).toBe(true)
    expect(connection.get('[data-testid="oidc-billing-connection-setup"]').exists()).toBe(true)

    const loginIssuer = wrapper.findAll('input').find(input => input.element !== connection.get('#billing-auth-url').element
      && (input.element as HTMLInputElement).value === 'https://auth.example.test'
      && !connection.element.contains(input.element))
    expect(loginIssuer).toBeDefined()
    await loginIssuer!.setValue('https://unsaved.example.test')
    expect((connection.get('#billing-auth-url').element as HTMLInputElement).value).toBe('https://auth.example.test')
    wrapper.unmount()
  })

  it('verifies and configures with only a billing secret, then enables billing and clears the secret', async () => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false, oidc_connect_enabled: true, oidc_only_enabled: true,
      oidc_connect_issuer_url: 'https://auth.example.test', oidc_billing_supported: false
    })
    const unconfigured = connectionSettings({ client_secret_configured: false })
    unconfigured.billing_center.enabled = false
    unconfigured.billing_center.base_url = ''
    unconfigured.billing_center.token_url = ''
    unconfigured.resource_server.enabled = false
    unconfigured.resource_server.allowed_client_ids = ''
    settingsApi.getOIDCBillingConnection.mockResolvedValue(unconfigured)
    settingsApi.setupOIDCBillingConnection.mockResolvedValue(connectionSettings({ oidc_billing_supported: true, source: 'database' }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('#billing-client-secret').setValue('dedicated-billing-secret')
    await wrapper.get('[data-testid="oidc-billing-connection-setup"]').trigger('click')
    await flushPromises()
    expect(settingsApi.setupOIDCBillingConnection).toHaveBeenCalledWith('dedicated-billing-secret')
    expect(settingsApi.updateOIDCBillingConnection).not.toHaveBeenCalled()
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="oidc-billing-toggle"]').attributes('disabled')).toBeUndefined()
    expect((wrapper.get('#billing-client-secret').element as HTMLInputElement).value).toBe('')
    expect(showSuccessMock).toHaveBeenCalledWith('admin.settings.oidc.connectionVerified')
    expect(wrapper.text()).toContain('admin.settings.oidc.connectionVerifiedHint')
    wrapper.unmount()
  })

  it('keeps a configured billing secret when setup is run with an empty password field', async () => {
    settingsApi.getSettings.mockResolvedValue({ backend_mode_enabled: false, oidc_connect_issuer_url: 'https://auth.example.test' })
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    settingsApi.setupOIDCBillingConnection.mockResolvedValue(connectionSettings({ oidc_billing_supported: true }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('[data-testid="oidc-billing-connection-setup"]').trigger('click')
    await flushPromises()
    expect(settingsApi.setupOIDCBillingConnection).toHaveBeenCalledWith('')
    wrapper.unmount()
  })

  it('requires a first billing secret before verifying the connection', async () => {
    settingsApi.getSettings.mockResolvedValue({ backend_mode_enabled: false, oidc_connect_issuer_url: 'https://auth.example.test' })
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings({ client_secret_configured: false }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('[data-testid="oidc-billing-connection-setup"]').trigger('click')
    expect(settingsApi.setupOIDCBillingConnection).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.settings.oidc.connectionSetupSecretRequired')
    wrapper.unmount()
  })

  it.each([
    ['OIDC_BILLING_SETUP_REQUIRES_OIDC', 'connectionSetupRequiresOIDC'],
    ['OIDC_BILLING_SETUP_INVALID_ISSUER', 'connectionSetupInvalidIssuer'],
    ['OIDC_BILLING_SETUP_SECRET_REQUIRED', 'connectionSetupSecretRequired'],
    ['OIDC_BILLING_SETUP_DISCOVERY_FAILED', 'connectionSetupDiscoveryFailed'],
    ['OIDC_BILLING_SETUP_UNTRUSTED_ENDPOINT', 'connectionSetupUntrustedEndpoint'],
    ['OIDC_BILLING_SETUP_CREDENTIALS_REJECTED', 'connectionSetupCredentialsRejected'],
    ['OIDC_BILLING_SETUP_CONFIG_CHANGED', 'connectionSetupConfigChanged']
  ])('explains setup rejection %s and keeps the secret without updating capability', async (reason, error) => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false, oidc_connect_enabled: true, oidc_only_enabled: true,
      oidc_connect_issuer_url: 'https://auth.example.test', oidc_billing_supported: false
    })
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings({ client_secret_configured: false }))
    settingsApi.setupOIDCBillingConnection.mockRejectedValue({ reason })
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('#billing-client-secret').setValue('test-secret')
    await wrapper.get('[data-testid="oidc-billing-connection-setup"]').trigger('click')
    await flushPromises()
    expect(showErrorMock).toHaveBeenCalledWith(`admin.settings.oidc.${error}`)
    expect((wrapper.get('#billing-client-secret').element as HTMLInputElement).value).toBe('test-secret')
    expect(wrapper.get('[data-testid="oidc-billing-toggle"]').attributes('disabled')).toBeDefined()
    expect(settingsApi.updateOIDCBillingConnection).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('uses the saved login issuer for an initial empty gateway issuer and clears a saved new secret', async () => {
    settingsApi.getSettings.mockResolvedValue({ backend_mode_enabled: false, oidc_connect_issuer_url: 'https://auth.example.test' })
    const settings = connectionSettings({ client_secret_configured: false })
    settings.resource_server.issuer_url = ''
    settingsApi.getOIDCBillingConnection.mockResolvedValue(settings)
    settingsApi.updateOIDCBillingConnection.mockImplementation(async (payload) => ({
      ...payload, client_secret_configured: true, source: 'database', oidc_billing_supported: true
    }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    expect((wrapper.get('#billing-resource-issuer').element as HTMLInputElement).value).toBe('https://auth.example.test')
    await wrapper.get('#billing-client-secret').setValue('replacement-secret')
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    await flushPromises()
    expect(settingsApi.updateOIDCBillingConnection.mock.calls[0][0].billing_center.client_secret).toBe('replacement-secret')
    expect((wrapper.get('#billing-client-secret').element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })

  it.each([
    ['#billing-base-url', 'http://auth.example.test', 'connectionEndpointsInvalid'],
    ['#billing-token-url', 'https://auth.example.test/token?secret=value', 'connectionEndpointsInvalid'],
    ['#billing-timeout', '61', 'connectionTimeoutInvalid'],
    ['#billing-allowed-clients', '', 'connectionResourceRequired'],
    ['#billing-allowed-actors', '', 'connectionActorRequired'],
    ['#billing-delegation-depth', '0', 'connectionActorRequired'],
    ['#billing-signing-algs', 'HS256', 'connectionAlgorithmsInvalid'],
    ['#billing-jwks-cache-ttl', '0', 'connectionCacheInvalid']
  ])('rejects invalid billing connection input %s', async (selector, value, error) => {
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get(selector).setValue(value)
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    await flushPromises()
    expect(settingsApi.updateOIDCBillingConnection).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith(`admin.settings.oidc.${error}`)
    wrapper.unmount()
  })

  it('requires the first billing secret and preserves input when the server rejects a save', async () => {
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings({ client_secret_configured: false }))
    settingsApi.updateOIDCBillingConnection.mockRejectedValue(new Error('Invalid connection'))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    expect(settingsApi.updateOIDCBillingConnection).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.settings.oidc.connectionCredentialsRequired')
    await wrapper.get('#billing-client-secret').setValue('new-secret')
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    await flushPromises()
    expect((wrapper.get('#billing-client-secret').element as HTMLInputElement).value).toBe('new-secret')
    expect(showErrorMock).toHaveBeenCalledWith('admin.settings.oidc.connectionSaveFailed')
    wrapper.unmount()
  })

  it('requires tenant validation and one delegation level before enabling automatic provisioning', async () => {
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    settingsApi.updateOIDCBillingConnection.mockImplementation(async (payload) => ({
      ...payload, client_secret_configured: true, source: 'database', oidc_billing_supported: true
    }))
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    await wrapper.get('[data-testid="oidc-billing-auto-provision-toggle"]').trigger('click')
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    expect(settingsApi.updateOIDCBillingConnection).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.settings.oidc.connectionAutoProvisionInvalid')
    await wrapper.get('[data-testid="oidc-billing-require-tenant-toggle"]').trigger('click')
    await wrapper.get('#billing-delegation-depth').setValue('1')
    await wrapper.get('[data-testid="oidc-billing-connection-save"]').trigger('click')
    await flushPromises()
    expect(settingsApi.updateOIDCBillingConnection).toHaveBeenCalledWith(expect.objectContaining({
      resource_server: expect.objectContaining({
        auto_provision: true, require_tenant: true,
        token_exchange: expect.objectContaining({ require_actor: true, max_delegation_depth: 1 })
      })
    }))
    wrapper.unmount()
  })

  it('keeps other settings available when connection loading fails and allows retry', async () => {
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.settings.oidc.connectionLoadFailed')
    expect(wrapper.find('[data-testid="oidc-billing-connection-save"]').exists()).toBe(false)
    expect(wrapper.find('form').exists()).toBe(true)
    settingsApi.getOIDCBillingConnection.mockResolvedValue(connectionSettings())
    await wrapper.get('[data-testid="oidc-billing-connection-retry"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="oidc-billing-connection-save"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('explains an unconfigured billing connection independently of OIDC-only mode', async () => {
    settingsApi.getSettings.mockResolvedValue({
      backend_mode_enabled: false, oidc_connect_enabled: true, oidc_only_enabled: true, oidc_billing_supported: false
    })
    const wrapper = mountOIDCBillingSettings()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.settings.oidc.billingRequiresConnection')
    expect(wrapper.text()).not.toContain('admin.settings.oidc.billingRequiresOnly')
    wrapper.unmount()
  })

})
