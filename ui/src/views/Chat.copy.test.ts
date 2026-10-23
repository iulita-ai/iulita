import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import Chat from './Chat.vue'
import { api } from '../api'

const { handlers, send } = vi.hoisted(() => ({ handlers: new Map<string, (payload: unknown) => void>(), send: vi.fn() }))
vi.mock('../api', () => ({ currentUser: () => ({ user_id: 'one' }), api: { getMessages: vi.fn() } }))
vi.mock('../composables/useLocale', () => ({ useLocale: () => ({ setLocale: vi.fn() }) }))
vi.mock('../composables/useWebSocket', async () => {
  const { ref } = await import('vue')
  return { useWebSocket: () => ({ connected: ref(true), connect: vi.fn(), send, on: (name: string, handler: (payload: unknown) => void) => handlers.set(name, handler) }) }
})

beforeEach(() => { vi.clearAllMocks(); handlers.clear(); vi.mocked(api.getMessages).mockResolvedValue([]) })
afterEach(() => { vi.unstubAllGlobals() })

describe('Chat copy and text delivery', () => {
  it('copies an archived assistant message without timestamps and leaves user text plain', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const content = '  שלום <script>alert(1)</script>\n`{name}` %s  '
    vi.mocked(api.getMessages).mockResolvedValue([
      { ID: 1, Role: 'user', Content: 'Translate this', ChatID: 'web:one', CreatedAt: '2026-10-20T12:00:00Z' },
      { ID: 2, Role: 'assistant', Content: content, ChatID: 'web:one', CreatedAt: '2026-10-20T12:00:01Z' },
    ])
    const wrapper = mount(Chat); await flushPromises()
    const copies = wrapper.findAll('button[aria-label="Copy assistant message"]')
    expect(copies).toHaveLength(1)
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.findAll('[dir="auto"]')[1]!.element.textContent).toBe(content)
    await copies[0]!.trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledExactlyOnceWith(content)
    expect(send).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('copies the final stream text and preserves the existing bookmark action', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const wrapper = mount(Chat); await flushPromises()
    const text = 'مرحبا\n<name> $12.50'
    handlers.get('stream_edit')!({ text: 'partial' }); await flushPromises()
    expect(wrapper.find('button[aria-label="Copy assistant message"]').exists()).toBe(false)
    handlers.get('stream_done')!({ text, message_id: 'translation-one', timestamp: '2026-10-20T12:00:01Z' }); await flushPromises()
    await wrapper.get('button[aria-label="Copy assistant message"]').trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledExactlyOnceWith(text)
    expect(wrapper.get('[dir="auto"]').element.textContent).toBe(text)
    await wrapper.get('button.bookmark-btn').trigger('click')
    expect(send).toHaveBeenCalledExactlyOnceWith({ remember_message_id: 'translation-one' })
    wrapper.unmount()
  })
  it('shows private SSH output literally without assistant actions or resending it', async () => {
    const wrapper = mount(Chat); await flushPromises()
    const text = '<script>alert(1)</script>\n**stdout**\n<img src=x onerror=alert(1)>\n$HOME'
    handlers.get('private_result')!({text, message_id:'never-bookmark-this'}); await flushPromises()
    expect(wrapper.get('.private-result pre').element.textContent).toBe(text)
    expect(wrapper.find('script').exists()).toBe(false);expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('If you approved model analysis')
    expect(wrapper.find('button.bookmark-btn').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="Copy assistant message"]').exists()).toBe(false)
    expect(send).not.toHaveBeenCalled();expect(api.getMessages).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    const fresh=mount(Chat);await flushPromises();expect(fresh.find('.private-result').exists()).toBe(false);fresh.unmount()
  })
  it('ignores malformed private result events',async()=>{
    const wrapper=mount(Chat);await flushPromises()
    handlers.get('private_result')!(null);handlers.get('private_result')!({text:{html:'untrusted'}});await flushPromises()
    expect(wrapper.find('.private-result').exists()).toBe(false);expect(send).not.toHaveBeenCalled();wrapper.unmount()
  })

})
