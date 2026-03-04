import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider } from 'naive-ui'
import ExternalSkills from './ExternalSkills.vue'
import type { InstalledSkill, ExternalSkillResult } from '../api'

// Mock api module
vi.mock('../api', () => ({
  api: {
    listExternalSkills: vi.fn(),
    installExternalSkill: vi.fn(),
    uninstallExternalSkill: vi.fn(),
    searchExternalSkills: vi.fn(),
    enableExternalSkill: vi.fn(),
    disableExternalSkill: vi.fn(),
    updateExternalSkill: vi.fn(),
    setConfig: vi.fn(),
  },
}))

import { api } from '../api'

const mockSkill: InstalledSkill = {
  id: 1,
  slug: 'weather-brief',
  name: 'Weather Brief',
  version: '1.2.0',
  source: 'clawhub',
  source_ref: 'weather-brief',
  isolation: 'text_only',
  install_dir: '/data/skills/weather-brief',
  enabled: true,
  pinned: false,
  checksum: 'abc123',
  description: 'Concise weather forecasts',
  author: 'community',
  tags: 'weather,forecast',
  capabilities: '',
  config_keys: '',
  secret_keys: '',
  requires_bins: '',
  requires_env: '',
  allowed_tools: '',
  has_code: false,
  effective_mode: '',
  install_warnings: '',
  installed_at: '2026-03-01T00:00:00Z',
}

const mockSkill2: InstalledSkill = {
  ...mockSkill,
  id: 2,
  slug: 'calculator',
  name: 'Calculator',
  version: '2.0.0',
  description: 'Math calculations',
  author: 'tools',
  tags: 'math,calc',
  enabled: false,
}

const mockSearchResult: ExternalSkillResult = {
  slug: 'joke-teller',
  name: 'Joke Teller',
  version: '1.0.0',
  description: 'Tells jokes on demand',
  author: 'fun-skills',
  tags: ['humor', 'jokes'],
  source: 'clawhub',
  source_ref: 'joke-teller',
}

// Wrap in NMessageProvider so useMessage() works
const Wrapper = defineComponent({
  setup() {
    return () => h(NMessageProvider, null, { default: () => h(ExternalSkills) })
  },
})

function mountComponent() {
  return mount(Wrapper, {
    attachTo: document.body,
  })
}

