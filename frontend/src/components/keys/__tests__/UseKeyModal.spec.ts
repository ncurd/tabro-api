import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
}))

import UseKeyModal from '../UseKeyModal.vue'

describe('UseKeyModal', () => {
  it('offers each client for a public key without an unassigned-group warning', async () => {
    const wrapper = mount(UseKeyModal, {
      props: { show: true, apiKey: 'sk-test', baseUrl: 'https://example.com/v1', platform: null, multiPlatform: true },
      global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Icon: { template: '<span />' } } }
    })
    expect(wrapper.text()).not.toContain('keys.useKeyModal.noGroupTitle')
    expect(wrapper.text()).toContain('keys.useKeyModal.cliTabs.codexCli')
    expect(wrapper.text()).toContain('keys.useKeyModal.cliTabs.claudeCode')
    expect(wrapper.text()).toContain('keys.useKeyModal.cliTabs.geminiCli')
    const gemini = wrapper.findAll('button').find(button => button.text().includes('keys.useKeyModal.cliTabs.geminiCli'))!
    await gemini.trigger('click')
    expect(wrapper.text()).toContain('GEMINI_API_KEY')
    const claude = wrapper.findAll('button').find(button => button.text().includes('keys.useKeyModal.cliTabs.claudeCode'))!
    await claude.trigger('click')
    expect(wrapper.text()).toContain('ANTHROPIC_AUTH_TOKEN')
    const opencode = wrapper.findAll('button').find(button => button.text().includes('keys.useKeyModal.cliTabs.opencode'))!
    await opencode.trigger('click')
    expect(wrapper.findAll('pre code')).toHaveLength(3)
  })
  it('renders GPT-6.1 Sol and GPT-6 Astra/Sol/Luna with the expected limits and variants in OpenCode config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    expect(wrapper.text()).toContain('model = "gpt-5.6-terra"')
    expect(wrapper.text()).toContain('review_model = "gpt-5.6-terra"')
    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )

    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.exists()).toBe(true)
    const config = JSON.parse(codeBlock.text())
    expect(config.provider.openai.models['gpt-6.1-sol']).toEqual({
      name: 'GPT-6.1 Sol',
      limit: {
        context: 1050000,
        output: 128000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {},
        max: {}
      }
    })
    expect(config.provider.openai.models['gpt-6-astra']).toEqual({
      name: 'GPT-6 Astra',
      limit: {
        context: 1050000,
        output: 128000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {},
        max: {}
      }
    })
    for (const model of ['gpt-6-sol', 'gpt-6-luna', 'gpt-5.6', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']) {
      expect(config.provider.openai.models[model].limit).toEqual({ context: 1050000, output: 128000 })
      expect(config.provider.openai.models[model].options).toEqual({ store: false })
      expect(config.provider.openai.models[model].variants).toEqual({
        none: {},
        low: {},
        medium: {},
        high: {},
        xhigh: {},
        max: {}
      })
    }
    for (const retired of ['gpt-5-codex', 'gpt-5.1-codex', 'gpt-5.1-codex-max', 'gpt-5.1-codex-mini', 'codex-mini-latest']) {
      expect(config.provider.openai.models).not.toHaveProperty(retired)
    }
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Mini"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Nano"')
    expect(codeBlock.text()).toContain('"name": "GPT-6 Sol"')
    expect(codeBlock.text()).toContain('"name": "GPT-6 Luna"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Sol"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Terra"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Luna"')
  })
})
