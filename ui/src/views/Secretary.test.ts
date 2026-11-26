import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import Secretary from './Secretary.vue'
import { businessApi, BusinessApiError, type BusinessConnection, type BusinessDraft, type BusinessMessages } from '../businessApi'
import i18n from '../i18n'
vi.mock('../api', () => ({ onLogout: vi.fn() }))
vi.mock('../businessApi', async () => {
  const actual = await vi.importActual<typeof import('../businessApi')>('../businessApi')
  return { ...actual, businessApi: Object.fromEntries(['connections', 'peers', 'messages', 'policy', 'peerPolicy', 'createDraft', 'editDraft', 'approveDraft', 'executeDraft', 'pause', 'purge', 'rebind', 'export', 'listActions', 'prepareAction', 'getAction', 'approveAction', 'executeAction', 'uploadMedia', 'compact', 'rejectDraft', 'cancelAction'].map(name => [name, vi.fn()])) }
})
const account: BusinessConnection = { id: 'account-a', revision: 1, account_label: 'Owner account', account_ref: 'owner …1234', state: 'connected', is_enabled: true, rights: { can_reply: true }, mode: 'confirmed_send', processing_consent: true, provider_available: true, provider_label: 'Configured model', retention_days: 7 }
const peer = { id: '42', revision: 2, label: 'Selected contact', recipient_ref: 'contact …0042', mode: 'confirmed_send' as const, processing_consent: true }
const draft: BusinessDraft = { id: 'draft-a', revision: 3, connection_id: account.id, peer_id: peer.id, text: 'Exact reply for this contact', state: 'draft', expires_at: '2099-01-01T00:00:00Z' }
const button = (wrapper: ReturnType<typeof mount>, text: string) => wrapper.findAll('button').find(item => item.text() === text)!
beforeEach(() => {
  vi.clearAllMocks(); i18n.global.locale.value = 'en'
  vi.mocked(businessApi.listActions).mockResolvedValue({ actions: [] })
  vi.mocked(businessApi.connections).mockResolvedValue({ connections: [structuredClone(account)], live_writes_enabled: false, capabilities: [] })
  vi.mocked(businessApi.peers).mockResolvedValue({ peers: [structuredClone(peer)] })
  vi.mocked(businessApi.messages).mockResolvedValue({ messages: [], drafts: [structuredClone(draft)] })
  vi.mocked(businessApi.approveDraft).mockResolvedValue({ draft: { ...draft, state: 'approved', revision: 4, approval_id: 'server' } })
  Object.defineProperty(window, 'confirm', { value: vi.fn(() => true), configurable: true, writable: true })
})