describe('ExternalSkills.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listExternalSkills).mockResolvedValue([])
  })

  describe('Installed tab', () => {
    it('renders page header', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      expect(wrapper.text()).toContain('External Skills')
      expect(wrapper.text()).toContain('Extend your assistant with community skills')
      wrapper.unmount()
    })

    it('shows empty state when no skills installed', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      expect(wrapper.text()).toContain('No external skills installed')
      expect(wrapper.text()).toContain('Browse Marketplace')
      wrapper.unmount()
    })

    it('loads and displays installed skills', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill, mockSkill2])

      const wrapper = mountComponent()
      await flushPromises()

      expect(api.listExternalSkills).toHaveBeenCalledOnce()
      expect(wrapper.text()).toContain('Weather Brief')
      expect(wrapper.text()).toContain('v1.2.0')
      expect(wrapper.text()).toContain('Text Only')
      expect(wrapper.text()).toContain('Concise weather forecasts')
      expect(wrapper.text()).toContain('Calculator')
      expect(wrapper.text()).toContain('v2.0.0')
      wrapper.unmount()
    })

    it('shows tags for installed skills', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])

      const wrapper = mountComponent()
      await flushPromises()

      expect(wrapper.text()).toContain('weather')
      expect(wrapper.text()).toContain('forecast')
      expect(wrapper.text()).toContain('community')
      wrapper.unmount()
    })

    it('shows error when loading fails', async () => {
      vi.mocked(api.listExternalSkills).mockRejectedValue(new Error('Network error'))

      const wrapper = mountComponent()
      await flushPromises()

      // Error is shown via message toast - component should still render
      expect(wrapper.text()).toContain('No external skills installed')
      wrapper.unmount()
    })

    it('calls enable API on toggle on', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([{ ...mockSkill, enabled: false }])
      vi.mocked(api.enableExternalSkill).mockResolvedValue({ status: 'ok', slug: 'weather-brief', enabled: true })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      await vm.handleToggle('weather-brief', true)
      await flushPromises()

      expect(api.enableExternalSkill).toHaveBeenCalledWith('weather-brief')
      wrapper.unmount()
    })

    it('calls disable API on toggle off', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])
      vi.mocked(api.disableExternalSkill).mockResolvedValue({ status: 'ok', slug: 'weather-brief', enabled: false })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      await vm.handleToggle('weather-brief', false)
      await flushPromises()

      expect(api.disableExternalSkill).toHaveBeenCalledWith('weather-brief')
      wrapper.unmount()
    })

    it('calls update API and refreshes skill data', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])
      const updatedSkill = { ...mockSkill, version: '1.3.0' }
      vi.mocked(api.updateExternalSkill).mockResolvedValue({ skill: updatedSkill, warnings: [] })

      const wrapper = mountComponent()
      await flushPromises()

      // Find and click Update button
      const buttons = wrapper.findAll('button')
      const updateBtn = buttons.find(b => b.text().includes('Update'))
      expect(updateBtn).toBeDefined()
      await updateBtn!.trigger('click')
      await flushPromises()

      expect(api.updateExternalSkill).toHaveBeenCalledWith('weather-brief')
      expect(wrapper.text()).toContain('v1.3.0')
      wrapper.unmount()
    })

    it('shows warnings after update', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])
      vi.mocked(api.updateExternalSkill).mockResolvedValue({
        skill: mockSkill,
        warnings: ['Shell commands not supported in text_only mode'],
      })

      const wrapper = mountComponent()
      await flushPromises()

      const buttons = wrapper.findAll('button')
      const updateBtn = buttons.find(b => b.text().includes('Update'))
      await updateBtn!.trigger('click')
      await flushPromises()

      expect(wrapper.text()).toContain('Warnings')
      expect(wrapper.text()).toContain('Shell commands not supported')
      wrapper.unmount()
    })

    it('removes skill from list after uninstall', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])
      vi.mocked(api.uninstallExternalSkill).mockResolvedValue({ status: 'deleted', slug: 'weather-brief' })

      const wrapper = mountComponent()
      await flushPromises()

      expect(wrapper.text()).toContain('Weather Brief')

      // Call handler directly (NPopconfirm positive-click binding)
      const vm = wrapper.findComponent(ExternalSkills).vm as any
      await vm.handleUninstall('weather-brief')
      await flushPromises()

      expect(api.uninstallExternalSkill).toHaveBeenCalledWith('weather-brief')
      // Skill should be removed from list
      expect(wrapper.text()).not.toContain('Weather Brief')
      wrapper.unmount()
    })
  })

  describe('Marketplace tab', () => {
    it('shows empty search state initially', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      // Switch to marketplace tab
      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      await wrapper.vm.$nextTick()

      expect(wrapper.text()).toContain('Search for skills in the ClawHub marketplace')
      wrapper.unmount()
    })

    it('searches and displays results', async () => {
      vi.mocked(api.searchExternalSkills).mockResolvedValue({
        results: [mockSearchResult],
        count: 1,
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = 'joke'
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      expect(api.searchExternalSkills).toHaveBeenCalledWith('joke')
      expect(wrapper.text()).toContain('Joke Teller')
      expect(wrapper.text()).toContain('Tells jokes on demand')
      expect(wrapper.text()).toContain('1 results')
      wrapper.unmount()
    })

    it('shows no results message', async () => {
      vi.mocked(api.searchExternalSkills).mockResolvedValue({
        results: [],
        count: 0,
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = 'nonexistent'
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      expect(wrapper.text()).toContain('No skills found')
      wrapper.unmount()
    })

    it('installs skill from marketplace', async () => {
      vi.mocked(api.searchExternalSkills).mockResolvedValue({
        results: [mockSearchResult],
        count: 1,
      })
      const installed: InstalledSkill = {
        ...mockSkill,
        slug: 'joke-teller',
        name: 'Joke Teller',
        version: '1.0.0',
      }
      vi.mocked(api.installExternalSkill).mockResolvedValue({
        skill: installed,
        warnings: [],
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = 'joke'
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      // Install the skill
      await vm.handleInstall('joke-teller', 'clawhub')
      await flushPromises()

      expect(api.installExternalSkill).toHaveBeenCalledWith('joke-teller', 'clawhub')
      wrapper.unmount()
    })

    it('shows "Installed" badge for already installed skills', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])
      vi.mocked(api.searchExternalSkills).mockResolvedValue({
        results: [{
          ...mockSearchResult,
          slug: 'weather-brief',
          name: 'Weather Brief',
        }],
        count: 1,
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = 'weather'
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      // Should show "Installed" button (disabled)
      const buttons = wrapper.findAll('button')
      const installedBtn = buttons.find(b => b.text().includes('Installed'))
      expect(installedBtn).toBeDefined()
      wrapper.unmount()
    })

    it('does not search with empty query', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = '   '
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      expect(api.searchExternalSkills).not.toHaveBeenCalled()
      wrapper.unmount()
    })
  })

  describe('URL install modal', () => {
    it('installs from URL source', async () => {
      const installed: InstalledSkill = {
        ...mockSkill,
        slug: 'custom-skill',
        name: 'Custom Skill',
        source: 'url',
      }
      vi.mocked(api.installExternalSkill).mockResolvedValue({
        skill: installed,
        warnings: [],
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = 'https://example.com/skill.zip'
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      expect(api.installExternalSkill).toHaveBeenCalledWith('https://example.com/skill.zip', 'url')
      expect(vm.showUrlModal).toBe(false)
      expect(vm.activeTab).toBe('installed')
      wrapper.unmount()
    })

    it('installs from clawhub slug via URL modal', async () => {
      const installed: InstalledSkill = {
        ...mockSkill,
        slug: 'some-skill',
        name: 'Some Skill',
      }
      vi.mocked(api.installExternalSkill).mockResolvedValue({
        skill: installed,
        warnings: [],
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = 'some-skill'
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      expect(api.installExternalSkill).toHaveBeenCalledWith('some-skill', 'clawhub')
      wrapper.unmount()
    })

    it('does not install with empty input', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = ''
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      expect(api.installExternalSkill).not.toHaveBeenCalled()
      wrapper.unmount()
    })

    it('shows warnings after URL install', async () => {
      vi.mocked(api.installExternalSkill).mockResolvedValue({
        skill: mockSkill,
        warnings: ['Requires curl binary'],
      })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = 'weather-brief'
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      expect(wrapper.text()).toContain('Warnings')
      expect(wrapper.text()).toContain('Requires curl binary')
      wrapper.unmount()
    })

    it('handles install error', async () => {
      vi.mocked(api.installExternalSkill).mockRejectedValue(new Error('max installed skills reached (50)'))

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = 'some-skill'
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      // Modal should stay open on error
      expect(vm.showUrlModal).toBe(true)
      expect(vm.lastError).toBe('Install failed: max installed skills reached (50)')
      wrapper.unmount()
    })
  })

  describe('isolation error handling', () => {
    it('parseIsolationError extracts level and config key', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      expect(vm.parseIsolationError('shell-isolation skills are disabled (set skills.external.allow_shell=true)')).toEqual({
        level: 'shell',
        configKey: 'skills.external.allow_shell',
      })
      expect(vm.parseIsolationError('docker-isolation skills are disabled (set skills.external.allow_docker=true)')).toEqual({
        level: 'docker',
        configKey: 'skills.external.allow_docker',
      })
      expect(vm.parseIsolationError('wasm-isolation skills are disabled (set skills.external.allow_wasm=true)')).toEqual({
        level: 'wasm',
        configKey: 'skills.external.allow_wasm',
      })
      wrapper.unmount()
    })

    it('parseIsolationError returns null for generic errors', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      expect(vm.parseIsolationError('max installed skills reached (50)')).toBeNull()
      expect(vm.parseIsolationError('network error')).toBeNull()
      wrapper.unmount()
    })

    it('marketplace install shows isolation alert instead of toast', async () => {
      vi.mocked(api.searchExternalSkills).mockResolvedValue({
        results: [mockSearchResult],
        count: 1,
      })
      vi.mocked(api.installExternalSkill).mockRejectedValue(
        new Error('shell-isolation skills are disabled (set skills.external.allow_shell=true)')
      )

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.activeTab = 'marketplace'
      vm.searchQuery = 'joke'
      await wrapper.vm.$nextTick()

      await vm.handleSearch()
      await flushPromises()

      await vm.handleInstall('joke-teller', 'clawhub')
      await flushPromises()

      expect(vm.isolationError).toEqual({
        level: 'shell',
        configKey: 'skills.external.allow_shell',
        skillRef: 'joke-teller',
        source: 'clawhub',
      })
      expect(wrapper.text()).toContain('Shell isolation is disabled')
      expect(wrapper.text()).toContain('Enable & Retry')
      wrapper.unmount()
    })

    it('URL install sets __url__ sentinel in isolationError', async () => {
      vi.mocked(api.installExternalSkill).mockRejectedValue(
        new Error('docker-isolation skills are disabled (set skills.external.allow_docker=true)')
      )

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.showUrlModal = true
      vm.urlInput = 'some-skill'
      await wrapper.vm.$nextTick()

      await vm.handleUrlInstall()
      await flushPromises()

      expect(vm.isolationError).toEqual({
        level: 'docker',
        configKey: 'skills.external.allow_docker',
        skillRef: 'some-skill',
        source: '__url__',
      })
      wrapper.unmount()
    })

    it('non-isolation error shows error alert (no isolation alert)', async () => {
      vi.mocked(api.installExternalSkill).mockRejectedValue(
        new Error('max installed skills reached (50)')
      )

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      await vm.handleInstall('some-skill', 'clawhub')
      await flushPromises()

      expect(vm.isolationError).toBeNull()
      expect(vm.lastError).toBe('Install failed: max installed skills reached (50)')
      expect(wrapper.text()).toContain('Install failed: max installed skills reached (50)')
      wrapper.unmount()
    })

    it('enable and retry success (marketplace path)', async () => {
      vi.mocked(api.setConfig).mockResolvedValue({ status: 'ok', key: 'skills.external.allow_shell' })
      const installed: InstalledSkill = {
        ...mockSkill,
        slug: 'joke-teller',
        name: 'Joke Teller',
      }
      vi.mocked(api.installExternalSkill).mockResolvedValue({ skill: installed, warnings: [] })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      // Simulate isolation error state from a marketplace install
      vm.isolationError = {
        level: 'shell',
        configKey: 'skills.external.allow_shell',
        skillRef: 'joke-teller',
        source: 'clawhub',
      }
      await wrapper.vm.$nextTick()

      await vm.handleEnableAndRetry()
      await flushPromises()

      expect(api.setConfig).toHaveBeenCalledWith('skills.external.allow_shell', 'true', false)
      expect(api.installExternalSkill).toHaveBeenCalledWith('joke-teller', 'clawhub')
      expect(vm.isolationError).toBeNull()
      wrapper.unmount()
    })

    it('enable and retry success (URL path)', async () => {
      vi.mocked(api.setConfig).mockResolvedValue({ status: 'ok', key: 'skills.external.allow_docker' })
      const installed: InstalledSkill = {
        ...mockSkill,
        slug: 'custom-skill',
        source: 'url',
      }
      vi.mocked(api.installExternalSkill).mockResolvedValue({ skill: installed, warnings: [] })

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.isolationError = {
        level: 'docker',
        configKey: 'skills.external.allow_docker',
        skillRef: 'https://example.com/skill.zip',
        source: '__url__',
      }
      await wrapper.vm.$nextTick()

      await vm.handleEnableAndRetry()
      await flushPromises()

      expect(api.setConfig).toHaveBeenCalledWith('skills.external.allow_docker', 'true', false)
      expect(api.installExternalSkill).toHaveBeenCalledWith('https://example.com/skill.zip', 'url')
      expect(vm.isolationError).toBeNull()
      expect(vm.showUrlModal).toBe(false)
      expect(vm.activeTab).toBe('installed')
      wrapper.unmount()
    })

    it('URL retry re-opens modal on generic failure', async () => {
      vi.mocked(api.setConfig).mockResolvedValue({ status: 'ok', key: 'skills.external.allow_shell' })
      vi.mocked(api.installExternalSkill).mockRejectedValue(new Error('unexpected error'))

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.isolationError = {
        level: 'shell',
        configKey: 'skills.external.allow_shell',
        skillRef: 'some-skill',
        source: '__url__',
      }
      await wrapper.vm.$nextTick()

      await vm.handleEnableAndRetry()
      await flushPromises()

      // Modal should stay open so user can retry manually
      expect(vm.showUrlModal).toBe(true)
      expect(vm.urlInput).toBe('some-skill')
      wrapper.unmount()
    })

    it('closing alert clears isolationError', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.isolationError = {
        level: 'shell',
        configKey: 'skills.external.allow_shell',
        skillRef: 'test',
        source: 'clawhub',
      }
      await wrapper.vm.$nextTick()

      expect(wrapper.text()).toContain('Shell isolation is disabled')

      // Simulate close
      vm.isolationError = null
      await wrapper.vm.$nextTick()

      expect(wrapper.text()).not.toContain('Shell isolation is disabled')
      wrapper.unmount()
    })

    it('setConfig failure shows error and clears alert', async () => {
      vi.mocked(api.setConfig).mockRejectedValue(new Error('permission denied'))

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      vm.isolationError = {
        level: 'shell',
        configKey: 'skills.external.allow_shell',
        skillRef: 'test',
        source: 'clawhub',
      }
      await wrapper.vm.$nextTick()

      await vm.handleEnableAndRetry()
      await flushPromises()

      expect(api.setConfig).toHaveBeenCalledWith('skills.external.allow_shell', 'true', false)
      // Alert should be cleared even on failure
      expect(vm.isolationError).toBeNull()
      wrapper.unmount()
    })
  })

  describe('helper functions', () => {
    it('isolationLabel returns correct labels', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      expect(vm.isolationLabel('text_only')).toBe('Text Only')
      expect(vm.isolationLabel('shell')).toBe('Shell')
      expect(vm.isolationLabel('docker')).toBe('Docker')
      expect(vm.isolationLabel('wasm')).toBe('WASM')
      expect(vm.isolationLabel('unknown')).toBe('unknown')
      wrapper.unmount()
    })

    it('parseTags splits comma-separated tags', async () => {
      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      expect(vm.parseTags('weather,forecast')).toEqual(['weather', 'forecast'])
      expect(vm.parseTags('')).toEqual([])
      expect(vm.parseTags('single')).toEqual(['single'])
      wrapper.unmount()
    })

    it('isInstalled checks slug against installed list', async () => {
      vi.mocked(api.listExternalSkills).mockResolvedValue([mockSkill])

      const wrapper = mountComponent()
      await flushPromises()

      const vm = wrapper.findComponent(ExternalSkills).vm as any
      expect(vm.isInstalled('weather-brief')).toBe(true)
      expect(vm.isInstalled('nonexistent')).toBe(false)
      wrapper.unmount()
    })
  })
})
