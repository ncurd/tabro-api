import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '../RegisterView.vue'
import ForgotPasswordView from '../ForgotPasswordView.vue'
import ResetPasswordView from '../ResetPasswordView.vue'
import EmailVerifyView from '../EmailVerifyView.vue'

const getPublicSettingsMock = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ register: vi.fn() }),
  useAppStore: () => ({
    cachedPublicSettings: { oidc_only_enabled: true },
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: getPublicSettingsMock,
  validatePromoCode: vi.fn(),
  validateInvitationCode: vi.fn(),
  forgotPassword: vi.fn(),
  resetPassword: vi.fn(),
  sendVerifyCode: vi.fn()
}))

vi.mock('@/components/layout', () => ({
  AuthLayout: { template: '<main><slot /><footer><slot name="footer" /></footer></main>' }
}))

vi.mock('@/components/auth/LinuxDoOAuthSection.vue', () => ({
  default: { template: '<div data-testid="linuxdo-oauth" />' }
}))

vi.mock('@/components/auth/OidcOAuthSection.vue', () => ({
  default: { template: '<div data-testid="oidc-oauth" />' }
}))

vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<span />' } }))
vi.mock('@/components/TurnstileWidget.vue', () => ({ default: { template: '<div />' } }))

const stubs = { RouterLink: { template: '<a><slot /></a>' } }

describe('OIDC-only local auth pages', () => {
  beforeEach(() => {
    sessionStorage.removeItem('register_data')
    getPublicSettingsMock.mockReset()
    getPublicSettingsMock.mockResolvedValue({
      oidc_only_enabled: true,
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'Tabro',
      linuxdo_oauth_enabled: true,
      oidc_oauth_enabled: true,
      oidc_oauth_provider_name: 'Corporate SSO'
    })
  })

  it.each([
    ['registration', RegisterView],
    ['forgot password', ForgotPasswordView],
    ['reset password', ResetPasswordView],
    ['email verification', EmailVerifyView]
  ])('hides the %s form and points to login', async (_name, component) => {
    const wrapper = mount(component, { global: { stubs } })
    await flushPromises()

    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.text()).toContain('auth.oidcOnlyTitle')
    expect(wrapper.text()).toContain('auth.backToLogin')
  })

  it('clears an unfinished local registration when email verification opens', async () => {
    sessionStorage.setItem('register_data', JSON.stringify({ email: 'user@example.com', password: 'secret' }))
    const wrapper = mount(EmailVerifyView, { global: { stubs } })
    await flushPromises()

    expect(wrapper.find('form').exists()).toBe(false)
    expect(sessionStorage.getItem('register_data')).toBeNull()
  })
})
