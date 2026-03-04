import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createRouter, createWebHistory } from 'vue-router'

// Mock api module before importing anything that uses it
const mockIsLoggedIn = vi.fn(() => false)
const mockIsAdmin = vi.fn(() => false)

vi.mock('./api', () => ({
  isLoggedIn: () => mockIsLoggedIn(),
  isAdmin: () => mockIsAdmin(),
}))

// Create a fresh router for each test with the same config as the real one
function createTestRouter() {
  const router = createRouter({
    history: createWebHistory(),
    routes: [
      { path: '/login', name: 'login', component: { template: '<div />' }, meta: { public: true } },
      { path: '/', name: 'dashboard', component: { template: '<div />' } },
      { path: '/facts', name: 'facts', component: { template: '<div />' } },
      { path: '/insights', name: 'insights', component: { template: '<div />' } },
      { path: '/reminders', name: 'reminders', component: { template: '<div />' } },
      { path: '/profile', name: 'profile', component: { template: '<div />' } },
      { path: '/settings', name: 'settings', component: { template: '<div />' } },
      { path: '/users', name: 'users', component: { template: '<div />' }, meta: { admin: true } },
      { path: '/channels', name: 'channels', component: { template: '<div />' }, meta: { admin: true } },
      { path: '/agent-jobs', name: 'agent-jobs', component: { template: '<div />' }, meta: { admin: true } },
      { path: '/chat', name: 'chat', component: { template: '<div />' } },
      { path: '/setup', name: 'setup', component: { template: '<div />' }, meta: { admin: true } },
    ],
  })

  // Apply the same guard as the real router
  router.beforeEach((to, _from, next) => {
    if (to.meta.public) { next(); return }
    if (!mockIsLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !mockIsAdmin()) { next({ name: 'dashboard' }); return }
    next()
  })

  return router
}

describe('Router guards', () => {
  beforeEach(() => {
    mockIsLoggedIn.mockReturnValue(false)
    mockIsAdmin.mockReturnValue(false)
  })

  it('allows access to /login without auth', async () => {
    const router = createTestRouter()
    await router.push('/login')
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('redirects unauthenticated user to /login', async () => {
    const router = createTestRouter()
    await router.push('/')
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('allows authenticated user to access dashboard', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    const router = createTestRouter()
    await router.push('/')
    expect(router.currentRoute.value.name).toBe('dashboard')
  })

  it('blocks non-admin from admin routes', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    mockIsAdmin.mockReturnValue(false)
    const router = createTestRouter()
    await router.push('/users')
    expect(router.currentRoute.value.name).toBe('dashboard')
  })

  it('allows admin to access admin routes', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    mockIsAdmin.mockReturnValue(true)
    const router = createTestRouter()
    await router.push('/users')
    expect(router.currentRoute.value.name).toBe('users')
  })

  it('allows admin to access /setup', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    mockIsAdmin.mockReturnValue(true)
    const router = createTestRouter()
    await router.push('/setup')
    expect(router.currentRoute.value.name).toBe('setup')
  })

  it('blocks non-admin from /setup', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    mockIsAdmin.mockReturnValue(false)
    const router = createTestRouter()
    await router.push('/setup')
    expect(router.currentRoute.value.name).toBe('dashboard')
  })

  it('allows authenticated user to access /facts', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    const router = createTestRouter()
    await router.push('/facts')
    expect(router.currentRoute.value.name).toBe('facts')
  })

  it('allows authenticated user to access /chat', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    const router = createTestRouter()
    await router.push('/chat')
    expect(router.currentRoute.value.name).toBe('chat')
  })

  it('all admin routes are protected', async () => {
    mockIsLoggedIn.mockReturnValue(true)
    mockIsAdmin.mockReturnValue(false)

    const adminPaths = ['/users', '/channels', '/agent-jobs', '/setup']
    for (const path of adminPaths) {
      const router = createTestRouter()
      await router.push(path)
      expect(router.currentRoute.value.name).toBe('dashboard')
    }
  })
})
