import { ref, onUnmounted } from 'vue'

export interface WSMessage {
  type: string
  payload: unknown
}

export function useWebSocket(path: string) {
  const connected = ref(false)
  const lastMessage = ref<WSMessage | null>(null)
  let ws: WebSocket | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let intentionalClose = false

  const listeners = new Map<string, Set<(payload: unknown) => void>>()

  function connect() {
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
      return
    }

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}${path}`
    ws = new WebSocket(url)

    ws.onopen = () => {
      connected.value = true
    }

    ws.onclose = () => {
      connected.value = false
      if (!intentionalClose) {
        reconnectTimer = setTimeout(connect, 3000)
      }
    }

    ws.onerror = () => {
      ws?.close()
    }

    ws.onmessage = (event) => {
      try {
        const msg: WSMessage = JSON.parse(event.data)
        lastMessage.value = msg
        const handlers = listeners.get(msg.type)
        if (handlers) {
          const data = msg.payload !== undefined ? msg.payload : msg
          handlers.forEach((fn) => fn(data))
        }
        const allHandlers = listeners.get('*')
        if (allHandlers) {
          allHandlers.forEach((fn) => fn(msg))
        }
      } catch {
        // ignore non-JSON messages
      }
    }
  }

  function send(data: unknown) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(data))
    }
  }

  function on(type: string, handler: (payload: unknown) => void) {
    if (!listeners.has(type)) {
      listeners.set(type, new Set())
    }
    listeners.get(type)!.add(handler)
  }

  function off(type: string, handler: (payload: unknown) => void) {
    listeners.get(type)?.delete(handler)
  }

  function close() {
    intentionalClose = true
    if (reconnectTimer) clearTimeout(reconnectTimer)
    ws?.close()
    ws = null
    connected.value = false
  }

  onUnmounted(close)

  return { connected, lastMessage, connect, send, on, off, close }
}
