<template>
  <div style="height: calc(100vh - 80px); display: flex; flex-direction: column;">
    <n-page-header :title="t('chat.title')" :subtitle="connected ? t('chat.connected') : t('chat.disconnected')" style="flex-shrink: 0;">
      <template #extra>
        <n-tag :type="connected ? 'success' : 'error'" size="small">
          {{ connected ? t('chat.online') : t('chat.offline') }}
        </n-tag>
      </template>
    </n-page-header>

    <div
      ref="messagesContainer"
      style="flex: 1; overflow-y: auto; min-height: 0; padding: 8px 0;"
      @scroll="handleScroll"
    >
      <!-- Loading older messages -->
      <div v-if="loadingOlder" style="text-align: center; padding: 12px;">
        <n-spin size="small" />
        <n-text :depth="3" style="margin-left: 8px; font-size: 12px">{{ t('chat.loadingOlder') }}</n-text>
      </div>
      <div v-else-if="allLoaded && messages.length > 0" style="text-align: center; padding: 8px;">
        <n-text :depth="3" style="font-size: 12px">{{ t('chat.beginning') }}</n-text>
      </div>

      <!-- Messages -->
      <div
        v-for="msg in messages"
        :key="msg.id"
        :style="{
          display: 'flex',
          justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start',
          padding: '4px 16px',
        }"
      >
        <!-- Interactive prompt message -->
        <n-card
          v-if="msg.role === 'prompt'"
          :style="{ maxWidth: '75%' }"
          size="small"
          :bordered="true"
        >
          <div style="white-space: pre-wrap; word-break: break-word; margin-bottom: 8px;">{{ msg.text }}</div>
          <n-space vertical :size="8" v-if="msg.promptId === activePromptId">
            <n-button
              v-for="opt in (msg.options || []).filter(o => o.id !== '__other__')"
              :key="opt.id"
              size="small"
              type="primary"
              secondary
              style="width: 100%; text-align: left;"
              @click="answerPrompt(msg.promptId!, opt.id, opt.label)"
            >
              {{ opt.label }}
            </n-button>
            <n-input-group style="margin-top: 4px;">
              <n-input
                v-model:value="inputText"
                :placeholder="t('chat.typeManually')"
                size="small"
                @keydown.enter.exact.prevent="answerPromptFreeText(msg.promptId!)"
              />
              <n-button size="small" type="primary" @click="answerPromptFreeText(msg.promptId!)">
                {{ t('chat.send') }}
              </n-button>
            </n-input-group>
          </n-space>
          <n-text v-else :depth="3" style="font-size: 12px">{{ t('chat.answered') }}</n-text>
          <div style="font-size: 11px; margin-top: 4px; opacity: 0.6;">{{ msg.timestamp }}</div>
        </n-card>
        <!-- Regular message -->
        <div
          v-else
          class="msg-wrap"
          @mouseenter="hoveredMsgId = msg.id"
          @mouseleave="hoveredMsgId = null"
        >
          <div
            :style="{
              maxWidth: '100%',
              padding: '8px 12px',
              borderRadius: '12px',
              background: msg.role === 'user' ? '#18a058' : '#e8e8e8',
              color: msg.role === 'user' ? '#fff' : '#333',
            }"
          >
            <div style="white-space: pre-wrap; word-break: break-word;">{{ msg.text }}</div>
            <div :style="{ fontSize: '11px', marginTop: '4px', opacity: 0.6, textAlign: msg.role === 'user' ? 'right' : 'left' }">
              {{ msg.timestamp }}
            </div>
          </div>
          <button
            v-if="msg.role === 'assistant' && msg.messageId"
            class="bookmark-btn"
            :class="{
              visible: hoveredMsgId === msg.id || msg.bookmarkState === 'saved' || msg.bookmarkState === 'saving',
              saved: msg.bookmarkState === 'saved',
              error: msg.bookmarkState === 'error',
              saving: msg.bookmarkState === 'saving',
            }"
            :disabled="msg.bookmarkState === 'saved' || msg.bookmarkState === 'saving'"
            :title="msg.bookmarkState === 'saved' ? t('chat.bookmarkSaved') : t('chat.bookmarkSave')"
            @click.stop="bookmarkMessage(msg)"
          >
            <span v-if="msg.bookmarkState === 'saving'">&#x23F3;</span>
            <span v-else-if="msg.bookmarkState === 'saved'">&#x2705;</span>
            <span v-else-if="msg.bookmarkState === 'error'">&#x274C;</span>
            <span v-else>&#x1F4BE;</span>
          </button>
        </div>
      </div>

      <!-- Processing indicator -->
      <div v-if="processing" style="display: flex; justify-content: flex-start; padding: 4px 16px;">
        <div style="padding: 8px 12px; border-radius: 12px; background: #e8e8e8; color: #333;">
          <!-- Active skills -->
          <div v-if="activeSkills.length > 0" style="margin-bottom: 6px;">
            <n-space :size="4">
              <n-tag
                v-for="sk in activeSkills"
                :key="sk"
                size="small"
                type="warning"
                :bordered="false"
                round
              >
                <template #icon><n-spin :size="12" /></template>
                {{ sk }}
              </n-tag>
            </n-space>
          </div>
          <!-- Completed skills -->
          <div v-if="completedSkills.length > 0" style="margin-bottom: 6px;">
            <n-space :size="4">
              <n-tag
                v-for="sk in completedSkills"
                :key="sk.name"
                size="small"
                type="success"
                :bordered="false"
                round
              >
                {{ sk.name }} ({{ sk.duration }}ms)
              </n-tag>
            </n-space>
          </div>
          <!-- Agent orchestration progress -->
          <AgentProgress
            v-if="orchestrationActive || agentList.length > 0"
            :agents="agentList"
            :orchestration-active="orchestrationActive"
          />
          <!-- Streaming text or thinking dots -->
          <div v-if="streamingText" style="white-space: pre-wrap; word-break: break-word;">{{ streamingText }}</div>
          <div v-else-if="!orchestrationActive" class="thinking-dots">
            <span></span><span></span><span></span>
          </div>
        </div>
      </div>

      <n-empty v-if="messages.length === 0 && !processing && !loadingHistory" :description="t('chat.emptyState')" style="padding: 40px 0;" />
    </div>

    <div style="flex-shrink: 0; padding: 8px 0;">
      <n-input-group>
        <n-input
          v-model:value="inputText"
          :placeholder="t('chat.placeholder')"
          :disabled="!connected"
          @keydown.enter.exact.prevent="sendMessage"
        />
        <n-button type="primary" :disabled="!connected || !inputText.trim()" @click="sendMessage">
          {{ t('chat.send') }}
        </n-button>
      </n-input-group>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, nextTick, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLocale } from '../composables/useLocale'
