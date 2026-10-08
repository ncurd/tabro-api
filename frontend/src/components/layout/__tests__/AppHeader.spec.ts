import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'
import type { OIDCAccountIdentity, OIDCAccountSummary } from '@/api/oidcAccount'

import AppHeader from '../AppHeader.vue'

const logout = vi.fn()
const push = vi.fn()
const replay = vi.fn()
const getAccount = vi.fn()
const getIdentity = vi.fn()

const authStore = reactive({
  token: 'api-token-one',
  user: {
    id: 1,
    username: 'AdminUser',
    email: 'admin@example.com',
    role: 'admin',
    balance: 12.34
  },
  isAdmin: true,
  isSimpleMode: false,
  logout
})

const appStore = reactive({
  oidcBillingEnabled: false,
  contactInfo: '',
  docUrl: '',
  cachedPublicSettings: {
    custom_menu_items: []
  },
  toggleMobileSidebar: vi.fn()
})

const adminSettingsStore = {
  customMenuItems: []
}

const messages: Record<string, string> = {
  'nav.profile': 'Profile',
  'nav.apiKeys': 'API Keys',
  'nav.github': 'GitHub',
  'nav.logout': 'Logout',
  'common.balance': 'Balance',
  'oidcAccount.availableCredits': '可用积分',
  'oidcAccount.accountMenu': '账户菜单',
  'oidcAccount.topUpsAndPlans': '充值与套餐',
  'oidcAccount.ordersAndInvoices': '订单与发票',
  'oidcAccount.accountAndBills': '账户与账单',
  'oidcAccount.profile': '个人资料',
  'oidcAccount.securitySettings': '安全设置',
  'oidcAccount.apiAdministration': 'API 管理后台',
  'oidcAccount.unavailable': '暂时无法读取可用积分',
  'oidcAccount.unlinked': '当前登录尚未关联 Auth 计费账户',
  'oidcAccount.identityMismatch': '请在 Auth 使用当前 API 的同一账户登录，再刷新积分。',
  'onboarding.restartTour': '重新查看新手引导'
}

vi.mock('@/api/oidcAccount', () => ({
  getOIDCAccountIdentity: (...args: unknown[]) => getIdentity(...args),
  getOIDCAccountSummary: (...args: unknown[]) => getAccount(...args)
}))

const account = {
  issuer: 'https://auth.tabro.cn', subject: 'auth-user-one', tenant_id: 'tenant-one',
  status: 'available',
  available_credits: '5678.123456789',
  links: {
    credit_details: 'https://auth.tabro.cn/Identity/Account/Manage/CreditDetails',
    top_ups_and_plans: 'https://auth.tabro.cn/Identity/Account/Manage/BillingPayments?TenantId=tenant-one',
    orders_and_invoices: 'https://auth.tabro.cn/Identity/Account/Manage/BillingOrders?TenantId=tenant-one',
    account_and_bills: 'https://auth.tabro.cn/Identity/Account/Manage/Billing?TenantId=tenant-one',
    profile: 'https://auth.tabro.cn/Identity/Account/Manage',
    security_settings: 'https://auth.tabro.cn/Identity/Account/Manage/TwoFactorAuthentication'
  }
} as const

const identity: OIDCAccountIdentity = {
  status: 'linked', issuer: account.issuer, subject: account.subject, client_id: 'tabro-llm',
  summary_url: 'https://auth.tabro.cn/api/account/header-summary?client_id=tabro-llm',
  links: {
    credit_details: account.links.credit_details,
    profile: account.links.profile,
    security_settings: account.links.security_settings
  }
}

function mountHeader() {
  return mount(AppHeader, {
    global: {
      stubs: {
        AnnouncementBell: true, LocaleSwitcher: true, SubscriptionProgressMini: true,
        Icon: true, RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }, transition: true
      },
      mocks: { $t: (key: string) => messages[key] ?? key }
    }
  })
}

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
  useOnboardingStore: () => ({
    replay
  })
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => adminSettingsStore
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push
  }),
  useRoute: () => ({
    name: 'Home',
    meta: {},
    params: {}
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key
    })
  }
})

