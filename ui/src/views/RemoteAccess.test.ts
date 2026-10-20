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

})