import { NPageHeader, NTag, NText, NInput, NInputGroup, NButton, NEmpty, NSpin, NSpace, NCard } from 'naive-ui'
import { useWebSocket } from '../composables/useWebSocket'
import { currentUser, getAccessToken, api } from '../api'
import AgentProgress from '../components/AgentProgress.vue'
import type { AgentInfo } from '../components/agentTypes'

const { t } = useI18n()
const { setLocale } = useLocale()

interface ChatMessage {
  id: string
  dbId?: number // database ID for pagination
  role: 'user' | 'assistant' | 'prompt'
  text: string
  timestamp: string
  messageId?: string // WS message_id for bookmark correlation
  bookmarkState?: 'idle' | 'saving' | 'saved' | 'error'
  promptId?: string
  options?: { id: string; label: string }[]
}

interface CompletedSkill {
  name: string
  duration: number
}

// Agent orchestration state.
const orchestrationActive = ref(false)
const agentList = ref<AgentInfo[]>([])

const PAGE_SIZE = 50
const MAX_MESSAGES = 500

const messages = ref<ChatMessage[]>([])
const inputText = ref('')
const streamingText = ref('')
const messagesContainer = ref<HTMLElement | null>(null)
const processing = ref(false)
const activeSkills = ref<string[]>([])
const completedSkills = ref<CompletedSkill[]>([])
const loadingHistory = ref(false)
const activePromptId = ref<string | null>(null)
const loadingOlder = ref(false)
const allLoaded = ref(false)
const hoveredMsgId = ref<string | null>(null)

