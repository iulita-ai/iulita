import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider, NSelect, NSwitch } from 'naive-ui'
import AgentJobs from './AgentJobs.vue'
import { api, type AgentJob } from '../api'
import { modelsApi } from '../modelApi'
vi.mock('../api', () => ({ api: { listAgentJobs: vi.fn(), updateAgentJob: vi.fn(), createAgentJob: vi.fn(), deleteAgentJob: vi.fn() } }))
vi.mock('../modelApi', () => ({ modelsApi: { settings: vi.fn() } }))
const job: AgentJob = { id: 1, name: 'Daily notes', prompt: 'Summarize', model: '', profile_id: 'glm-main', cron_expr: '', interval: '24h', delivery_chat_id: '', enabled: true, created_at: '', updated_at: '' }
const profileState = { revision: 1, active_revision: 1, health: 'ready', affected_roles: [], encryption_available: true, settings: { schema_version: 1, profiles: [{ id: 'glm-main', name: 'GLM 5.3', connection: 'zai', model: 'glm-5.3', max_output_tokens: 8192, thinking: 'enabled' as const, clear_thinking: true }, { id: 'ds-pro', name: 'DeepSeek Pro', connection: 'deepseek', model: 'deepseek-v4-pro', max_output_tokens: 8192, thinking: 'disabled' as const, clear_thinking: false }], policy: { everyday: 'glm-main', complex: '', background: '', vision: '', classifier: { enabled: false } } }, effective_profiles: [{ id: 'glm-main', eligibility: 'production_eligible', fingerprint: 'fp', evidence: [] }, { id: 'ds-pro', eligibility: 'experimental', fingerprint: 'other', evidence: [] }], connections: [{ provider: 'zai', endpoint: '', source: 'database', generation: 'g', credential_set: true, availability: 'untested' }] }
function mountJobs() { return mount(defineComponent({ render: () => h(NMessageProvider, () => h(AgentJobs)) })) }
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api.listAgentJobs).mockResolvedValue([structuredClone(job)]); vi.mocked(api.updateAgentJob).mockResolvedValue({} as AgentJob); vi.mocked(modelsApi.settings).mockResolvedValue(structuredClone(profileState)) })
async function edit(wrapper: ReturnType<typeof mountJobs>) { await flushPromises(); await wrapper.findAll('button').find(button => button.text() === 'Edit')!.trigger('click'); await flushPromises() }
describe('Agent job profile overrides', () => {
  it('only offers eligible active profiles', async () => {
    const wrapper = mountJobs(); await edit(wrapper)
    expect(wrapper.findComponent(NSelect).props('options')).toEqual([{ value: 'glm-main', label: 'GLM 5.3', disabled: false }])
    wrapper.unmount()
  })
  it('clears an override explicitly with null and does not revive a legacy route', async () => {
    vi.mocked(api.listAgentJobs).mockResolvedValue([{ ...job, model: 'ollama' }])
    const wrapper = mountJobs(); await edit(wrapper)
    wrapper.findComponent(NSelect).vm.$emit('update:value', null)
    await flushPromises(); await wrapper.findAll('button').find(button => button.text() === 'Save')!.trigger('click'); await flushPromises()
    expect(api.updateAgentJob).toHaveBeenCalledWith(1, expect.objectContaining({ profile_id: null, model: '' }))
    wrapper.unmount()
  })
  it('omits profile fields when only toggling a job', async () => {
    const wrapper = mountJobs(); await flushPromises()
    await wrapper.findComponent(NSwitch).trigger('click')
    await flushPromises()
    expect(api.updateAgentJob).toHaveBeenCalledWith(1, { enabled: false })
    wrapper.unmount()
  })

  it('preserves legacy route names when the profile is unchanged and keeps toggle updates narrow', async () => {
    vi.mocked(api.listAgentJobs).mockResolvedValue([{ ...job, profile_id: '', model: 'ollama' }])
    const wrapper = mountJobs(); await edit(wrapper)
    await wrapper.findAll('button').find(button => button.text() === 'Save')!.trigger('click'); await flushPromises()
    expect(api.updateAgentJob).toHaveBeenCalledWith(1, expect.objectContaining({ profile_id: null, model: 'ollama' }))
    wrapper.unmount()
  })
})
