import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const { useOnboardingTour, setReplayCallback } = vi.hoisted(() => ({
  useOnboardingTour: vi.fn(() => ({
    replayTour: vi.fn()
  })),
  setReplayCallback: vi.fn()
}))

vi.mock('@/composables/useOnboardingTour', () => ({
  useOnboardingTour
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    sidebarCollapsed: false
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: {
      role: 'admin'
    }
  })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    setReplayCallback
  })
}))

import AppLayout from '../AppLayout.vue'

describe('AppLayout', () => {
  beforeEach(() => {
    useOnboardingTour.mockClear()
    setReplayCallback.mockClear()
  })

  it('disables onboarding auto-start when initializing the app layout', () => {
    mount(AppLayout, {
      global: {
        stubs: {
          AppSidebar: { template: '<div />' },
          AppHeader: { template: '<div />' }
        }
      }
    })

    expect(useOnboardingTour).toHaveBeenCalledWith({
      storageKey: 'admin_guide',
      autoStart: false
    })
  })
})
