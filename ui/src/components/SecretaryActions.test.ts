import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SecretaryActions from './SecretaryActions.vue'
import { businessApi, type BusinessAction, type BusinessConnection } from '../businessApi'
import i18n from '../i18n'
vi.mock('../businessApi', async () => {
  const actual = await vi.importActual<typeof import('../businessApi')>('../businessApi')
  return { ...actual, businessApi: Object.fromEntries(['listActions','prepareAction','getAction','approveAction','executeAction','cancelAction','policy','peerPolicy','uploadMedia'].map(key => [key, vi.fn()])) }
})
const account: BusinessConnection = { id:'account', revision:4, account_label:'Owner account', account_ref:'1234', bot_ref:'9876', state:'confirmed_send', is_enabled:true, mode:'confirmed_send', processing_consent:true, rights:{can_transfer_stars:true,can_edit_name:true}, allowed_methods:['transferBusinessAccountStars'], policy_version:'policy-current' }
const action: BusinessAction = { id:'operation', method:'transferBusinessAccountStars', params:{star_count:50}, digest:'server-exact', state:'prepared', approval_steps:0, required_steps:2, expires_at:'2099-01-01T00:00:00Z' }
const button = (wrapper: ReturnType<typeof mount>, label: string) => wrapper.findAll('button').find(item => item.text() === label)!
beforeEach(() => { vi.clearAllMocks(); i18n.global.locale.value='en'; Object.defineProperty(window,'confirm',{value:vi.fn(()=>true),configurable:true,writable:true}); vi.mocked(businessApi.listActions).mockResolvedValue({actions:[]}); vi.mocked(businessApi.prepareAction).mockResolvedValue({action}); vi.mocked(businessApi.policy).mockResolvedValue({connection:account}) })
describe('Typed account action review', () => {
  it('requires two separate explicit financial approvals and never executes while live actions are disabled', async () => {
    vi.mocked(businessApi.approveAction).mockResolvedValueOnce({action:{...action,approval_steps:1}}).mockResolvedValueOnce({action:{...action,approval_steps:2,state:'approved'}})
    const wrapper=mount(SecretaryActions,{props:{connection:account,liveWrites:false}}); await flushPromises()
    expect(businessApi.prepareAction).not.toHaveBeenCalled(); expect(businessApi.getAction).not.toHaveBeenCalled()
    await wrapper.findAll('select')[1]!.setValue(action.method); await flushPromises()
    await wrapper.find('input[type=number]').setValue('50')
    await wrapper.find('form').trigger('submit'); await flushPromises()
    expect(businessApi.prepareAction).toHaveBeenCalledWith(account.id,undefined,4,action.method,{star_count:50})
    expect(wrapper.find('.preview').text()).toContain('Owner account'); expect(wrapper.find('.preview').text()).toContain('50')
    expect(button(wrapper,'Confirm 1 of 2').attributes('disabled')).toBeDefined()
    await wrapper.find('.preview input[type=checkbox]').setValue(true)
    await button(wrapper,'Confirm 1 of 2').trigger('click'); await flushPromises()
    expect(businessApi.approveAction).toHaveBeenCalledTimes(1)
    expect(button(wrapper,'Confirm 2 of 2').attributes('disabled')).toBeDefined()
    await wrapper.find('.preview input[type=checkbox]').setValue(true)
    await button(wrapper,'Confirm 2 of 2').trigger('click'); await flushPromises()
    expect(businessApi.approveAction).toHaveBeenCalledTimes(2)
    expect(button(wrapper,'Execute this approved operation').attributes('disabled')).toBeDefined()
    await button(wrapper,'Execute this approved operation').trigger('click')
    expect(businessApi.executeAction).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('grants only the explicitly selected method using the current owner policy revision', async () => {
    const wrapper=mount(SecretaryActions,{props:{connection:{...account,allowed_methods:[]},liveWrites:false}}); await flushPromises()
    expect(businessApi.policy).not.toHaveBeenCalled()
    await button(wrapper,'Allow this method after approval').trigger('click'); await flushPromises()
    expect(businessApi.policy).toHaveBeenCalledWith(account.id,{expected_revision:4,mode:'confirmed_send',processing_consent:true,policy_version:'policy-current',auto_reply:false,media_consent:false,media_provider:undefined,token_only:false,allowed_methods:['setBusinessAccountName']})
    expect(wrapper.emitted('refresh')).toHaveLength(1); wrapper.unmount()
  })
  it('restores a saved unknown operation with a read and prevents a new preparation', async () => {
    const unknown = {...action,state:'unknown'}
    vi.mocked(businessApi.listActions).mockResolvedValue({actions:[unknown]})
    vi.mocked(businessApi.getAction).mockResolvedValue({action:unknown})
    const wrapper=mount(SecretaryActions,{props:{connection:{...account,actions:[unknown]},liveWrites:false}}); await flushPromises()
    await button(wrapper,'Transfer Stars to the bot · Outcome unknown').trigger('click'); await flushPromises()
    expect(businessApi.getAction).toHaveBeenCalledWith(account.id,undefined,action.id)
    expect(button(wrapper,'Prepare exact preview').attributes('disabled')).toBeDefined()
    expect(businessApi.prepareAction).not.toHaveBeenCalled(); expect(businessApi.approveAction).not.toHaveBeenCalled(); expect(businessApi.executeAction).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('keeps the newest unknown operation discoverable when the scope has more than twenty operations', async () => {
    const actions=Array.from({length:25},(_,index)=>({...action,id:`operation-${index}`,state:index===24?'unknown':'sent'}))
    vi.mocked(businessApi.listActions).mockResolvedValue({actions})
    const wrapper=mount(SecretaryActions,{props:{connection:account,liveWrites:false}}); await flushPromises()
    const savedButtons=wrapper.findAll('.saved-actions button')
    expect(savedButtons).toHaveLength(20)
    expect(savedButtons[0]!.text()).toContain('Outcome unknown')
    vi.mocked(businessApi.getAction).mockResolvedValue({action:actions[24]!})
    await savedButtons[0]!.trigger('click'); await flushPromises()
    expect(businessApi.getAction).toHaveBeenCalledWith(account.id,undefined,'operation-24')
    expect(businessApi.executeAction).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('editing a pending preview requires explicit local cancellation first', async () => {
    const wrapper=mount(SecretaryActions,{props:{connection:account,liveWrites:false}}); await flushPromises()
    await wrapper.findAll('select')[1]!.setValue(action.method); await wrapper.find('input[type=number]').setValue('50'); await wrapper.find('form').trigger('submit'); await flushPromises()
    expect(wrapper.find('.preview').exists()).toBe(true)
    expect(wrapper.find('input[type=number]').attributes('disabled')).toBeDefined()
    vi.mocked(businessApi.cancelAction).mockResolvedValue({action:{...action,state:'cancelled'}})
    await button(wrapper,'Cancel local approval').trigger('click'); await flushPromises()
    expect(businessApi.cancelAction).toHaveBeenCalledWith(account.id,undefined,action)
    expect(wrapper.find('input[type=number]').attributes('disabled')).toBeUndefined()
    await wrapper.find('input[type=number]').setValue('51'); await flushPromises()
    expect(wrapper.find('.preview').exists()).toBe(false); expect(businessApi.approveAction).not.toHaveBeenCalled(); wrapper.unmount()
  })
})
