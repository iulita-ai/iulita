import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider } from 'naive-ui'
import Setup from './Setup.vue'
import type { WizardSchemaResponse, WizardStatus } from '../api'

// Mock api module
vi.mock('../api', () => ({
  api: {
    getWizardStatus: vi.fn(),
    getWizardSchema: vi.fn(),
    completeWizard: vi.fn(),
    importTOML: vi.fn(),
    setConfig: vi.fn(),
  },
}))

import { api } from '../api'

const mockSchema: WizardSchemaResponse = {
  sections: [
    {
      name: 'claude',
      label: 'Claude (Anthropic)',
      description: 'Primary LLM provider',
      optional: false,
      is_llm: true,
      fields: [
        { key: 'claude.api_key', label: 'API Key', description: 'Anthropic API key', type: 'secret', secret: true, required: false, section: 'claude' },
        { key: 'claude.model', label: 'Model', description: 'Claude model ID', type: 'select', default: 'claude-sonnet-4-5-20250929', options: ['claude-sonnet-4-5-20250929', 'claude-haiku-4-5-20251001'], secret: false, required: false, section: 'claude' },
      ],
    },
    {
      name: 'openai',
      label: 'OpenAI',
      description: 'OpenAI-compatible provider',
      optional: true,
      is_llm: true,
      fields: [
        { key: 'openai.api_key', label: 'API Key', description: 'OpenAI API key', type: 'secret', secret: true, required: false, section: 'openai' },
        { key: 'openai.model', label: 'Model', description: 'Model name', type: 'string', secret: false, required: false, section: 'openai' },
      ],
    },
    {
      name: 'app',
      label: 'Application',
      description: 'General app settings',
      optional: true,
      is_llm: false,
      fields: [
        { key: 'app.system_prompt', label: 'System Prompt', description: 'Default prompt', type: 'string', default: 'You are Iulita', secret: false, required: false, section: 'app' },
      ],
    },
  ],
  encryption_enabled: false,
}

// Wrap Setup in NMessageProvider so useMessage() works
const SetupWrapper = defineComponent({
  render() {
    return h(NMessageProvider, null, {
      default: () => h(Setup),
    })
  },
})

function mountSetup() {
  return mount(SetupWrapper)
}

