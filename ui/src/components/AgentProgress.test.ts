import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import AgentProgress from './AgentProgress.vue'
import type { AgentInfo } from './agentTypes'
import en from '../i18n/locales/en.json'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackLocale: 'en',
  messages: { en },
})

function mountComponent(agents: AgentInfo[], orchestrationActive = true) {
  return mount(AgentProgress, {
    props: { agents, orchestrationActive },
    global: { plugins: [i18n] },
  })
}

describe('AgentProgress', () => {
  it('renders nothing when no agents and inactive', () => {
    const wrapper = mountComponent([], false)
    expect(wrapper.find('.agent-progress').exists()).toBe(false)
  })

  it('renders agent list when active', () => {
    const agents: AgentInfo[] = [
      { id: 'researcher1', type: 'researcher', status: 'running' },
      { id: 'analyst1', type: 'analyst', status: 'running' },
    ]
    const wrapper = mountComponent(agents)
    expect(wrapper.find('.agent-progress').exists()).toBe(true)
    expect(wrapper.findAll('.agent-item')).toHaveLength(2)
    expect(wrapper.text()).toContain('researcher1')
    expect(wrapper.text()).toContain('analyst1')
  })

  it('shows correct icons for agent types', () => {
    const agents: AgentInfo[] = [
      { id: 'r', type: 'researcher', status: 'running' },
      { id: 'c', type: 'coder', status: 'running' },
    ]
    const wrapper = mountComponent(agents)
    const icons = wrapper.findAll('.agent-icon')
    expect(icons[0].text()).toBe('🔍')
    expect(icons[1].text()).toBe('💻')
  })

  it('shows completion state', () => {
    const agents: AgentInfo[] = [
      { id: 'a1', type: 'generic', status: 'completed', durationMs: 1500 },
    ]
    const wrapper = mountComponent(agents)
    expect(wrapper.find('.agent-item.completed').exists()).toBe(true)
    expect(wrapper.text()).toContain('✅')
    expect(wrapper.text()).toContain('1.5s')
  })

  it('shows error state', () => {
    const agents: AgentInfo[] = [
      { id: 'a1', type: 'generic', status: 'failed', error: 'timeout exceeded' },
    ]
    const wrapper = mountComponent(agents)
    expect(wrapper.find('.agent-item.failed').exists()).toBe(true)
    expect(wrapper.text()).toContain('❌')
    expect(wrapper.text()).toContain('timeout exceeded')
  })

  it('shows turn progress for running agents', () => {
    const agents: AgentInfo[] = [
      { id: 'a1', type: 'researcher', status: 'running', turn: 3 },
    ]
    const wrapper = mountComponent(agents)
    expect(wrapper.text()).toContain('turn 3')
  })

  it('truncates long error messages', () => {
    const longError = 'A'.repeat(50)
    const agents: AgentInfo[] = [
      { id: 'a1', type: 'generic', status: 'failed', error: longError },
    ]
    const wrapper = mountComponent(agents)
    expect(wrapper.find('.agent-error').text()).toContain('...')
  })
})
