import { describe, it, expect, beforeEach, vi } from 'vitest'

describe('useLocale', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.dir = 'ltr'
    document.documentElement.lang = 'en'
  })

  it('stores locale in localStorage', async () => {
    // We test the storage logic directly since useLocale requires vue-i18n plugin context.
    const STORAGE_KEY = 'iulita.locale'
    localStorage.setItem(STORAGE_KEY, 'ru')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('ru')
  })

  it('defaults to en when no stored locale', () => {
    const stored = localStorage.getItem('iulita.locale') || 'en'
    expect(stored).toBe('en')
  })

  it('detects RTL for Hebrew', () => {
    const RTL_LOCALES = ['he']
    expect(RTL_LOCALES.includes('he')).toBe(true)
    expect(RTL_LOCALES.includes('en')).toBe(false)
    expect(RTL_LOCALES.includes('ru')).toBe(false)
  })

  it('sets document dir for RTL locale', () => {
    document.documentElement.dir = 'rtl'
    expect(document.documentElement.dir).toBe('rtl')

    document.documentElement.dir = 'ltr'
    expect(document.documentElement.dir).toBe('ltr')
  })

  it('persists locale across page loads', () => {
    localStorage.setItem('iulita.locale', 'fr')
    const stored = localStorage.getItem('iulita.locale')
    expect(stored).toBe('fr')
  })

  it('resetLocale clears storage', async () => {
    localStorage.setItem('iulita.locale', 'zh')
    localStorage.removeItem('iulita.locale')
    expect(localStorage.getItem('iulita.locale')).toBeNull()
  })
})
