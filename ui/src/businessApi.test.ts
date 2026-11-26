import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { businessApi, BusinessApiError, defaultBusinessAutoRules, validBusinessAutoRules } from './businessApi'
const token = vi.hoisted(() => ({ value: 'owner-session' as string | null }))
const cleared = vi.hoisted(() => vi.fn())
vi.mock('./api', () => ({ getAccessToken: () => token.value, clearTokens: () => { cleared(); token.value = null } }))
beforeEach(() => { token.value = 'owner-session'; cleared.mockClear() })
afterEach(() => vi.unstubAllGlobals())
const draft = { id: 'draft/a', revision: 7, connection_id: 'local/ref', peer_id: '42', text: 'Exact reviewed reply', state: 'approved', approval_id: 'server-issued', digest: 'server-digest' }

describe('Business action transport boundary', () => {
  it('does not issue a request without an authenticated owner session', async () => {
    token.value = null
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    await expect(businessApi.connections()).rejects.toMatchObject({ status: 401, code: 'unauthorized' })
    expect(fetch).not.toHaveBeenCalled()
  })
  it.each([401, 403, 409, 429, 503])('never replays a mutation on HTTP %s or leaks server text', async status => {
    const fetch = vi.fn().mockResolvedValue(new Response('PRIVATE SERVER CONTENT', { status })); vi.stubGlobal('fetch', fetch)
    let error: unknown
    try { await businessApi.executeDraft(draft) } catch (cause) { error = cause }
    expect(error).toBeInstanceOf(BusinessApiError)
    expect((error as Error).message).not.toContain('PRIVATE SERVER CONTENT')
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('treats an unreadable successful mutation response as uncertain without replay', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response('{truncated', { status: 200 })); vi.stubGlobal('fetch', fetch)
    await expect(businessApi.approveDraft(draft)).rejects.toMatchObject({ code: 'unknown_outcome' })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('rebind is an explicit revision-bound local operation', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ connection: {} }))); vi.stubGlobal('fetch', fetch)
    await businessApi.rebind('local/ref', 9)
    expect(fetch).toHaveBeenCalledWith('/api/telegram-business/connections/local%2Fref/rebind', expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_revision: 9 }) }))
  })
  it('known unauthorized response clears the current local session without replaying a mutation', async () => {
    const fetch=vi.fn().mockResolvedValue(new Response('{}',{status:401})); vi.stubGlobal('fetch',fetch)
    await expect(businessApi.executeDraft(draft)).rejects.toMatchObject({status:401,code:'unauthorized'})
    expect(cleared).toHaveBeenCalledTimes(1); expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('a late unauthorized response cannot clear a newer session token', async () => {
    vi.stubGlobal('fetch',vi.fn().mockImplementation(async()=>{token.value='new-session';return new Response('{}',{status:401})}))
    await expect(businessApi.connections()).rejects.toMatchObject({status:401})
    expect(cleared).not.toHaveBeenCalled(); expect(token.value).toBe('new-session')
  })
  it('returns an uncertain result after a lost mutation response, without another send', async () => {
    const fetch = vi.fn().mockRejectedValue(new TypeError('PRIVATE ENDPOINT')); vi.stubGlobal('fetch', fetch)
    await expect(businessApi.executeDraft(draft)).rejects.toMatchObject({ status: 0, code: 'unknown_outcome' })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('uses encoded references and exact revision-bound approval without an owner override', async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ draft }))); vi.stubGlobal('fetch', fetch)
    await businessApi.executeDraft(draft)
    expect(fetch).toHaveBeenCalledWith('/api/telegram-business/drafts/draft%2Fa/execute', expect.objectContaining({ method: 'POST', cache: 'no-store', headers: expect.objectContaining({ Authorization: 'Bearer owner-session' }), body: JSON.stringify({ expected_revision: 7, approval_id: 'server-issued' }) }))
    await businessApi.messages('local/ref', '42')
    expect(fetch.mock.calls[1]![0]).toBe('/api/telegram-business/connections/local%2Fref/peers/42/messages?limit=20')
  })
  it('keeps approval and execution separate and forwards only the server digest', async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ draft }))); vi.stubGlobal('fetch', fetch)
    await businessApi.approveDraft(draft)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch.mock.calls[0]![0]).toMatch(/\/approve$/)
    expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({ expected_revision: 7, expected_digest: 'server-digest' })
  })
  it('uses exact action scope, server digest and next approval step without replay', async () => {
    const action = {id:'action/ref',method:'transferGift',params:{owned_gift_id:'gift'},digest:'exact',state:'prepared',required_steps:2,approval_steps:1,expires_at:'2099-01-01'}
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ action }))); vi.stubGlobal('fetch', fetch)
    await businessApi.approveAction('local/ref',undefined,action)
    expect(fetch).toHaveBeenCalledWith('/api/telegram-business/connections/local%2Fref/actions/action%2Fref/approve',expect.objectContaining({method:'POST',body:JSON.stringify({digest:'exact',step:2})}))
    await businessApi.prepareAction('local/ref','42',3,'readBusinessMessage',{message_id:12})
    expect(fetch.mock.calls[1]![0]).toBe('/api/telegram-business/connections/local%2Fref/peers/42/actions')
    expect(JSON.parse(fetch.mock.calls[1]![1].body)).toEqual({expected_revision:3,method:'readBusinessMessage',params:{message_id:12}})
  })
  it('preserves a safe conflict code but discards arbitrary error content', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 'revision_conflict', error: 'PRIVATE' }), { status: 409 })))
    await expect(businessApi.editDraft(draft.id, 6, 'new')).rejects.toMatchObject({ status: 409, code: 'revision_conflict', message: 'revision_conflict' })
  })
})

describe('Automatic reply rule boundaries', () => {
  it('uses finite staged defaults without enabling or sending anything', () => {
    expect(defaultBusinessAutoRules()).toEqual({classes:['greeting'],timezone:'UTC',start_hour:9,end_hour:18,max_replies_per_day:5})
    expect(validBusinessAutoRules(defaultBusinessAutoRules())).toBe(true)
  })
  it('rejects unknown classes, invalid zones, equal hours and unbounded daily caps', () => {
    for(const patch of [{classes:[]},{classes:['all_messages']},{classes:['greeting','greeting']},{timezone:'not/a/timezone'},{start_hour:25},{start_hour:9,end_hour:9},{max_replies_per_day:0},{max_replies_per_day:11}])expect(validBusinessAutoRules({...defaultBusinessAutoRules(),...patch})).toBe(false)
    expect(validBusinessAutoRules({...defaultBusinessAutoRules(),start_hour:22,end_hour:6,timezone:'Europe/Moscow'})).toBe(true)
  })
})
