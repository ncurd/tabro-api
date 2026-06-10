import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

import { buildModelMappingObject, getModelsByPlatform } from '../useModelWhitelist'

describe('useModelWhitelist', () => {
  it('claude 和 antigravity 模型列表包含 Claude Fable 5', () => {
    expect(getModelsByPlatform('anthropic')).toContain('claude-fable-5')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5')
  })

  it('openai 模型列表包含 GPT-5.4 官方快照', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-5.4')
    expect(models).toContain('gpt-5.4-mini')
    expect(models).toContain('gpt-5.4-nano')
    expect(models).toContain('gpt-5.4-2026-03-05')
  })

  it('openai 模型列表包含 GPT-5.5 官方快照', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-5.5')
    expect(models).toContain('gpt-5.5-2026-04-23')
    expect(models).toContain('gpt-5.5-pro')
    expect(models).toContain('gpt-5.5-pro-2026-04-23')
  })

  it('openai 模型列表包含最新图片和实时模型', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-image-1-mini')
    expect(models).toContain('gpt-image-1.5')
    expect(models).toContain('gpt-image-1.5-2025-12-16')
    expect(models).toContain('gpt-image-2')
    expect(models).toContain('gpt-image-2-2026-04-21')
    expect(models).toContain('gpt-realtime-1.5')
    expect(models).toContain('gpt-realtime-2')
    expect(models).toContain('gpt-realtime-mini')
    expect(models).toContain('gpt-realtime-translate')
  })

  it('antigravity 模型列表包含图片模型兼容项', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models).toContain('gemini-3-pro-image')
  })

  it('gemini 模型列表包含原生生图模型', () => {
    const models = getModelsByPlatform('gemini')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.0-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
  })

  it('antigravity 模型列表会把新的 Gemini 图片模型排在前面', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash-lite'))
  })

  it('whitelist 模式会忽略通配符条目', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image'
    })
  })

  it('whitelist 模式会保留 GPT-5.4 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-2026-03-05'], [])

    expect(mapping).toEqual({
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05'
    })
  })

  it('whitelist keeps GPT-5.4 mini and nano exact mappings', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-mini', 'gpt-5.4-nano'], [])

    expect(mapping).toEqual({
      'gpt-5.4-mini': 'gpt-5.4-mini',
      'gpt-5.4-nano': 'gpt-5.4-nano'
    })
  })

  it('whitelist 模式会保留 GPT-5.5 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.5-2026-04-23'], [])

    expect(mapping).toEqual({
      'gpt-5.5-2026-04-23': 'gpt-5.5-2026-04-23'
    })
  })
})
