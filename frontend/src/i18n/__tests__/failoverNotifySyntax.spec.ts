import { describe, expect, it } from 'vitest'

import de from '../locales/de'
import en from '../locales/en'
import es from '../locales/es'
import fr from '../locales/fr'
import hi from '../locales/hi'
import itLocale from '../locales/it'
import ptBR from '../locales/pt-BR'
import vi from '../locales/vi'
import zh from '../locales/zh'
import zhCN from '../locales/zh-CN'
import zhTW from '../locales/zh-TW'

const localeMessages = {
  de,
  en,
  es,
  fr,
  hi,
  it: itLocale,
  'pt-BR': ptBR,
  vi,
  zh,
  'zh-CN': zhCN,
  'zh-TW': zhTW
}

describe('failover notification i18n messages', () => {
  for (const [locale, messages] of Object.entries(localeMessages)) {
    it(`${locale} escapes @ in the email placeholder for Vue I18n`, () => {
      const placeholder = (messages as any).admin.settings.failoverNotify.emailPlaceholder

      expect(placeholder).toBe("admin{'@'}example.com")
      expect(placeholder).not.toContain('admin@example.com')
    })
  }
})
