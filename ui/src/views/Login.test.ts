import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createWebHistory } from 'vue-router'
import Login from './Login.vue'

// Mock api module
vi.mock('../api', () => ({
  api: {
    login: vi.fn(),
    changePassword: vi.fn(),
    getSystem: vi.fn(),
  },
  setTokens: vi.fn(),
  clearTokens: vi.fn(),
}))

import { api, setTokens, clearTokens } from '../api'

function createTestRouter() {
  return createRouter({
    history: createWebHistory(),
    routes: [
      { path: '/login', name: 'login', component: { template: '<div />' } },
      { path: '/', name: 'dashboard', component: { template: '<div />' } },
      { path: '/setup', name: 'setup', component: { template: '<div />' } },
    ],
  })
}

function mountLogin() {
  const router = createTestRouter()
  router.push('/login')

  const wrapper = mount(Login, {
    global: {
      plugins: [router],
    },
  })
  return { wrapper, router }
}

describe('Login.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    // Default: system is normal (not setup mode)
    vi.mocked(api.getSystem).mockResolvedValue({
      app: 'iulita',
      version: '1.0.0',
      uptime: '1h',
      uptime_sec: 3600,
      go_version: 'go1.22',
      started_at: new Date().toISOString(),
      setup_mode: false,
      wizard_completed: true,
    })
  })

  it('renders login form', () => {
    const { wrapper } = mountLogin()
    expect(wrapper.text()).toContain('Iulita')
    expect(wrapper.text()).toContain('Sign In')
  })

  it('shows error when submitting empty form', async () => {
    const { wrapper } = mountLogin()

    // Click Sign In button
    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Username and password required')
    expect(api.login).not.toHaveBeenCalled()
  })

  it('calls login API and redirects to dashboard on success', async () => {
    vi.mocked(api.login).mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      must_change_password: false,
    })

    const { wrapper, router } = mountLogin()
    const pushSpy = vi.spyOn(router, 'push')

    // Set form values via vm
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'pass123'

    // Trigger login
    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    expect(api.login).toHaveBeenCalledWith('admin', 'pass123')
    expect(setTokens).toHaveBeenCalledWith('at', 'rt')
    expect(pushSpy).toHaveBeenCalledWith('/')
  })

  it('shows must_change_password form when required', async () => {
    vi.mocked(api.login).mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      must_change_password: true,
    })

    const { wrapper } = mountLogin()
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'admin'

    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('You must change your password')
    expect(wrapper.text()).toContain('Change Password')
  })

  it('shows error on invalid credentials', async () => {
    vi.mocked(api.login).mockRejectedValue(new Error('API error 401: unauthorized'))

    const { wrapper } = mountLogin()
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'wrong'

    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Invalid credentials')
    expect(clearTokens).toHaveBeenCalled()
  })

  it('redirects to /setup when in setup mode', async () => {
    vi.mocked(api.login).mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      must_change_password: false,
    })
    vi.mocked(api.getSystem).mockResolvedValue({
      app: 'iulita',
      version: '1.0.0',
      uptime: '1m',
      uptime_sec: 60,
      go_version: 'go1.22',
      started_at: new Date().toISOString(),
      setup_mode: true,
      wizard_completed: false,
    })

    const { wrapper, router } = mountLogin()
    const pushSpy = vi.spyOn(router, 'push')
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'pass'

    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    expect(pushSpy).toHaveBeenCalledWith('/setup')
  })

  it('password change validation - too short', async () => {
    vi.mocked(api.login).mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      must_change_password: true,
    })

    const { wrapper } = mountLogin()
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'admin'

    // Trigger login to show password change
    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    // Try short password
    vm.newPassword = '12345'
    vm.confirmPassword = '12345'

    await wrapper.vm.$nextTick()
    const allButtons = wrapper.findAll('button')
    const changeBtn = allButtons.find(b => b.text().includes('Change Password'))
    await changeBtn!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Password must be at least 6 characters')
    expect(api.changePassword).not.toHaveBeenCalled()
  })

  it('password change validation - mismatch', async () => {
    vi.mocked(api.login).mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      must_change_password: true,
    })

    const { wrapper } = mountLogin()
    const vm = wrapper.vm as any
    vm.username = 'admin'
    vm.password = 'admin'

    const buttons = wrapper.findAll('button')
    const signInBtn = buttons.find(b => b.text().includes('Sign In'))
    await signInBtn!.trigger('click')
    await flushPromises()

    vm.newPassword = 'newpass123'
    vm.confirmPassword = 'different'

    await wrapper.vm.$nextTick()
    const allButtons = wrapper.findAll('button')
    const changeBtn = allButtons.find(b => b.text().includes('Change Password'))
    await changeBtn!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Passwords do not match')
  })
})
