import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zh from './zh.json'
import en from './en.json'
import zhQTS from './zh.qts.json'
import enQTS from './en.qts.json'

export type Lang = 'zh' | 'en'

// The only thing tern keeps in localStorage.
export const LANG_KEY = 'tern.lang'

export function detectLang(): Lang {
  try {
    const saved = localStorage.getItem(LANG_KEY)
    if (saved === 'zh' || saved === 'en') return saved
  } catch {
    // storage blocked: fall through to the browser language
  }
  return (navigator.language || '').toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

export function setLang(lang: Lang) {
  try {
    localStorage.setItem(LANG_KEY, lang)
  } catch {
    // not remembered, but still switched
  }
  void i18n.changeLanguage(lang)
}

export function currentLang(): Lang {
  return i18n.language === 'zh' ? 'zh' : 'en'
}

i18n.on('languageChanged', (lng) => {
  document.documentElement.lang = lng
})

// i18next merges added bundles into the objects it was given: it gets
// copies, so the imported texts stay pristine for switching back.
const copy = <T,>(o: T): T => structuredClone(o)

void i18n.use(initReactI18next).init({
  resources: { zh: { translation: copy(zh) }, en: { translation: copy(en) } },
  lng: detectLang(),
  fallbackLng: 'en',
  initAsync: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
})

let platform = 'docker'

// applyPlatform switches to the wording of the platform tern runs on
// (status.platform): the QNAP package talks about NAS paths and QTS where
// the image talks about containers and compose files. It is called before
// any page renders, and the platform never changes while the page is open.
export function applyPlatform(p: string | undefined) {
  const next = p === 'qts' ? 'qts' : 'docker'
  if (next === platform) return
  platform = next
  // deep and overwrite: the overlays hold only the keys that differ
  i18n.addResourceBundle('zh', 'translation', copy(next === 'qts' ? zhQTS : zh), true, true)
  i18n.addResourceBundle('en', 'translation', copy(next === 'qts' ? enQTS : en), true, true)
}

export default i18n
