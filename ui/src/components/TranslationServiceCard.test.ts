import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import TranslationServiceCard from './TranslationServiceCard.vue'
import type { TranslationServiceStatus } from '../api'
import en from '../i18n/locales/en.json'
import ru from '../i18n/locales/ru.json'
import zh from '../i18n/locales/zh.json'
import es from '../i18n/locales/es.json'
import fr from '../i18n/locales/fr.json'
import he from '../i18n/locales/he.json'

const status = (state: TranslationServiceStatus['state']): TranslationServiceStatus => ({ provider: 'zai', service: 'general_translation', state, checked: false })

describe('TranslationServiceCard', () => {
  it('distinguishes configured credentials from verified agent access', async () => {
    const wrapper = mount(TranslationServiceCard, { props: { admin: true, serviceStatus: status('ready') } })
    expect(wrapper.text()).toContain('Uses the shared Z.ai key')
    expect(wrapper.text()).toContain('Access to the Translation Agent API has not been verified')
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('select').exists()).toBe(false)
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('manage-connection')).toHaveLength(1)
    wrapper.unmount()
  })

  it.each(['not_configured', 'forbidden', 'revoked', 'runtime_unavailable', 'disabled'] as const)('shows %s without administrative actions to an ordinary user', (state) => {
    const wrapper = mount(TranslationServiceCard, { props: { admin: false, serviceStatus: status(state) } })
    expect(wrapper.get('[role="status"]').text()).toBe(en.translation.states[state])
    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.text()).toContain('Your administrator manages the connection')
    expect(wrapper.text()).not.toContain('Access to the Translation Agent API has not been verified')
    wrapper.unmount()
  })

  it('fails closed when the server did not provide status', () => {
    const wrapper = mount(TranslationServiceCard, { props: { admin: false } })
    expect(wrapper.get('[role="status"]').text()).toBe(en.translation.states.runtime_unavailable)
    wrapper.unmount()
  })

  it('provides every translation and copy string in all six locales', () => {
    for (const locale of [en, ru, zh, es, fr, he]) {
      expect(Object.keys(locale.translation).sort()).toEqual(Object.keys(en.translation).sort())
      expect(Object.keys(locale.translation.states).sort()).toEqual(Object.keys(en.translation.states).sort())
      for (const value of Object.values(locale.translation)) {
        if (typeof value === 'string') expect(value.trim().length).toBeGreaterThan(0)
      }
      for (const value of Object.values(locale.translation.states)) expect(value.trim().length).toBeGreaterThan(0)
      for (const key of ['copy', 'copyMessage', 'copied', 'copyError', 'copyUnavailable'] as const) expect(locale.chat[key].trim().length).toBeGreaterThan(0)
    }
  })
})
