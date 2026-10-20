import { afterEach, describe, expect, it, vi } from 'vitest'
import { remoteAccessApi } from './remoteAccessApi'
vi.mock('./api',()=>({getAccessToken:()=> 'session-token'}))
afterEach(()=>vi.unstubAllGlobals())
describe('SSH API safety',()=>{
  it('normalizes owner-scoped catalog maps for the view',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(JSON.stringify({enabled:false,servers:{a:{id:'a'}}}))))
    const s=await remoteAccessApi.snapshot();expect(s.servers).toEqual([{id:'a'}]);expect(s.identities).toEqual([])
  })
  it('never replays a command or displays server response bodies after authentication failure',async()=>{
    const fetch=vi.fn().mockResolvedValue(new Response('SECRET',{status:401}));vi.stubGlobal('fetch',fetch)
    await expect(remoteAccessApi.execute({command:'uptime'})).rejects.toThrow('Remote access request failed (401)')
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('binds explicit execution approval to the preview hash',async()=>{
    const fetch=vi.fn().mockResolvedValue(new Response('{}'));vi.stubGlobal('fetch',fetch)
    await remoteAccessApi.execute({route_id:'route',command:'uptime',expected_plan_hash:'hash'})
    expect(fetch).toHaveBeenCalledWith('/api/remote-access/executions',expect.objectContaining({method:'POST',body:JSON.stringify({route_id:'route',command:'uptime',expected_plan_hash:'hash'})}))
  })
})
