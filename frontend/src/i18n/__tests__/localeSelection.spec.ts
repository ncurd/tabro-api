import { beforeEach, describe, expect, it, vi } from 'vitest'

describe('i18n locale selection', () => {
  beforeEach(() => {
    vi.resetModules()
    localStorage.clear()
    document.documentElement.removeAttribute('lang')
  })

  it('normalizes legacy zh locale to zh-CN', async () => {
    localStorage.setItem('sub2api_locale', 'zh')

    const { getLocale, initI18n } = await import('../index')

    await initI18n()

    expect(getLocale()).toBe('zh-CN')
    expect(localStorage.getItem('sub2api_locale')).toBe('zh-CN')
    expect(document.documentElement.lang).toBe('zh-CN')
  })
})
