import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setTokens, clearTokens, api } from './api'

function adminToken(): string {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const body = btoa(JSON.stringify({ user_id: 'u1', username: 'admin', role: 'admin', exp: Math.floor(Date.now() / 1000) + 3600 }))
  return `${header}.${body}.sig`
}

describe('import API', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    setTokens(adminToken(), 'refresh')
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    clearTokens()
    vi.unstubAllGlobals()
  })

  function ok(body: unknown) {
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }

  it('getImportStatus GETs the status endpoint', async () => {
    fetchMock.mockResolvedValue(ok([{ job_id: 'a', status: 'done' }]))
    const runs = await api.getImportStatus()
    expect(fetchMock).toHaveBeenCalledWith('/api/import/status', expect.anything())
    expect(runs).toHaveLength(1)
    expect(runs[0].job_id).toBe('a')
  })

  it('searchImported encodes the query and returns results', async () => {
    fetchMock.mockResolvedValue(ok({ results: [{ message_id: 1, snippet: 'x' }], vector_search: true }))
    const res = await api.searchImported('kube cluster', 15)
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('/api/import/search?q=kube%20cluster')
    expect(url).toContain('limit=15')
    expect(res.vector_search).toBe(true)
    expect(res.results).toHaveLength(1)
  })

  it('cancelImport DELETEs the job endpoint with an encoded id', async () => {
    fetchMock.mockResolvedValue(ok({ status: 'canceled' }))
    const res = await api.cancelImport('sha/with:special')
    const [url, opts] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/import/sha%2Fwith%3Aspecial')
    expect(opts.method).toBe('DELETE')
    expect(res.status).toBe('canceled')
  })

  it('listImportedConversations passes pagination', async () => {
    fetchMock.mockResolvedValue(ok([]))
    await api.listImportedConversations(25, 50)
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('/api/import/conversations?limit=25&offset=50')
  })

  it('getImportedConversationMessages encodes the uuid', async () => {
    fetchMock.mockResolvedValue(ok([]))
    await api.getImportedConversationMessages('c 1')
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toBe('/api/import/conversations/c%201')
  })

  it('sends the Authorization header', async () => {
    fetchMock.mockResolvedValue(ok([]))
    await api.getImportStatus()
    const opts = fetchMock.mock.calls[0][1]
    expect(opts.headers.Authorization).toMatch(/^Bearer /)
  })
})