const user = currentUser()
const token = getAccessToken()
const userID = user?.user_id ?? 'anonymous'
const username = user?.username ?? 'anonymous'
const chatID = `web:${userID}`

const { connected, connect, send, on } = useWebSocket(
  `/ws/chat?user_id=${encodeURIComponent(userID)}&username=${encodeURIComponent(username)}&token=${encodeURIComponent(token ?? '')}`
)

function scrollToBottom(smooth = false) {
  nextTick(() => {
    if (messagesContainer.value) {
      messagesContainer.value.scrollTo({
        top: messagesContainer.value.scrollHeight,
        behavior: smooth ? 'smooth' : 'auto',
      })
    }
  })
}

function isNearBottom(): boolean {
  const el = messagesContainer.value
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight < 100
}

function formatTime(dateStr: string): string {
  try {
    return new Date(dateStr).toLocaleTimeString()
  } catch {
    return dateStr
  }
}

// Load initial history from REST API
async function loadHistory() {
  loadingHistory.value = true
  try {
    const msgs = await api.getMessages(chatID, { limit: PAGE_SIZE })
    if (msgs && msgs.length > 0) {
      messages.value = msgs.map(m => ({
        id: `db-${m.ID}`,
        dbId: m.ID,
        role: m.Role === 'user' ? 'user' as const : 'assistant' as const,
        text: m.Content,
        timestamp: formatTime(m.CreatedAt),
      }))
      if (msgs.length < PAGE_SIZE) {
        allLoaded.value = true
      }
      scrollToBottom()
    } else {
      allLoaded.value = true
    }
  } catch (e) {
    console.error('Failed to load history:', e)
  } finally {
    loadingHistory.value = false
  }
}

// Load older messages when scrolling to top
async function loadOlderMessages() {
  if (loadingOlder.value || allLoaded.value || messages.value.length === 0) return

  const firstMsg = messages.value[0]
  if (!firstMsg.dbId) return

  loadingOlder.value = true
  const el = messagesContainer.value
  const prevScrollHeight = el?.scrollHeight ?? 0

  try {
    const msgs = await api.getMessages(chatID, { limit: PAGE_SIZE, before_id: firstMsg.dbId })
    if (!msgs || msgs.length === 0) {
      allLoaded.value = true
      return
    }
    if (msgs.length < PAGE_SIZE) {
      allLoaded.value = true
    }

    const older = msgs.map(m => ({
      id: `db-${m.ID}`,
      dbId: m.ID,
      role: m.Role === 'user' ? 'user' as const : 'assistant' as const,
      text: m.Content,
      timestamp: formatTime(m.CreatedAt),
    }))
    messages.value = [...older, ...messages.value]

    // Preserve scroll position after prepending
    nextTick(() => {
      if (el) {
        el.scrollTop = el.scrollHeight - prevScrollHeight
      }
    })
  } catch (e) {
    console.error('Failed to load older messages:', e)
  } finally {
    loadingOlder.value = false
  }
}

function handleScroll() {
  const el = messagesContainer.value
  if (!el) return
  if (el.scrollTop < 50 && !loadingOlder.value && !allLoaded.value) {
    loadOlderMessages()
  }
}

// Trim messages if exceeding max to prevent memory issues
function trimMessages() {
  if (messages.value.length > MAX_MESSAGES) {
    messages.value = messages.value.slice(messages.value.length - MAX_MESSAGES)
    allLoaded.value = false // older messages were trimmed, can reload
  }
}

let orchestrationCleanupTimer: ReturnType<typeof setTimeout> | null = null

function resetProcessingState() {
  processing.value = false
  activeSkills.value = []
  completedSkills.value = []
  orchestrationActive.value = false
  agentList.value = []
  if (orchestrationCleanupTimer) {
    clearTimeout(orchestrationCleanupTimer)
    orchestrationCleanupTimer = null
  }
}

