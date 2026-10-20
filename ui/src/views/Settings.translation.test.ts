import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider, NSwitch } from 'naive-ui'
import Settings from './Settings.vue'
import type { SkillConfigResponse, SkillInfo, TranslationServiceStatus } from '../api'

const { admin, push, apiMock } = vi.hoisted(() => ({
  admin: vi.fn(() => true), push: vi.fn(),
  apiMock: {
    getSystem: vi.fn(), getSkills: vi.fn(), getChats: vi.fn(), getSchedulers: vi.fn(), getTasks: vi.fn(), getTaskCounts: vi.fn(),
    getConfig: vi.fn(), getConfigSchema: vi.fn(), listGoogleAccounts: vi.fn(), getGoogleStatus: vi.fn(), getSlackUserStatus: vi.fn(),
    getSkillConfig: vi.fn(), listCredentials: vi.fn(), listCredentialsByConsumer: vi.fn(), toggleSkill: vi.fn(), setSkillConfig: vi.fn(),
  },
}))
vi.mock('../api', () => ({ api: apiMock, isAdmin: admin }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))

function service(state: TranslationServiceStatus['state']): TranslationServiceStatus { return { provider: 'zai', service: 'general_translation', state, checked: false } }
function skill(state: TranslationServiceStatus['state'] = 'disabled'): SkillInfo {
  return { name: 'translate', manifest_group: 'translate', description: 'internal description', type: 'tool', enabled: state !== 'disabled', has_capabilities: state === 'ready', has_config: true, service_status: service(state) }
}
function details(state: TranslationServiceStatus['state']): SkillConfigResponse {
  return { skill: 'translate', manifest: 'translate', description: 'internal description', schema: [{ key: 'skills.translate.enabled', value: 'true', has_value: true, has_override: true, secret: false }], encryption_enabled: true, service_status: service(state) }
}
const Wrapper = defineComponent({ setup: () => () => h(NMessageProvider, () => h(Settings)) })
const mountSettings = () => mount(Wrapper, { attachTo: document.body })

beforeEach(() => {
  vi.clearAllMocks(); admin.mockReturnValue(true)
  apiMock.getSystem.mockResolvedValue({}); apiMock.getSkills.mockResolvedValue([skill()]); apiMock.getChats.mockResolvedValue([])
  apiMock.getSchedulers.mockResolvedValue([]); apiMock.getTasks.mockResolvedValue([]); apiMock.getTaskCounts.mockResolvedValue({})
  apiMock.getConfig.mockResolvedValue({ overrides: [], encryption_enabled: true }); apiMock.getConfigSchema.mockResolvedValue({ sections: [] })
  apiMock.listGoogleAccounts.mockResolvedValue([]); apiMock.getGoogleStatus.mockResolvedValue(null); apiMock.getSlackUserStatus.mockResolvedValue(null)
  apiMock.getSkillConfig.mockResolvedValue(details('ready')); apiMock.toggleSkill.mockResolvedValue({ status: 'ok' })
})

describe('Settings translation integration', () => {
  it('keeps the unavailable opt-in skill visible and uses one admin toggle', async () => {
    const wrapper = mountSettings(); await flushPromises()
    const row = wrapper.get('[data-skill="translate"]')
    expect(row.text()).toContain('Z.ai translator')
    expect(row.text()).toContain('Translation is disabled')
    expect(row.findAllComponents(NSwitch)).toHaveLength(1)
    expect(row.get('[role="switch"]').attributes('aria-label')).toBe('Enable Z.ai translation')
    expect(apiMock.toggleSkill).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('opens read-only service details without fetching credentials or starting checks', async () => {
    const wrapper = mountSettings(); await flushPromises()
    await wrapper.get('[data-skill="translate"]').findAll('button').find(button => button.text() === 'Connection details')!.trigger('click'); await flushPromises()
    const card = wrapper.get('[data-testid="translation-service"]')
    expect(card.text()).toContain('Access to the Translation Agent API has not been verified')
    expect(card.findAllComponents(NSwitch)).toHaveLength(0)
    expect(card.find('input').exists()).toBe(false)
    expect(wrapper.find('.n-drawer input[type="password"]').exists()).toBe(false)
    expect(apiMock.listCredentials).not.toHaveBeenCalled()
    expect(apiMock.listCredentialsByConsumer).not.toHaveBeenCalled()
    expect(apiMock.setSkillConfig).not.toHaveBeenCalled()
    expect(apiMock.toggleSkill).not.toHaveBeenCalled()
    await card.get('button').trigger('click')
    expect(push).toHaveBeenCalledExactlyOnceWith({ name: 'models' })
    wrapper.unmount()
  })

  it('shows refreshed backend availability after enabling and keeps a single persisted write', async () => {
    apiMock.getSkills.mockResolvedValueOnce([skill()]).mockResolvedValueOnce([skill('forbidden')])
    const wrapper = mountSettings(); await flushPromises()
    wrapper.get('[data-skill="translate"]').findComponent(NSwitch).vm.$emit('update:value', true); await flushPromises()
    expect(apiMock.toggleSkill).toHaveBeenCalledExactlyOnceWith('translate', true)
    expect(apiMock.getSkills).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-skill="translate"]').text()).toContain('Z.ai is not allowed by the current policy')
    expect(apiMock.setSkillConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('provides safe details but no global toggle, credential calls or Models link to ordinary users', async () => {
    admin.mockReturnValue(false); apiMock.getSkills.mockResolvedValue([skill('not_configured')]); apiMock.getSkillConfig.mockResolvedValue(details('not_configured'))
    const wrapper = mountSettings(); await flushPromises()
    const row = wrapper.get('[data-skill="translate"]')
    expect(row.findAllComponents(NSwitch)).toHaveLength(0)
    await row.findAll('button').find(button => button.text() === 'Connection details')!.trigger('click'); await flushPromises()
    const card = wrapper.get('[data-testid="translation-service"]')
    expect(card.text()).toContain('Your administrator manages the connection')
    expect(card.find('button').exists()).toBe(false)
    expect(wrapper.findAll('.n-drawer [role="switch"]')).toHaveLength(0)
    expect(apiMock.getConfig).not.toHaveBeenCalled(); expect(apiMock.getConfigSchema).not.toHaveBeenCalled()
    expect(apiMock.listCredentials).not.toHaveBeenCalled(); expect(apiMock.listCredentialsByConsumer).not.toHaveBeenCalled()
    expect(apiMock.toggleSkill).not.toHaveBeenCalled(); expect(apiMock.setSkillConfig).not.toHaveBeenCalled(); expect(push).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not leave an old ready state in the drawer after a revoked connection', async () => {
    const wrapper = mountSettings(); await flushPromises()
    const configure = () => wrapper.get('[data-skill="translate"]').findAll('button').find(button => button.text() === 'Connection details')!
    await configure().trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="translation-service"]').text()).toContain('Connection configured')
    apiMock.getSkillConfig.mockResolvedValue(details('revoked'))
    await configure().trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="translation-service"]').text()).toContain('Z.ai connection has been revoked')
    expect(wrapper.get('[data-testid="translation-service"]').text()).not.toContain('Access to the Translation Agent API has not been verified')
    wrapper.unmount()
  })
})