describe('AppHeader', () => {
  afterEach(() => vi.useRealTimers())
  beforeEach(() => {
    appStore.oidcBillingEnabled = false
    authStore.user.id = 1
    authStore.token = 'api-token-one'
    authStore.isAdmin = true
    logout.mockReset()
    push.mockReset()
    replay.mockReset()
    getAccount.mockReset()
    getIdentity.mockReset()
    getAccount.mockResolvedValue(account)
    getIdentity.mockResolvedValue(identity)
  })

  it('does not render GitHub or restart-tour actions in the user dropdown', async () => {
    const wrapper = mount(AppHeader, {
      global: {
        stubs: {
          AnnouncementBell: true,
          LocaleSwitcher: true,
          SubscriptionProgressMini: true,
          Icon: true,
          RouterLink: {
            props: ['to'],
            template: '<a><slot /></a>'
          },
          transition: false
        },
        mocks: {
          $t: (key: string) => messages[key] ?? key
        }
      }
    })

    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await nextTick()

    expect(wrapper.text()).not.toContain('GitHub')
    expect(wrapper.text()).not.toContain('重新查看新手引导')
    expect(getAccount).not.toHaveBeenCalled()
    expect(getIdentity).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('hides local balance and subscription progress when Auth bills usage', async () => {
    appStore.oidcBillingEnabled = true
    const wrapper = mount(AppHeader, {
      global: {
        stubs: {
          AnnouncementBell: true, LocaleSwitcher: true, SubscriptionProgressMini: true,
          Icon: true, RouterLink: { template: '<a><slot /></a>' }, transition: false
        },
        mocks: { $t: (key: string) => messages[key] ?? key }
      }
    })
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    expect(wrapper.text()).not.toContain('12.34')
    expect(wrapper.text()).not.toContain('Balance')
    expect(wrapper.find('subscription-progress-mini-stub').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows Auth available credits in the header and links the matching Auth account menu', async () => {
    appStore.oidcBillingEnabled = true
    const wrapper = mountHeader()
    await flushPromises()
    const credits = wrapper.get('[data-testid="auth-available-credits"]')
    expect(credits.text()).toContain('5678.12 ✦')
    expect(credits.attributes('title')).toContain('5678.123456789 ✦')
    expect(credits.attributes('href')).toBe(account.links.credit_details)
    expect(credits.classes()).not.toContain('hidden')
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await flushPromises()

    const links = wrapper.findAll('#header-account-menu a[target="_blank"]')
    expect(links.map(link => link.text())).toEqual(['充值与套餐', '订单与发票', '账户与账单', '个人资料', '安全设置'])
    expect(links.map(link => link.attributes('href'))).toEqual([
      account.links.top_ups_and_plans, account.links.orders_and_invoices, account.links.account_and_bills,
      account.links.profile, account.links.security_settings
    ])
    for (const link of links) expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(wrapper.get('a[href="/keys"]').text()).toBe('API Keys')
    expect(wrapper.get('a[href="/admin/dashboard"]').text()).toBe('API 管理后台')
    expect(wrapper.text()).not.toContain('12.34')
    expect(wrapper.find('subscription-progress-mini-stub').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not add billing or Auth administrator menus when the response only permits account settings', async () => {
    appStore.oidcBillingEnabled = true
    authStore.isAdmin = false
    getIdentity.mockResolvedValue({
      ...identity, status: 'unlinked', summary_url: null,
      links: { profile: account.links.profile, security_settings: account.links.security_settings }
    })
    const wrapper = mountHeader()
    await flushPromises()
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('—')
    expect(wrapper.text()).toContain('当前登录尚未关联 Auth 计费账户')
    expect(wrapper.text()).not.toContain('充值与套餐')
    expect(wrapper.text()).not.toContain('订单与发票')
    expect(wrapper.text()).not.toContain('账户与账单')
    expect(wrapper.find('a[href="/admin/dashboard"]').exists()).toBe(false)
    expect(wrapper.findAll('a[href*="Admin"]').length).toBe(0)
    expect(getAccount).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows an unknown amount on a failed refresh and never substitutes the local wallet', async () => {
    appStore.oidcBillingEnabled = true
    const wrapper = mountHeader()
    await flushPromises()
    getAccount.mockRejectedValue(new Error('Auth unavailable'))
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('—')
    expect(wrapper.text()).not.toContain('5678.123456789')
    expect(wrapper.text()).not.toContain('12.34')
    expect(wrapper.text()).not.toContain('0 ✦')
    expect(wrapper.get('[data-testid="auth-available-credits"]').attributes('title')).toBe('暂时无法读取可用积分')
    wrapper.unmount()
  })

  it('does not apply a previous login response after the current user changes', async () => {
    appStore.oidcBillingEnabled = true
    let resolveOldAccount!: (value: OIDCAccountSummary) => void
    getAccount.mockImplementationOnce(() => new Promise(resolve => { resolveOldAccount = resolve }))
    getAccount.mockResolvedValueOnce({ ...account, available_credits: '42.5' })
    const wrapper = mountHeader()
    await flushPromises()
    authStore.user.id = 2
    await flushPromises()
    resolveOldAccount(account)
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('42.50 ✦')
    expect(wrapper.text()).not.toContain('5678.123456789')
    wrapper.unmount()
  })

  it('removes Auth data when billing mode is switched off and restores the local wallet', async () => {
    appStore.oidcBillingEnabled = true
    const wrapper = mountHeader()
    await flushPromises()
    appStore.oidcBillingEnabled = false
    await nextTick()
    expect(wrapper.find('[data-testid="auth-available-credits"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('12.34')
    expect(wrapper.find('subscription-progress-mini-stub').exists()).toBe(true)
    wrapper.unmount()
  })

  it('invalidates a pending Auth summary when the same API user obtains a new login token', async () => {
    appStore.oidcBillingEnabled = true
    let resolveOldAccount!: (value: OIDCAccountSummary) => void
    getAccount.mockImplementationOnce(() => new Promise(resolve => { resolveOldAccount = resolve }))
    getAccount.mockResolvedValueOnce({ ...account, available_credits: '88' })
    const wrapper = mountHeader()
    await flushPromises()
    const oldSignal = getAccount.mock.calls[0][1] as AbortSignal
    authStore.token = 'new-api-login-token'
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOldAccount(account)
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('88.00 ✦')
    expect(wrapper.text()).not.toContain('5678.123456789')
    wrapper.unmount()
  })

  it('cancels an in-flight Auth summary on unmount and does not issue duplicate requests on focus', async () => {
    vi.useFakeTimers()
    appStore.oidcBillingEnabled = true
    getAccount.mockImplementationOnce(() => new Promise(() => {}))
    const wrapper = mountHeader()
    await flushPromises()
    window.dispatchEvent(new Event('focus'))
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    expect(getAccount).toHaveBeenCalledTimes(1)
    const signal = getAccount.mock.calls[0][1] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('aborts a slow Auth summary after ten seconds and clears the unavailable balance', async () => {
    vi.useFakeTimers()
    appStore.oidcBillingEnabled = true
    getAccount.mockImplementationOnce((_url, signal: AbortSignal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(new Error('Request aborted')), { once: true })
    }))
    const wrapper = mountHeader()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('—')
    expect(wrapper.get('[data-testid="auth-available-credits"]').attributes('title')).toBe('暂时无法读取可用积分')
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not render unsafe links and closes the account dropdown with Escape', async () => {
    appStore.oidcBillingEnabled = true
    getIdentity.mockResolvedValue({
      ...identity,
      links: { credit_details: 'javascript:alert(1)', profile: 'http://auth.tabro.cn/profile' }
    })
    getAccount.mockResolvedValue({ ...account, tenant_id: null })
    const wrapper = mountHeader()
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').element.tagName).toBe('DIV')
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('a[target="_blank"]').length).toBe(0)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(wrapper.find('#header-account-menu').exists()).toBe(false)
    expect(wrapper.get('button[aria-label="User Menu"]').attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })

  it.each([
    { ...account, subject: 'someone-else', available_credits: '999' },
    { ...account, issuer: 'https://other-auth.example', available_credits: '999' }
  ])('does not display another Auth session or issuer balance and removes tenant menus', async summary => {
    appStore.oidcBillingEnabled = true
    getAccount.mockResolvedValue(summary)
    const wrapper = mountHeader()
    await flushPromises()
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('—')
    expect(wrapper.text()).not.toContain('999')
    expect(wrapper.text()).not.toContain('充值与套餐')
    expect(wrapper.text()).not.toContain('订单与发票')
    expect(wrapper.text()).not.toContain('账户与账单')
    expect(wrapper.text()).toContain('个人资料')
    expect(wrapper.text()).toContain('请在 Auth 使用当前 API 的同一账户登录，再刷新积分。')
    wrapper.unmount()
  })

  it('does not request a cross-origin summary or render cross-origin account links', async () => {
    appStore.oidcBillingEnabled = true
    getIdentity.mockResolvedValue({
      ...identity,
      summary_url: 'https://attacker.example/api/account/header-summary?client_id=tabro-llm',
      links: { profile: 'https://attacker.example/profile' }
    })
    const wrapper = mountHeader()
    await flushPromises()
    await wrapper.get('button[aria-label="User Menu"]').trigger('click')
    await flushPromises()
    expect(getAccount).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('—')
    expect(wrapper.findAll('a[target="_blank"]').length).toBe(0)
    wrapper.unmount()
  })

  it('shows an Auth zero as a known balance and refreshes when returning from Auth', async () => {
    appStore.oidcBillingEnabled = true
    getAccount.mockResolvedValueOnce({ ...account, available_credits: '0.0000' })
    const wrapper = mountHeader()
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('0.00 ✦')
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(wrapper.get('[data-testid="auth-available-credits"]').text()).toContain('5678.12 ✦')
    const calls = getAccount.mock.calls.length
    wrapper.unmount()
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(getAccount).toHaveBeenCalledTimes(calls)
  })

})
