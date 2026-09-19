import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

import {
  buildModelMappingObject,
  getModelsByPlatform,
  getPresetMappingsByPlatform
} from '../useModelWhitelist'

describe('useModelWhitelist', () => {
  it('does not suggest retired OpenAI API models', () => {
    const models = getModelsByPlatform('openai', 'apikey')
    const presetTargets = getPresetMappingsByPlatform('openai', 'apikey').map(({ to }) => to)
    for (const retired of [
      'gpt-4-turbo-preview', 'gpt-4.5-preview', 'o1-preview', 'o1-mini',
      'gpt-5-chat-latest', 'gpt-5-codex', 'gpt-5.1-chat-latest',
      'gpt-5.1-codex', 'gpt-5.1-codex-max', 'gpt-5.1-codex-mini',
      'gpt-5.2-codex', 'gpt-5.2-chat-latest', 'chatgpt-4o-latest',
      'gpt-4o-audio-preview', 'gpt-4o-realtime-preview'
    ]) {
      expect(models).not.toContain(retired)
      expect(presetTargets).not.toContain(retired)
    }
  })

  it('filters ChatGPT-only retirements for OAuth while keeping API key availability', () => {
    const oauthModels = getModelsByPlatform('openai', 'oauth')
    const apiKeyModels = getModelsByPlatform('openai', 'apikey')
    const oauthTargets = getPresetMappingsByPlatform('openai', 'oauth').map(({ to }) => to)
    for (const model of [
      'gpt-5', 'gpt-5-2025-08-07', 'gpt-5-chat', 'gpt-5-pro', 'gpt-5-pro-2025-10-06',
      'gpt-5-mini', 'gpt-5-mini-2025-08-07', 'gpt-5-nano', 'gpt-5-nano-2025-08-07',
      'gpt-5.1', 'gpt-5.1-2025-11-13', 'gpt-5.2-pro', 'gpt-5.2-pro-2025-12-11',
      'gpt-5.2', 'gpt-5.2-2025-12-11', 'gpt-5.3-codex', 'gpt-5.3-codex-spark',
      'gpt-5.4', 'gpt-5.4-mini', 'gpt-5.4-pro', 'gpt-5.4-2026-03-05'
    ]) {
      expect(oauthModels).not.toContain(model)
      expect(oauthTargets).not.toContain(model)
      expect(apiKeyModels).toContain(model)
    }
    expect(oauthModels).toEqual(expect.arrayContaining([
      'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-image-2.5-flare'
    ]))
    expect(buildModelMappingObject('whitelist', ['gpt-5.4'], [])).toEqual({
      'gpt-5.4': 'gpt-5.4'
    })
  })

  it('removes retired Mistral and Aliyun hosted choices while preserving custom model IDs', () => {
    for (const retired of ['open-mistral-7b', 'open-mixtral-8x7b', 'open-mixtral-8x22b']) {
      expect(getModelsByPlatform('mistral')).not.toContain(retired)
    }
    const qwenModels = getModelsByPlatform('qwen')
    expect(qwenModels).toEqual(expect.arrayContaining(['qwen-turbo', 'qwen-max', 'qwen3-235b-a22b']))
    for (const retired of ['qwen-max-longcontext', 'qwen2-72b-instruct', 'qwen2.5-72b-instruct', 'qwen2.5-coder-32b-instruct', 'qwq-32b', 'qwq-32b-preview']) {
      expect(qwenModels).not.toContain(retired)
    }
    expect(buildModelMappingObject('whitelist', ['qwen2.5-72b-instruct'], [])).toEqual({
      'qwen2.5-72b-instruct': 'qwen2.5-72b-instruct'
    })
  })

  it('removes confirmed direct Claude, Gemini and Perplexity retirements', () => {
    const claudeModels = getModelsByPlatform('anthropic')
    const claudeTargets = getPresetMappingsByPlatform('anthropic').map(({ to }) => to)
    for (const retired of [
      'claude-opus-4-1-20250805', 'claude-sonnet-4-20250514', 'claude-opus-4-20250514',
      'claude-3-haiku-20240307', 'claude-3-5-haiku-20241022', 'claude-3-7-sonnet-20250219',
      'claude-3-opus-20240229', 'claude-3-5-sonnet-20240620', 'claude-3-5-sonnet-20241022',
      'claude-2.0', 'claude-2.1', 'claude-3-sonnet-20240229', 'claude-instant-1.2'
    ]) {
      expect(claudeModels).not.toContain(retired)
      expect(claudeTargets).not.toContain(retired)
      if (retired === 'claude-3-5-haiku-20241022') {
        expect(getModelsByPlatform('anthropic', 'bedrock')).not.toContain(retired)
      } else {
        expect(getModelsByPlatform('anthropic', 'bedrock')).toContain(retired)
      }
    }
    expect(claudeModels).toContain('claude-sonnet-4-5-20250929')
    expect(getModelsByPlatform('gemini')).not.toContain('gemini-2.0-flash')
    expect(getModelsByPlatform('gemini')).toContain('gemini-3-pro-preview')
    expect(getPresetMappingsByPlatform('gemini').map(({ to }) => to)).not.toContain('gemini-2.0-flash')
    expect(getModelsByPlatform('perplexity')).toEqual(['sonar', 'sonar-pro'])
    expect(getModelsByPlatform('hunyuan')).toEqual(['hunyuan-vision'])
    expect(getModelsByPlatform('baidu')).toEqual(['ernie-4.0-8k-latest', 'ernie-3.5-128k'])
    expect(getPresetMappingsByPlatform('anthropic', 'bedrock')).toEqual(getPresetMappingsByPlatform('bedrock'))
  })

  it('claude 和 antigravity 模型列表包含 Claude Fable 5', () => {
    expect(getModelsByPlatform('anthropic')).toContain('claude-fable-5')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5')
  })

  it('直连模型列表包含 GPT-6 Astra、Claude Fable 5.1 和 Claude Opus 5', () => {
    expect(getModelsByPlatform('openai')).toContain('gpt-6-astra')
    expect(getModelsByPlatform('anthropic')).toContain('claude-fable-5-1')
    expect(getModelsByPlatform('anthropic')).toContain('claude-opus-5')

    expect(getModelsByPlatform('antigravity')).not.toContain('claude-fable-5-1')
    expect(getModelsByPlatform('antigravity')).not.toContain('claude-opus-5')
  })

  it('直连和 Bedrock 预设包含新模型，Antigravity 预设不包含新 Claude 模型', () => {
    expect(getPresetMappingsByPlatform('openai')).toContainEqual(expect.objectContaining({
      label: 'GPT-6 Astra',
      from: 'gpt-6-astra',
      to: 'gpt-6-astra'
    }))
    expect(getPresetMappingsByPlatform('anthropic')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'claude-fable-5-1', to: 'claude-fable-5-1' }),
      expect.objectContaining({ from: 'claude-opus-5', to: 'claude-opus-5' })
    ]))
    expect(getPresetMappingsByPlatform('bedrock')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'claude-fable-5-1', to: 'anthropic.claude-fable-5-1' }),
      expect.objectContaining({ from: 'claude-opus-5', to: 'anthropic.claude-opus-5' })
    ]))

    const antigravityModels = getPresetMappingsByPlatform('antigravity').map(({ from }) => from)
    expect(antigravityModels).not.toContain('claude-fable-5-1')
    expect(antigravityModels).not.toContain('claude-opus-5')
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

  it('openai 模型列表包含 GPT-5.6 Sol/Terra/Luna', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-5.6')
    expect(models).toContain('gpt-5.6-sol')
    expect(models).toContain('gpt-5.6-terra')
    expect(models).toContain('gpt-5.6-luna')
  })

  it('openai 模型列表包含最新图片和实时模型', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-image-1-mini')
    expect(models).toContain('gpt-image-1.5')
    expect(models).toContain('gpt-image-1.5-2025-12-16')
    expect(models).toContain('gpt-image-2')
    expect(models).toContain('gpt-image-2-2026-04-21')
    expect(models).toContain('gpt-image-2.5-sunburst')
    expect(models).toContain('gpt-image-2.5-sunburst-2026-09-08')
    expect(models).toContain('gpt-image-2.5-flare')
    expect(models).toContain('gpt-image-2.5-flare-2026-09-08')
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
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
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

  it('whitelist 模式会保留 GPT-5.6 三档模型的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna'], [])

    expect(mapping).toEqual({
      'gpt-5.6-sol': 'gpt-5.6-sol',
      'gpt-5.6-terra': 'gpt-5.6-terra',
      'gpt-5.6-luna': 'gpt-5.6-luna'
    })
  })

  it('whitelist 模式会保留新增直连模型的精确映射', () => {
    const models = ['gpt-6-astra', 'claude-fable-5-1', 'claude-opus-5']

    expect(buildModelMappingObject('whitelist', models, [])).toEqual({
      'gpt-6-astra': 'gpt-6-astra',
      'claude-fable-5-1': 'claude-fable-5-1',
      'claude-opus-5': 'claude-opus-5'
    })
  })
})
