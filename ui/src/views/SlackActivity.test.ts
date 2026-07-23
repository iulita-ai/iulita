import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider } from 'naive-ui'
import SlackActivity from './SlackActivity.vue'

vi.mock('../api', () => ({
  api: {
    getSlackActivity: vi.fn(),
  },
}))

import { api } from '../api'
import type { SlackActivityEntry } from '../api'

// Detail shapes mirror the server-whitelisted responses: search rows carry
// mode/outcome/result_count; post rows carry channel/decision (no `mode`).
const entries: SlackActivityEntry[] = [
  { id: 2, action: 'slack.search.ok', success: true, detail: { mode: 'user', outcome: 'ok', result_count: 7 }, created_at: '2026-07-20T10:00:00Z' },
  { id: 1, action: 'slack.post.sent', success: true, detail: { channel: 'C1', decision: 'draft_approved' }, created_at: '2026-07-19T09:00:00Z' },
]

function mountView() {
  const Wrapper = defineComponent({
    render() {
      return h(NMessageProvider, () => h(SlackActivity))
    },
  })
  return mount(Wrapper, { attachTo: document.body })
}

describe('SlackActivity.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('fetches activity on mount with a limit', async () => {
    vi.mocked(api.getSlackActivity).mockResolvedValue(entries)
    const wrapper = mountView()
    await flushPromises()
    expect(api.getSlackActivity).toHaveBeenCalledTimes(1)
    expect(api.getSlackActivity).toHaveBeenCalledWith(200)
    wrapper.unmount()
  })

  it('renders action rows and metadata-only detail', async () => {
    vi.mocked(api.getSlackActivity).mockResolvedValue(entries)
    const wrapper = mountView()
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('slack.search.ok')
    expect(text).toContain('slack.post.sent')
    // metadata rendered
    expect(text).toContain('result_count=7')
    expect(text).toContain('decision=draft_approved')
    // the target channel is shown for post rows (the core audit question)
    expect(text).toContain('channel=C1')
    wrapper.unmount()
  })

  it('shows empty state when there is no activity', async () => {
    vi.mocked(api.getSlackActivity).mockResolvedValue([])
    const wrapper = mountView()
    await flushPromises()
    // n-empty renders; table should not be present
    expect(wrapper.find('.n-data-table').exists()).toBe(false)
    wrapper.unmount()
  })

  it('handles a null response gracefully', async () => {
    vi.mocked(api.getSlackActivity).mockResolvedValue(null as unknown as SlackActivityEntry[])
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('.n-data-table').exists()).toBe(false)
    wrapper.unmount()
  })
})