describe('Setup.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()

    // Default: wizard not completed, setup mode active
    vi.mocked(api.getWizardStatus).mockResolvedValue({
      wizard_completed: false,
      setup_mode: true,
      encryption_enabled: false,
      has_llm_provider: false,
    })

    vi.mocked(api.getWizardSchema).mockResolvedValue(mockSchema)
    vi.mocked(api.setConfig).mockResolvedValue({ status: 'ok', key: '' })
    vi.mocked(api.completeWizard).mockResolvedValue({ status: 'completed', message: 'ok' })
  })

  it('renders wizard title', async () => {
    const wrapper = mountSetup()
    await flushPromises()
    expect(wrapper.text()).toContain('Iulita Setup Wizard')
  })

  it('starts on step 1 (Welcome)', async () => {
    const wrapper = mountSetup()
    await flushPromises()
    expect(wrapper.text()).toContain('Welcome to Iulita')
  })

  it('loads schema on mount', async () => {
    mountSetup()
    await flushPromises()
    expect(api.getWizardStatus).toHaveBeenCalled()
    expect(api.getWizardSchema).toHaveBeenCalled()
  })

  it('shows import button when base config has values', async () => {
    vi.mocked(api.getWizardSchema).mockResolvedValue({
      ...mockSchema,
      sections: mockSchema.sections.map(s => ({
        ...s,
        fields: s.fields.map(f => ({ ...f, has_value: true, value: 'test' })),
      })),
    })

    const wrapper = mountSetup()
    await flushPromises()

    expect(wrapper.text()).toContain('Existing Configuration Detected')
    expect(wrapper.text()).toContain('Import All Settings to DB')
  })

  it('does not show import button when no base config', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    expect(wrapper.text()).not.toContain('Existing Configuration Detected')
  })

  it('handles TOML import', async () => {
    vi.mocked(api.getWizardSchema).mockResolvedValue({
      ...mockSchema,
      sections: mockSchema.sections.map(s => ({
        ...s,
        fields: s.fields.map(f => ({ ...f, has_value: true, value: 'v' })),
      })),
    })
    vi.mocked(api.importTOML).mockResolvedValue({
      imported: 5,
      skipped: 2,
      status: 'ok',
    })

    const wrapper = mountSetup()
    await flushPromises()

    // Find and click import button
    const importBtn = wrapper.findAll('button').find(b => b.text().includes('Import All'))
    expect(importBtn).toBeDefined()
    await importBtn!.trigger('click')
    await flushPromises()

    expect(api.importTOML).toHaveBeenCalled()
    expect(wrapper.text()).toContain('Imported 5 settings')
  })

  it('navigates to step 2 when clicking Configure Manually', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const btn = wrapper.findAll('button').find(b => b.text().includes('Configure Manually'))
    await btn!.trigger('click')
    await flushPromises()

    // Step 2: provider selection
    expect(wrapper.text()).toContain('Select which LLM providers')
  })

  it('shows LLM provider checkboxes on step 2', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    // Navigate to step 2
    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 2
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Claude (Anthropic)')
    expect(wrapper.text()).toContain('OpenAI')
  })

  it('disables Next button when no provider selected', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 2
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('You must select at least one LLM provider')
  })

  it('pre-selects providers with existing values', async () => {
    vi.mocked(api.getWizardSchema).mockResolvedValue({
      ...mockSchema,
      sections: [
        {
          ...mockSchema.sections[0],
          fields: mockSchema.sections[0].fields.map(f =>
            f.key === 'claude.api_key' ? { ...f, has_value: true, value: '***' } : f
          ),
        },
        ...mockSchema.sections.slice(1),
      ],
    })

    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    expect(vm.selectedProviders).toContain('claude')
  })

  it('pre-fills field defaults', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    expect(vm.fieldValues['claude.model']).toBe('claude-sonnet-4-5-20250929')
    expect(vm.fieldValues['app.system_prompt']).toBe('You are Iulita')
  })

  it('separates LLM and feature sections correctly', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    expect(vm.llmSections.length).toBe(2) // claude, openai
    expect(vm.featureSections.length).toBe(1) // app
  })

  it('activeLLMSections filters by selection', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.selectedProviders = ['claude']
    await wrapper.vm.$nextTick()

    expect(vm.activeLLMSections.length).toBe(1)
    expect(vm.activeLLMSections[0].name).toBe('claude')
  })

  it('saves dirty fields when moving from step 3 to step 4', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 3
    vm.selectedProviders = ['claude']
    vm.fieldValues['claude.api_key'] = 'sk-test-key'
    vm.fieldDirty['claude.api_key'] = true
    await wrapper.vm.$nextTick()

    // Call saveProviderConfig
    await vm.saveProviderConfig()
    await flushPromises()

    expect(api.setConfig).toHaveBeenCalledWith('claude.api_key', 'sk-test-key', true)
    // Should also set routing.default_provider
    expect(api.setConfig).toHaveBeenCalledWith('routing.default_provider', 'claude', false)
    expect(vm.currentStep).toBe(4)
  })

  it('skips saving when no dirty fields', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 3
    vm.selectedProviders = ['claude']

    await vm.saveProviderConfig()
    await flushPromises()

    // Only routing.default_provider should be saved
    expect(api.setConfig).toHaveBeenCalledTimes(1)
    expect(vm.currentStep).toBe(4)
  })

  it('completes wizard on step 4 save', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 4

    await vm.saveFeatureConfig()
    await flushPromises()

    expect(api.completeWizard).toHaveBeenCalled()
    expect(vm.currentStep).toBe(5)
  })

  it('shows success on step 5', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    vm.currentStep = 5
    vm.completeError = ''
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Setup Complete')
    expect(wrapper.text()).toContain('Restart the application')
  })

  it('shows error on step 5 when completion fails', async () => {
    vi.mocked(api.completeWizard).mockRejectedValue(new Error('No LLM configured'))

    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    await vm.handleComplete()
    await flushPromises()

    expect(vm.currentStep).toBe(5)
    expect(vm.completeError).toBe('No LLM configured')
    expect(wrapper.text()).toContain('Setup Error')
  })

  it('redirects to dashboard if wizard already completed', async () => {
    vi.mocked(api.getWizardStatus).mockResolvedValue({
      wizard_completed: true,
      setup_mode: false,
      encryption_enabled: false,
      has_llm_provider: true,
    })

    // Mock window.location
    const locationMock = { href: '' }
    vi.stubGlobal('location', locationMock)

    mountSetup()
    await flushPromises()

    expect(locationMock.href).toBe('/')
  })

  it('selectOptions maps to label/value pairs', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    const opts = vm.selectOptions({
      options: ['a', 'b'],
    })
    expect(opts).toEqual([
      { label: 'a', value: 'a' },
      { label: 'b', value: 'b' },
    ])
  })

  it('selectOptions handles empty options', async () => {
    const wrapper = mountSetup()
    await flushPromises()

    const vm = wrapper.findComponent(Setup).vm as any
    expect(vm.selectOptions({})).toEqual([])
    expect(vm.selectOptions({ options: [] })).toEqual([])
  })
})