function sendMessage() {
  const text = inputText.value.trim()
  if (!text) return

  messages.value.push({
    id: `local-${Date.now()}`,
    role: 'user',
    text,
    timestamp: new Date().toLocaleTimeString(),
  })
  inputText.value = ''
  trimMessages()
  scrollToBottom()

  send({ text })
}

// Status events (processing, skill_start, skill_done, stream_start)
on('status', (payload: any) => {
  switch (payload.status) {
    case 'processing':
      processing.value = true
      activeSkills.value = []
      completedSkills.value = []
      streamingText.value = ''
      break
    case 'skill_start':
      if (payload.skill_name && !activeSkills.value.includes(payload.skill_name)) {
        activeSkills.value.push(payload.skill_name)
      }
      break
    case 'skill_done':
      activeSkills.value = activeSkills.value.filter(s => s !== payload.skill_name)
      if (payload.skill_name) {
        completedSkills.value.push({
          name: payload.skill_name,
          duration: payload.duration_ms || 0,
        })
      }
      break
    case 'stream_start':
      // Keep processing=true, streaming will follow
      break
    case 'locale_changed':
      // Backend changed channel locale via set_language skill — sync frontend.
      if (payload.data?.locale) {
        setLocale(payload.data.locale)
      }
      break
    case 'orchestration_started':
      orchestrationActive.value = true
      agentList.value = []
      // Clear any pending cleanup timer from a previous orchestration.
      if (orchestrationCleanupTimer) {
        clearTimeout(orchestrationCleanupTimer)
        orchestrationCleanupTimer = null
      }
      break
    case 'orchestration_done':
      orchestrationActive.value = false
      // Clear agent list after a brief delay so user can see final state.
      orchestrationCleanupTimer = setTimeout(() => {
        agentList.value = []
        orchestrationCleanupTimer = null
      }, 3000)
      break
    case 'agent_started':
      if (payload.data?.agent_id) {
        agentList.value.push({
          id: payload.data.agent_id,
          type: payload.data.agent_type || 'generic',
          status: 'running',
        })
      }
      break
    case 'agent_progress':
      if (payload.data?.agent_id) {
        const ag = agentList.value.find(a => a.id === payload.data.agent_id)
        if (ag) {
          ag.turn = parseInt(payload.data.turn || '0', 10)
        }
      }
      break
    case 'agent_completed':
      if (payload.data?.agent_id) {
        const ag = agentList.value.find(a => a.id === payload.data.agent_id)
        if (ag) {
          ag.status = 'completed'
          ag.durationMs = parseInt(payload.data.duration_ms || '0', 10)
          ag.tokens = parseInt(payload.data.tokens || '0', 10)
        }
      }
      break
    case 'agent_failed':
      if (payload.data?.agent_id) {
        const ag = agentList.value.find(a => a.id === payload.data.agent_id)
        if (ag) {
          ag.status = 'failed'
          ag.error = payload.data.error || 'Unknown error'
        }
      }
      break
    case 'error':
      resetProcessingState()
      messages.value.push({
        id: `err-${Date.now()}`,
        role: 'assistant',
        text: t('chat.error', { error: payload.error || 'Something went wrong' }),
        timestamp: new Date().toLocaleTimeString(),
      })
      if (isNearBottom()) scrollToBottom(true)
      break
  }
  if (isNearBottom()) scrollToBottom(true)
})

// Complete message (non-streaming response)
on('message', (payload: any) => {
  resetProcessingState()
  streamingText.value = ''
  messages.value.push({
    id: `msg-${Date.now()}`,
    role: 'assistant',
    text: payload.text,
    timestamp: payload.timestamp ? formatTime(payload.timestamp) : new Date().toLocaleTimeString(),
    messageId: payload.message_id || undefined,
    bookmarkState: payload.message_id ? 'idle' : undefined,
  })
  trimMessages()
  if (isNearBottom()) scrollToBottom(true)
})

// Streaming updates
on('stream_edit', (payload: any) => {
  streamingText.value = payload.text
  if (isNearBottom()) scrollToBottom()
})

