import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const STORAGE_KEY = 'iulita.locale'
const RTL_LOCALES = ['he']

const currentLocale = ref(localStorage.getItem(STORAGE_KEY) || 'en')

export function useLocale() {
  const { locale } = useI18n({ useScope: 'global' })

  // Sync vue-i18n locale on first call.
  locale.value = currentLocale.value

  const isRTL = computed(() => RTL_LOCALES.includes(currentLocale.value))
  const direction = computed<'rtl' | 'ltr'>(() => isRTL.value ? 'rtl' : 'ltr')

  async function setLocale(tag: string) {
    currentLocale.value = tag
    locale.value = tag
    localStorage.setItem(STORAGE_KEY, tag)

    // Set lang attribute but NOT dir on <html> — RTL is applied per-component
    // to avoid flipping the entire shell layout (sidebar, sider position).
    document.documentElement.lang = tag

    // Persist to backend (best-effort).
    try {
      const token = localStorage.getItem('token')
      if (token) {
        await fetch('/api/auth/locale', {
          method: 'PATCH',
          headers: {
            'Content-Type': 'application/json',
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ locale: tag }),
        })
      }
    } catch {
      // Ignore network errors — locale is persisted locally.
    }
  }

  return {
    locale: currentLocale,
    isRTL,
    direction,
    setLocale,
  }
}

// Reset locale state on logout.
export function resetLocale() {
  localStorage.removeItem(STORAGE_KEY)
  currentLocale.value = 'en'
  document.documentElement.lang = 'en'
}
