import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import {
  setTokens, clearTokens, getAccessToken, getRefreshToken,
  isLoggedIn, isAdmin, currentUser, parseToken, api,
} from './api'

// Helper: create a fake JWT token with given payload
function fakeJWT(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const body = btoa(JSON.stringify(payload))
  return `${header}.${body}.fake-signature`
}

// Helper: create a valid admin token (expires in 1 hour)
function adminToken(): string {
  return fakeJWT({
    user_id: 'u1',
    username: 'admin',
    role: 'admin',
    exp: Math.floor(Date.now() / 1000) + 3600,
  })
}

// Helper: create a valid user token
function userToken(): string {
  return fakeJWT({
    user_id: 'u2',
    username: 'user',
    role: 'user',
    exp: Math.floor(Date.now() / 1000) + 3600,
  })
}

// Helper: create an expired token
function expiredToken(): string {
  return fakeJWT({
    user_id: 'u1',
    username: 'admin',
    role: 'admin',
    exp: Math.floor(Date.now() / 1000) - 60,
  })
}

describe('Token management', () => {
  beforeEach(() => {
    localStorage.removeItem('iulita_access_token')
    localStorage.removeItem('iulita_refresh_token')
  })

  it('setTokens stores both tokens in localStorage', () => {
    setTokens('access123', 'refresh456')
    expect(getAccessToken()).toBe('access123')
    expect(getRefreshToken()).toBe('refresh456')
  })

  it('clearTokens removes both tokens', () => {
    setTokens('a', 'b')
    clearTokens()
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })

  it('getAccessToken returns null when not set', () => {
    expect(getAccessToken()).toBeNull()
  })

  it('isLoggedIn returns true when token exists', () => {
    setTokens('x', 'y')
    expect(isLoggedIn()).toBe(true)
  })

  it('isLoggedIn returns false when no token', () => {
    expect(isLoggedIn()).toBe(false)
  })
})

describe('parseToken', () => {
  it('parses a valid JWT payload', () => {
    const token = fakeJWT({ user_id: 'u1', username: 'test', role: 'admin', exp: 999 })
    const parsed = parseToken(token)
    expect(parsed).toEqual({ user_id: 'u1', username: 'test', role: 'admin', exp: 999 })
  })

  it('returns null for invalid token', () => {
    expect(parseToken('not-a-jwt')).toBeNull()
    expect(parseToken('')).toBeNull()
  })

  it('returns null for malformed base64', () => {
    expect(parseToken('header.!!!invalid!!!.sig')).toBeNull()
  })
})

describe('currentUser', () => {
  beforeEach(() => {
    localStorage.removeItem('iulita_access_token')
    localStorage.removeItem('iulita_refresh_token')
  })

  it('returns null when no token', () => {
    expect(currentUser()).toBeNull()
  })

  it('returns user info from valid token', () => {
    setTokens(adminToken(), 'refresh')
    const user = currentUser()
    expect(user).toEqual({ user_id: 'u1', username: 'admin', role: 'admin' })
  })

  it('returns null and clears tokens when expired', () => {
    setTokens(expiredToken(), 'refresh')
    expect(currentUser()).toBeNull()
    expect(getAccessToken()).toBeNull()
  })
})

describe('isAdmin', () => {
  beforeEach(() => {
    localStorage.removeItem('iulita_access_token')
    localStorage.removeItem('iulita_refresh_token')
  })

  it('returns true for admin role', () => {
    setTokens(adminToken(), 'r')
    expect(isAdmin()).toBe(true)
  })

  it('returns false for user role', () => {
    setTokens(userToken(), 'r')
    expect(isAdmin()).toBe(false)
  })

  it('returns false when not logged in', () => {
    expect(isAdmin()).toBe(false)
  })
})

