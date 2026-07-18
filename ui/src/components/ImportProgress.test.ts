import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ImportProgress from './ImportProgress.vue'
import en from '../i18n/locales/en.json'

const i18n = createI18n({ legacy: false, locale: 'en', fallbackLocale: 'en', messages: { en } })

function mountIt(payload: any, done = false, failed = false) {
  return mount(ImportProgress, { props: { payload, done, failed }, global: { plugins: [i18n] } })
}

describe('ImportProgress', () => {
  it('shows the conversations phase with an indeterminate bar (unknown total)', () => {
    const w = mountIt({ job_id: 'j', phase: 'conversations', done: 5 })
    expect(w.text()).toContain('Archiving conversations (5)')
  })

  it('computes percentage from done/total when total is known', () => {
    const w = mountIt({ job_id: 'j', phase: 'memories', done: 3, total: 3 })
    // 3/3 → 100%
    expect(w.html()).toContain('100')
  })

  it('renders the done state', () => {
    const w = mountIt({ job_id: 'j', phase: 'embedding', done: 10 }, true, false)
    expect(w.text()).toContain('Done')
  })

  it('renders the failed state', () => {
    const w = mountIt({ job_id: 'j', phase: 'conversations', done: 2 }, false, true)
    expect(w.text()).toContain('Failed')
  })

  it('shows stored/skipped counters', () => {
    const w = mountIt({ job_id: 'j', phase: 'conversations', done: 5, stored: 42, skipped: 3 })
    expect(w.text()).toContain('42')
    expect(w.text()).toContain('3')
  })
})
