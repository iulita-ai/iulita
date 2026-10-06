import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ModelAPIError, modelsApi } from './modelApi'
vi.mock('./api', () => ({ getAccessToken: () => 'test-token' }))
const fetchMock = vi.fn()
beforeEach(() => { vi.stubGlobal('fetch', fetchMock); fetchMock.mockReset() })
describe('model API boundaries', () => {
  it('polls test status with GET and never starts a paid operation', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ id: 'test/one', status: 'running' })))
    await modelsApi.probeStatus('test/one')
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/models/probes/test%2Fone')
    expect(fetchMock.mock.calls[0][1].method).toBe('GET')
    expect(fetchMock.mock.calls[0][1].body).toBeUndefined()
  })
  it('does not retry a paid action after authentication fails', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ code: 'password_change_required', message: 'ignored body' }), { status: 403 }))
    await expect(modelsApi.probe({ expected_revision: 1, profile_id: 'ds-flash', kind: 'text', idempotency_key: 'key' })).rejects.toMatchObject({ code: 'password_change_required', status: 403 })
    expect(fetchMock).toHaveBeenCalledOnce()
  })
  it('does not display arbitrary upstream or proxy error text', async () => {
    fetchMock.mockResolvedValue(new Response('secret=should-not-display', { status: 502 }))
    try { await modelsApi.settings(); throw new Error('expected failure') }
    catch (error) { expect(error).toBeInstanceOf(ModelAPIError); expect((error as Error).message).not.toContain('secret=') }
  })
})