describe('Secretary owner review boundaries', () => {
  it('mount reads only; approving never executes when external actions are disabled', async () => {
    const wrapper = mount(Secretary); await flushPromises()
    expect(businessApi.createDraft).not.toHaveBeenCalled(); expect(businessApi.policy).not.toHaveBeenCalled()
    await button(wrapper, 'Review exact reply').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('Exact reply for this contact')
    await button(wrapper, 'Approve this exact text').trigger('click'); await flushPromises()
    expect(businessApi.approveDraft).toHaveBeenCalledTimes(1)
    const send = button(wrapper, 'Send this approved reply'); expect(send.attributes('disabled')).toBeDefined()
    await send.trigger('click'); expect(businessApi.executeDraft).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('unsaved edits invalidate approval of the old stored text', async () => {
    const wrapper = mount(Secretary); await flushPromises()
    await button(wrapper, 'Edit').trigger('click')
    await wrapper.find('textarea').setValue('A different reply')
    expect(button(wrapper, 'Review exact reply').attributes('disabled')).toBeDefined()
    expect(businessApi.approveDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('rejecting a draft cancels only its revision-bound local object without executing', async () => {
    vi.mocked(businessApi.rejectDraft).mockResolvedValue({rejected:true})
    const wrapper=mount(Secretary); await flushPromises()
    await button(wrapper,'Discard local draft').trigger('click'); await flushPromises()
    expect(businessApi.rejectDraft).toHaveBeenCalledWith(draft)
    expect(wrapper.text()).toContain('No new reply was sent')
    expect(businessApi.executeDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('does not call an unavailable model or assume consent from a connection grant', async () => {
    vi.mocked(businessApi.connections).mockResolvedValue({ connections: [{ ...account, mode: 'off', processing_consent: false, provider_available: false }], live_writes_enabled: false, capabilities: [] })
    const wrapper = mount(Secretary); await flushPromises()
    expect(wrapper.find('input[type=checkbox]').element).toHaveProperty('checked', false)
    expect(button(wrapper, 'Generate a draft').attributes('disabled')).toBeDefined()
    await button(wrapper, 'Generate a draft').trigger('click'); expect(businessApi.createDraft).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('unknown model pricing needs explicit token-only acknowledgement while manual drafts remain available', async () => {
    vi.mocked(businessApi.connections).mockResolvedValue({ connections: [{...account,requires_token_only:true,token_only:false,policy_version:'current'}], live_writes_enabled:false,capabilities:[] })
    vi.mocked(businessApi.policy).mockResolvedValue({connection:{...account,requires_token_only:true,token_only:true,policy_version:'current'}})
    const wrapper=mount(Secretary); await flushPromises()
    expect(button(wrapper,'Generate a draft').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('Manual drafts remain available')
    await wrapper.find('textarea').setValue('Manual draft'); expect(button(wrapper,'Save a manual draft').attributes('disabled')).toBeUndefined()
    const checks=wrapper.findAll('.form input[type=checkbox]')
    expect(checks[1]!.element).toHaveProperty('checked',false)
    await checks[1]!.setValue(true)
    await wrapper.find('.form').trigger('submit'); await flushPromises()
    expect(businessApi.policy).toHaveBeenCalledWith(account.id,expect.objectContaining({expected_revision:1,token_only:true,policy_version:'current'}))
    expect(businessApi.createDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('selecting automatic mode cannot save without its separate explicit acknowledgement', async () => {
    vi.mocked(businessApi.policy).mockResolvedValue({connection:{...account,mode:'auto',auto_reply:true}})
    const wrapper=mount(Secretary); await flushPromises()
    await wrapper.find('#business-mode').setValue('auto')
    await wrapper.find('.form').trigger('submit'); await flushPromises()
    expect(businessApi.policy).not.toHaveBeenCalled()
    const consent=wrapper.findAll('.form label').find(item=>item.text().includes('explicitly allow automatic replies'))!
    await consent.find('input').setValue(true)
    await wrapper.find('.form').trigger('submit'); await flushPromises()
    expect(businessApi.policy).toHaveBeenCalledWith(account.id,expect.objectContaining({mode:'auto',auto_reply:true}))
    expect(businessApi.executeDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('automatic account consent never silently opts the selected chat into automatic replies', async () => {
    vi.mocked(businessApi.connections).mockResolvedValue({connections:[{...account,mode:'auto',state:'auto',auto_reply:true}],live_writes_enabled:false,capabilities:[]})
    vi.mocked(businessApi.peerPolicy).mockResolvedValue({peer:{...peer,mode:'auto',auto_reply:true}})
    const wrapper=mount(Secretary); await flushPromises()
    expect(button(wrapper,'Enable automatic replies for this chat').attributes('disabled')).toBeDefined()
    const consent=wrapper.findAll('label').find(item=>item.text().includes('explicitly allow automatic replies to new messages in this selected chat'))!
    await consent.find('input').setValue(true)
    await button(wrapper,'Enable automatic replies for this chat').trigger('click'); await flushPromises()
    expect(businessApi.peerPolicy).toHaveBeenCalledWith(account.id,peer.id,expect.objectContaining({mode:'auto',auto_reply:true,allowed_methods:['sendMessage']}))
    expect(businessApi.createDraft).not.toHaveBeenCalled(); expect(businessApi.executeDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('shows persisted context sizes on load and after a read-only refresh', async () => {
    vi.mocked(businessApi.messages).mockResolvedValue({messages:[],drafts:[],context:{changed:true,before_bytes:900,after_bytes:300,messages:4}})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.text()).toContain('900 → 300 bytes; 4 source messages')
    await button(wrapper,'Refresh status').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('900 → 300 bytes; 4 source messages')
    expect(businessApi.compact).not.toHaveBeenCalled(); expect(businessApi.createDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('attachment consent is explicit for account and selected chat and never fetches media on load', async () => {
    const mediaAccount={...account,media_available:true,media_provider:'media-v1:local-PDF;stt-fixture',media_consent:false}
    vi.mocked(businessApi.connections).mockResolvedValue({connections:[mediaAccount],live_writes_enabled:false,capabilities:[]})
    vi.mocked(businessApi.policy).mockResolvedValue({connection:{...mediaAccount,media_consent:true}})
    vi.mocked(businessApi.peerPolicy).mockResolvedValue({peer:{...peer,media_consent:true}})
    const wrapper=mount(Secretary); await flushPromises()
    const accountConsent=wrapper.findAll('.form label').find(item=>item.text().includes('explicitly allow attachment processing'))!
    expect(accountConsent.find('input').element).toHaveProperty('checked',false)
    expect(businessApi.uploadMedia).not.toHaveBeenCalled(); expect(businessApi.createDraft).not.toHaveBeenCalled()
    await accountConsent.find('input').setValue(true); await wrapper.find('.form').trigger('submit'); await flushPromises()
    expect(businessApi.policy).toHaveBeenCalledWith(account.id,expect.objectContaining({media_consent:true,media_provider:mediaAccount.media_provider}))
    const chatConsent=wrapper.findAll('label').find(item=>item.text().includes('explicitly allow processing the latest incoming attachments in this chat'))!
    expect(chatConsent.find('input').element).toHaveProperty('checked',false)
    await chatConsent.find('input').setValue(true); await button(wrapper,'Save chat attachment consent').trigger('click'); await flushPromises()
    expect(businessApi.peerPolicy).toHaveBeenCalledWith(account.id,peer.id,expect.objectContaining({media_consent:true,media_provider:mediaAccount.media_provider}))
    expect(businessApi.createDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('renders cached attachment text as a distinct untrusted plain-text source', async () => {
    vi.mocked(businessApi.messages).mockResolvedValue({messages:[{id:'voice',direction:'incoming',text:'',created_at:'',extracted_text:'<script>untrusted</script>',extracted_provider:'media-fixture'}],drafts:[],media_omitted:2})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.find('.messages details').text()).toContain('Attachment transcript / extracted text — untrusted source')
    expect(wrapper.find('.messages details p').text()).toBe('<script>untrusted</script>')
    expect(wrapper.find('.messages script').exists()).toBe(false)
    expect(wrapper.text()).toContain('2 older attachments were omitted')
    expect(businessApi.createDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('shows a closed reply window separately from draft expiry without blocking manual drafts', async () => {
    vi.mocked(businessApi.peers).mockResolvedValue({peers:[{...peer,reply_until:'2026-01-01T00:00:00Z',reply_available:false}]})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.text()).toContain('Telegram reply window ends')
    expect(wrapper.text()).toContain('A new incoming message is needed')
    await wrapper.find('textarea').setValue('Useful local draft')
    expect(button(wrapper,'Save a manual draft').attributes('disabled')).toBeUndefined()
    await wrapper.find('textarea').setValue('')
    await button(wrapper,'Review exact reply').trigger('click'); await flushPromises()
    expect(button(wrapper,'Approve this exact text').attributes('disabled')).toBeDefined()
    expect(businessApi.approveDraft).not.toHaveBeenCalled()
    await button(wrapper,'Cancel').trigger('click'); await button(wrapper,'Refresh status').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('A new incoming message is needed')
    expect(wrapper.text()).not.toContain('The item changed')
    wrapper.unmount()
  })
  it('explains metadata-only storage readiness and cannot activate body processing', async () => {
    vi.mocked(businessApi.connections).mockResolvedValue({connections:[{...account,state:'metadata_only',mode:'off',storage_ready:false,processing_consent:false}],live_writes_enabled:false,capabilities:[]})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.text()).toContain('administrator must configure encrypted storage')
    expect(wrapper.find('.form input[type=checkbox]').attributes('disabled')).toBeDefined()
    await wrapper.find('#business-mode').setValue('draft_only')
    await wrapper.find('.form').trigger('submit'); await flushPromises()
    expect(businessApi.policy).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('shows safe owner processing failures without raw provider details', async () => {
    vi.mocked(businessApi.peers).mockResolvedValue({peers:[{...peer,last_processing_state:'failed',last_processing_reason:'PRIVATE PROVIDER DETAIL',last_processed_at:'2026-11-26T12:00:00Z'}]})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.text()).toContain('Automatic processing did not complete')
    expect(wrapper.text()).not.toContain('PRIVATE PROVIDER DETAIL'); wrapper.unmount()
  })
  it('unknown automatic send outcome never recommends a retry', async () => {
    vi.mocked(businessApi.peers).mockResolvedValue({peers:[{...peer,last_processing_state:'failed',last_processing_reason:'unknown_outcome'}]})
    const wrapper=mount(Secretary); await flushPromises()
    expect(wrapper.text()).toContain('The send outcome is unknown. Do not resend automatically')
    expect(wrapper.text()).not.toContain('Automatic processing did not complete. Refresh status or request a manual draft')
    wrapper.unmount()
  })
  it('manual context compaction reports the committed size change without generating a reply', async () => {
    vi.mocked(businessApi.compact).mockResolvedValue({context:{changed:true,before_bytes:400,after_bytes:100,messages:2}})
    const wrapper=mount(Secretary); await flushPromises()
    await button(wrapper,'Compact saved chat context').trigger('click'); await flushPromises()
    expect(businessApi.compact).toHaveBeenCalledWith(account.id,peer.id,peer.revision)
    expect(wrapper.text()).toContain('400 → 100 bytes; 2 source messages')
    expect(businessApi.createDraft).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('a late old-connection response cannot appear under the new selected account', async () => {
    let resolveOld!: (value: BusinessMessages) => void
    vi.mocked(businessApi.connections).mockResolvedValue({ connections: [account, { ...account, id: 'account-b', account_label: 'Second account' }], live_writes_enabled: false, capabilities: [] })
    vi.mocked(businessApi.messages).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve })).mockResolvedValue({ messages: [{ id: 'new', direction: 'incoming', text: 'NEW ACCOUNT', created_at: '' }], drafts: [] })
    const wrapper = mount(Secretary); await flushPromises()
    await wrapper.findAll('.connection-choice')[1]!.trigger('click'); await flushPromises()
    resolveOld({ messages: [{ id: 'old', direction: 'incoming', text: 'OLD PRIVATE CONTENT', created_at: '' }], drafts: [] }); await flushPromises()
    expect(wrapper.text()).toContain('NEW ACCOUNT'); expect(wrapper.text()).not.toContain('OLD PRIVATE CONTENT')
    wrapper.unmount()
  })
  it('pause remains available during generation and its newer snapshot rejects a late model result', async () => {
    let finishGeneration!: (value: { draft: BusinessDraft }) => void
    vi.mocked(businessApi.createDraft).mockImplementation(() => new Promise(resolve => { finishGeneration = resolve }))
    vi.mocked(businessApi.pause).mockResolvedValue({ connection: { ...account, paused: true, state: 'paused', revision: 2 } })
    vi.mocked(businessApi.connections).mockResolvedValueOnce({ connections: [account], live_writes_enabled: false, capabilities: [] }).mockResolvedValue({ connections: [{ ...account, paused: true, state: 'paused', revision: 2 }], live_writes_enabled: false, capabilities: [] })
    const wrapper = mount(Secretary); await flushPromises()
    await button(wrapper, 'Generate a draft').trigger('click'); await flushPromises()
    expect(button(wrapper, 'Pause processing').attributes('disabled')).toBeUndefined()
    await button(wrapper, 'Pause processing').trigger('click'); await flushPromises()
    expect(businessApi.pause).toHaveBeenCalledWith(account.id, account.revision, true, undefined)
    finishGeneration({ draft: { ...draft, text: 'STALE MODEL OUTPUT' } }); await flushPromises()
    expect(wrapper.text()).not.toContain('STALE MODEL OUTPUT')
    expect(button(wrapper, 'Generate a draft').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('requires explicit peer mode upgrade after the account switches to confirmed replies', async () => {
    vi.mocked(businessApi.peers).mockResolvedValue({ peers: [{ ...peer, mode: 'draft_only' }] })
    vi.mocked(businessApi.peerPolicy).mockResolvedValue({ peer })
    const wrapper = mount(Secretary); await flushPromises()
    await button(wrapper, 'Review exact reply').trigger('click'); await flushPromises()
    expect(button(wrapper, 'Approve this exact text').attributes('disabled')).toBeDefined()
    await button(wrapper, 'Cancel').trigger('click'); await flushPromises()
    await button(wrapper, 'Use the account reply mode').trigger('click'); await flushPromises()
    expect(businessApi.peerPolicy).toHaveBeenCalledWith(account.id, peer.id, expect.objectContaining({ expected_revision: peer.revision, mode: 'confirmed_send', processing_consent: true, token_only: false }))
    wrapper.unmount()
  })

  it('chat erasure names the scope, resets consent when reopened, and forwards only its revision', async () => {
    vi.mocked(businessApi.purge).mockResolvedValue({ purged: true })
    const wrapper = mount(Secretary); await flushPromises()
    await button(wrapper, 'Erase this chat’s local data').trigger('click'); await flushPromises()
    const checks = () => wrapper.findAll('input[type=checkbox]')
    await checks()[checks().length - 1]!.setValue(true)
    await button(wrapper, 'Cancel').trigger('click'); await flushPromises()
    await button(wrapper, 'Erase this chat’s local data').trigger('click'); await flushPromises()
    expect(checks()[checks().length - 1]!.element).toHaveProperty('checked', false)
    await checks()[checks().length - 1]!.setValue(true)
    await wrapper.findAll('button').filter(item => item.text() === 'Erase local data').slice(-1)[0]!.trigger('click'); await flushPromises()
    expect(businessApi.purge).toHaveBeenCalledWith(account.id, peer.revision, peer.id)
    wrapper.unmount()
  })

  it('unavailable is not an empty inbox and raw server errors are never rendered', async () => {
    vi.mocked(businessApi.connections).mockRejectedValue(new BusinessApiError(503, 'unavailable'))
    const wrapper = mount(Secretary); await flushPromises()
    expect(wrapper.find('.error[role=alert]').text()).toContain('Could not load')
    expect(wrapper.text()).not.toContain('No connection discovered yet'); wrapper.unmount()
  })
})