describe('API methods (HTTP)', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    localStorage.removeItem('iulita_access_token')
    localStorage.removeItem('iulita_refresh_token')
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  function mockResponse(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  }

  describe('login', () => {
    it('sends POST to /api/auth/login without auth header', async () => {
      fetchMock.mockResolvedValue(mockResponse({
        access_token: 'at', refresh_token: 'rt', must_change_password: false,
      }))
      const resp = await api.login('admin', 'pass')
      expect(resp.access_token).toBe('at')
      expect(fetchMock).toHaveBeenCalledWith('/api/auth/login', expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ username: 'admin', password: 'pass' }),
      }))
      // Verify no Authorization header on public endpoint
      const headers = fetchMock.mock.calls[0][1].headers as Record<string, string>
      expect(headers['Authorization']).toBeUndefined()
    })

    it('throws on 401', async () => {
      fetchMock.mockResolvedValue(new Response('{"error":"bad"}', { status: 401 }))
      await expect(api.login('x', 'y')).rejects.toThrow('API error 401')
    })
  })

  describe('authenticated GET', () => {
    it('sends Authorization header', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({ app: 'iulita' }))

      await api.getSystem()

      const headers = fetchMock.mock.calls[0][1].headers as Record<string, string>
      expect(headers['Authorization']).toMatch(/^Bearer /)
    })

    it('handles API error gracefully', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(new Response('{"error":"not found"}', { status: 404 }))
      await expect(api.getSystem()).rejects.toThrow('not found')
    })
  })

  describe('token refresh on 401', () => {
    it('retries request after successful refresh', async () => {
      setTokens(adminToken(), 'rt')

      // First call: 401, second call (refresh): success, third call (retry): success
      fetchMock
        .mockResolvedValueOnce(new Response('', { status: 401 })) // original GET fails
        .mockResolvedValueOnce(mockResponse({ access_token: 'new-token' })) // refresh succeeds
        .mockResolvedValueOnce(mockResponse({ app: 'iulita' })) // retry succeeds

      const result = await api.getSystem()
      expect(result.app).toBe('iulita')
      expect(fetchMock).toHaveBeenCalledTimes(3)
    })

    it('redirects to login when refresh fails', async () => {
      setTokens(adminToken(), 'rt')

      // Mock window.location
      const locationMock = { href: '' }
      vi.stubGlobal('location', locationMock)

      fetchMock
        .mockResolvedValueOnce(new Response('', { status: 401 })) // original fails
        .mockResolvedValueOnce(new Response('', { status: 401 })) // refresh fails

      await expect(api.getSystem()).rejects.toThrow('Session expired')
      expect(locationMock.href).toBe('/login')
    })
  })

  describe('query parameter building', () => {
    it('getFacts builds correct query string', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse([]))

      await api.getFacts({ chat_id: 'c1', q: 'test', limit: 10 })

      const url = fetchMock.mock.calls[0][0] as string
      expect(url).toContain('chat_id=c1')
      expect(url).toContain('q=test')
      expect(url).toContain('limit=10')
    })

    it('getFacts omits empty params', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse([]))

      await api.getFacts()
      expect(fetchMock.mock.calls[0][0]).toBe('/api/facts')
    })

    it('getMessages includes chat_id and pagination', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse([]))

      await api.getMessages('chat1', { limit: 50, before_id: 100 })

      const url = fetchMock.mock.calls[0][0] as string
      expect(url).toContain('chat_id=chat1')
      expect(url).toContain('limit=50')
      expect(url).toContain('before_id=100')
    })
  })

  describe('PUT and DELETE methods', () => {
    it('setConfig sends PUT with body', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({ status: 'ok', key: 'test.key' }))

      await api.setConfig('test.key', 'value', true)

      expect(fetchMock.mock.calls[0][1].method).toBe('PUT')
      expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({
        value: 'value',
        encrypt: true,
      })
    })

    it('deleteConfig sends DELETE', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({ status: 'ok' }))

      await api.deleteConfig('test.key')

      expect(fetchMock.mock.calls[0][1].method).toBe('DELETE')
      expect(fetchMock.mock.calls[0][0]).toBe('/api/config/test.key')
    })
  })

  describe('wizard API', () => {
    it('getWizardStatus calls correct endpoint', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({
        wizard_completed: false,
        setup_mode: true,
        encryption_enabled: false,
        has_llm_provider: false,
      }))

      const status = await api.getWizardStatus()
      expect(status.setup_mode).toBe(true)
      expect(fetchMock.mock.calls[0][0]).toBe('/api/wizard/status')
    })

    it('completeWizard sends POST', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({ status: 'completed', message: 'ok' }))

      const resp = await api.completeWizard()
      expect(resp.status).toBe('completed')
      expect(fetchMock.mock.calls[0][1].method).toBe('POST')
    })

    it('importTOML sends POST', async () => {
      setTokens(adminToken(), 'rt')
      fetchMock.mockResolvedValue(mockResponse({ imported: 5, skipped: 2, status: 'ok' }))

      const resp = await api.importTOML()
      expect(resp.imported).toBe(5)
      expect(resp.status).toBe('ok')
    })
  })
})
