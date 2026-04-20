import { createI18n } from 'vue-i18n'

export type LocaleCode = 'de' | 'en' | 'es' | 'fr' | 'hi' | 'it' | 'pt-BR' | 'vi' | 'zh-CN' | 'zh-TW'

type LocaleMessages = Record<string, any>

const LOCALE_KEY = 'sub2api_locale'
const DEFAULT_LOCALE: LocaleCode = 'en'
const LEGACY_LOCALE_MAP: Record<string, LocaleCode> = {
  zh: 'zh-CN'
}

const localeLoaders: Record<LocaleCode, () => Promise<{ default: LocaleMessages }>> = {
  de: () => import('./locales/de'),
  en: () => import('./locales/en'),
  es: () => import('./locales/es'),
  fr: () => import('./locales/fr'),
  hi: () => import('./locales/hi'),
  it: () => import('./locales/it'),
  'pt-BR': () => import('./locales/pt-BR'),
  vi: () => import('./locales/vi'),
  'zh-CN': () => import('./locales/zh-CN'),
  'zh-TW': () => import('./locales/zh-TW')
}

function isLocaleCode(value: string): value is LocaleCode {
  return value in localeLoaders
}

function normalizeLocale(value: string | null | undefined): LocaleCode | null {
  if (!value) {
    return null
  }

  if (isLocaleCode(value)) {
    return value
  }

  const legacyLocale = LEGACY_LOCALE_MAP[value]
  if (legacyLocale) {
    return legacyLocale
  }

  const lowerValue = value.toLowerCase()

  if (lowerValue === 'pt-br') {
    return 'pt-BR'
  }

  const exactMatch = (Object.keys(localeLoaders) as LocaleCode[]).find(
    (locale) => locale.toLowerCase() === lowerValue
  )
  if (exactMatch) {
    return exactMatch
  }

  const primaryMatch = (Object.keys(localeLoaders) as LocaleCode[]).find(
    (locale) => locale.split('-')[0].toLowerCase() === lowerValue.split('-')[0]
  )
  return primaryMatch ?? null
}

function getDefaultLocale(): LocaleCode {
  const saved = normalizeLocale(localStorage.getItem(LOCALE_KEY))
  if (saved) {
    if (saved !== localStorage.getItem(LOCALE_KEY)) {
      localStorage.setItem(LOCALE_KEY, saved)
    }
    return saved
  }

  const browserLocale = normalizeLocale(navigator.language)
  if (browserLocale) {
    return browserLocale
  }

  return DEFAULT_LOCALE
}

export const i18n = createI18n({
  legacy: false,
  locale: getDefaultLocale(),
  fallbackLocale: DEFAULT_LOCALE,
  messages: {},
  // 禁用 HTML 消息警告 - 引导步骤使用富文本内容（driver.js 支持 HTML）
  // 这些内容是内部定义的，不存在 XSS 风险
  warnHtmlMessage: false
})

const loadedLocales = new Set<LocaleCode>()

export async function loadLocaleMessages(locale: LocaleCode): Promise<void> {
  if (loadedLocales.has(locale)) {
    return
  }

  const loader = localeLoaders[locale]
  const module = await loader()
  i18n.global.setLocaleMessage(locale, module.default)
  loadedLocales.add(locale)
}

export async function initI18n(): Promise<void> {
  const current = getLocale()
  await loadLocaleMessages(current)
  document.documentElement.setAttribute('lang', current)
}

export async function setLocale(locale: string): Promise<void> {
  const normalizedLocale = normalizeLocale(locale)
  if (!normalizedLocale) {
    return
  }

  await loadLocaleMessages(normalizedLocale)
  i18n.global.locale.value = normalizedLocale
  localStorage.setItem(LOCALE_KEY, normalizedLocale)
  document.documentElement.setAttribute('lang', normalizedLocale)

  // 同步更新浏览器页签标题，使其跟随语言切换
  const { resolveDocumentTitle } = await import('@/router/title')
  const { default: router } = await import('@/router')
  const { useAppStore } = await import('@/stores/app')
  const route = router.currentRoute.value
  const appStore = useAppStore()
  document.title = resolveDocumentTitle(route.meta.title, appStore.siteName, route.meta.titleKey as string)
}

export function getLocale(): LocaleCode {
  const current = i18n.global.locale.value
  return normalizeLocale(current) ?? DEFAULT_LOCALE
}

export const availableLocales = [
  { code: 'de', name: 'Deutsch', flag: '🇩🇪' },
  { code: 'en', name: 'English', flag: '🇺🇸' },
  { code: 'es', name: 'Español', flag: '🇪🇸' },
  { code: 'fr', name: 'Français', flag: '🇫🇷' },
  { code: 'hi', name: 'हिन्दी', flag: '🇮🇳' },
  { code: 'it', name: 'Italiano', flag: '🇮🇹' },
  { code: 'pt-BR', name: 'Português (Brasil)', flag: '🇧🇷' },
  { code: 'vi', name: 'Tiếng Việt', flag: '🇻🇳' },
  { code: 'zh-CN', name: '简体中文', flag: '🇨🇳' },
  { code: 'zh-TW', name: '繁體中文', flag: '🇹🇼' }
] as const

export default i18n
