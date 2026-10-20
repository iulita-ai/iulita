import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CopyTextButton from './CopyTextButton.vue'

afterEach(() => { vi.unstubAllGlobals() })

describe('CopyTextButton', () => {
  it('copies the exact text once, without metadata or HTML conversion', async () => {
    let finish!: () => void
    const writeText = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const text = '  مرحبا <script>alert(1)</script>\n`{name}` %s €12.50  '
    const wrapper = mount(CopyTextButton, { props: { text } })
    const button = wrapper.get('button')
    expect(button.attributes('type')).toBe('button')
    expect(button.attributes('aria-label')).toBe('Copy assistant message')
    await button.trigger('click')
    expect(writeText).toHaveBeenCalledExactlyOnceWith(text)
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('aria-busy')).toBe('true')
    finish(); await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('Copied')
    expect(button.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('explains clipboard rejection, then allows an explicit retry', async () => {
    const writeText = vi.fn().mockRejectedValueOnce(new Error('private browser error')).mockResolvedValueOnce(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const wrapper = mount(CopyTextButton, { props: { text: 'translation' } })
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('Select the text and copy it manually')
    expect(wrapper.text()).not.toContain('private browser error')
    expect(writeText).toHaveBeenCalledTimes(1)
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('Copied')
    expect(writeText).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('offers manual selection when the browser has no clipboard API', async () => {
    vi.stubGlobal('navigator', {})
    const wrapper = mount(CopyTextButton, { props: { text: 'translation' } })
    await wrapper.get('button').trigger('click')
    expect(wrapper.get('[role="status"]').text()).toBe('Clipboard is unavailable. Select the text and copy it manually.')
    expect(wrapper.find('textarea').exists()).toBe(false)
    wrapper.unmount()
  })
})
