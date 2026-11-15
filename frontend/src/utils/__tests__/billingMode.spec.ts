import { describe, expect, it } from 'vitest'
import { BILLING_MODE_DURATION, getBillingModeBadgeClass, getBillingModeLabel } from '../billingMode'
import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhTW from '@/i18n/locales/zh-TW'

describe('duration billing labels', () => {
  it('labels duration independently from tokens and preserves old central and default token labels', () => {
    const t = (key: string) => key
    expect(getBillingModeLabel(BILLING_MODE_DURATION, t)).toBe('admin.usage.billingModeDuration')
    expect(getBillingModeLabel('central', t)).toBe('admin.usage.billingModeCentral')
    expect(getBillingModeLabel(undefined, t)).toBe('admin.usage.billingModeToken')
    expect(getBillingModeBadgeClass('duration')).not.toBe(getBillingModeBadgeClass('token'))
  })

  it.each([['en', en], ['zh-CN', zhCN], ['zh-TW', zhTW]] as const)('includes editor and usage duration labels in %s', (_locale, messages) => {
    expect(messages.modelManagement.modes.duration).toBeTruthy()
    expect(messages.admin.usage.billingModeDuration).toBeTruthy()
    for (const field of ['input_audio_price_per_second', 'output_audio_price_per_second', 'output_video_price_per_second'] as const) {
      expect(messages.modelManagement[field]).toBeTruthy()
    }
  })
})
