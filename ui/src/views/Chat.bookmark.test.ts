import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref, nextTick } from 'vue'

// ---- Unit tests for bookmark logic extracted from Chat.vue ----
// These test the bookmark state machine and WS message format
// without mounting the full Chat component (which has heavy dependencies).

interface ChatMessage {
  id: string
  dbId?: number
  role: 'user' | 'assistant' | 'prompt'
  text: string
  timestamp: string
  messageId?: string
  bookmarkState?: 'idle' | 'saving' | 'saved' | 'error'
}

describe('Bookmark feature logic', () => {
  let messages: ChatMessage[]
  let sentMessages: any[]

  function send(data: any) {
    sentMessages.push(data)
  }

  function bookmarkMessage(msg: ChatMessage) {
    if (!msg.messageId || msg.bookmarkState === 'saved' || msg.bookmarkState === 'saving') return
    msg.bookmarkState = 'saving'
    send({ remember_message_id: msg.messageId })
  }

  function handleRememberAck(payload: any) {
    const ack = payload.remember_ack || payload
    const msg = messages.find(m => m.messageId === ack.message_id)
    if (msg) {
      if (ack.status === 'saved') {
        msg.bookmarkState = 'saved'
      } else {
        msg.bookmarkState = 'error'
      }
    }
  }

  beforeEach(() => {
    messages = []
    sentMessages = []
  })

  it('sends correct WS message on bookmark click', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello world',
      timestamp: '12:00',
      messageId: 'nano-123',
      bookmarkState: 'idle',
    }
    messages.push(msg)

    bookmarkMessage(msg)

    expect(sentMessages).toHaveLength(1)
    expect(sentMessages[0]).toEqual({ remember_message_id: 'nano-123' })
    expect(msg.bookmarkState).toBe('saving')
  })

  it('does not send when already saved', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
      messageId: 'nano-123',
      bookmarkState: 'saved',
    }
    messages.push(msg)

    bookmarkMessage(msg)

    expect(sentMessages).toHaveLength(0)
  })

  it('does not send when already saving', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
      messageId: 'nano-123',
      bookmarkState: 'saving',
    }
    messages.push(msg)

    bookmarkMessage(msg)

    expect(sentMessages).toHaveLength(0)
  })

  it('does not send when no messageId', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
    }
    messages.push(msg)

    bookmarkMessage(msg)

    expect(sentMessages).toHaveLength(0)
  })

  it('sets bookmarkState to saved on remember_ack success', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
      messageId: 'nano-456',
      bookmarkState: 'saving',
    }
    messages.push(msg)

    handleRememberAck({ remember_ack: { message_id: 'nano-456', status: 'saved', fact_id: 42 } })

    expect(msg.bookmarkState).toBe('saved')
  })

  it('sets bookmarkState to error on remember_ack failure', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
      messageId: 'nano-789',
      bookmarkState: 'saving',
    }
    messages.push(msg)

    handleRememberAck({ remember_ack: { message_id: 'nano-789', status: 'error', error: 'db error' } })

    expect(msg.bookmarkState).toBe('error')
  })

  it('handles remember_ack for unknown message gracefully', () => {
    const msg: ChatMessage = {
      id: 'msg-1',
      role: 'assistant',
      text: 'Hello',
      timestamp: '12:00',
      messageId: 'nano-111',
      bookmarkState: 'saving',
    }
    messages.push(msg)

    // Different message_id — should not match.
    handleRememberAck({ remember_ack: { message_id: 'nano-999', status: 'saved' } })

    expect(msg.bookmarkState).toBe('saving') // unchanged
  })

  it('sets messageId and bookmarkState from stream_done payload', () => {
    // Simulate what the stream_done handler does.
    const payload = { text: 'Response text', message_id: 'nano-stream-1', timestamp: '2026-03-17T12:00:00Z' }
    const msg: ChatMessage = {
      id: `msg-${Date.now()}`,
      role: 'assistant',
      text: payload.text,
      timestamp: payload.timestamp,
      messageId: payload.message_id || undefined,
      bookmarkState: payload.message_id ? 'idle' : undefined,
    }
    messages.push(msg)

    expect(msg.messageId).toBe('nano-stream-1')
    expect(msg.bookmarkState).toBe('idle')
  })

  it('message without message_id has no bookmark state', () => {
    const payload = { text: 'Response', timestamp: '2026-03-17T12:00:00Z' }
    const msg: ChatMessage = {
      id: `msg-${Date.now()}`,
      role: 'assistant',
      text: payload.text,
      timestamp: payload.timestamp,
      messageId: (payload as any).message_id || undefined,
      bookmarkState: (payload as any).message_id ? 'idle' : undefined,
    }
    messages.push(msg)

    expect(msg.messageId).toBeUndefined()
    expect(msg.bookmarkState).toBeUndefined()
  })
})
