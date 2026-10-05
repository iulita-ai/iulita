import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { NCollapseItem, NInput, NPopconfirm } from 'naive-ui'
import Models from './Models.vue'
import { api } from '../api'
import { modelsApi, ModelAPIError, type ModelSettingsView } from '../modelApi'
const { admin } = vi.hoisted(() => ({ admin: vi.fn(() => true) }))
vi.mock('../api', () => ({ isAdmin: admin, currentUser: () => ({ user_id: 'admin-one' }), api: { getWizardStatus: vi.fn() } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('../modelApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../modelApi')>()
  return { ...actual, modelsApi: { settings: vi.fn(), catalog: vi.fn(), stage: vi.fn(), activate: vi.fn(), discard: vi.fn(), probe: vi.fn(), probeStatus: vi.fn(), cancelProbe: vi.fn(), revoke: vi.fn(), history: vi.fn(), restore: vi.fn() } }
})
const settings = { schema_version: 1, profiles: [{ id: 'ds-flash', name: 'DeepSeek Flash', connection: 'deepseek', model: 'deepseek-flash', max_output_tokens: 8192, thinking: 'disabled' as const, clear_thinking: false }], policy: { everyday: '', complex: '', vision: '', background: '', classifier: { enabled: false } } }
function fixture(): ModelSettingsView {
  return { revision: 1, active_revision: 0, settings: structuredClone(settings), connections: [{ provider: 'deepseek', endpoint: 'https://api.deepseek.com/v1', generation: 'first', source: 'database', credential_set: true, availability: 'untested' }], effective_profiles: [], health: 'setup_required', affected_roles: [], encryption_available: true, stage: { id: 'stage-one', base_revision: 1, config_hash: 'hash', created_at: '', expires_at: '2099-01-01', settings: structuredClone(settings), connections: [{ provider: 'deepseek', endpoint: 'https://api.deepseek.com/v1', generation: 'second', source: 'database', credential_set: true, availability: 'untested' }], effective_profiles: [{ id: 'ds-flash', eligibility: 'experimental', fingerprint: 'fp', evidence: [] }] } }
}
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api.getWizardStatus).mockResolvedValue({ setup_mode: false, wizard_completed: true, encryption_enabled: true, has_llm_provider: true }); sessionStorage.clear(); admin.mockReturnValue(true); vi.mocked(modelsApi.settings).mockResolvedValue(fixture()); vi.mocked(modelsApi.catalog).mockResolvedValue([{ provider: 'deepseek', model: 'deepseek-flash', name: 'Flash', context_tokens: 1000000, max_output_tokens: 384000, images: true, tools: true, streaming: true, thinking_required: false, source_url: '', catalog_version: '' }]) })
async function openProfile(wrapper: ReturnType<typeof mount>) { await flushPromises(); await wrapper.findComponent(NCollapseItem).find('.n-collapse-item__header-main').trigger('click'); await flushPromises() }
describe('Models admin controls', () => {
  it('explains an API balance rejection without retrying a paid check', async () => {
    vi.mocked(modelsApi.probe).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'failed', error_code: 'insufficient_balance', started_at: '', deadline: '' })
    vi.mocked(modelsApi.probeStatus).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'failed', error_code: 'insufficient_balance', started_at: '', deadline: '' })
    const wrapper = mount(Models); await openProfile(wrapper)
    await wrapper.findAll('button').find(b => b.text() === 'Test text')!.trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="probe-error"]').text()).toContain('insufficient API balance')
    expect(modelsApi.probe).toHaveBeenCalledOnce()
    expect(modelsApi.activate).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('ordinary users see a static explanation with no model requests', async () => {
    admin.mockReturnValue(false)
    const wrapper = mount(Models); await flushPromises()
    expect(wrapper.text()).toContain('Models are managed by your administrator')
    expect(modelsApi.settings).not.toHaveBeenCalled(); expect(modelsApi.catalog).not.toHaveBeenCalled(); expect(modelsApi.probe).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('loads model status without starting provider tests', async () => {
    const wrapper = mount(Models); await flushPromises()
    expect(modelsApi.settings).toHaveBeenCalledOnce(); expect(modelsApi.catalog).toHaveBeenCalledOnce(); expect(modelsApi.probe).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('distinguishes preserved legacy configuration from paid verification and retains its vision assignment', async () => {
    const data = fixture()
    const legacy = { id: 'legacy-claude', name: 'Current Claude', connection: 'claude', model: 'claude-sonnet-4-5', max_output_tokens: 8192, thinking: 'disabled' as const, clear_thinking: false }
    data.settings.profiles = [legacy]
    data.settings.policy.everyday = legacy.id
    data.settings.policy.vision = legacy.id
    data.stage!.settings = structuredClone(data.settings)
    data.stage!.connections = [{ provider: 'claude', endpoint: '', source: 'encrypted_store', generation: 'old', credential_set: true, availability: 'untested' }]
    data.stage!.effective_profiles = [{ id: legacy.id, eligibility: 'legacy_preserved', fingerprint: 'legacy-fp', evidence: [] }]
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    const wrapper = mount(Models); await openProfile(wrapper)
    expect(wrapper.text()).toContain('Existing configuration preserved')
    expect(wrapper.text()).not.toContain('Ready to assign')
    expect(wrapper.text()).not.toContain('Test images: Passed')
    expect(wrapper.find('[data-testid="activate"]').attributes('disabled')).toBeUndefined()
    const model = wrapper.findAllComponents({ name: 'Select' }).find(select => select.props('value') === 'claude-sonnet-4-5')!
    model.vm.$emit('update:value', 'claude-other')
    await flushPromises()
    expect(wrapper.text()).not.toContain('Existing configuration preserved')
    expect(wrapper.find('[data-testid="activate"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('prepares a previous version only on request and does not activate it automatically', async () => {
    const data = fixture(); data.stage = undefined
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    vi.mocked(modelsApi.history).mockResolvedValue({ history: [{ revision: 0, settings: structuredClone(settings), created_at: '2026-10-04' }] })
    vi.mocked(modelsApi.restore).mockResolvedValue({ ...fixture().stage!, id: 'restored-stage' })
    const wrapper = mount(Models); await flushPromises()
    expect(modelsApi.history).not.toHaveBeenCalled()
    const history = wrapper.findAllComponents(NCollapseItem).find(item => item.props('title') === 'Previous settings')!
    await history.find('.n-collapse-item__header-main').trigger('click'); await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'Refresh previous settings')!.trigger('click'); await flushPromises()
    expect(modelsApi.restore).not.toHaveBeenCalled()
    await wrapper.find('[data-testid="restore-history"]').trigger('click'); await flushPromises()
    expect(modelsApi.restore).toHaveBeenCalledWith(0, 1)
    expect(modelsApi.activate).not.toHaveBeenCalled(); expect(modelsApi.probe).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="restore-history"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('shows restart guidance after activation in a setup-only server', async () => {
    const data = fixture(); data.settings.policy.everyday = 'ds-flash'
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    vi.mocked(api.getWizardStatus).mockResolvedValue({ wizard_completed: false, setup_mode: true, encryption_enabled: true, has_llm_provider: true, models_ready: true, model_restart_required: true })
    const wrapper = mount(Models); await flushPromises()
    expect(wrapper.text()).toContain('restart Iulita')
    wrapper.unmount()
  })

  it('keeps environment and unsupported legacy connection editors read-only', async () => {
    const data = fixture(); data.stage = undefined
    data.connections[0]!.source = 'environment'
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    const wrapper = mount(Models); await flushPromises()
    const inputs = wrapper.findAllComponents(NInput)
    expect(inputs.filter(input => input.props('type') === 'password' && !input.props('disabled'))).toHaveLength(1)
    expect(inputs.find(input => input.props('value') === 'https://api.deepseek.com/v1')!.props('disabled')).toBe(true)
    expect(wrapper.text()).toContain('environment setting controls this connection')
    wrapper.unmount()
  })

  it('opens the failing profile and focuses its input after validation rejects a collapsed field', async () => {
    vi.mocked(modelsApi.stage).mockRejectedValue(new ModelAPIError('invalid_settings', 422, [{ path: 'profiles[0].name', code: 'invalid_name', message: 'Choose a name' }]))
    const wrapper = mount(Models, { attachTo: document.body }); await flushPromises()
    await wrapper.find('[data-testid="save-stage"]').trigger('click'); await flushPromises()
    const field = wrapper.find('[data-field="profiles[0].name"] input')
    expect(field.exists()).toBe(true)
    expect(document.activeElement).toBe(field.element)
    wrapper.unmount()
  })

  it('imports current roles and custom routes explicitly without paid calls or activation', async () => {
    const data = fixture()
    data.legacy_settings = structuredClone(settings)
    data.legacy_settings.policy = { ...data.legacy_settings.policy, everyday: 'ds-flash', classifier: { enabled: true, profile: 'old-selector' }, legacy_hints: { light: 'background' }, legacy_profile_hints: { creative: 'ds-flash' } }
    data.legacy_classifier_active = true
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    const wrapper = mount(Models); await flushPromises()
    await wrapper.find('[data-testid="import-legacy"]').trigger('click'); await flushPromises()
    const stored = JSON.parse(sessionStorage.getItem('iulita_models_draft_admin-one')!)
    expect(stored.settings.policy.everyday).toBe('ds-flash')
    expect(stored.settings.policy.legacy_hints).toEqual({ light: 'background' })
    expect(stored.settings.policy.legacy_profile_hints).toEqual({ creative: 'ds-flash' })
    expect(stored.settings.policy.classifier.enabled).toBe(false)
    expect(modelsApi.probe).not.toHaveBeenCalled(); expect(modelsApi.activate).not.toHaveBeenCalled(); expect(modelsApi.stage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('emergency revocation targets the active generation while a replacement is staged', async () => {
    vi.mocked(modelsApi.revoke).mockResolvedValue({ ...fixture(), stage: undefined, health: 'suspended', affected_roles: ['everyday'] })
    const wrapper = mount(Models); await flushPromises()
    wrapper.findComponent(NPopconfirm).vm.$emit('positive-click')
    await flushPromises()
    expect(modelsApi.revoke).toHaveBeenCalledWith('deepseek', 1, 'first')
    wrapper.unmount()
  })

  it('does not show previous test evidence as current after editing model identity', async () => {
    const data = fixture()
    data.stage!.effective_profiles[0].eligibility = 'production_eligible'
    data.stage!.effective_profiles[0].evidence = [{ kind: 'text', passed: true, checked_at: '2026-10-05', requested_model: 'deepseek-flash', served_model: 'deepseek-flash' }]
    vi.mocked(modelsApi.settings).mockResolvedValue(data)
    const wrapper = mount(Models); await openProfile(wrapper)
    expect(wrapper.text()).toContain('Ready to assign')
    const model = wrapper.findAllComponents(NInput).find(input => input.props('value') === 'DeepSeek Flash')!
    model.vm.$emit('update:value', 'My renamed model')
    await flushPromises()
    expect(wrapper.text()).toContain('Ready to assign')
    const selects = wrapper.findAllComponents({ name: 'Select' })
    const modelSelect = selects.find(select => select.props('value') === 'deepseek-flash')!
    modelSelect.vm.$emit('update:value', 'deepseek-v4-pro')
    await flushPromises()
    expect(wrapper.text()).not.toContain('Ready to assign')
    expect(wrapper.text()).not.toContain('Test text: Passed')
    wrapper.unmount()
  })

  it('keeps nonsecret changes and clears entered keys after a conflict', async () => {
    vi.mocked(modelsApi.stage).mockRejectedValue(new ModelAPIError('revision_conflict', 409))
    const wrapper = mount(Models); await openProfile(wrapper)
    const inputs = wrapper.findAllComponents(NInput)
    const name = inputs.find(input => input.props('value') === 'DeepSeek Flash')!
    name.vm.$emit('update:value', 'My new name')
    const secret = inputs.find(input => input.props('type') === 'password')!
    secret.vm.$emit('update:value', 'private-api-key')
    await wrapper.find('[data-testid="save-stage"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('Your draft is kept')
    expect(sessionStorage.getItem('iulita_models_draft_admin-one')).toContain('My new name')
    expect(sessionStorage.getItem('iulita_models_draft_admin-one')).not.toContain('private-api-key')
    expect(secret.props('value')).toBeUndefined()
    wrapper.unmount()
  })
  it('does not duplicate an unknown paid test and retries with the same key explicitly', async () => {
    vi.mocked(modelsApi.probe).mockRejectedValueOnce(new TypeError('lost response')).mockResolvedValueOnce({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'completed', started_at: '', deadline: '' })
    vi.mocked(modelsApi.probeStatus).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'completed', started_at: '', deadline: '' })
    const wrapper = mount(Models); await openProfile(wrapper)
    const textTest = wrapper.findAll('button').find(button => button.text() === 'Test text')!
    await textTest.trigger('click'); await flushPromises()
    expect(modelsApi.probe).toHaveBeenCalledOnce()
    expect(textTest.attributes('disabled')).toBeDefined()
    const recovery = wrapper.findAll('button').find(button => button.text() === 'Recover the same test')!
    await recovery.trigger('click'); await flushPromises()
    expect(modelsApi.probe).toHaveBeenCalledTimes(2)
    expect(vi.mocked(modelsApi.probe).mock.calls[0][0].idempotency_key).toBe(vi.mocked(modelsApi.probe).mock.calls[1][0].idempotency_key)
    wrapper.unmount()
  })
  it('does not claim a test stopped before the server confirms cancellation', async () => {
    vi.mocked(modelsApi.probe).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'running', started_at: '', deadline: '' })
    vi.mocked(modelsApi.probeStatus).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'running', started_at: '', deadline: '' })
    vi.mocked(modelsApi.cancelProbe).mockResolvedValue({ id: 'probe-one', profile_id: 'ds-flash', kind: 'text', status: 'cancel_requested', started_at: '', deadline: '' })
    const wrapper = mount(Models); await openProfile(wrapper)
    await wrapper.findAll('button').find(b => b.text() === 'Test text')!.trigger('click'); await flushPromises()
    await wrapper.find('[data-testid="cancel-probe"]').trigger('click'); await flushPromises()
    expect(modelsApi.cancelProbe).toHaveBeenCalledWith('probe-one')
    expect(wrapper.text()).toContain('Stopping test')
    expect(wrapper.text()).not.toContain('Test stopped')
    wrapper.unmount()
  })
})
