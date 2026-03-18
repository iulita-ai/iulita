import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider } from 'naive-ui'
import Usage from './Usage.vue'

// Mock api module.
vi.mock('../api', () => ({
  api: {
    getUsageByDay: vi.fn(),
    getUsageByModel: vi.fn(),
  },
}))

import { api } from '../api'

const mockDailyResponse = {
  summary: {
    total_input_tokens: 15000,
    total_output_tokens: 7500,
    total_cache_read_tokens: 3000,
    total_cache_creation_tokens: 500,
    total_requests: 42,
    total_cost_usd: 0.1234,
  },
  rows: [
    { date: '2026-03-18', input_tokens: 10000, output_tokens: 5000, cache_read_tokens: 2000, cache_creation_tokens: 300, requests: 30, cost_usd: 0.08 },
    { date: '2026-03-17', input_tokens: 5000, output_tokens: 2500, cache_read_tokens: 1000, cache_creation_tokens: 200, requests: 12, cost_usd: 0.0434 },
  ],
}

const mockModelResponse = {
  rows: [
    { model: 'claude-sonnet', provider: 'anthropic', input_tokens: 12000, output_tokens: 6000, cache_read_tokens: 2500, cache_creation_tokens: 400, requests: 35, cost_usd: 0.10 },
    { model: 'gpt-4o', provider: 'openai', input_tokens: 3000, output_tokens: 1500, cache_read_tokens: 500, cache_creation_tokens: 100, requests: 7, cost_usd: 0.0234 },
  ],
}

// Wrap component in NMessageProvider since Usage uses useMessage().
function mountUsage() {
  const Wrapper = defineComponent({
    render() {
      return h(NMessageProvider, () => h(Usage))
    },
  })

  return mount(Wrapper, {
    attachTo: document.body,
  })
}

describe('Usage.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders loading state initially', () => {
    // Make API calls hang so we can observe loading state.
    vi.mocked(api.getUsageByDay).mockReturnValue(new Promise(() => {}))
    vi.mocked(api.getUsageByModel).mockReturnValue(new Promise(() => {}))

    const wrapper = mountUsage()
    // The n-spin component should be present with show=true.
    const spin = wrapper.find('.n-spin-container')
    expect(spin.exists()).toBe(true)
    wrapper.unmount()
  })

  it('fetches data and displays KPI statistics cards', async () => {
    vi.mocked(api.getUsageByDay).mockResolvedValue(mockDailyResponse)
    vi.mocked(api.getUsageByModel).mockResolvedValue(mockModelResponse)

    const wrapper = mountUsage()
    await flushPromises()

    const text = wrapper.text()

    // KPI cards should display summary values.
    // NStatistic may or may not locale-format numbers depending on environment.
    expect(text).toMatch(/15[,.]?000/)  // total_input_tokens
    expect(text).toMatch(/7[,.]?500/)   // total_output_tokens
    expect(text).toMatch(/3[,.]?000/)   // total_cache_read_tokens
    expect(text).toContain('42')        // total_requests
    expect(text).toContain('$0.1234')   // total_cost_usd

    wrapper.unmount()
  })

  it('displays model breakdown table', async () => {
    vi.mocked(api.getUsageByDay).mockResolvedValue(mockDailyResponse)
    vi.mocked(api.getUsageByModel).mockResolvedValue(mockModelResponse)

    const wrapper = mountUsage()
    await flushPromises()

    const text = wrapper.text()

    expect(text).toContain('claude-sonnet')
    expect(text).toContain('anthropic')
    expect(text).toContain('gpt-4o')
    expect(text).toContain('openai')

    wrapper.unmount()
  })

  it('displays daily breakdown table', async () => {
    vi.mocked(api.getUsageByDay).mockResolvedValue(mockDailyResponse)
    vi.mocked(api.getUsageByModel).mockResolvedValue(mockModelResponse)

    const wrapper = mountUsage()
    await flushPromises()

    const text = wrapper.text()

    expect(text).toContain('2026-03-18')
    expect(text).toContain('2026-03-17')

    wrapper.unmount()
  })

  it('handles empty data gracefully', async () => {
    vi.mocked(api.getUsageByDay).mockResolvedValue({
      summary: {
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_cache_read_tokens: 0,
        total_cache_creation_tokens: 0,
        total_requests: 0,
        total_cost_usd: 0,
      },
      rows: [],
    })
    vi.mocked(api.getUsageByModel).mockResolvedValue({ rows: [] })

    const wrapper = mountUsage()
    await flushPromises()

    const text = wrapper.text()

    // KPI cards should show zero values.
    expect(text).toContain('$0.0000')

    wrapper.unmount()
  })

  it('calls API with correct parameters on mount', async () => {
    vi.mocked(api.getUsageByDay).mockResolvedValue(mockDailyResponse)
    vi.mocked(api.getUsageByModel).mockResolvedValue(mockModelResponse)

    const wrapper = mountUsage()
    await flushPromises()

    // Both endpoints should be called.
    expect(api.getUsageByDay).toHaveBeenCalledTimes(1)
    expect(api.getUsageByModel).toHaveBeenCalledTimes(1)

    // getUsageByDay should receive from/to date params (default 30 days).
    const dayCall = vi.mocked(api.getUsageByDay).mock.calls[0][0]
    expect(dayCall).toHaveProperty('from')
    expect(dayCall).toHaveProperty('to')

    wrapper.unmount()
  })
})
