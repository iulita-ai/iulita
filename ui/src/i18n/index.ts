import { createI18n } from 'vue-i18n'
import en from './locales/en.json'
import ru from './locales/ru.json'
import zh from './locales/zh.json'
import es from './locales/es.json'
import fr from './locales/fr.json'
import he from './locales/he.json'

const savedLocale = localStorage.getItem('iulita.locale') || navigator.language?.split('-')[0] || 'en'
const supportedLocales = ['en', 'ru', 'zh', 'es', 'fr', 'he']
const initialLocale = supportedLocales.includes(savedLocale) ? savedLocale : 'en'

const i18n = createI18n({
  legacy: false,
  locale: initialLocale,
  fallbackLocale: 'en',
  messages: { en, ru, zh, es, fr, he },
})

export default i18n
export { supportedLocales }
