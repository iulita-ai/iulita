<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AgentInfo } from './agentTypes'

const props = defineProps<{
  agents: AgentInfo[]
  orchestrationActive: boolean
}>()

const { t } = useI18n()

const agentIcon = (type: string): string => {
  switch (type) {
    case 'researcher': return '🔍'
    case 'analyst': return '📊'
    case 'planner': return '📋'
    case 'coder': return '💻'
    case 'summarizer': return '📝'
    default: return '🤖'
  }
}

const statusIcon = (status: string): string => {
  switch (status) {
    case 'completed': return '✅'
    case 'failed': return '❌'
    default: return '⏳'
  }
}

const hasAgents = computed(() => props.agents.length > 0)
</script>

<template>
  <div v-if="orchestrationActive && hasAgents" class="agent-progress">
    <div class="agent-progress-header">
      {{ t('chat.orchestrationStarted', { count: agents.length }) }}
    </div>
    <div v-for="ag in agents" :key="ag.id" class="agent-item" :class="ag.status">
      <span class="agent-icon">{{ agentIcon(ag.type) }}</span>
      <span class="agent-name">{{ ag.id }}</span>
      <span class="agent-type">({{ ag.type }})</span>
      <span class="agent-status-icon">{{ statusIcon(ag.status) }}</span>
      <span v-if="ag.status === 'running' && ag.turn" class="agent-turn">
        turn {{ ag.turn }}
      </span>
      <span v-if="ag.status === 'completed' && ag.durationMs" class="agent-duration">
        {{ (ag.durationMs / 1000).toFixed(1) }}s
      </span>
      <span v-if="ag.status === 'failed' && ag.error" class="agent-error" :title="ag.error">
        {{ ag.error.length > 30 ? ag.error.slice(0, 30) + '...' : ag.error }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.agent-progress {
  background: var(--n-color-modal, #f5f5f5);
  border-radius: 8px;
  padding: 8px 12px;
  margin: 4px 0;
  font-size: 13px;
}
.agent-progress-header {
  font-weight: 600;
  margin-bottom: 4px;
  opacity: 0.8;
}
.agent-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 0;
  transition: opacity 0.3s;
}
.agent-item.completed {
  opacity: 0.7;
}
.agent-item.failed {
  opacity: 0.8;
}
.agent-icon {
  flex-shrink: 0;
}
.agent-name {
  font-weight: 500;
}
.agent-type {
  opacity: 0.6;
  font-size: 12px;
}
.agent-status-icon {
  margin-left: auto;
}
.agent-turn {
  opacity: 0.5;
  font-size: 12px;
}
.agent-duration {
  opacity: 0.5;
  font-size: 12px;
}
.agent-error {
  color: #e74c3c;
  font-size: 12px;
}
</style>
