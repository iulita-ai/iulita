import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, config } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { useWebSocket } from './useWebSocket'

function validToken(name: string, expires = Math.floor(Date.now() / 1000) + 3600) {
  return `${btoa('{}')}.${btoa(JSON.stringify({ name, exp: expires }))}.signature`
}

// Mock WebSocket
class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  readyState = MockWebSocket.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  url: string
  sentMessages: string[] = []

  constructor(url: string) {
    this.url = url
    // Auto-track instances
    MockWebSocket.instances.push(this)
  }

  send(data: string) {
    this.sentMessages.push(data)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
    if (this.onclose) {
      this.onclose(new CloseEvent('close'))
    }
  }

  // Simulate server opening connection
  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    if (this.onopen) this.onopen(new Event('open'))
  }

  // Simulate receiving a message
  simulateMessage(data: unknown) {
    if (this.onmessage) {
      this.onmessage(new MessageEvent('message', { data: JSON.stringify(data) }))
    }
  }

  // Simulate error
  simulateError() {
    if (this.onerror) this.onerror(new Event('error'))
  }

  static instances: MockWebSocket[] = []
  static reset() {
    MockWebSocket.instances = []
  }
}

// Helper: create a wrapper component that uses the composable
function createWrapper() {
  let composableResult: ReturnType<typeof useWebSocket> | undefined

  const TestComponent = defineComponent({
    setup() {
      composableResult = useWebSocket('/ws/test')
      return composableResult
    },
    template: '<div>{{ connected }}</div>',
  })

  const wrapper = mount(TestComponent)
  return { wrapper, ws: () => composableResult! }
}

describe('useWebSocket', () => {
  beforeEach(() => {
    localStorage.setItem('iulita_access_token', validToken('initial'))
    localStorage.removeItem('iulita_refresh_token')
    vi.useFakeTimers()
    MockWebSocket.reset()
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    localStorage.removeItem('iulita_access_token')
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('starts disconnected', () => {
    const { ws } = createWrapper()
    expect(ws().connected.value).toBe(false)
    expect(ws().lastMessage.value).toBeNull()
  })

  it('connects and sets connected=true', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    await nextTick()

    expect(ws().connected.value).toBe(true)
  })

  it('builds WebSocket URL from location', () => {
    const { ws } = createWrapper()
    ws().connect()
    const instance = MockWebSocket.instances[0]
    expect(instance.url).toContain('/ws/test')
  })

  it('authenticates every reconnect with the current access token', () => {
    const first = validToken('first')
    localStorage.setItem('iulita_access_token', first)
    const { ws } = createWrapper()
    ws().connect()
    expect(new URL(MockWebSocket.instances[0].url).searchParams.get('token')).toBe(first)
    MockWebSocket.instances[0].simulateOpen()
    const refreshed = validToken('refreshed')
    localStorage.setItem('iulita_access_token', refreshed)
    MockWebSocket.instances[0].close()
    vi.advanceTimersByTime(3000)
    expect(new URL(MockWebSocket.instances[1].url).searchParams.get('token')).toBe(refreshed)
    ws().close()
  })

  it('refreshes an expired session before opening a WebSocket', async () => {
    localStorage.setItem('iulita_access_token', validToken('expired', 1))
    localStorage.setItem('iulita_refresh_token', 'refresh-secret')
    const refreshed = validToken('new')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ access_token: refreshed }) }))
    const { ws } = createWrapper()
    ws().connect()
    expect(MockWebSocket.instances).toHaveLength(0)
    await vi.waitFor(() => expect(MockWebSocket.instances).toHaveLength(1))
    expect(new URL(MockWebSocket.instances[0].url).searchParams.get('token')).toBe(refreshed)
    expect(fetch).toHaveBeenCalledWith('/api/auth/refresh', expect.any(Object))
    ws().close()
    vi.unstubAllGlobals()
  })

  it('never opens an anonymous WebSocket', () => {
    localStorage.removeItem('iulita_access_token')
    const { ws } = createWrapper()
    ws().connect()
    expect(MockWebSocket.instances).toHaveLength(0)
  })

  it('receives and parses JSON messages', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.simulateMessage({ type: 'update', payload: { count: 42 } })
    await nextTick()

    expect(ws().lastMessage.value).toEqual({ type: 'update', payload: { count: 42 } })
  })

  it('calls type-specific listeners', async () => {
    const { ws } = createWrapper()
    const handler = vi.fn()
    ws().on('update', handler)
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.simulateMessage({ type: 'update', payload: { x: 1 } })
    await nextTick()

    expect(handler).toHaveBeenCalledWith({ x: 1 })
  })

  it('calls wildcard listeners with full message', async () => {
    const { ws } = createWrapper()
    const handler = vi.fn()
    ws().on('*', handler)
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.simulateMessage({ type: 'foo', payload: 'bar' })
    await nextTick()

    expect(handler).toHaveBeenCalledWith({ type: 'foo', payload: 'bar' })
  })

  it('does not call unrelated type listeners', async () => {
    const { ws } = createWrapper()
    const handler = vi.fn()
    ws().on('other', handler)
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.simulateMessage({ type: 'update', payload: {} })
    await nextTick()

    expect(handler).not.toHaveBeenCalled()
  })

  it('off() removes a listener', async () => {
    const { ws } = createWrapper()
    const handler = vi.fn()
    ws().on('update', handler)
    ws().off('update', handler)
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.simulateMessage({ type: 'update', payload: {} })
    await nextTick()

    expect(handler).not.toHaveBeenCalled()
  })

  it('send() serializes and sends JSON', () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    ws().send({ action: 'ping' })

    expect(instance.sentMessages).toEqual(['{"action":"ping"}'])
  })

  it('send() does nothing when not connected', () => {
    const { ws } = createWrapper()
    ws().connect()
    // Don't call simulateOpen — still CONNECTING
    ws().send({ action: 'ping' })

    expect(MockWebSocket.instances[0].sentMessages).toEqual([])
  })

  it('close() prevents auto-reconnect', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    ws().close()

    expect(ws().connected.value).toBe(false)

    // Advance timers — should NOT create a new WebSocket
    vi.advanceTimersByTime(5000)
    expect(MockWebSocket.instances.length).toBe(1)
  })

  it('auto-reconnects after unintentional close', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()

    // Simulate server disconnect (not intentional)
    instance.close()
    await nextTick()

    expect(ws().connected.value).toBe(false)

    // Advance past reconnect timer (3s)
    vi.advanceTimersByTime(3500)
    expect(MockWebSocket.instances.length).toBe(2)
  })

  it('does not reconnect twice for same disconnect', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()
    instance.close()
    await nextTick()

    vi.advanceTimersByTime(3500)
    // Should only have created 1 new connection
    expect(MockWebSocket.instances.length).toBe(2)
  })

  it('ignores non-JSON messages', async () => {
    const { ws } = createWrapper()
    ws().connect()

    const instance = MockWebSocket.instances[0]
    instance.simulateOpen()

    // Send a non-JSON message
    if (instance.onmessage) {
      instance.onmessage(new MessageEvent('message', { data: 'not json' }))
    }
    await nextTick()

    // lastMessage should remain null
    expect(ws().lastMessage.value).toBeNull()
  })
})
