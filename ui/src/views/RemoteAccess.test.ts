import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import RemoteAccess from './RemoteAccess.vue'
import { remoteAccessApi } from '../remoteAccessApi'
vi.mock('../remoteAccessApi',()=>({remoteAccessApi:{snapshot:vi.fn(),approvals:vi.fn(),keyOperations:vi.fn(),save:vi.fn(),enabled:vi.fn(),preview:vi.fn(),execute:vi.fn(),cancel:vi.fn(),artifact:vi.fn()}}))
const snapshot={enabled:true,servers:[{id:'s',label:'Target',address:'host',port:22,host_keys:[],trust_source:'console',enabled:true}],identities:[{id:'i',username:'deploy',auth_type:'private_key',enabled:true}],routes:[{id:'r',label:'Production',hops:[{server_id:'s',identity_id:'i'}],enabled:true}],grants:[],executions:[]}
beforeEach(()=>{vi.clearAllMocks();vi.mocked(remoteAccessApi.keyOperations).mockResolvedValue([]);vi.mocked(remoteAccessApi.approvals).mockResolvedValue([]);vi.mocked(remoteAccessApi.snapshot).mockResolvedValue(structuredClone(snapshot))})
function button(w:ReturnType<typeof mount>,text:string){return w.findAll('button').find(b=>b.text()===text)!}
describe('Remote SSH dashboard',()=>{
  it('shows unknown outcomes without suggesting a retry',async()=>{
    vi.mocked(remoteAccessApi.snapshot).mockResolvedValue({...structuredClone(snapshot),executions:[{id:'x',state:'unknown_outcome',route_id:'r'}]})
    const w=mount(RemoteAccess);await flushPromises();await w.findAll('.n-tabs-tab').find(t=>t.text()==='Operations')!.trigger('click');await flushPromises()
    expect(w.text()).toContain('The command may have completed');expect(w.text()).toContain('No automatic retry');w.unmount()
  })
  it('renders imported secrets as masked write-only fields and clears them on close',async()=>{
    const w=mount(RemoteAccess,{attachTo:document.body});await flushPromises();await w.findAll('.n-tabs-tab').find(t=>t.text()==='Identities')!.trigger('click');await flushPromises();await button(w,'Edit').trigger('click');await flushPromises()
    const secrets=document.querySelectorAll('input[type=password]');expect(secrets.length).toBe(2);expect((secrets[0] as HTMLInputElement).value).toBe('');expect(w.text()).not.toContain('credential_ref');w.unmount()
  })
  it('keeps metadata edits free of secret fields and strict API unknown fields',async()=>{
    const w=mount(RemoteAccess);await flushPromises();await w.findAll('.n-tabs-tab').find(t=>t.text()==='Identities')!.trigger('click');await flushPromises();await button(w,'Edit').trigger('click');await flushPromises()
    await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.save).toHaveBeenCalledWith('identities',{identity:{username:'deploy',auth_type:'private_key',enabled:true}},'i');w.unmount()
  })
  it('previews a command without executing and requires explicit confirmation',async()=>{
    vi.mocked(remoteAccessApi.preview).mockResolvedValue({approval_id:'nonce',execution_id:'x',plan_hash:'hash',command:'uptime',servers:snapshot.servers,identities:snapshot.identities,expires_at:'soon'})
    const w=mount(RemoteAccess);await flushPromises();await w.findAll('.n-tabs-tab').find(t=>t.text()==='Operations')!.trigger('click');await flushPromises();await button(w,'Prepare operation').trigger('click');await flushPromises()
    await w.find('select').setValue('r');await w.find('textarea').setValue('uptime');await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.execute).not.toHaveBeenCalled();await button(w,'Allow one execution').trigger('click');await flushPromises()
    expect(remoteAccessApi.execute).toHaveBeenCalledWith({route_id:'r',command:'uptime',timeout_seconds:60,expected_plan_hash:'hash',approval_id:'nonce'});w.unmount()
  })

  it('preserves PEM newlines when pasted into the masked key import',async()=>{
    const w=mount(RemoteAccess);await flushPromises();await w.findAll('.n-tabs-tab').find(t=>t.text()==='Identities')!.trigger('click');await flushPromises();await button(w,'Edit').trigger('click');await flushPromises()
    const key='-----BEGIN OPENSSH PRIVATE KEY-----\nbase64\n-----END OPENSSH PRIVATE KEY-----\n'
    await w.find('input[type=password]').trigger('paste',{clipboardData:{getData:()=>key}});await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.save).toHaveBeenCalledWith('identities',expect.objectContaining({secret:{private_key:key}}),'i');w.unmount()
  })

  async function editGrant(grant:Record<string,unknown>){
    vi.mocked(remoteAccessApi.snapshot).mockResolvedValue({...structuredClone(snapshot),grants:[{id:'g',caller_id:'dashboard',route_id:'r',capability:'exec',route_revision:1,enabled:true,...grant}]})
    const w=mount(RemoteAccess);await flushPromises()
    await w.findAll('.n-tabs-tab').find(t=>t.text()==='Permissions')!.trigger('click');await flushPromises()
    await button(w,'Edit').trigger('click');await flushPromises();return w
  }
  it('converts a temporary permission to permanent without sending a stale expiration',async()=>{
    const w=await editGrant({expires_at:'2026-10-22T08:20:00Z'})
    expect(w.find('input[type="datetime-local"]').attributes('required')).toBeDefined()
    await w.findAll('input[type=checkbox]')[1]!.setValue(true)
    expect(w.find('input[type="datetime-local"]').exists()).toBe(false)
    await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.save).toHaveBeenCalledWith('grants',{enabled:true,caller_id:'dashboard',route_id:'r',capability:'exec',permanent:true},'g');w.unmount()
  })
  it('preserves explicit permanent permissions with a backend zero date',async()=>{
    const w=await editGrant({permanent:true,expires_at:'0001-01-01T00:00:00Z'})
    expect(w.text()).toContain('No expiration');expect(w.text()).not.toContain('0001-')
    expect(w.find('input[type="datetime-local"]').exists()).toBe(false)
    await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.save).toHaveBeenCalledWith('grants',expect.objectContaining({permanent:true}),'g')
    expect(vi.mocked(remoteAccessApi.save).mock.calls[0]![1]).not.toHaveProperty('expires_at');w.unmount()
  })
  it('requires an expiration when changing a permanent permission to temporary',async()=>{
    const w=await editGrant({permanent:true,expires_at:'0001-01-01T00:00:00Z'})
    await w.findAll('input[type=checkbox]')[1]!.setValue(false)
    const date=w.find('input[type="datetime-local"]');expect((date.element as HTMLInputElement).value).toBe('')
    expect(date.attributes('required')).toBeDefined();await date.setValue('2026-10-22T10:00')
    await w.find('form').trigger('submit');await flushPromises()
    expect(remoteAccessApi.save).toHaveBeenCalledWith('grants',expect.objectContaining({permanent:false,expires_at:new Date('2026-10-22T10:00').toISOString()}),'g');w.unmount()
  })
  it('defaults new permissions to temporary without inferring permanence from a missing expiry',async()=>{
    const w=await editGrant({expires_at:'0001-01-01T00:00:00Z'})
    expect((w.findAll('input[type=checkbox]')[1]!.element as HTMLInputElement).checked).toBe(false)
    expect((w.find('input[type="datetime-local"]').element as HTMLInputElement).value).toBe('')
    await button(w,'Cancel').trigger('click');await flushPromises();await button(w,'Add').trigger('click');await flushPromises()
    expect((w.findAll('input[type=checkbox]')[1]!.element as HTMLInputElement).checked).toBe(false)
    expect(w.find('input[type="datetime-local"]').attributes('required')).toBeDefined();w.unmount()
  })

})