// Streaming done
on('stream_done', (payload: any) => {
  resetProcessingState()
  streamingText.value = ''
  messages.value.push({
    id: `msg-${Date.now()}`,
    role: 'assistant',
    text: payload.text,
    timestamp: payload.timestamp ? formatTime(payload.timestamp) : new Date().toLocaleTimeString(),
    messageId: payload.message_id || undefined,
    bookmarkState: payload.message_id ? 'idle' : undefined,
  })
  trimMessages()
  if (isNearBottom()) scrollToBottom(true)
})

// Interactive prompt from a skill
on('prompt', (payload: any) => {
  activePromptId.value = payload.prompt_id
  messages.value.push({
    id: `prompt-${Date.now()}`,
    role: 'prompt',
    text: payload.text,
    timestamp: payload.timestamp ? formatTime(payload.timestamp) : new Date().toLocaleTimeString(),
    promptId: payload.prompt_id,
    options: payload.options || [],
  })
  trimMessages()
  if (isNearBottom()) scrollToBottom(true)
})

// Bookmark acknowledgment from server
on('remember_ack', (payload: any) => {
  const ack = payload.remember_ack || payload
  const msg = messages.value.find(m => m.messageId === ack.message_id)
  if (msg) {
    if (ack.status === 'saved') {
      msg.bookmarkState = 'saved'
    } else {
      msg.bookmarkState = 'error'
      // Auto-reset to idle after 3 seconds so user can retry
      setTimeout(() => {
        if (msg.bookmarkState === 'error') {
          msg.bookmarkState = 'idle'
        }
      }, 3000)
    }
  }
})

function bookmarkMessage(msg: ChatMessage) {
  if (!msg.messageId || msg.bookmarkState === 'saved' || msg.bookmarkState === 'saving') return
  msg.bookmarkState = 'saving'
  send({ remember_message_id: msg.messageId })
}

function answerPrompt(promptId: string, optionId: string, label: string) {
  activePromptId.value = null
  // Show user's choice as a message.
  messages.value.push({
    id: `user-${Date.now()}`,
    role: 'user',
    text: label,
    timestamp: new Date().toLocaleTimeString(),
  })
  send({ prompt_id: promptId, prompt_answer: optionId, text: optionId })
  trimMessages()
  scrollToBottom(true)
}

function answerPromptFreeText(promptId: string) {
  const text = inputText.value.trim()
  if (!text) return
  activePromptId.value = null
  messages.value.push({
    id: `user-${Date.now()}`,
    role: 'user',
    text: text,
    timestamp: new Date().toLocaleTimeString(),
  })
  send({ prompt_id: promptId, prompt_answer: text, text: text })
  inputText.value = ''
  trimMessages()
  scrollToBottom(true)
}

onMounted(async () => {
  await loadHistory()
  connect()
})

onUnmounted(() => {
  if (orchestrationCleanupTimer) {
    clearTimeout(orchestrationCleanupTimer)
    orchestrationCleanupTimer = null
  }
})
</script>

<style scoped>
.msg-wrap {
  position: relative;
  max-width: 75%;
  display: inline-flex;
  align-items: flex-end;
  gap: 4px;
}
.bookmark-btn {
  flex-shrink: 0;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  opacity: 0;
  transition: opacity 0.2s ease;
  padding: 2px 4px;
  border-radius: 4px;
  line-height: 1;
}
.bookmark-btn:hover:not(:disabled) {
  background: rgba(0, 0, 0, 0.05);
}
.bookmark-btn.visible {
  opacity: 1;
}
.bookmark-btn.saved {
  opacity: 0.7;
  cursor: default;
}
.bookmark-btn.error {
  opacity: 1;
}
.bookmark-btn.saving {
  opacity: 1;
  cursor: wait;
}
.thinking-dots {
  display: flex;
  gap: 4px;
  padding: 4px 0;
}
.thinking-dots span {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #999;
  animation: dot-pulse 1.4s infinite ease-in-out;
}
.thinking-dots span:nth-child(2) {
  animation-delay: 0.2s;
}
.thinking-dots span:nth-child(3) {
  animation-delay: 0.4s;
}
@keyframes dot-pulse {
  0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
  40% { transform: scale(1); opacity: 1; }
}
</style>
