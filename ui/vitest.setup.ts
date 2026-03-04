import { vi } from 'vitest'
import { config } from '@vue/test-utils'

// Ensure localStorage works in happy-dom — MUST be set before any module import that uses localStorage.
const store: Record<string, string> = {}
const localStorageMock: Storage = {
  getItem: (key: string) => store[key] ?? null,
  setItem: (key: string, value: string) => { store[key] = String(value) },
  removeItem: (key: string) => { delete store[key] },
  clear: () => { Object.keys(store).forEach(k => delete store[k]) },
  get length() { return Object.keys(store).length },
  key: (i: number) => Object.keys(store)[i] ?? null,
}
Object.defineProperty(globalThis, 'localStorage', { value: localStorageMock, writable: true })

// Mock window.matchMedia (used by Naive UI)
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
})

// Import i18n AFTER localStorage mock is ready.
const { default: i18n } = await import('./src/i18n')

// Register vue-i18n plugin globally for all test components.
config.global.plugins = config.global.plugins || []
config.global.plugins.push(i18n)

// Stub teleport (used by Naive UI modals/dialogs)
config.global.stubs = {
  teleport: true,
}
