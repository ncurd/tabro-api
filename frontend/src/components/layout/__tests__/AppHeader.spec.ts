import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import AppHeader from '../AppHeader.vue'

const logout = vi.fn()
const push = vi.fn()
const replay = vi.fn()

const authStore = {
  user: {
    username: 'AdminUser',
    email: 'admin@example.com',
    role: 'admin',
    balance: 12.34
  },
  isAdmin: true,
  isSimpleMode: false,
  logout
}

const appStore = {
  contactInfo: '',
  docUrl: '',
  cachedPublicSettings: {
    custom_menu_items: []
  },
  toggleMobileSidebar: vi.fn()
}

const adminSettingsStore = {
  customMenuItems: []
}

const messages: Record<string, string> = {
  'nav.profile': 'Profile',
  'nav.apiKeys': 'API Keys',
  'nav.github': 'GitHub',
  'nav.logout': 'Logout',
  'common.balance': 'Balance',
  'onboarding.restartTour': '重新查看新手引导'
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
  beforeEach(() => {
    logout.mockReset()
    push.mockReset()
    replay.mockReset()
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
  })
})
