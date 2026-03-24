import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'

describe('usage service tier locale keys', () => {
  it('contains zh-CN labels for service tier tooltip', () => {
    expect(zhCN.usage.serviceTier).toBe('服务档位')
    expect(zhCN.usage.serviceTierPriority).toBe('Fast')
    expect(zhCN.usage.serviceTierFlex).toBe('Flex')
    expect(zhCN.usage.serviceTierStandard).toBe('Standard')
  })

  it('contains en labels for service tier tooltip', () => {
    expect(en.usage.serviceTier).toBe('Service tier')
    expect(en.usage.serviceTierPriority).toBe('Fast')
    expect(en.usage.serviceTierFlex).toBe('Flex')
    expect(en.usage.serviceTierStandard).toBe('Standard')
  })
})
