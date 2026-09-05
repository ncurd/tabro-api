import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const { mockGetPublicSettings } = vi.hoisted(() => ({
  mockGetPublicSettings: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute: { value: { query: {} } },
    push: vi.fn(),
  }),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: vi.fn(),
    login2FA: vi.fn(),
  }),
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn(),
  }),
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: mockGetPublicSettings,
  isTotp2FARequired: () => false,
}))

vi.mock('@/components/layout', () => ({
  AuthLayout: {
    template: '<main><slot /><footer><slot name="footer" /></footer></main>',
  },
}))

vi.mock('@/components/auth/LinuxDoOAuthSection.vue', () => ({
  default: {
    name: 'LinuxDoOAuthSection',
    template: '<div data-testid="linuxdo-oauth" />',
  },
}))

vi.mock('@/components/auth/OidcOAuthSection.vue', () => ({
  default: {
    name: 'OidcOAuthSection',
    template: '<div data-testid="oidc-oauth" />',
  },
}))

vi.mock('@/components/auth/TotpLoginModal.vue', () => ({
  default: { template: '<div data-testid="totp-modal" />' },
}))

vi.mock('@/components/icons/Icon.vue', () => ({
  default: { template: '<span />' },
}))

vi.mock('@/components/TurnstileWidget.vue', () => ({
  default: { template: '<div data-testid="turnstile" />' },
}))

interface LoginSettings {
  backend_mode_enabled: boolean
  linuxdo_oauth_enabled: boolean
  oidc_oauth_enabled: boolean
}

async function mountLoginView(settings: LoginSettings) {
  mockGetPublicSettings.mockResolvedValue({
    ...settings,
    oidc_oauth_provider_name: 'Corporate SSO',
    password_reset_enabled: false,
    turnstile_enabled: false,
    turnstile_site_key: '',
  })

  const wrapper = mount(LoginView, {
    global: {
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('LoginView OAuth availability', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    sessionStorage.clear()
  })

  it('shows OIDC and password login but hides LinuxDo in backend mode', async () => {
    const wrapper = await mountLoginView({
      backend_mode_enabled: true,
      linuxdo_oauth_enabled: true,
      oidc_oauth_enabled: true,
    })

    expect(wrapper.find('[data-testid="oidc-oauth"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="linuxdo-oauth"]').exists()).toBe(false)
    expect(wrapper.find('#email').exists()).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(true)
  })

  it('shows both OAuth providers with password login outside backend mode', async () => {
    const wrapper = await mountLoginView({
      backend_mode_enabled: false,
      linuxdo_oauth_enabled: true,
      oidc_oauth_enabled: true,
    })

    expect(wrapper.find('[data-testid="oidc-oauth"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="linuxdo-oauth"]').exists()).toBe(true)
    expect(wrapper.find('#email').exists()).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(true)
  })

  it('keeps password login when OAuth is disabled in backend mode', async () => {
    const wrapper = await mountLoginView({
      backend_mode_enabled: true,
      linuxdo_oauth_enabled: false,
      oidc_oauth_enabled: false,
    })

    expect(wrapper.find('[data-testid="oidc-oauth"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="linuxdo-oauth"]').exists()).toBe(false)
    expect(wrapper.find('#email').exists()).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(true)
  })
})
